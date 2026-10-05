package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrfsArgs(t *testing.T) {
	a := &Args{Streams: 4}
	assert.Equal(t, []string{"/src", "--destination", "localhost:99", "--quiet", "--streams", "4"},
		brfsArgs(a, "/src", "localhost:99"))

	a = &Args{Streams: 2, Window: 8, BrfsArgs: []string{"--debug"}}
	assert.Equal(t, []string{"/src", "--destination", "localhost:99", "--quiet", "--streams", "2", "--window", "8", "--debug"},
		brfsArgs(a, "/src", "localhost:99"))
}

func TestRwfsArgs(t *testing.T) {
	a := &Args{Streams: 3, RwfsArgs: []string{"--retries", "1"}}
	assert.Equal(t, []string{"restore", "localhost:99", "--rules-stdin", "--quiet", "--streams", "3", "--retries", "1"},
		rwfsArgs(a, "localhost:99"))
}

func TestRestoreRules(t *testing.T) {
	assert.JSONEq(t,
		`{"rules":[{"host":"","path":"/work/src","include":true,"dest_path":"/work/restored"}]}`,
		restoreRules("/work/src", "/work/restored"))
}

func TestCheckBinaries(t *testing.T) {
	dir := t.TempDir()
	assert.Error(t, checkBinaries(dir), "nothing there")

	for _, n := range []string{"brfs", "bwfs"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o755))
	}
	err := checkBinaries(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rwfs")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "rwfs"), []byte("x"), 0o755))
	assert.NoError(t, checkBinaries(dir))
}
