package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

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

// bwfs list must show which versions are damaged, so an operator can see the
// loss before a restore hits it; a healed (re-uploaded) file is healthy again.
func TestQueryFileRows_MarksDamagedRows(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/data/ok.txt:1000", 10, []byte{1}, "job1", 5000)
	seedDamagedFile(t, store, "fs://hosta:f:/data/lost.txt:1000", "job1", 5000)
	const healedID = "fs://hosta:f:/data/healed.txt:1000"
	seedDamagedFile(t, store, healedID, "job1", 5000)
	reuploadFile(t, store, healedID)

	rows, err := queryFileRows(store, "", "", "")
	require.NoError(t, err)

	damaged := map[string]bool{}
	for _, r := range rows {
		damaged[r.Path] = r.Damaged
	}
	assert.Equal(t, map[string]bool{
		"/data/ok.txt":     false,
		"/data/lost.txt":   true,
		"/data/healed.txt": false,
	}, damaged)
}
