package listformat

import (
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

	out := toJSONRows(rows)
	data, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `"file_data_id": "abc-123"`)
	assert.Contains(t, s, `"source": "workstation"`)
	assert.Contains(t, s, `"created_at": "2026-06-29T08:10:42Z"`)
	assert.Contains(t, s, `"timestamp": 1782605538`)
}
