package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCreateTempFile_SameDirectoryHiddenAndMatchesPattern(t *testing.T) {
	dir := t.TempDir()
	f, err := createTempFile(filepath.Join(dir, "a.txt"))
	require.NoError(t, err)
	defer f.Close()

	assert.Equal(t, dir, filepath.Dir(f.Name()))
	base := filepath.Base(f.Name())
	assert.True(t, strings.HasPrefix(base, ".a.txt.mptmp-"), base)
	assert.True(t, isTempName(base), base)
}

func TestCreateTempFile_TwoCallsGetDistinctNames(t *testing.T) {
	dir := t.TempDir()
	f1, err := createTempFile(filepath.Join(dir, "a"))
	require.NoError(t, err)
	defer f1.Close()
	f2, err := createTempFile(filepath.Join(dir, "a"))
	require.NoError(t, err)
	defer f2.Close()
	assert.NotEqual(t, f1.Name(), f2.Name())
}

func TestCreateTempFile_LongNameStillFits(t *testing.T) {
	dir := t.TempDir()
	f, err := createTempFile(filepath.Join(dir, strings.Repeat("x", 255)))
	require.NoError(t, err)
	defer f.Close()
	assert.LessOrEqual(t, len(filepath.Base(f.Name())), 255)
}

func TestCreateTempFile_MissingDirectoryFails(t *testing.T) {
	_, err := createTempFile(filepath.Join(t.TempDir(), "nope", "a"))
	require.Error(t, err)
}

func TestIsTempName(t *testing.T) {
	assert.True(t, isTempName(".a.txt.mptmp-0123abcd"))
	assert.False(t, isTempName("a.txt"))
	assert.False(t, isTempName("a.mptmp-0123abcd"), "must be hidden")
	assert.False(t, isTempName(".a.mptmp-0123abc"), "suffix must be 8 hex digits")
	assert.False(t, isTempName(".a.mptmp-0123abcz"), "suffix must be hex")
	assert.False(t, isTempName(".mptmp-0123abcd"), "needs a base name")
	assert.True(t, isTempName(".a.mptmp-deadbeef"))
	assert.False(t, isTempName(".a.mptmp-DEADBEEF"), "must be lowercase hex")
}

func TestSweepStaleTemp_RemovesOnlyTempFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".a.txt.mptmp-deadbeef")
	keepReal := filepath.Join(dir, "a.txt")
	keepLookalike := filepath.Join(dir, ".b.mptmp-zzzzzzzz")
	for _, p := range []string{stale, keepReal, keepLookalike} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}

	n := sweepStaleTemp(discardLogger(), []string{dir, dir, filepath.Join(dir, "missing")})

	assert.Equal(t, 1, n)
	assert.NoFileExists(t, stale)
	assert.FileExists(t, keepReal)
	assert.FileExists(t, keepLookalike)
}

func TestDestDirs_Unique(t *testing.T) {
	got := destDirs([]restoreFile{
		{DestPath: "/x/a"}, {DestPath: "/x/b"}, {DestPath: "/y/c"},
	})
	assert.ElementsMatch(t, []string{"/x", "/y"}, got)
}
