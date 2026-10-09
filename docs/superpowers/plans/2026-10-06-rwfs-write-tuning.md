# rwfs Write-Path Tuning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `rwfs restore` write files via temp file + deferred, batched, durable atomic commit, with `fallocate` preallocation, early writeback and page-cache hygiene — then measure it with `mpbench`.

**Architecture:** `writeRestoreFile` writes a hidden temp file next to the destination and hands it, verified, to a shared per-run `commitQueue`. The queue batches files and, at a checkpoint, fsyncs, drops cache, closes, renames into place and fsyncs each parent directory once. OS-specific syscalls live in `restoreio_{linux,windows}.go`.

**Tech Stack:** Go 1.26, `golang.org/x/sys/unix` (already in `src/go.mod`), testify, `mpbench`.

**Spec:** `docs/superpowers/specs/2026-10-06-rwfs-write-tuning-design.md`

## Global Constraints

- All Go commands run from `/home/alex/miniprotector/src` (that is where `go.mod` lives). Work on branch `restore-write-tuning`.
- No proto change, no new CLI flags. Config keys only: `restore_commit_files` (default `64`, range 0–1024, `0` = flush after every file) and `restore_commit_bytes` (default `67108864`, ≥ 0, `0` = flush after every file).
- Temp file name: `.<name>.mptmp-<8 hex>` in the destination's own directory.
- A file must never exist at its final path before it is durable; `--overwrite` must never destroy the old file before the new one is verified and durable.
- Windows must still build (`GOOS=windows go build ./cmd/rwfs`): Windows preallocates with `Truncate`; writeback, cache drop and dir sync are no-ops.
- Existing behavior stays: first failure aborts the run and cancels in-flight transfers; retries only for network/RPC-facing errors; skip-existing; directory at destination is a hard error.
- Every commit message ends with:
  ```
  Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr
  ```
- Per `.claude/CLAUDE.md`: update `docs/components/rwfs.md`, add a `CHANGELOG.md` entry before merge.

---

### Task 1: Config keys

**Files:**
- Modify: `src/common/config/config.go` (struct near line 135, defaults near line 200, parse switch near line 545)
- Test: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.RestoreCommitFiles int`, `config.Config.RestoreCommitBytes int64`, exported const `config.MaxRestoreCommitFiles = 1024`.

- [ ] **Step 1: Write the failing tests** (append to `config_test.go`)

```go
func writeTestConf(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n" + extra
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestParseConfig_RestoreCommitDefaults(t *testing.T) {
	conf, err := ParseConfig(writeTestConf(t, ""))
	require.NoError(t, err)
	assert.Equal(t, 64, conf.RestoreCommitFiles)
	assert.EqualValues(t, 64<<20, conf.RestoreCommitBytes)
}

func TestParseConfig_RestoreCommitParsesCorrectly(t *testing.T) {
	conf, err := ParseConfig(writeTestConf(t, "restore_commit_files=0\nrestore_commit_bytes=1048576\n"))
	require.NoError(t, err)
	assert.Equal(t, 0, conf.RestoreCommitFiles)
	assert.EqualValues(t, 1048576, conf.RestoreCommitBytes)
}

func TestParseConfig_RestoreCommitRejectsInvalid(t *testing.T) {
	for _, extra := range []string{
		"restore_commit_files=-1\n",
		"restore_commit_files=1025\n",
		"restore_commit_files=abc\n",
		"restore_commit_bytes=-5\n",
		"restore_commit_bytes=abc\n",
	} {
		_, err := ParseConfig(writeTestConf(t, extra))
		require.Error(t, err, extra)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/alex/miniprotector/src && go test ./common/config/ -run RestoreCommit`
Expected: FAIL (compile error: `conf.RestoreCommitFiles undefined`).

- [ ] **Step 3: Implement**

In the `Config` struct after `RwfsRetries int`:

```go
	RestoreCommitFiles               int
	RestoreCommitBytes               int64
```

Add above or beside the other bounds consts:

```go
// MaxRestoreCommitFiles caps restore_commit_files: every pending file holds
// its descriptor open until the checkpoint.
const MaxRestoreCommitFiles = 1024
```

In the defaults literal after `RwfsRetries: 3,`:

```go
		RestoreCommitFiles:               64,
		RestoreCommitBytes:               64 << 20,
```

In the parse switch, before `default:`:

```go
		case "restore_commit_files":
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 || number > MaxRestoreCommitFiles {
				return nil, fmt.Errorf("invalid restore_commit_files value at line %d: %s (must be 0-%d)", lineNum, value, MaxRestoreCommitFiles)
			}
			config.RestoreCommitFiles = number
		case "restore_commit_bytes":
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number < 0 {
				return nil, fmt.Errorf("invalid restore_commit_bytes value at line %d: %s (must be >= 0)", lineNum, value)
			}
			config.RestoreCommitBytes = number
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /home/alex/miniprotector/src && go test ./common/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector && git add src/common/config && git commit -m "feat(config): restore_commit_files and restore_commit_bytes

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 2: Per-OS I/O hooks

**Files:**
- Create: `src/cmd/rwfs/restoreio_linux.go`, `src/cmd/rwfs/restoreio_windows.go`
- Test: `src/cmd/rwfs/restoreio_linux_test.go`

**Interfaces:**
- Produces (both OSes, same signatures):
  - `osPreallocate(f *os.File, size int64) error` — reserve `size` bytes and set the file length to `size`; `size <= 0` is a no-op. Linux: `fallocate`, falls back to `Truncate` on `EOPNOTSUPP`/`ENOSYS`; a real `ENOSPC` is returned as an error wrapping `syscall.ENOSPC`.
  - `osStartWriteback(f *os.File, off, n int64)` — best-effort, non-blocking start of writeback for the range; never errors.
  - `osDropCache(f *os.File)` — best-effort drop of the file's clean cached pages; call only after `Sync`.
  - `osSyncDir(dir string) error` — make directory entries durable.

- [ ] **Step 1: Write the failing tests**

```go
//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOsPreallocate_SetsSizeAndAllocatesBlocks(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, osPreallocate(f, 1<<20))

	info, err := f.Stat()
	require.NoError(t, err)
	assert.EqualValues(t, 1<<20, info.Size())
	st := info.Sys().(*syscall.Stat_t)
	assert.Greater(t, st.Blocks, int64(0), "fallocate should allocate blocks, not make a hole")
}

func TestOsPreallocate_ZeroSizeIsNoOp(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, osPreallocate(f, 0))
}

func TestOsPreallocate_FullFilesystemFailsWithENOSPC(t *testing.T) {
	// A tmpfs cannot satisfy an absurd reservation; skip where /dev/shm is
	// missing or the kernel reports something other than ENOSPC.
	f, err := os.CreateTemp("/dev/shm", "mptest")
	if err != nil {
		t.Skip("no /dev/shm")
	}
	defer os.Remove(f.Name())
	defer f.Close()

	err = osPreallocate(f, 1<<50)
	if err == nil || !errors.Is(err, syscall.ENOSPC) {
		t.Skipf("kernel did not report ENOSPC (got %v)", err)
	}
}

func TestOsStartWritebackAndDropCache_DoNotPanicOrCorrupt(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Write([]byte("hello"))
	require.NoError(t, err)

	osStartWriteback(f, 0, 5)
	require.NoError(t, f.Sync())
	osDropCache(f)

	got, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestOsSyncDir(t *testing.T) {
	require.NoError(t, osSyncDir(t.TempDir()))
	assert.Error(t, osSyncDir(filepath.Join(t.TempDir(), "missing")))
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/rwfs/ -run 'TestOs'`
Expected: FAIL (`undefined: osPreallocate`).

- [ ] **Step 3: Implement `restoreio_linux.go`**

```go
//go:build linux

// restoreio_linux.go holds the Linux write-path hooks for rwfs restore:
// fallocate preallocation, early writeback, page-cache hygiene and
// directory fsync. See
// docs/superpowers/specs/2026-10-06-rwfs-write-tuning-design.md.
package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// osPreallocate reserves size bytes with fallocate (mode 0 also sets the
// file length). A filesystem without fallocate support degrades to
// Truncate; any other error -- notably ENOSPC -- is returned so a full
// disk fails the file before a single chunk is consumed.
func osPreallocate(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	for {
		err := unix.Fallocate(int(f.Fd()), 0, 0, size)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOSYS):
			return f.Truncate(size)
		default:
			return fmt.Errorf("fallocate %d bytes: %w", size, err)
		}
	}
}

// osStartWriteback asks the kernel to begin writing [off, off+n) back
// without waiting for it. Best-effort: a failure only means the later
// fsync does more work.
func osStartWriteback(f *os.File, off, n int64) {
	_ = unix.SyncFileRange(int(f.Fd()), off, n, unix.SYNC_FILE_RANGE_WRITE)
}

// osDropCache evicts f's clean pages so a large restore does not push the
// user's working set out of the page cache. Only effective after Sync.
func osDropCache(f *os.File) {
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}

// osSyncDir fsyncs a directory so renames into it survive a crash.
func osSyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
```

- [ ] **Step 4: Implement `restoreio_windows.go`**

```go
//go:build windows

// restoreio_windows.go is the Windows counterpart of restoreio_linux.go:
// preallocation is a plain Truncate, and the writeback, cache and
// directory-sync hooks are no-ops (NTFS journals metadata, and
// os.File.Sync maps to FlushFileBuffers).
package main

import "os"

func osPreallocate(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return f.Truncate(size)
}

func osStartWriteback(*os.File, int64, int64) {}

func osDropCache(*os.File) {}

func osSyncDir(string) error { return nil }
```

- [ ] **Step 5: Run to verify pass and Windows build**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/rwfs/ -run 'TestOs' -v && GOOS=windows go vet ./cmd/rwfs/`
Expected: PASS (ENOSPC test may SKIP); vet clean. (Windows vet only compiles after Task 3–5 are done if other symbols are missing; at this task it should pass since the hooks are unused.)

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector && git add src/cmd/rwfs/restoreio_*.go && git commit -m "feat(rwfs): per-OS write hooks (fallocate, writeback, cache drop, dir sync)

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 3: commitQueue

**Files:**
- Create: `src/cmd/rwfs/restoresink.go`
- Test: `src/cmd/rwfs/restoresink_test.go`

**Interfaces:**
- Consumes: `osDropCache`, `osSyncDir` from Task 2.
- Produces:
  - `type commitLimits struct { Files int; Bytes int64 }`; package var `restoreCommit commitLimits` (defaults `{64, 64<<20}`); `setRestoreCommitLimits(files int, bytes int64)`.
  - `type commitHooks struct { Sync func(*os.File) error; DropCache func(*os.File); Rename func(oldpath, newpath string) error; SyncDir func(dir string) error }`; `defaultCommitHooks() commitHooks`.
  - `type pendingFile struct { File *os.File; TmpPath, DestPath string; Bytes int64 }`
  - `newCommitQueue(limits commitLimits, hooks commitHooks) *commitQueue`
  - `(*commitQueue).Add(p pendingFile) error` — queue takes ownership of `p.File` and the temp file on every path (success, flush failure, aborted). Flushes when `len(pending) >= Files || pendingBytes >= Bytes` (so a `0` limit flushes every file).
  - `(*commitQueue).Flush() error`, `(*commitQueue).Abort()`, `(*commitQueue).Stats() (files int, bytes int64)`; `errCommitAborted`.

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/rwfs/ -run TestCommitQueue`
Expected: FAIL (`undefined: newCommitQueue`).

- [ ] **Step 3: Implement `restoresink.go`**

```go
// restoresink.go implements the deferred, batched, durable commit of
// restored files: writeRestoreFile hands each verified temp file to a
// per-run commitQueue, which at a checkpoint fsyncs, drops cache, closes,
// renames into place and fsyncs each parent directory once. A file only
// appears under its real name once it is durable. See
// docs/superpowers/specs/2026-10-06-rwfs-write-tuning-design.md.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// commitLimits triggers a checkpoint when pending files >= Files or pending
// bytes >= Bytes. A zero limit therefore flushes after every file.
type commitLimits struct {
	Files int
	Bytes int64
}

// restoreCommit is the process-wide checkpoint policy, set once at start-up
// from config (setRestoreCommitLimits), like connection.SetFlowControlWindow.
var restoreCommit = commitLimits{Files: 64, Bytes: 64 << 20}

func setRestoreCommitLimits(files int, bytes int64) {
	restoreCommit = commitLimits{Files: files, Bytes: bytes}
}

// commitHooks are the side effects of a checkpoint, injectable for tests.
type commitHooks struct {
	Sync      func(*os.File) error
	DropCache func(*os.File)
	Rename    func(oldpath, newpath string) error
	SyncDir   func(dir string) error
}

func defaultCommitHooks() commitHooks {
	return commitHooks{
		Sync:      func(f *os.File) error { return f.Sync() },
		DropCache: osDropCache,
		Rename:    os.Rename,
		SyncDir:   osSyncDir,
	}
}

// pendingFile is a fully written and verified temp file awaiting commit.
type pendingFile struct {
	File     *os.File
	TmpPath  string
	DestPath string
	Bytes    int64
}

var errCommitAborted = errors.New("restore commit aborted")

type commitQueue struct {
	limits commitLimits
	hooks  commitHooks

	flushMu sync.Mutex // serializes checkpoints

	mu             sync.Mutex // guards the fields below
	pending        []pendingFile
	pendingBytes   int64
	aborted        bool
	committedFiles int
	committedBytes int64
}

func newCommitQueue(limits commitLimits, hooks commitHooks) *commitQueue {
	return &commitQueue{limits: limits, hooks: hooks}
}

// discard closes and removes the temp files of ps (best-effort).
func discard(ps []pendingFile) {
	for _, p := range ps {
		p.File.Close()
		os.Remove(p.TmpPath)
	}
}

// Add takes ownership of p (its open file and temp path) on every path:
// the queue commits it, or removes it on failure or abort. It runs a
// checkpoint inline when a limit is reached and returns that checkpoint's
// error, which may concern other files in the same batch.
func (q *commitQueue) Add(p pendingFile) error {
	q.mu.Lock()
	if q.aborted {
		q.mu.Unlock()
		discard([]pendingFile{p})
		return errCommitAborted
	}
	q.pending = append(q.pending, p)
	q.pendingBytes += p.Bytes
	full := len(q.pending) >= q.limits.Files || q.pendingBytes >= q.limits.Bytes
	q.mu.Unlock()

	if full {
		return q.Flush()
	}
	return nil
}

// Flush commits everything currently pending.
func (q *commitQueue) Flush() error {
	q.flushMu.Lock()
	defer q.flushMu.Unlock()

	q.mu.Lock()
	batch := q.pending
	q.pending = nil
	q.pendingBytes = 0
	q.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}
	return q.commit(batch)
}

// commit runs one checkpoint: fsync + dropCache + close every file, rename
// each into place, then fsync each distinct parent directory once. Before
// the first rename a failure removes the whole batch; a failed rename
// removes the files not yet renamed, leaving earlier ones in place.
func (q *commitQueue) commit(batch []pendingFile) error {
	for _, p := range batch {
		if err := q.hooks.Sync(p.File); err != nil {
			discard(batch)
			return fmt.Errorf("fsync %s: %w", p.DestPath, err)
		}
		q.hooks.DropCache(p.File)
		if err := p.File.Close(); err != nil {
			discard(batch)
			return fmt.Errorf("close %s: %w", p.DestPath, err)
		}
	}

	dirs := map[string]struct{}{}
	for i, p := range batch {
		if err := q.hooks.Rename(p.TmpPath, p.DestPath); err != nil {
			discard(batch[i:])
			return fmt.Errorf("rename into place %s: %w", p.DestPath, err)
		}
		dirs[filepath.Dir(p.DestPath)] = struct{}{}
	}

	sorted := make([]string, 0, len(dirs))
	for d := range dirs {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	for _, d := range sorted {
		if err := q.hooks.SyncDir(d); err != nil {
			return fmt.Errorf("fsync directory %s: %w", d, err)
		}
	}

	var bytes int64
	for _, p := range batch {
		bytes += p.Bytes
	}
	q.mu.Lock()
	q.committedFiles += len(batch)
	q.committedBytes += bytes
	q.mu.Unlock()
	return nil
}

// Abort removes every pending temp file and makes later Adds fail with
// errCommitAborted (removing their temp files). Safe to call after a final
// successful Flush: nothing is pending then.
func (q *commitQueue) Abort() {
	q.mu.Lock()
	q.aborted = true
	batch := q.pending
	q.pending = nil
	q.pendingBytes = 0
	q.mu.Unlock()
	discard(batch)
}

// Stats reports files and bytes durably committed so far.
func (q *commitQueue) Stats() (files int, bytes int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.committedFiles, q.committedBytes
}
```

- [ ] **Step 4: Run to verify pass (with race detector)**

Run: `cd /home/alex/miniprotector/src && go test -race ./cmd/rwfs/ -run TestCommitQueue -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector && git add src/cmd/rwfs/restoresink*.go && git commit -m "feat(rwfs): commitQueue for deferred, batched, durable restore commits

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 4: Temp-file naming and stale sweep

**Files:**
- Create: `src/cmd/rwfs/restoretemp.go`
- Test: `src/cmd/rwfs/restoretemp_test.go`

**Interfaces:**
- Consumes: `defaultRestoreFilePerm` (restorefile.go).
- Produces:
  - `createTempFile(destPath string) (*os.File, error)` — exclusive-create `<dir>/.<base>.mptmp-<8 hex>` (base truncated to 200 bytes so the name fits `NAME_MAX`).
  - `isTempName(name string) bool`
  - `sweepStaleTemp(logger *slog.Logger, dirs []string) int` — removes regular files matching `isTempName` directly in each directory; ignores unreadable/missing dirs; returns count removed.
  - `destDirs(files []restoreFile) []string` — unique parent directories of the files' `DestPath`s.

- [ ] **Step 1: Write the failing tests**

```go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/rwfs/ -run 'TestCreateTempFile|TestIsTempName|TestSweepStaleTemp|TestDestDirs'`
Expected: FAIL (`undefined: createTempFile`).

- [ ] **Step 3: Implement `restoretemp.go`**

```go
// restoretemp.go names, creates and sweeps the hidden temp files restore
// writes before committing them (see restoresink.go).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	tempMarker      = ".mptmp-"
	tempSuffixLen   = 8   // hex digits after tempMarker
	maxTempBaseLen  = 200 // leaves room under NAME_MAX (255) for "." + marker + suffix
	tempCreateTries = 5
)

// createTempFile exclusively creates "<dir>/.<base>.mptmp-<8 hex>" next to
// destPath, so a later rename stays on one filesystem.
func createTempFile(destPath string) (*os.File, error) {
	dir, base := filepath.Split(destPath)
	if len(base) > maxTempBaseLen {
		base = base[:maxTempBaseLen]
	}
	for i := 0; i < tempCreateTries; i++ {
		var rnd [tempSuffixLen / 2]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, "."+base+tempMarker+hex.EncodeToString(rnd[:]))
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, defaultRestoreFilePerm)
		if err == nil || !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, fmt.Errorf("create temp file for %s: too many name collisions", destPath)
}

// isTempName reports whether name looks like a createTempFile name.
func isTempName(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	idx := strings.LastIndex(name, tempMarker)
	if idx < 2 { // needs "." plus at least one base character before the marker
		return false
	}
	suffix := name[idx+len(tempMarker):]
	if len(suffix) != tempSuffixLen {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

// destDirs returns the unique parent directories of the files' destinations.
func destDirs(files []restoreFile) []string {
	seen := make(map[string]struct{}, len(files))
	var dirs []string
	for _, f := range files {
		d := filepath.Dir(f.DestPath)
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		dirs = append(dirs, d)
	}
	return dirs
}

// sweepStaleTemp removes leftover temp files (from a crashed earlier run)
// directly inside each directory, so a rerun starts clean. Unreadable or
// missing directories are skipped. Returns how many files were removed.
func sweepStaleTemp(logger *slog.Logger, dirs []string) int {
	removed := 0
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !isTempName(e.Name()) {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if err := os.Remove(path); err == nil {
				removed++
				logger.Info("removed stale restore temp file", "path", path)
			}
		}
	}
	return removed
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/rwfs/ -run 'TestCreateTempFile|TestIsTempName|TestSweepStaleTemp|TestDestDirs' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector && git add src/cmd/rwfs/restoretemp*.go && git commit -m "feat(rwfs): restore temp-file naming and stale sweep

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 5: Wire into writeRestoreFile and restoreFileContent

**Files:**
- Modify: `src/cmd/rwfs/restorefile.go` (`writeRestoreFile`, `writeRestoreFileWithRetry`, doc comments)
- Modify: `src/cmd/rwfs/restore.go:190-260` (`restoreFileContent`)
- Modify: `src/cmd/rwfs/main.go` (call `setRestoreCommitLimits`)
- Test: `src/cmd/rwfs/restorefile_test.go` (update existing tests via helpers, add new), `src/cmd/rwfs/restore_test.go` (add new)

**Interfaces:**
- Consumes: `osPreallocate`, `osStartWriteback` (Task 2); `commitQueue`, `pendingFile`, `restoreCommit`, `setRestoreCommitLimits`, `newCommitQueue`, `defaultCommitHooks` (Task 3); `createTempFile`, `sweepStaleTemp`, `destDirs` (Task 4).
- Produces:
  - `writeRestoreFile(parent context.Context, client pb.RestoreServiceClient, f restoreFile, overwrite bool, q *commitQueue) restoreFileResult` — on success the result's `Bytes` is set and the file is **pending in q, not yet at `f.DestPath`**.
  - `writeRestoreFileWithRetry(ctx, logger, client, f, overwrite, maxRetries int, q *commitQueue) restoreFileResult`.

- [ ] **Step 1: Update existing tests through helpers, then add new failing tests**

Add to `restorefile_test.go`:

```go
// writeRestoreFileNow runs writeRestoreFile and then a final checkpoint, so
// tests that assert on the finished destination see the committed file.
func writeRestoreFileNow(ctx context.Context, client pb.RestoreServiceClient, f restoreFile, overwrite bool) restoreFileResult {
	q := newCommitQueue(commitLimits{Files: 1000, Bytes: 1 << 40}, defaultCommitHooks())
	r := writeRestoreFile(ctx, client, f, overwrite, q)
	if r.Err != nil {
		q.Abort()
		return r
	}
	if err := q.Flush(); err != nil {
		r.Err = err
	}
	return r
}

func writeRestoreFileWithRetryNow(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, f restoreFile, overwrite bool, maxRetries int) restoreFileResult {
	q := newCommitQueue(commitLimits{Files: 1000, Bytes: 1 << 40}, defaultCommitHooks())
	r := writeRestoreFileWithRetry(ctx, logger, client, f, overwrite, maxRetries, q)
	if r.Err != nil {
		q.Abort()
		return r
	}
	if err := q.Flush(); err != nil {
		r.Err = err
	}
	return r
}
```

Then redirect the existing call sites:

```bash
cd /home/alex/miniprotector/src/cmd/rwfs
sed -i 's/:= writeRestoreFile(context\./:= writeRestoreFileNow(context./; s/^\twriteRestoreFile(context\./\twriteRestoreFileNow(context./; s/writeRestoreFileWithRetry(context\./writeRestoreFileWithRetryNow(context./' restorefile_test.go
grep -n "writeRestoreFile" restorefile_test.go | grep -v "func \|Now("
```
Expected: the grep prints nothing (every call site uses a `Now` helper). Fix any remaining by hand.

Add the new tests to `restorefile_test.go` (they use the existing `seedRestorableFileChunks`, `realRestoreServer`, `dialRestoreClient`, `hashMismatchRestoreServer` helpers):

```go
func TestWriteRestoreFile_FileAppearsOnlyAfterCommit(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFileChunks(t, store, "hosta", "/data/a.txt", "job1", 1000, [][]byte{[]byte("hello")})
	client := dialRestoreClient(t, &realRestoreServer{store: store})

	dir := t.TempDir()
	dest := filepath.Join(dir, "a.txt")
	q := newCommitQueue(commitLimits{Files: 100, Bytes: 1 << 40}, defaultCommitHooks())

	r := writeRestoreFile(context.Background(), client, restoreFile{FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: dest}, false, q)
	require.NoError(t, r.Err)

	assert.NoFileExists(t, dest, "not visible before the checkpoint")
	entries, _ := os.ReadDir(dir)
	require.Len(t, entries, 1)
	assert.True(t, isTempName(entries[0].Name()), entries[0].Name())

	require.NoError(t, q.Flush())
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
	entries, _ = os.ReadDir(dir)
	assert.Len(t, entries, 1, "temp file is gone after the rename")
}

func TestWriteRestoreFile_OverwriteKeepsOldContentUntilCommit(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFileChunks(t, store, "hosta", "/data/a.txt", "job1", 1000, [][]byte{[]byte("new content")})
	client := dialRestoreClient(t, &realRestoreServer{store: store})

	dest := filepath.Join(t.TempDir(), "a.txt")
	require.NoError(t, os.WriteFile(dest, []byte("old content"), 0o644))
	q := newCommitQueue(commitLimits{Files: 100, Bytes: 1 << 40}, defaultCommitHooks())

	r := writeRestoreFile(context.Background(), client, restoreFile{FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: dest}, true, q)
	require.NoError(t, r.Err)
	got, _ := os.ReadFile(dest)
	assert.Equal(t, "old content", string(got), "old file untouched until the checkpoint")

	require.NoError(t, q.Flush())
	got, _ = os.ReadFile(dest)
	assert.Equal(t, "new content", string(got))
}

func TestWriteRestoreFile_FailedOverwriteKeepsOldFileAndLeavesNoTemp(t *testing.T) {
	client := dialRestoreClient(t, &hashMismatchRestoreServer{})
	dir := t.TempDir()
	dest := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(dest, []byte("precious"), 0o644))

	r := writeRestoreFileNow(context.Background(), client, restoreFile{FileUUID: "x", Source: "h", Path: "/a.txt", DestPath: dest}, true)
	require.Error(t, r.Err)

	got, _ := os.ReadFile(dest)
	assert.Equal(t, "precious", string(got))
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "partial temp file removed")
}

func TestWriteRestoreFile_AbortedQueueRemovesTempAndFails(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFileChunks(t, store, "hosta", "/data/a.txt", "job1", 1000, [][]byte{[]byte("hello")})
	client := dialRestoreClient(t, &realRestoreServer{store: store})

	dir := t.TempDir()
	q := newCommitQueue(commitLimits{Files: 100, Bytes: 1 << 40}, defaultCommitHooks())
	q.Abort()

	r := writeRestoreFile(context.Background(), client, restoreFile{FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: filepath.Join(dir, "a.txt")}, false, q)
	require.Error(t, r.Err)
	assert.False(t, r.Retryable, "an aborted run is not retried")
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)
}
```

(Add `"path/filepath"` to the test file's imports if absent.)

Also update the existing `*_RemovesPartialFile` tests (`BlakeMismatch…`, `CRCMismatch…`) so that, in addition to their current assertion, they check that the destination directory is empty. Replace their dest-not-exists assertion with `entries, _ := os.ReadDir(filepath.Dir(destPath)); assert.Empty(t, entries)` (keep the `assert.NoFileExists` as well).

Add to `restore_test.go`:

```go
func TestRestoreFileContent_CommitsAllFilesAcrossCheckpointsAndSweepsStaleTemp(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	client := dialRestoreClient(t, &realRestoreServer{store: store})

	destBase := t.TempDir()
	stale := filepath.Join(destBase, ".old.txt.mptmp-deadbeef")
	require.NoError(t, os.WriteFile(stale, []byte("junk"), 0o644))

	var files []restoreFile
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		uuid := seedRestorableFileChunks(t, store, "hosta", "/data/"+name, "job1", 1000, [][]byte{[]byte("content " + name)})
		files = append(files, restoreFile{FileUUID: uuid, Source: "hosta", Path: "/data/" + name, DestPath: filepath.Join(destBase, name)})
	}

	saved := restoreCommit
	t.Cleanup(func() { restoreCommit = saved })
	setRestoreCommitLimits(2, 1<<40) // two mid-run checkpoints plus the final flush

	require.NoError(t, restoreFileContent(context.Background(), discardLogger(), client, files, false, 2, 1))

	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("f%d.txt", i)
		got, err := os.ReadFile(filepath.Join(destBase, name))
		require.NoError(t, err)
		assert.Equal(t, "content "+name, string(got))
	}
	assert.NoFileExists(t, stale)
	entries, _ := os.ReadDir(destBase)
	assert.Len(t, entries, 5, "no temp files remain")
}

func TestRestoreFileContent_FailureLeavesNoTempFiles(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	good := seedRestorableFileChunks(t, store, "hosta", "/data/good.txt", "job1", 1000, [][]byte{[]byte("ok")})
	client := dialRestoreClient(t, &realRestoreServer{store: store})

	destBase := t.TempDir()
	files := []restoreFile{
		{FileUUID: good, Source: "hosta", Path: "/data/good.txt", DestPath: filepath.Join(destBase, "good.txt")},
		{FileUUID: "does-not-exist", Source: "hosta", Path: "/data/bad.txt", DestPath: filepath.Join(destBase, "bad.txt")},
	}
	saved := restoreCommit
	t.Cleanup(func() { restoreCommit = saved })
	setRestoreCommitLimits(100, 1<<40)

	err = restoreFileContent(context.Background(), discardLogger(), client, files, false, 1, 1)
	require.Error(t, err)

	entries, _ := os.ReadDir(destBase)
	for _, e := range entries {
		assert.False(t, isTempName(e.Name()), "leftover temp file %s", e.Name())
	}
}
```

(Ensure `fmt`, `os`, `path/filepath` are imported in `restore_test.go`.)

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/rwfs/ 2>&1 | head -20`
Expected: FAIL to compile (`too many arguments in call to writeRestoreFile`/ `not enough arguments`).

- [ ] **Step 3: Implement `restorefile.go` changes**

Add a writeback-aware writer (below the constants):

```go
// writebackWriter forwards writes to f and, after each, asks the kernel to
// start writing that range back (osStartWriteback), so dirty pages drain
// while the stream is still arriving instead of piling up for the fsync.
// bufio hands it ~1MB writes, so this is one syscall per MB, not per chunk.
type writebackWriter struct {
	f   *os.File
	off int64
}

func (w *writebackWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if n > 0 {
		osStartWriteback(w.f, w.off, int64(n))
		w.off += int64(n)
	}
	return n, err
}
```

Change `writeRestoreFile`'s signature and body. Replace the signature line with:

```go
func writeRestoreFile(parent context.Context, client pb.RestoreServiceClient, f restoreFile, overwrite bool, q *commitQueue) restoreFileResult {
```

Replace everything from `out, err := os.OpenFile(...)` through the end of the function with:

```go
	out, err := createTempFile(f.DestPath)
	if err != nil {
		base.Err = err
		return base
	}
	// Until the queue owns the file, any early return closes and removes
	// the temp file -- the real destination is never touched.
	owned := false
	defer func() {
		if !owned {
			out.Close() // Windows disallows removing a file that's still open -- close before remove.
			os.Remove(out.Name())
		}
	}()

	if err := osPreallocate(out, meta.Size); err != nil {
		base.Err = fmt.Errorf("preallocate: %w", err)
		return base
	}

	bufw := bufio.NewWriterSize(&writebackWriter{f: out}, restoreWriteBufferSize)
	hasher := crc32.NewIEEE()
	var written int64

	for {
		event, err := stream.Recv()
		if err != nil {
			base.Err = fmt.Errorf("stream error: %w", err)
			base.Retryable = true
			return base
		}
		touch()
		chunk := event.GetChunk()
		if chunk == nil {
			base.Err = fmt.Errorf("stream error: expected RestoreChunk")
			base.Retryable = true
			return base
		}

		computed := blake3.Sum256(chunk.Data)
		if !bytes.Equal(computed[:], chunk.Hash) {
			base.Err = fmt.Errorf("blake3_mismatch: chunk %d", chunk.Index)
			return base
		}

		n, err := bufw.Write(chunk.Data)
		if err != nil {
			base.Err = fmt.Errorf("write error: %w", err)
			return base
		}
		written += int64(n)
		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(chunk.Data))

		if chunk.Eof {
			break
		}
	}

	if err := bufw.Flush(); err != nil {
		base.Err = fmt.Errorf("write error: %w", err)
		return base
	}

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	if !bytes.Equal(buf[:], meta.ExpectedChecksum) {
		base.Err = fmt.Errorf("crc_mismatch")
		return base
	}

	// Verified. From here the queue owns the open file and its temp path:
	// it commits them at the next checkpoint, or removes them on a failed
	// checkpoint or an aborted run. A queue error is local (not
	// network-facing), so it is never retried.
	owned = true
	if err := q.Add(pendingFile{File: out, TmpPath: out.Name(), DestPath: f.DestPath, Bytes: written}); err != nil {
		base.Err = fmt.Errorf("commit error: %w", err)
		return base
	}

	base.Bytes = written
	return base
}
```

Update the doc comment above `writeRestoreFile`: replace "On any failure, a partially-written destination file is removed (best-effort) so a corrupt/incomplete file never looks restored." with "Content goes to a hidden temp file beside the destination and, once verified, is handed to q, which renames it into place at its next checkpoint -- so the destination only ever holds a complete, durable file, and an `--overwrite` never destroys the old file early. Any failure removes the temp file." Also drop the now-stale `// Falls through here...` comment's mention of `O_CREATE|O_TRUNC` (replace with: "(will be created via a temp file below)").

Update `writeRestoreFileWithRetry`:

```go
func writeRestoreFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, f restoreFile, overwrite bool, maxRetries int, q *commitQueue) restoreFileResult {
	return withRetry(ctx, logger.With("source", f.Source, "path", f.Path, "dest_path", f.DestPath), maxRetries,
		func(ctx context.Context) restoreFileResult { return writeRestoreFile(ctx, client, f, overwrite, q) },
		func(r restoreFileResult) bool { return r.Err != nil && r.Retryable },
		func(r restoreFileResult) string { return r.Err.Error() },
	)
}
```
and in its doc comment replace "writeRestoreFile's own defer already removes any partial destination file" with "writeRestoreFile's own defer already removes any partial temp file", and "(fresh stat, open, truncate)" with "(fresh stat, temp file, preallocate)".

- [ ] **Step 4: Implement `restore.go` changes in `restoreFileContent`**

After the `findDuplicateDestPath` block and before `logger.Info("restoring file content")`, add:

```go
	sweepStaleTemp(logger, destDirs(files))

	q := newCommitQueue(restoreCommit, defaultCommitHooks())
	// Removes whatever is still pending on any early or failed exit; a
	// no-op after the final successful Flush below.
	defer q.Abort()
```

Change the worker call to pass `q`:

```go
		return writeRestoreFileWithRetry(ctx, logger, client, f, overwrite, retries, q)
```

Replace the tail of the function (from `if firstErr != nil {` to the end) with:

```go
	if firstErr != nil {
		return firstErr
	}
	if err := q.Flush(); err != nil {
		logger.Error("failed to commit restored files", "reason", err)
		return fmt.Errorf("commit restored files: %w", err)
	}
	committedFiles, committedBytes := q.Stats()
	logger.Info("restore complete", "files_written", committedFiles, "bytes_written", committedBytes, "skipped", skipped)
	return nil
```

and remove the now-unused `filesWritten` / `bytesWritten` counters: in the loop's `default:` branch keep only the `logger.Debug("file written", ...)` line (rename its meaning to "written, pending commit" is unnecessary — leave the message). Update the function doc comment with one sentence: "Files are written to temp files and committed (fsync + rename + directory fsync) in batches by a commitQueue; the summary counts only committed files."

In `main.go`, directly after `connection.SetFlowControlWindow(conf.GrpcWindowBytes)` add:

```go
	setRestoreCommitLimits(conf.RestoreCommitFiles, conf.RestoreCommitBytes)
```

- [ ] **Step 5: Run the whole rwfs package, race detector, and Windows vet**

Run: `cd /home/alex/miniprotector/src && go build ./... && go test -race ./cmd/rwfs/ ./common/config/ && GOOS=windows go vet ./cmd/rwfs/`
Expected: PASS, vet clean. If `go vet` complains about an unused variable (`filesWritten`), remove it.

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector && git add src && git commit -m "feat(rwfs): restore via temp file and batched durable atomic commit

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 6: Full test suite and benchmark

**Files:**
- Modify: `docs/superpowers/specs/2026-10-06-rwfs-write-tuning-design.md` (add a Status block with the results)

**Interfaces:** none (measurement task). Scratch/output files go in the session scratchpad directory, not the repo.

- [ ] **Step 1: Run the full suite**

Run: `cd /home/alex/miniprotector && make test`
Expected: PASS. Fix any regression before benchmarking.

- [ ] **Step 2: Build baseline (`main`) and candidate binaries**

```bash
cd /home/alex/miniprotector
S=/tmp/claude-1000/-home-alex-miniprotector/5a74b391-e17d-40ec-b6b3-2ee0dbdb7d6b/scratchpad
git worktree add $S/base main
(cd $S/base && make build mpbench)
make build mpbench
ls $S/base/bin/rwfs bin/rwfs bin/mpbench
```
Expected: all exist. (`bin/` is the repo's `BINARY_DIR`; if `make build` writes elsewhere, use that directory in the next steps.)

- [ ] **Step 3: Run the comparison matrix**

Cold cache needs root; if `sudo -n true` fails, drop `--cold-cache` and say so in the results. For each profile `small`, `mixed`, `large`, run 3 variants with identical flags, same seed, `--runs 5`:

```bash
for P in small mixed large; do
  # A: baseline main (direct write, no fsync)
  sudo -n $S/base/bin/mpbench --bin-dir $S/base/bin --files 500 --profile $P --runs 5 --seed 1 --cold-cache --json $S/A-$P.json
  # B: this branch, defaults (64 files / 64 MiB checkpoints)
  sudo -n bin/mpbench --bin-dir bin --files 500 --profile $P --runs 5 --seed 1 --cold-cache --json $S/B-$P.json
  # C: this branch, per-file fsync+rename (the slow pattern; baseline for batching)
  sudo -n bin/mpbench --bin-dir bin --files 500 --profile $P --runs 5 --seed 1 --cold-cache \
    --conf restore_commit_files=0,restore_commit_bytes=0 --json $S/C-$P.json
done
```
Expected: every run exits 0 (restored tree matches the source). Record, per profile and variant, the `restore` phase median time / throughput and client RSS median.

- [ ] **Step 4: Ablations for fallocate and cache hygiene (on variant B only, not committed)**

For each, make a temporary edit, rebuild `bin/rwfs`, re-run only profile `mixed` and `large` as in B, then `git checkout -- src` to revert:
1. Fallocate off: in `restoreio_linux.go` make `osPreallocate` do `return f.Truncate(size)` when `size > 0`.
2. Cache hygiene off: make `osDropCache` an empty function.

Run: `git status --short` after reverting.
Expected: clean tree (only scratch files outside the repo).

- [ ] **Step 5: Decide and record**

Rule: the temp-file + deferred-commit contract is a correctness change and is kept regardless; it is acceptable if variant B's restore time is within +10% of A. Batching (B vs C) is kept if B is faster than C beyond run-to-run spread (min–max). `fallocate` is kept unless it regresses beyond spread (its early-ENOSPC benefit is not visible in mpbench). Cache hygiene is kept only if it does not regress beyond spread (its working-set benefit is not visible in mpbench). If an optimization fails its rule, remove it (revert its code, keep tests that still apply) and record it as not adopted.

Add this block at the top of the spec, in the style of the read-path-tuning design's Status block, with the actual numbers (table: profile × variant A/B/C × restore median, MB/s or files/s, client RSS; the ablation deltas; hardware/FS description from `df -T` and `nproc`; whether cold cache was used) and an adopted / not adopted line per optimization.

- [ ] **Step 6: Clean up the worktree and commit**

```bash
cd /home/alex/miniprotector && git worktree remove --force $S/base && git add docs && git commit -m "docs: record rwfs write-tuning measurements

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 7: Documentation and changelog

**Files:**
- Modify: `docs/components/rwfs.md` (restore section: write contract, temp files, config keys)
- Modify: `docs/PERFORMANCE.md` (new subsection on restore write tuning with the measured numbers)
- Modify: `docs/components/mpbench.md` only if usage changed (it should not; skip otherwise)
- Modify: `CHANGELOG.md` (new entry at the top)
- Search: config key reference

- [ ] **Step 1: Find where config keys are documented**

Run: `cd /home/alex/miniprotector && grep -rln "RestoreCleanupGracePeriodSec\|default_streams" docs README.md | grep -v superpowers`
Expected: lists the doc(s) with the config-key reference. Add `restore_commit_files` and `restore_commit_bytes` (name, default, range, meaning from Global Constraints) there too.

- [ ] **Step 2: Update `docs/components/rwfs.md`**

In the `restore` section add a "Write contract" subsection stating, factually and matching the final adopted set from Task 6:
- content is written to hidden `.<name>.mptmp-<8 hex>` files beside the destination, preallocated with `fallocate` (early `ENOSPC`), with writeback started as data arrives;
- files are committed in batches (every `restore_commit_files` files or `restore_commit_bytes` bytes, and at end of run): fsync, cache drop, rename, one fsync per parent directory — a file appears under its final name only once durable;
- `--overwrite` replaces atomically and never destroys the old file before the new one is verified;
- a replaced file takes the standard 0644 mode (previously an overwritten file kept its old mode);
- after a crash only `.mptmp-*` files remain; the next `rwfs restore` removes them from the destination directories it touches;
- the two config keys, and that Windows uses plain truncate with no writeback/cache hooks.
Link the design: `../superpowers/specs/2026-10-06-rwfs-write-tuning-design.md`. Omit any optimization that Task 6 marked not adopted, or say so in one line.

- [ ] **Step 3: Update `docs/PERFORMANCE.md` and `CHANGELOG.md`**

PERFORMANCE.md: one subsection summarizing the Task 6 table and the tuning keys. CHANGELOG.md: new top entry `## 2026-10-06 — rwfs restore: atomic, durable, batched writes` — a short paragraph on what changed and why (crash-safe atomic commit, `--overwrite` no longer destroys the old file early, batched fsync, preallocation, cache hygiene; measured effect in one sentence; any not-adopted item named), not a file list.

- [ ] **Step 4: Verify docs and tests, then commit**

Run: `cd /home/alex/miniprotector && git diff --stat && make test`
Expected: only the intended docs changed; tests PASS.

```bash
git add docs CHANGELOG.md && git commit -m "docs: rwfs write contract, performance notes, changelog

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

- [ ] **Step 5: Report**

Report the `mpbench` result change versus the `main` baseline (per the standing preference to run the benchmark after big work), then offer to merge `restore-write-tuning` to `main` via superpowers:finishing-a-development-branch. Do not merge without being asked.
