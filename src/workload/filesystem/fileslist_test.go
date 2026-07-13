package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pathPresent checks whether any discovered FileInfo's ID (format
// fs://host:type:path:mtime) carries absPath as its path field. The path
// field is bounded by ":" on both sides, so this substring check can't
// false-positive against ":"+"<longer path with absPath as prefix>".
func pathPresent(files FilesList, absPath string) bool {
	needle := ":" + absPath + ":"
	for _, f := range files {
		if strings.Contains(f.ID(), needle) {
			return true
		}
	}
	return false
}

func TestDiscover_NoExcludePatterns_IncludesEverything(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "nested.txt"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*"}, nil)
	require.NoError(t, err)

	assert.Len(t, files, 4) // root, keep.txt, sub, sub/nested.txt
	assert.True(t, pathPresent(files, root))
	assert.True(t, pathPresent(files, filepath.Join(root, "keep.txt")))
	assert.True(t, pathPresent(files, filepath.Join(root, "sub")))
	assert.True(t, pathPresent(files, filepath.Join(root, "sub", "nested.txt")))
}

func TestDiscover_ExcludeBasenamePattern_MatchesAtAnyDepth(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "skip.tmp"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "nested.tmp"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*"}, []string{"*.tmp"})
	require.NoError(t, err)

	assert.True(t, pathPresent(files, filepath.Join(root, "keep.txt")))
	assert.True(t, pathPresent(files, filepath.Join(root, "sub")), "directory itself must survive; only the .tmp file inside is excluded")
	assert.False(t, pathPresent(files, filepath.Join(root, "skip.tmp")))
	assert.False(t, pathPresent(files, filepath.Join(root, "sub", "nested.tmp")))
}

func TestDiscover_ExcludeDirectory_PrunesEntireSubtree(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "excluded_dir", "deeper"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "excluded_dir", "inner.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "excluded_dir", "deeper", "deepfile.txt"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*"}, []string{"excluded_dir"})
	require.NoError(t, err)

	assert.Len(t, files, 2) // root, keep.txt
	assert.True(t, pathPresent(files, root))
	assert.True(t, pathPresent(files, filepath.Join(root, "keep.txt")))
	assert.False(t, pathPresent(files, filepath.Join(root, "excluded_dir")))
	assert.False(t, pathPresent(files, filepath.Join(root, "excluded_dir", "inner.txt")))
	assert.False(t, pathPresent(files, filepath.Join(root, "excluded_dir", "deeper", "deepfile.txt")))
}

func TestDiscover_ExcludeRelativePathPattern_MatchesOnlyThatDepth(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "skip.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b", "skip.txt"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*"}, []string{"a/skip.txt"})
	require.NoError(t, err)

	assert.False(t, pathPresent(files, filepath.Join(root, "a", "skip.txt")))
	assert.True(t, pathPresent(files, filepath.Join(root, "b", "skip.txt")))
	assert.True(t, pathPresent(files, filepath.Join(root, "a")))
	assert.True(t, pathPresent(files, filepath.Join(root, "b")))
}

func TestDiscover_IncludeFiltersFilesButNotDirectories(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.log"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "deep.log"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*.log"}, nil)
	require.NoError(t, err)

	assert.True(t, pathPresent(files, root), "root directory is never filtered by include")
	assert.True(t, pathPresent(files, filepath.Join(root, "sub")), "sub directory is never filtered by include, even though its name doesn't match *.log")
	assert.True(t, pathPresent(files, filepath.Join(root, "app.log")))
	assert.True(t, pathPresent(files, filepath.Join(root, "sub", "deep.log")))
	assert.False(t, pathPresent(files, filepath.Join(root, "notes.txt")))
}

func TestDiscover_ExcludeTakesPrecedenceOverInclude(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.log"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*.log"}, []string{"keep.log"})
	require.NoError(t, err)

	assert.Len(t, files, 1) // root only
	assert.True(t, pathPresent(files, root))
	assert.False(t, pathPresent(files, filepath.Join(root, "keep.log")))
}

func TestDiscover_NonexistentRootReturnsError(t *testing.T) {
	_, err := Discover(filepath.Join(t.TempDir(), "does-not-exist"), []string{"*"}, nil)
	assert.Error(t, err)
}
