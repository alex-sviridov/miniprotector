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

// childRange is a half-open lexicographic interval [Lower, Upper)
// covering every path strictly below some base path, under one separator
// convention. SQLite compares TEXT with the BINARY collation by default
// (no column here declares otherwise), so these bounds are compared
// byte-wise, which is exactly what the arithmetic below assumes.
type childRange struct {
	Lower string
	Upper string
}

// separatorChildRanges holds the child interval for each separator
// convention a stored path may use. Both are always applied: file_data_records
// is keyed by (source_host, path) across every host that ever backed up to
// this store, so a single host-agnostic folder rule can legitimately span
// Unix-style and Windows-style source hosts at once. Picking one convention
// per query would silently drop the other host's files -- the exact class of
// silent under-match this whole helper exists to prevent.
type separatorChildRanges struct {
	Unix    childRange
	Windows childRange
}

// restoreChildRanges computes the child intervals for path under both
// separator conventions.
//
// The upper bound of each interval is the base plus the byte immediately
// following that separator, which is what keeps the range from leaking into
// siblings: '0' (0x30) follows '/' (0x2F), and ']' (0x5D) follows '\' (0x5C).
// So for base "/etc", ["/etc/", "/etc0") admits "/etc/nginx.conf" but not
// "/etc2/other.conf", whose 5th byte '2' (0x32) sorts at or above the upper
// bound.
//
// A single trailing separator is stripped first so a root path works. "/"
// normalizes to "" (giving ["/", "0"), which admits every absolute Unix
// path) and `C:\` normalizes to "C:" (giving [`C:\`, "C:]")). Without that
// strip, a root would produce bounds like ["//", "/0") whose upper bound
// sorts below every real child -- "/etc/x" has 'e' (0x65) where the bound
// has '0' (0x30) -- so a root folder rule matched nothing at all.
//
// This deliberately reimplements, rather than imports, the separator
// awareness cmd/rwfs/rules.go already has: both are package main and cannot
// import each other. It is also intentionally convention-agnostic rather
// than sniffing for a drive letter the way isWindowsStyleRestorePath does --
// the query has to serve every host in the store at once, not classify one
// path.
func restoreChildRanges(path string) separatorChildRanges {
	base := path
	if n := len(base); n > 0 && (base[n-1] == '/' || base[n-1] == '\\') {
		base = base[:n-1]
	}
	return separatorChildRanges{
		Unix:    childRange{Lower: base + "/", Upper: base + "0"},
		Windows: childRange{Lower: base + "\\", Upper: base + "]"},
	}
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
		// The folder itself, plus its children under either separator
		// convention. Keeping this as one OR'd expression (rather than
		// chained Where calls, which AND) is load-bearing.
		r := restoreChildRanges(filter.GetPath())
		query = query.Where("fd.path = ? OR (fd.path >= ? AND fd.path < ?) OR (fd.path >= ? AND fd.path < ?)",
			filter.GetPath(),
			r.Unix.Lower, r.Unix.Upper,
			r.Windows.Lower, r.Windows.Upper)
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

// resolveRestoreDirectoryFilter mirrors resolveRestoreFilter's shape, but
// queries file_version_records directly (WHERE type = 'd') since a
// directory never has a file_data_records row to join through. Reuses
// restoreChildRanges verbatim -- same separator-aware subtree matching. A
// directory has no content-version concept to disambiguate the way a
// file's checksum does, so GROUP BY (source_host, path) alone fully
// collapses to one row per directory; the [filter.NotBefore,
// filter.NotAfter] window is enforced entirely in WHERE below, and since
// this round only checks existence (not which version), no aggregate is
// needed to pick a winner among a directory's versions the way
// resolveRestoreFilter's MAX(fv.created_at)/ORDER BY dedup does for files
// -- see
// docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md.
//
// Only ever called for a path_is_prefix filter -- an exact-path filter is
// forced to match nothing here, via the unconditional "1 = 0" branch below
// (its own literal path is a leaf a directory could share, but "d" rows
// this query returns are folder containers a caller is about to recreate,
// and an exact-file rule has no reason to ask for that), so callers don't
// need to gate this themselves, though ResolveRestoreFiles does anyway for
// clarity (see below).
func resolveRestoreDirectoryFilter(store *wfs.Store, filter *pb.RestoreFileFilter, yield func(source, path string) bool) error {
	query := store.RawDB().
		Table("file_version_records").
		Select("source_host, path").
		Where("type = ?", "d").
		Group("source_host, path").
		Order("source_host ASC, path ASC")

	if filter.GetHost() != "" {
		query = query.Where("source_host = ?", filter.GetHost())
	}
	if filter.GetPathIsPrefix() {
		r := restoreChildRanges(filter.GetPath())
		query = query.Where("path = ? OR (path >= ? AND path < ?) OR (path >= ? AND path < ?)",
			filter.GetPath(), r.Unix.Lower, r.Unix.Upper, r.Windows.Lower, r.Windows.Upper)
	} else {
		// An exact-path (file-level) filter never targets a directory row,
		// even if its literal path happens to collide with some directory's
		// path -- "d" rows this query returns are folder containers a
		// caller is about to recreate, and an exact-file rule has no reason
		// to ask for that. Force no match here (rather than a literal
		// "path = ?" equality check) so an accidental path collision with
		// an unrelated file target never surfaces a directory.
		query = query.Where("1 = 0")
	}
	if filter.GetNotBefore() != 0 {
		query = query.Where("created_at >= ?", time.Unix(filter.GetNotBefore(), 0))
	}
	if filter.GetNotAfter() != 0 {
		query = query.Where("created_at <= ?", time.Unix(filter.GetNotAfter(), 0))
	}

	rows, err := query.Rows()
	if err != nil {
		return fmt.Errorf("resolve restore directory filter query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var source, path string
		if err := rows.Scan(&source, &path); err != nil {
			return fmt.Errorf("scan resolved directory: %w", err)
		}
		if !yield(source, path) {
			return rows.Err()
		}
	}
	return rows.Err()
}

func (s *listServer) ResolveRestoreFiles(req *pb.ResolveRestoreFilesRequest, stream pb.ListService_ResolveRestoreFilesServer) error {
	for filterIndex, filter := range req.GetFilters() {
		var sendErr error
		err := resolveRestoreFilter(s.store, filter, func(c resolvedCandidate) bool {
			err := stream.Send(&pb.ResolveRestoreFilesResponse{
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
			if err != nil {
				sendErr = err
				return false
			}
			return true
		})
		if sendErr != nil {
			return sendErr
		}
		if err != nil {
			s.logger.Error("ResolveRestoreFiles query failed", "filter_index", filterIndex, "error", err)
			return err
		}

		if !filter.GetPathIsPrefix() {
			continue
		}
		err = resolveRestoreDirectoryFilter(s.store, filter, func(source, path string) bool {
			err := stream.Send(&pb.ResolveRestoreFilesResponse{
				Row:         &pb.FileRow{Source: source, Type: "d", Path: path},
				FilterIndex: int32(filterIndex),
			})
			if err != nil {
				sendErr = err
				return false
			}
			return true
		})
		if sendErr != nil {
			return sendErr
		}
		if err != nil {
			s.logger.Error("ResolveRestoreFiles directory query failed", "filter_index", filterIndex, "error", err)
			return err
		}
	}
	return nil
}
