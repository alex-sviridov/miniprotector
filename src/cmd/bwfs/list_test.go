package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- parseFileID ---

func TestParseFileID_ValidLinuxPath(t *testing.T) {
	src, typ, path, ts := parseFileID("fs://workstation:f:/var/log/spooler:1782605538")
	assert.Equal(t, "workstation", src)
	assert.Equal(t, "f", typ)
	assert.Equal(t, "/var/log/spooler", path)
	assert.Equal(t, int64(1782605538), ts)
}

func TestParseFileID_WindowsPathWithColons(t *testing.T) {
	src, typ, path, ts := parseFileID("fs://workstation:f:C:/Users/foo/bar.txt:1782605538")
	assert.Equal(t, "workstation", src)
	assert.Equal(t, "f", typ)
	assert.Equal(t, "C:/Users/foo/bar.txt", path)
	assert.Equal(t, int64(1782605538), ts)
}

func TestParseFileID_Directory(t *testing.T) {
	src, typ, path, ts := parseFileID("fs://host:d:/some/dir:999")
	assert.Equal(t, "host", src)
	assert.Equal(t, "d", typ)
	assert.Equal(t, "/some/dir", path)
	assert.Equal(t, int64(999), ts)
}

func TestParseFileID_MissingPrefix(t *testing.T) {
	src, typ, path, ts := parseFileID("not-a-valid-id")
	assert.Equal(t, "?", src)
	assert.Equal(t, "?", typ)
	assert.Equal(t, "not-a-valid-id", path)
	assert.Equal(t, int64(0), ts)
}

func TestParseFileID_TooFewTokens(t *testing.T) {
	// fs://host:f:1234 has only 3 tokens after stripping prefix — below minimum of 4
	src, typ, _, ts := parseFileID("fs://host:f:1234")
	assert.Equal(t, "?", src)
	assert.Equal(t, "?", typ)
	assert.Equal(t, int64(0), ts)
}

func TestParseFileID_NonNumericTimestamp(t *testing.T) {
	src, typ, path, ts := parseFileID("fs://host:f:/some/path:notanumber")
	assert.Equal(t, "?", src)
	assert.Equal(t, "?", typ)
	assert.Equal(t, "fs://host:f:/some/path:notanumber", path)
	assert.Equal(t, int64(0), ts)
}

func TestParseFileID_EmptyString(t *testing.T) {
	src, typ, path, ts := parseFileID("")
	assert.Equal(t, "?", src)
	assert.Equal(t, "?", typ)
	assert.Equal(t, "", path)
	assert.Equal(t, int64(0), ts)
}

// --- formatSize ---

func TestFormatSize_Bytes(t *testing.T) {
	assert.Equal(t, "0 B", formatSize(0))
	assert.Equal(t, "1 B", formatSize(1))
	assert.Equal(t, "1023 B", formatSize(1023))
}

func TestFormatSize_Kilobytes(t *testing.T) {
	assert.Equal(t, "1 KB", formatSize(1024))
	assert.Equal(t, "1023 KB", formatSize(1024*1024-1))
}

func TestFormatSize_Megabytes(t *testing.T) {
	assert.Equal(t, "1 MB", formatSize(1024*1024))
	assert.Equal(t, "1023 MB", formatSize(1024*1024*1024-1))
}

func TestFormatSize_Gigabytes(t *testing.T) {
	assert.Equal(t, "1 GB", formatSize(1024*1024*1024))
	assert.Equal(t, "10 GB", formatSize(10*1024*1024*1024))
}

// --- renderJSON ---

func TestRenderJSON_EmptyProducesArray(t *testing.T) {
	// json.Marshal of an empty non-nil slice must produce "[]" not "null"
	out := make([]fileRowJSON, 0)
	data, err := json.Marshal(out)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestRenderJSON_CreatedAtIsRFC3339UTC(t *testing.T) {
	ts, err := time.Parse(time.RFC3339, "2026-06-29T08:10:42Z")
	require.NoError(t, err)

	rows := []fileRow{{
		FileDataID: "abc-123",
		Source:     "workstation",
		Type:       "f",
		Path:       "/var/log/test",
		Timestamp:  1782605538,
		Size:       4096,
		Chunks:     3,
		Versions:   2,
		CreatedAt:  ts,
	}}
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
	data, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `"file_data_id": "abc-123"`)
	assert.Contains(t, s, `"source": "workstation"`)
	assert.Contains(t, s, `"created_at": "2026-06-29T08:10:42Z"`)
	assert.Contains(t, s, `"timestamp": 1782605538`)
}

// --- renderTable ---

func TestRenderTable_EmptyDoesNotError(t *testing.T) {
	// renderTable writes to os.Stdout; we just verify it doesn't error on nil/empty input
	err := renderTable(nil)
	assert.NoError(t, err)

	err = renderTable([]fileRow{})
	assert.NoError(t, err)
}
