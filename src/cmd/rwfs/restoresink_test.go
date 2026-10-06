package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func recordingHooks(l *eventLog) commitHooks {
	return commitHooks{
		Sync:      func(f *os.File) error { l.add("sync:" + filepath.Base(f.Name())); return nil },
		DropCache: func(f *os.File) { l.add("drop:" + filepath.Base(f.Name())) },
		Rename: func(o, n string) error {
			l.add("rename:" + filepath.Base(n))
			return os.Rename(o, n)
		},
		SyncDir: func(d string) error { l.add("syncdir:" + filepath.Base(d)); return nil },
	}
}

// newPending creates a real temp file in dir holding data, as
// writeRestoreFile would hand it over.
func newPending(t *testing.T, dir, name string, data string) pendingFile {
	t.Helper()
	f, err := os.CreateTemp(dir, "."+name+".mptmp-")
	require.NoError(t, err)
	_, err = f.WriteString(data)
	require.NoError(t, err)
	return pendingFile{File: f, TmpPath: f.Name(), DestPath: filepath.Join(dir, name), Bytes: int64(len(data))}
}

func TestCommitQueue_BatchesByFileCount(t *testing.T) {
	dir := t.TempDir()
	log := &eventLog{}
	q := newCommitQueue(commitLimits{Files: 3, Bytes: 1 << 30}, recordingHooks(log))

	require.NoError(t, q.Add(newPending(t, dir, "a", "1")))
	require.NoError(t, q.Add(newPending(t, dir, "b", "2")))
	assert.Empty(t, log.snapshot(), "no checkpoint before the limit")
	assert.NoFileExists(t, filepath.Join(dir, "a"))

	require.NoError(t, q.Add(newPending(t, dir, "c", "3")))
	assert.FileExists(t, filepath.Join(dir, "a"))
	assert.FileExists(t, filepath.Join(dir, "c"))
	files, bytes := q.Stats()
	assert.Equal(t, 3, files)
	assert.EqualValues(t, 3, bytes)
}

func TestCommitQueue_BatchesByBytes(t *testing.T) {
	dir := t.TempDir()
	q := newCommitQueue(commitLimits{Files: 100, Bytes: 5}, recordingHooks(&eventLog{}))
	require.NoError(t, q.Add(newPending(t, dir, "a", "123")))
	assert.NoFileExists(t, filepath.Join(dir, "a"))
	require.NoError(t, q.Add(newPending(t, dir, "b", "456")))
	assert.FileExists(t, filepath.Join(dir, "a"))
	assert.FileExists(t, filepath.Join(dir, "b"))
}

func TestCommitQueue_ZeroLimitFlushesEveryFile(t *testing.T) {
	dir := t.TempDir()
	q := newCommitQueue(commitLimits{Files: 0, Bytes: 0}, recordingHooks(&eventLog{}))
	require.NoError(t, q.Add(newPending(t, dir, "a", "1")))
	assert.FileExists(t, filepath.Join(dir, "a"))
}

func TestCommitQueue_FlushCommitsRemainder(t *testing.T) {
	dir := t.TempDir()
	q := newCommitQueue(commitLimits{Files: 100, Bytes: 1 << 30}, recordingHooks(&eventLog{}))
	require.NoError(t, q.Add(newPending(t, dir, "a", "1")))
	require.NoError(t, q.Flush())
	assert.FileExists(t, filepath.Join(dir, "a"))
	require.NoError(t, q.Flush(), "flushing an empty queue is a no-op")
}

func TestCommitQueue_CheckpointOrdering(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	log := &eventLog{}
	q := newCommitQueue(commitLimits{Files: 100, Bytes: 1 << 30}, recordingHooks(log))
	require.NoError(t, q.Add(newPending(t, d1, "a", "1")))
	require.NoError(t, q.Add(newPending(t, d1, "b", "2")))
	require.NoError(t, q.Add(newPending(t, d2, "c", "3")))
	require.NoError(t, q.Flush())

	ev := log.snapshot()
	lastSync, firstRename, lastRename, firstSyncDir := -1, len(ev), -1, len(ev)
	syncDirs := 0
	for i, e := range ev {
		switch {
		case strings.HasPrefix(e, "sync:"):
			lastSync = i
		case strings.HasPrefix(e, "rename:"):
			firstRename = min(firstRename, i)
			lastRename = i
		case strings.HasPrefix(e, "syncdir:"):
			firstSyncDir = min(firstSyncDir, i)
			syncDirs++
		}
	}
	assert.Less(t, lastSync, firstRename, "every fsync precedes any rename: %v", ev)
	assert.Less(t, lastRename, firstSyncDir, "directory syncs follow all renames: %v", ev)
	assert.Equal(t, 2, syncDirs, "one dir sync per distinct parent directory: %v", ev)
}

func TestCommitQueue_RenameFailureRemovesRemainingTempFiles(t *testing.T) {
	dir := t.TempDir()
	hooks := recordingHooks(&eventLog{})
	calls := 0
	hooks.Rename = func(o, n string) error {
		calls++
		if calls == 2 {
			return os.ErrPermission
		}
		return os.Rename(o, n)
	}
	q := newCommitQueue(commitLimits{Files: 100, Bytes: 1 << 30}, hooks)
	require.NoError(t, q.Add(newPending(t, dir, "a", "1")))
	require.NoError(t, q.Add(newPending(t, dir, "b", "2")))
	require.NoError(t, q.Add(newPending(t, dir, "c", "3")))

	err := q.Flush()
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(dir, "a"), "already-renamed file stays")
	assert.NoFileExists(t, filepath.Join(dir, "b"))
	assert.NoFileExists(t, filepath.Join(dir, "c"))
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "no temp files left behind")
}

func TestCommitQueue_AbortRemovesPendingAndRejectsLaterAdds(t *testing.T) {
	dir := t.TempDir()
	q := newCommitQueue(commitLimits{Files: 100, Bytes: 1 << 30}, recordingHooks(&eventLog{}))
	require.NoError(t, q.Add(newPending(t, dir, "a", "1")))

	q.Abort()
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)

	err := q.Add(newPending(t, dir, "b", "2"))
	assert.ErrorIs(t, err, errCommitAborted)
	entries, _ = os.ReadDir(dir)
	assert.Empty(t, entries, "a late add's temp file is removed too")
}

func TestCommitQueue_ConcurrentAddsCommitEverything(t *testing.T) {
	dir := t.TempDir()
	q := newCommitQueue(commitLimits{Files: 4, Bytes: 1 << 30}, recordingHooks(&eventLog{}))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := string(rune('a' + i))
			assert.NoError(t, q.Add(newPending(t, dir, name, "x")))
		}(i)
	}
	wg.Wait()
	require.NoError(t, q.Flush())
	files, _ := q.Stats()
	assert.Equal(t, 20, files)
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 20)
}
