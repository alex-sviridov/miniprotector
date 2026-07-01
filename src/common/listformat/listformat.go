// Package listformat renders file-listing rows as a table or JSON.
// Used by both bwfs's local SQLite-backed list and rwfs's gRPC-backed
// list, so the two commands produce identical output for identical data.
package listformat

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"
)

// Row is a rendering-ready file listing entry, independent of where the
// underlying data came from (local SQLite query or a gRPC ListResponse).
type Row struct {
	FileUUID  string
	Source    string
	Type      string
	Path      string
	Timestamp int64
	Size      int64
	Chunks    int
	Versions  int64
	CreatedAt time.Time
}

type jsonRow struct {
	FileUUID  string `json:"file_uuid"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	Timestamp int64  `json:"timestamp"`
	Size      int64  `json:"size"`
	Chunks    int    `json:"chunks"`
	Versions  int64  `json:"versions"`
	CreatedAt string `json:"created_at"`
}

func toJSONRows(rows []Row) []jsonRow {
	out := make([]jsonRow, len(rows))
	for i, r := range rows {
		out[i] = jsonRow{
			FileUUID:  r.FileUUID,
			Source:    r.Source,
			Type:      r.Type,
			Path:      r.Path,
			Timestamp: r.Timestamp,
			Size:      r.Size,
			Chunks:    r.Chunks,
			Versions:  r.Versions,
			CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return out
}

// FormatSize renders a byte count as a human-readable B/KB/MB/GB string.
func FormatSize(bytes int64) string {
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

// RenderTable writes rows to stdout as a tab-aligned table.
func RenderTable(rows []Row) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tTYPE\tPATH\tTIMESTAMP\tSIZE\tCHUNKS\tVERSIONS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%d\n",
			r.Source, r.Type, r.Path, r.Timestamp, FormatSize(r.Size), r.Chunks, r.Versions)
	}
	return w.Flush()
}

// RenderJSON writes rows to stdout as indented JSON.
func RenderJSON(rows []Row) error {
	out := toJSONRows(rows)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
