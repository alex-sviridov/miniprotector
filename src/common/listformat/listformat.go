// Package listformat renders file-listing rows as a table or JSON.
// Used by both bwfs's local SQLite-backed list and rwfs's gRPC-backed
// list, so the two commands produce identical output for identical data.
package listformat

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
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
	// Damaged reports that bwfs flagged this version's data as damaged (a
	// chunk it needs was lost), so restoring it fails with DataLoss.
	Damaged bool
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
	// omitempty keeps healthy rows exactly as they were before damage was
	// reported, so existing JSON consumers see no change.
	Damaged bool `json:"damaged,omitempty"`
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
			Damaged:   r.Damaged,
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

// RenderTable writes rows to stdout as a tab-aligned table. A DAMAGED column
// is added only when at least one row is damaged, so the usual table (and any
// script parsing it) is unchanged.
func RenderTable(rows []Row) error {
	return writeTable(os.Stdout, rows)
}

func writeTable(out io.Writer, rows []Row) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	damaged := slices.ContainsFunc(rows, func(r Row) bool { return r.Damaged })
	header := "SOURCE\tTYPE\tPATH\tTIMESTAMP\tSIZE\tCHUNKS\tVERSIONS"
	if damaged {
		header += "\tDAMAGED"
	}
	fmt.Fprintln(w, header)
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%d",
			r.Source, r.Type, r.Path, r.Timestamp, FormatSize(r.Size), r.Chunks, r.Versions)
		if damaged {
			marker := ""
			if r.Damaged {
				marker = "yes"
			}
			fmt.Fprintf(w, "\t%s", marker)
		}
		fmt.Fprintln(w)
	}
	return w.Flush()
}

// RenderJSON writes rows to stdout as indented JSON.
func RenderJSON(rows []Row) error {
	return writeJSON(os.Stdout, rows)
}

func writeJSON(out io.Writer, rows []Row) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(toJSONRows(rows))
}
