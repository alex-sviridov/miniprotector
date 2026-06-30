package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/common/listformat"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func runList(logger *slog.Logger, storagePath, serverName, pathPrefix, output, filter string) error {
	store, err := wfs.NewReadOnly(storagePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	rows, err := queryFileRows(store, serverName, pathPrefix, filter)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	switch output {
	case "json":
		return listformat.RenderJSON(rows)
	default:
		return listformat.RenderTable(rows)
	}
}

type queryResult struct {
	FileDataID string    `gorm:"column:file_data_id"`
	FileID     string    `gorm:"column:file_id"`
	Size       int64     `gorm:"column:size"`
	Chunks     int       `gorm:"column:chunks"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	Versions   int64     `gorm:"column:versions"`
}

// queryFileRows returns the latest finalized FileDataRecord per file_id,
// optionally narrowed by source hostname (exact match), path (prefix
// match), and a free-text substring filter on the path.
func queryFileRows(store *wfs.Store, serverName, pathPrefix, filter string) ([]listformat.Row, error) {
	// Subquery picks the single latest finalized FileDataRecord per file_id,
	// so non-aggregated columns (id, size, chunk_count, created_at) are
	// unambiguous even if multiple records share the same file_id.
	// COUNT(DISTINCT fv.id) avoids inflation from the cross-join when multiple
	// FileDataRecords exist.
	query := store.RawDB().
		Table("file_data_records fd").
		Select("fd.id AS file_data_id, fd.file_id, fd.size, fd.chunk_count AS chunks, fd.created_at, COUNT(DISTINCT fv.id) AS versions").
		Joins("LEFT JOIN file_version_records fv ON fv.file_id = fd.file_id").
		Where("fd.checksum IS NOT NULL").
		Where("fd.created_at = (SELECT MAX(fd2.created_at) FROM file_data_records fd2 WHERE fd2.file_id = fd.file_id AND fd2.checksum IS NOT NULL)").
		Group("fd.file_id").
		Order("fd.created_at ASC")

	if serverName != "" {
		query = query.Where("fd.file_id LIKE ?", "fs://"+serverName+":%")
	}
	if filter != "" {
		query = query.Where("fd.file_id LIKE ?", "%"+filter+"%")
	}

	var results []queryResult
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	rows := make([]listformat.Row, 0, len(results))
	for _, r := range results {
		src, typ, path, ts := parseFileID(r.FileID)
		if pathPrefix != "" && !strings.HasPrefix(path, pathPrefix) {
			continue
		}
		rows = append(rows, listformat.Row{
			FileDataID: r.FileDataID,
			Source:     src,
			Type:       typ,
			Path:       path,
			Timestamp:  ts,
			Size:       r.Size,
			Chunks:     r.Chunks,
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt,
		})
	}
	return rows, nil
}

// parseFileID splits "fs://host:type:path:mtime" into its four parts.
// type is always a single character. path may contain colons (Windows C:/foo).
// Returns ("?","?",fileID,0) for malformed IDs — never errors.
func parseFileID(fileID string) (source, fileType, path string, timestamp int64) {
	const prefix = "fs://"
	if !strings.HasPrefix(fileID, prefix) {
		return "?", "?", fileID, 0
	}
	rest := fileID[len(prefix):]
	tokens := strings.Split(rest, ":")
	if len(tokens) < 4 {
		return "?", "?", fileID, 0
	}
	source = tokens[0]
	fileType = tokens[1]
	ts, err := strconv.ParseInt(tokens[len(tokens)-1], 10, 64)
	if err != nil {
		return "?", "?", fileID, 0
	}
	path = strings.Join(tokens[2:len(tokens)-1], ":")
	return source, fileType, path, ts
}
