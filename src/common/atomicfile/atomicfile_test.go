package atomicfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrite_CreatesParentDirectoryIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.json")

	require.NoError(t, Write(path, []byte(`{"a":1}`)))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(data))
}

func TestWrite_OverwritesPreviousValueAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.json")

	require.NoError(t, Write(path, []byte("first")))
	require.NoError(t, Write(path, []byte("second")))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second", string(data))

	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err), "no leftover temp file after a successful write")
}
