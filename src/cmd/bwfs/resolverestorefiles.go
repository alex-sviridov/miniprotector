package main

import (
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

// resolvedCandidate is the single winning row bwfs found for one
// (source_host, path) a RestoreFileFilter matched -- the version whose
// latest in-window file_version_records.created_at is greatest.
type resolvedCandidate struct {
	FileUUID   string
	Source     string
	Path       string
	Size       int64
	ChunkCount int
}

// pathPrefixUpperBound returns the exclusive upper bound of the
// lexicographic range covering every path "under" prefix (prefix followed
// by a path separator and anything else). '0' (0x30) is the next ASCII
// byte after '/' (0x2F), so [prefix+"/", prefix+"0") matches exactly
// "prefix/...", never a sibling like "prefix2/...".
func pathPrefixUpperBound(prefix string) string {
	return prefix + "0"
}

// resolveRestoreFilter streams the winning candidate row per distinct
// (source_host, path) that filter selects, using a real DB cursor rather
// than materializing the whole match set in memory -- see
// docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md's
// Performance Notes. yield is called once per winning row; a false return
// stops iteration early.
//
// For each candidate file_id, only its latest finalized FileDataRecord is
// considered (mirrors cmd/bwfs/list.go's queryFileRows), and only
// file_version_records whose created_at falls inside
// [filter.NotBefore, filter.NotAfter] (0 = unbounded on that side) count
// toward "backed up inside this timeframe" -- a file whose content hasn't
// changed can still have been re-attested by many later backup jobs, and
// any of those re-attestations satisfies the window (see the design doc's
// Problem section).
func resolveRestoreFilter(store *wfs.Store, filter *pb.RestoreFileFilter, yield func(resolvedCandidate) bool) error {
	query := store.RawDB().
		Table("file_data_records fd").
		Select("fd.uuid AS uuid, fd.source_host AS source_host, fd.path AS path, fd.size AS size, fd.chunk_count AS chunk_count, MAX(fv.created_at) AS best_version_at").
		Joins("JOIN file_version_records fv ON fv.object_id = fd.file_id").
		Where("fd.checksum IS NOT NULL").
		Where("fd.created_at = (SELECT MAX(fd2.created_at) FROM file_data_records fd2 WHERE fd2.file_id = fd.file_id AND fd2.checksum IS NOT NULL)").
		Group("fd.uuid, fd.source_host, fd.path, fd.size, fd.chunk_count").
		Order("fd.source_host ASC, fd.path ASC, best_version_at DESC")

	if filter.GetHost() != "" {
		query = query.Where("fd.source_host = ?", filter.GetHost())
	}
	if filter.GetPathIsPrefix() {
		query = query.Where("fd.path = ? OR (fd.path >= ? AND fd.path < ?)",
			filter.GetPath(), filter.GetPath()+"/", pathPrefixUpperBound(filter.GetPath()))
	} else {
		query = query.Where("fd.path = ?", filter.GetPath())
	}
	if filter.GetNotBefore() != 0 {
		query = query.Where("fv.created_at >= ?", time.Unix(filter.GetNotBefore(), 0))
	}
	if filter.GetNotAfter() != 0 {
		query = query.Where("fv.created_at <= ?", time.Unix(filter.GetNotAfter(), 0))
	}

	rows, err := query.Rows()
	if err != nil {
		return fmt.Errorf("resolve restore filter query: %w", err)
	}
	defer rows.Close()

	var lastSource, lastPath string
	haveLast := false
	for rows.Next() {
		var c resolvedCandidate
		// best_version_at (MAX(fv.created_at)) is only used to drive the
		// ORDER BY / dedup logic below; its value is never read in Go. Scan
		// it as `any` rather than time.Time: SQLite's MAX() aggregate loses
		// the column's type-affinity hint, so the modernc.org/sqlite driver
		// returns it as a plain string, which database/sql cannot
		// auto-convert into *time.Time.
		var bestVersionAt any
		if err := rows.Scan(&c.FileUUID, &c.Source, &c.Path, &c.Size, &c.ChunkCount, &bestVersionAt); err != nil {
			return fmt.Errorf("scan resolved candidate: %w", err)
		}
		if haveLast && c.Source == lastSource && c.Path == lastPath {
			continue // an older mtime for a path already emitted -- ORDER BY put the winner first
		}
		lastSource, lastPath, haveLast = c.Source, c.Path, true
		if !yield(c) {
			return rows.Err()
		}
	}
	return rows.Err()
}

func (s *listServer) ResolveRestoreFiles(req *pb.ResolveRestoreFilesRequest, stream pb.ListService_ResolveRestoreFilesServer) error {
	for filterIndex, filter := range req.GetFilters() {
		err := resolveRestoreFilter(s.store, filter, func(c resolvedCandidate) bool {
			sendErr := stream.Send(&pb.ResolveRestoreFilesResponse{
				Row: &pb.FileRow{
					FileUuid: c.FileUUID,
					Source:   c.Source,
					Type:     "f",
					Path:     c.Path,
					Size:     c.Size,
					Chunks:   int32(c.ChunkCount),
				},
				FilterIndex: int32(filterIndex),
			})
			return sendErr == nil
		})
		if err != nil {
			s.logger.Error("ResolveRestoreFiles query failed", "filter_index", filterIndex, "error", err)
			return err
		}
	}
	return nil
}
