package listformat

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatSize_Bytes(t *testing.T) {
	assert.Equal(t, "0 B", FormatSize(0))
	assert.Equal(t, "1 B", FormatSize(1))
	assert.Equal(t, "1023 B", FormatSize(1023))
}

func TestFormatSize_Kilobytes(t *testing.T) {
	assert.Equal(t, "1 KB", FormatSize(1024))
	assert.Equal(t, "1023 KB", FormatSize(1024*1024-1))
}

func TestFormatSize_Megabytes(t *testing.T) {
	assert.Equal(t, "1 MB", FormatSize(1024*1024))
	assert.Equal(t, "1023 MB", FormatSize(1024*1024*1024-1))
}

func TestFormatSize_Gigabytes(t *testing.T) {
	assert.Equal(t, "1 GB", FormatSize(1024*1024*1024))
	assert.Equal(t, "10 GB", FormatSize(10*1024*1024*1024))
}

func TestRenderTable_EmptyDoesNotError(t *testing.T) {
	err := RenderTable(nil)
	assert.NoError(t, err)

	err = RenderTable([]Row{})
	assert.NoError(t, err)
}

func TestRenderJSON_EmptyProducesArray(t *testing.T) {
	err := RenderJSON([]Row{})
	assert.NoError(t, err)
}

func TestRenderJSON_CreatedAtIsRFC3339UTC(t *testing.T) {
	ts, err := time.Parse(time.RFC3339, "2026-06-29T08:10:42Z")
	require.NoError(t, err)

	rows := []Row{{
		FileUUID:  "abc-123",
		Source:    "workstation",
		Type:      "f",
		Path:      "/var/log/test",
		Timestamp: 1782605538,
		Size:      4096,
		Chunks:    3,
		Versions:  2,
		CreatedAt: ts,
	}}

	out := toJSONRows(rows)
	data, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `"file_uuid": "abc-123"`)
	assert.Contains(t, s, `"source": "workstation"`)
	assert.Contains(t, s, `"created_at": "2026-06-29T08:10:42Z"`)
	assert.Contains(t, s, `"timestamp": 1782605538`)
}

func sampleRows() []Row {
	ts, _ := time.Parse(time.RFC3339, "2026-06-29T08:10:42Z")
	return []Row{
		{FileUUID: "u1", Source: "ws", Type: "f", Path: "/a", Timestamp: 1, Size: 10, Chunks: 1, Versions: 1, CreatedAt: ts},
		{FileUUID: "u2", Source: "ws", Type: "f", Path: "/bb", Timestamp: 2, Size: 2048, Chunks: 2, Versions: 3, CreatedAt: ts},
	}
}

// The table without damaged rows must stay byte-for-byte what it always was,
// so scripts parsing it keep working.
func TestWriteTable_NoDamagedRowsKeepsTheExistingLayout(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, writeTable(&buf, sampleRows()))

	want := "" +
		"SOURCE  TYPE  PATH  TIMESTAMP  SIZE  CHUNKS  VERSIONS\n" +
		"ws      f     /a    1          10 B  1       1\n" +
		"ws      f     /bb   2          2 KB  2       3\n"
	assert.Equal(t, want, buf.String())
}

func TestWriteTable_DamagedRowGetsAMarkerColumn(t *testing.T) {
	rows := sampleRows()
	rows[1].Damaged = true
	var buf bytes.Buffer
	require.NoError(t, writeTable(&buf, rows))

	want := "" +
		"SOURCE  TYPE  PATH  TIMESTAMP  SIZE  CHUNKS  VERSIONS  DAMAGED\n" +
		"ws      f     /a    1          10 B  1       1         \n" +
		"ws      f     /bb   2          2 KB  2       3         yes\n"
	assert.Equal(t, want, buf.String())
}

func TestWriteJSON_DamagedIsSetOnlyOnDamagedRows(t *testing.T) {
	rows := sampleRows()
	rows[1].Damaged = true
	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, rows))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got, 2)
	assert.NotContains(t, got[0], "damaged", "a healthy row omits the field")
	assert.Equal(t, true, got[1]["damaged"])
}
