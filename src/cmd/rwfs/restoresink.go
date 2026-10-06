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
	err            error // latched error from first failed flush (first error wins)
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
	// If a previous flush failed, return the latched error
	if q.err != nil {
		q.mu.Unlock()
		discard([]pendingFile{p})
		return q.err
	}
	// If explicitly aborted (no prior flush error), return errCommitAborted
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
	// If a previous flush failed, return the latched error
	if q.err != nil {
		defer q.mu.Unlock()
		return q.err
	}
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
			commitErr := fmt.Errorf("fsync %s: %w", p.DestPath, err)
			q.mu.Lock()
			if q.err == nil {
				q.err = commitErr
				q.aborted = true
			}
			q.mu.Unlock()
			return commitErr
		}
		q.hooks.DropCache(p.File)
		if err := p.File.Close(); err != nil {
			discard(batch)
			commitErr := fmt.Errorf("close %s: %w", p.DestPath, err)
			q.mu.Lock()
			if q.err == nil {
				q.err = commitErr
				q.aborted = true
			}
			q.mu.Unlock()
			return commitErr
		}
	}

	dirs := map[string]struct{}{}
	for i, p := range batch {
		if err := q.hooks.Rename(p.TmpPath, p.DestPath); err != nil {
			discard(batch[i:])
			commitErr := fmt.Errorf("rename into place %s: %w", p.DestPath, err)
			q.mu.Lock()
			if q.err == nil {
				q.err = commitErr
				q.aborted = true
			}
			q.mu.Unlock()
			return commitErr
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
			commitErr := fmt.Errorf("fsync directory %s: %w", d, err)
			q.mu.Lock()
			if q.err == nil {
				q.err = commitErr
				q.aborted = true
			}
			q.mu.Unlock()
			return commitErr
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
// successful Flush: nothing is pending then. Takes flushMu first to ensure
// any in-flight Flush completes before we proceed.
func (q *commitQueue) Abort() {
	q.flushMu.Lock()
	defer q.flushMu.Unlock()

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
