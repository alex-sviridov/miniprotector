package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type fileRow struct {
	FileDataID string
	FileID     string
	Source     string
	Type       string
	Path       string
	Timestamp  int64
	Size       int64
	Chunks     int
	Versions   int64
	CreatedAt  time.Time
}

type fileRowJSON struct {
	FileDataID string `json:"file_data_id"`
	Source     string `json:"source"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	Timestamp  int64  `json:"timestamp"`
	Size       int64  `json:"size"`
	Chunks     int    `json:"chunks"`
	Versions   int64  `json:"versions"`
	CreatedAt  string `json:"created_at"`
}

func runList(logger *slog.Logger, storagePath, output, filter string) error {
	store, err := wfs.NewReadOnly(storagePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	rows, err := queryFileRows(store, filter)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	switch output {
	case "json":
		return renderJSON(rows)
	default:
		return renderTable(rows)
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

func queryFileRows(store *wfs.Store, filter string) ([]fileRow, error) {
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

	if filter != "" {
		query = query.Where("fd.file_id LIKE ?", "%"+filter+"%")
	}

	var results []queryResult
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	rows := make([]fileRow, len(results))
	for i, r := range results {
		src, typ, path, ts := parseFileID(r.FileID)
		rows[i] = fileRow{
			FileDataID: r.FileDataID,
			FileID:     r.FileID,
			Source:     src,
			Type:       typ,
			Path:       path,
			Timestamp:  ts,
			Size:       r.Size,
			Chunks:     r.Chunks,
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt,
		}
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
	// Minimum valid: host, type, path, mtime = 4 tokens
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

func formatSize(bytes int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case bytes < kb:
		return fmt.Sprintf("%d B", bytes)
	case bytes < mb:
		return fmt.Sprintf("%d KB", bytes/kb)
	case bytes < gb:
		return fmt.Sprintf("%d MB", bytes/mb)
	default:
		return fmt.Sprintf("%d GB", bytes/gb)
	}
}

func renderTable(rows []fileRow) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tTYPE\tPATH\tTIMESTAMP\tSIZE\tCHUNKS\tVERSIONS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%d\n",
			r.Source, r.Type, r.Path, r.Timestamp, formatSize(r.Size), r.Chunks, r.Versions)
	}
	return w.Flush()
}

func renderJSON(rows []fileRow) error {
	out := make([]fileRowJSON, len(rows))
	for i, r := range rows {
		out[i] = fileRowJSON{
			FileDataID: r.FileDataID,
			Source:     r.Source,
			Type:       r.Type,
			Path:       r.Path,
			Timestamp:  r.Timestamp,
			Size:       r.Size,
			Chunks:     r.Chunks,
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
