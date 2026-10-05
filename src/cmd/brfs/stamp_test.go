package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/retention"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

func discover(t *testing.T, root string) map[string]filesystem.FileInfo {
	t.Helper()
	list, err := filesystem.Discover(root, []string{"*"}, nil)
	require.NoError(t, err)
	out := map[string]filesystem.FileInfo{}
	for _, f := range list {
		out[f.Path()] = f
	}
	return out
}

func TestStamper_AppliesFirstMatchingRowPerFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tmp"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tmp", "a.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.txt"), []byte("x"), 0o644))

	p := filepath.Join(t.TempDir(), "m.json")
	require.NoError(t, retention.WriteFile(p, retention.Matrix{
		{Prefix: "tmp", KeepSeconds: 100},
		{Prefix: "", KeepSeconds: 1000},
	}))
	m, err := retention.LoadFile(p)
	require.NoError(t, err)
	st := &stamper{m: m, root: root}
	files := discover(t, root)
	now := time.Unix(5000, 0)

	assert.Equal(t, int64(5100), st.expireAt(files[filepath.Join(root, "tmp", "a.txt")], now))
	assert.Equal(t, int64(6000), st.expireAt(files[filepath.Join(root, "b.txt")], now))
}

func TestStamper_NilSendsZero(t *testing.T) {
	var st *stamper
	assert.Equal(t, int64(0), st.expireAt(filesystem.FileInfo{}, time.Now()))
}
