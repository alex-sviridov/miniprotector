# Backup Job Completion Verification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the refcount-based "streams closed = finished" signal with a `BackupCommit` RPC that hash-verifies file completeness, plus a stall-timeout watchdog and startup reconciliation, so `bwfs` never again reports a job as finished without actually verifying it.

**Architecture:** `backup_jobs` gains a `status` column (`in_progress`/`success`/`failure`). `brfs` computes a SHA256 over the sorted IDs of files it believes it sent successfully and submits it via a new unary `BackupCommit` RPC; `bwfs` recomputes the same hash from its own `file_versions` rows and only marks the job `success` if they match, purging `file_versions` on mismatch. An in-memory per-job "last activity" tracker drives a background watchdog that fails any job gone silent past a configurable timeout, and a startup pass fails any job left `in_progress` by an unclean previous shutdown.

**Tech Stack:** Go 1.26, gRPC + protobuf (`protoc`), GORM over SQLite (single connection, WAL mode), testify (`assert`/`require`), Cobra for CLI args.

## Global Constraints

- Reuse `job-id` as gRPC outgoing/incoming metadata for the new unary RPC exactly as streams already do — no proto field carries it.
- `JobStatusInProgress`/`JobStatusSuccess`/`JobStatusFailure` string constants live in package `storage` and are the only source of truth for status values everywhere (no magic strings).
- `FinalizeBackupJob` must be a CAS (`WHERE status = in_progress`) so the watchdog and `BackupCommit` can never double-transition or double-delete a job's `file_versions`.
- Config keys are flat `key=value` lines parsed by `src/common/config/config.go`; unknown keys are a hard error, so every new key needs an explicit `case` there.
- Follow the existing package-internal (white-box) test style: `_test.go` files in `package filesystem` / `package main` reach into unexported fields directly (see `store_test.go`, `integration_test.go`).

---

### Task 1: Storage contract — job status field, constants, and read accessor

**Files:**
- Modify: `src/storage/interface.go`
- Modify: `src/storage/filesystem/models.go`
- Modify: `src/storage/filesystem/backupjob.go`
- Test: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Produces: `storage.JobStatusInProgress`, `storage.JobStatusSuccess`, `storage.JobStatusFailure` (string constants); `storage.BackupJob{JobID, SourceHost string; StartedAt time.Time; FinishedAt *time.Time; Status string}`; `BackupStore.GetBackupJob(jobID string) (*storage.BackupJob, error)`.
- Consumes: existing `BackupJobRecord` GORM model, `EnsureBackupJob`.

- [ ] **Step 1: Write the failing tests**

Add to `src/storage/filesystem/store_test.go`, replacing nothing yet (leave `TestFinishBackupJob_SetsFinishedAt` in place for now — it's removed in Task 2):

```go
func TestEnsureBackupJob_SetsInProgressStatus(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, storage.JobStatusInProgress, record.Status)
}

func TestGetBackupJob_ReturnsRecord(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	job, err := store.GetBackupJob("job-1")
	require.NoError(t, err)
	assert.Equal(t, "job-1", job.JobID)
	assert.Equal(t, "host-a", job.SourceHost)
	assert.Equal(t, storage.JobStatusInProgress, job.Status)
	assert.Nil(t, job.FinishedAt)
}

func TestGetBackupJob_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetBackupJob("does-not-exist")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./storage/filesystem/... -run 'TestEnsureBackupJob_SetsInProgressStatus|TestGetBackupJob' -v`
Expected: FAIL — `storage.JobStatusInProgress` and `store.GetBackupJob` undefined.

- [ ] **Step 3: Add the schema field, constants, and interface methods**

In `src/storage/interface.go`, add near the top (after `ErrChunkNotFound`):

```go
const (
	JobStatusInProgress = "in_progress"
	JobStatusSuccess    = "success"
	JobStatusFailure    = "failure"
)
```

Replace the "Backup job operations" block in the `BackupStore` interface:

```go
	// Backup job operations - track discrete backup runs (one brfs invocation each).
	EnsureBackupJob(jobID, sourceHost string) error
	GetBackupJob(jobID string) (*BackupJob, error)
	FileVersionsForJob(jobID string) ([]string, error)
	FinalizeBackupJob(jobID string, success bool) (bool, error)
	FailStaleInProgressJobs() (int64, error)
```

(`FinishBackupJob` is removed here; `FinalizeBackupJob`/`FailStaleInProgressJobs`/`FileVersionsForJob` are implemented in Tasks 2–3 — the interface just declares the full contract now so every later task type-checks against a stable surface.)

Add the `BackupJob` struct next to `FileVersion`:

```go
// BackupJob represents a discrete backup run (one brfs invocation).
type BackupJob struct {
	JobID      string
	SourceHost string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string // JobStatusInProgress | JobStatusSuccess | JobStatusFailure
}
```

In `src/storage/filesystem/models.go`, add the field to `BackupJobRecord`:

```go
type BackupJobRecord struct {
	JobID      string `gorm:"primaryKey"`
	SourceHost string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string `gorm:"default:in_progress"`
}
```

In `src/storage/filesystem/backupjob.go`, set `Status` explicitly on insert and add `GetBackupJob`:

```go
package filesystem

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage"
)

// EnsureBackupJob idempotently records that a backup job has started. Safe
// to call once per stream of a multi-stream job — only the first call for a
// given jobID creates a row; later calls are no-ops.
func (s *Store) EnsureBackupJob(jobID, sourceHost string) error {
	record := BackupJobRecord{
		JobID:      jobID,
		SourceHost: sourceHost,
		StartedAt:  time.Now(),
		Status:     storage.JobStatusInProgress,
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}},
		DoNothing: true,
	}).Create(&record).Error
}

// GetBackupJob returns the job record, for source-host verification and
// status checks in the BackupCommit RPC and the stall watchdog.
func (s *Store) GetBackupJob(jobID string) (*storage.BackupJob, error) {
	var record BackupJobRecord
	err := s.db.Where("job_id = ?", jobID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("backup job not found: %s", jobID)
	}
	if err != nil {
		return nil, err
	}
	return &storage.BackupJob{
		JobID:      record.JobID,
		SourceHost: record.SourceHost,
		StartedAt:  record.StartedAt,
		FinishedAt: record.FinishedAt,
		Status:     record.Status,
	}, nil
}
```

Note: `FinishBackupJob` still exists at the bottom of this file for now — it's deleted in Task 2 along with its test, to keep this step's diff focused on the schema/read-path addition.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./storage/filesystem/... -run 'TestEnsureBackupJob_SetsInProgressStatus|TestGetBackupJob' -v`
Expected: PASS. (The package won't fully build yet — `filesystem.Store` no longer satisfies `storage.BackupStore` until Tasks 2–3 add the other three methods. That's expected; don't run the full package test suite until Task 3 is done.)

- [ ] **Step 5: Commit**

```bash
git add src/storage/interface.go src/storage/filesystem/models.go src/storage/filesystem/backupjob.go src/storage/filesystem/store_test.go
git commit -m "feat(storage): add backup job status field and GetBackupJob accessor"
```

---

### Task 2: FinalizeBackupJob — CAS status transition with cascading purge

**Files:**
- Modify: `src/storage/filesystem/backupjob.go`
- Test: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Consumes: `storage.JobStatusInProgress/Success/Failure` (Task 1), `FileVersionRecord` (existing).
- Produces: `func (s *Store) FinalizeBackupJob(jobID string, success bool) (bool, error)` — `bool` return is `true` iff this call performed the transition (false if the job was already finalized).

- [ ] **Step 1: Write the failing tests**

Replace `TestFinishBackupJob_SetsFinishedAt` in `src/storage/filesystem/store_test.go` with:

```go
func TestFinalizeBackupJob_SuccessSetsStatusAndFinishedAt(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta"), 100))

	changed, err := store.FinalizeBackupJob("job-1", true)
	require.NoError(t, err)
	assert.True(t, changed)

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, storage.JobStatusSuccess, record.Status)
	require.NotNil(t, record.FinishedAt)
	assert.WithinDuration(t, time.Now(), *record.FinishedAt, 5*time.Second)

	// file_versions must survive a success finalize
	var count int64
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-1").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestFinalizeBackupJob_FailurePurgesOnlyThatJobsFileVersions(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))
	require.NoError(t, store.EnsureBackupJob("job-2", "host-a"))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta"), 100))
	require.NoError(t, store.EnsureFileVersion("job-2", "obj-2", []byte("meta"), 100))

	changed, err := store.FinalizeBackupJob("job-1", false)
	require.NoError(t, err)
	assert.True(t, changed)

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, storage.JobStatusFailure, record.Status)
	require.NotNil(t, record.FinishedAt)

	var job1Count, job2Count int64
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-1").Count(&job1Count).Error)
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-2").Count(&job2Count).Error)
	assert.Equal(t, int64(0), job1Count, "failed job's file_versions must be purged")
	assert.Equal(t, int64(1), job2Count, "other job's file_versions must be untouched")
}

func TestFinalizeBackupJob_SecondCallIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	changed1, err := store.FinalizeBackupJob("job-1", true)
	require.NoError(t, err)
	assert.True(t, changed1)

	var firstFinish BackupJobRecord
	require.NoError(t, store.db.First(&firstFinish, "job_id = ?", "job-1").Error)

	changed2, err := store.FinalizeBackupJob("job-1", false)
	require.NoError(t, err)
	assert.False(t, changed2, "job already finalized as success; a later failure call must be a no-op")

	var afterSecond BackupJobRecord
	require.NoError(t, store.db.First(&afterSecond, "job_id = ?", "job-1").Error)
	assert.Equal(t, storage.JobStatusSuccess, afterSecond.Status, "status must not flip on the no-op call")
	assert.Equal(t, firstFinish.FinishedAt.Unix(), afterSecond.FinishedAt.Unix())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./storage/filesystem/... -run TestFinalizeBackupJob -v`
Expected: FAIL — `store.FinalizeBackupJob` undefined.

- [ ] **Step 3: Implement FinalizeBackupJob, remove FinishBackupJob**

In `src/storage/filesystem/backupjob.go`, delete the old `FinishBackupJob` function and add:

```go
// FinalizeBackupJob atomically transitions a job from in_progress to
// success/failure. On failure it also purges the job's file_versions rows
// in the same transaction — raw chunk/file data is reclaimed later by
// Vacuum, out of scope here. Returns false (no-op) if the job was already
// finalized, guarding the race between BackupCommit and the stall watchdog,
// and making duplicate/retried BackupCommit calls idempotent.
func (s *Store) FinalizeBackupJob(jobID string, success bool) (bool, error) {
	newStatus := storage.JobStatusFailure
	if success {
		newStatus = storage.JobStatusSuccess
	}

	var changed bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		result := tx.Model(&BackupJobRecord{}).
			Where("job_id = ? AND status = ?", jobID, storage.JobStatusInProgress).
			Updates(map[string]any{"status": newStatus, "finished_at": now})
		if result.Error != nil {
			return result.Error
		}
		changed = result.RowsAffected > 0
		if changed && !success {
			if err := tx.Delete(&FileVersionRecord{}, "job_id = ?", jobID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./storage/filesystem/... -run TestFinalizeBackupJob -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/storage/filesystem/backupjob.go src/storage/filesystem/store_test.go
git commit -m "feat(storage): replace FinishBackupJob with CAS FinalizeBackupJob"
```

---

### Task 3: FileVersionsForJob and FailStaleInProgressJobs

**Files:**
- Modify: `src/storage/filesystem/fileversion.go`
- Modify: `src/storage/filesystem/backupjob.go`
- Test: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Produces: `func (s *Store) FileVersionsForJob(jobID string) ([]string, error)`; `func (s *Store) FailStaleInProgressJobs() (int64, error)`.
- After this task, `*filesystem.Store` fully satisfies `storage.BackupStore` again (the `var _ storage.BackupStore = (*Store)(nil)` assertion in `store.go` compiles).

- [ ] **Step 1: Write the failing tests**

Add to `src/storage/filesystem/store_test.go`:

```go
func TestFileVersionsForJob_ReturnsObjectIDsForThatJobOnly(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-a", []byte("meta"), 1))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-b", []byte("meta"), 2))
	require.NoError(t, store.EnsureFileVersion("job-2", "obj-c", []byte("meta"), 3))

	ids, err := store.FileVersionsForJob("job-1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"obj-a", "obj-b"}, ids)
}

func TestFileVersionsForJob_EmptyForUnknownJob(t *testing.T) {
	store := newTestStore(t)
	ids, err := store.FileVersionsForJob("no-such-job")
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestFailStaleInProgressJobs_FlipsOnlyInProgressJobs(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-stale-1", "host-a"))
	require.NoError(t, store.EnsureBackupJob("job-stale-2", "host-a"))
	require.NoError(t, store.EnsureBackupJob("job-done", "host-a"))
	require.NoError(t, store.EnsureFileVersion("job-stale-1", "obj-1", []byte("meta"), 1))
	require.NoError(t, store.EnsureFileVersion("job-done", "obj-2", []byte("meta"), 1))

	changed, err := store.FinalizeBackupJob("job-done", true)
	require.NoError(t, err)
	require.True(t, changed)

	count, err := store.FailStaleInProgressJobs()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	var stale1, stale2, done BackupJobRecord
	require.NoError(t, store.db.First(&stale1, "job_id = ?", "job-stale-1").Error)
	require.NoError(t, store.db.First(&stale2, "job_id = ?", "job-stale-2").Error)
	require.NoError(t, store.db.First(&done, "job_id = ?", "job-done").Error)
	assert.Equal(t, storage.JobStatusFailure, stale1.Status)
	assert.Equal(t, storage.JobStatusFailure, stale2.Status)
	assert.Equal(t, storage.JobStatusSuccess, done.Status, "already-finalized job must be untouched")

	var staleVersions, doneVersions int64
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-stale-1").Count(&staleVersions).Error)
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-done").Count(&doneVersions).Error)
	assert.Equal(t, int64(0), staleVersions, "stale job's file_versions must be purged")
	assert.Equal(t, int64(1), doneVersions, "already-succeeded job's file_versions must survive")
}

func TestFailStaleInProgressJobs_NoInProgressJobsReturnsZero(t *testing.T) {
	store := newTestStore(t)
	count, err := store.FailStaleInProgressJobs()
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./storage/filesystem/... -run 'TestFileVersionsForJob|TestFailStaleInProgressJobs' -v`
Expected: FAIL — both methods undefined.

- [ ] **Step 3: Implement both methods**

In `src/storage/filesystem/fileversion.go`, add:

```go
// FileVersionsForJob returns the object IDs of every file_versions row
// recorded for jobID, for BackupCommit's hash verification.
func (s *Store) FileVersionsForJob(jobID string) ([]string, error) {
	var objectIDs []string
	err := s.db.Model(&FileVersionRecord{}).
		Where("job_id = ?", jobID).
		Pluck("object_id", &objectIDs).Error
	return objectIDs, err
}
```

In `src/storage/filesystem/backupjob.go`, add:

```go
// FailStaleInProgressJobs bulk-transitions every in_progress job to failure
// (purging their file_versions in the same transaction). Called once at
// bwfs startup to clean up jobs orphaned by an unclean previous shutdown.
func (s *Store) FailStaleInProgressJobs() (int64, error) {
	var count int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var jobIDs []string
		if err := tx.Model(&BackupJobRecord{}).
			Where("status = ?", storage.JobStatusInProgress).
			Pluck("job_id", &jobIDs).Error; err != nil {
			return err
		}
		if len(jobIDs) == 0 {
			return nil
		}
		if err := tx.Delete(&FileVersionRecord{}, "job_id IN ?", jobIDs).Error; err != nil {
			return err
		}
		now := time.Now()
		result := tx.Model(&BackupJobRecord{}).
			Where("job_id IN ?", jobIDs).
			Updates(map[string]any{"status": storage.JobStatusFailure, "finished_at": now})
		if result.Error != nil {
			return result.Error
		}
		count = result.RowsAffected
		return nil
	})
	return count, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./storage/... -v`
Expected: PASS for the whole `storage` and `storage/filesystem` packages — this is the first point where `*Store` satisfies `storage.BackupStore` again.

- [ ] **Step 5: Commit**

```bash
git add src/storage/filesystem/fileversion.go src/storage/filesystem/backupjob.go src/storage/filesystem/store_test.go
git commit -m "feat(storage): add FileVersionsForJob and FailStaleInProgressJobs"
```

---

### Task 4: BackupCommit RPC — proto definition and codegen

**Files:**
- Modify: `src/api/backup.proto`
- Generated (via `make proto`): `src/api/backup.pb.go`, `src/api/backup_grpc.pb.go`

**Interfaces:**
- Produces: `pb.BackupCommitRequest{FileListHash []byte}`, `pb.BackupCommitResponse{Success bool}`, `pb.BackupServiceClient.BackupCommit(ctx, *BackupCommitRequest, ...) (*BackupCommitResponse, error)`, and the server-side method `BackupCommit(context.Context, *pb.BackupCommitRequest) (*pb.BackupCommitResponse, error)` that `pb.BackupServiceServer` now requires.

This task has no unit test of its own — it only adds generated code. Note: `backupServer` embeds `pb.UnimplementedBackupServiceServer` (see `server.go`), so adding `BackupCommit` to the proto service does NOT break `cmd/bwfs` compilation — the embedded type auto-satisfies any interface method it doesn't explicitly implement (returning `codes.Unimplemented` at runtime until Task 6 adds the real handler). Separately, and unrelated to this task: `cmd/bwfs` already fails to compile as of Task 1, because `server.go` still calls `server.store.FinishBackupJob(jobID)`, a method Task 1 removed from the `BackupStore` interface. That pre-existing break is fixed in Task 5, not here.

- [ ] **Step 1: Add the RPC and messages to the proto file**

In `src/api/backup.proto`, change the service definition:

```proto
service BackupService {
  rpc ProcessBackupStream(stream FileRequest) returns (stream FileResponse);
  rpc BackupCommit(BackupCommitRequest) returns (BackupCommitResponse);
}
```

Add the two new messages at the end of the file:

```proto
message BackupCommitRequest {
  bytes file_list_hash = 1; // SHA256 over the sorted, newline-joined object IDs brfs believes it sent successfully
}

message BackupCommitResponse {
  bool success = 1;
}
```

- [ ] **Step 2: Regenerate protobuf code**

Run: `make proto`
Expected: `Generating protobuf code... ✅` (or equivalent success output), and `git status` shows `src/api/backup.pb.go` and `src/api/backup_grpc.pb.go` modified.

- [ ] **Step 3: Verify the proto package still builds on its own**

Run: `cd src && go build ./api/...`
Expected: success (the generated code compiles in isolation). Don't run `go build ./...` or any `cmd/bwfs` test here — that package is already broken by Task 1's interface change (see the note above) and stays broken until Task 5; that's expected and unrelated to this task's own success criteria.

- [ ] **Step 4: Commit**

```bash
git add src/api/backup.proto src/api/backup.pb.go src/api/backup_grpc.pb.go
git commit -m "feat(api): add BackupCommit RPC to BackupService"
```

---

### Task 5: bwfs — jobLiveness tracker, replacing jobtracker, wired into the stream handler

**Files:**
- Create: `src/cmd/bwfs/liveness.go`
- Test: `src/cmd/bwfs/liveness_test.go`
- Delete: `src/cmd/bwfs/jobtracker.go`
- Delete: `src/cmd/bwfs/jobtracker_test.go`
- Modify: `src/cmd/bwfs/server.go`
- Modify: `src/cmd/bwfs/integration_test.go`

**Interfaces:**
- Produces: `newJobLiveness() *jobLiveness`; `(*jobLiveness) Touch(jobID string)`; `(*jobLiveness) Complete(jobID string)`; `(*jobLiveness) IsFinalized(jobID string) bool`; `(*jobLiveness) StaleJobs(timeout time.Duration) []string`; `backupServer.liveness *jobLiveness` field (replaces `jobs *jobTracker`).
- Consumes: `storage.JobStatusInProgress` (Task 1).
- This fully replaces `jobTracker`/`newJobTracker`/`Start`/`Finish` — no code anywhere should reference those names after this task. `ProcessBackupStream` no longer calls `FinishBackupJob`/sets `finished_at` on stream close — that's now exclusively driven by `BackupCommit` (Task 6) and the watchdog (Task 7).

**Why this is one task, not two:** `cmd/bwfs` is currently broken — `server.go` calls `server.store.FinishBackupJob(jobID)`, a method Task 1 removed from the `BackupStore` interface. Splitting "add jobLiveness" and "wire it into server.go" into separate tasks would leave `cmd/bwfs` uncompilable (and `liveness_test.go`, which lives in the same `package main`, unable to run) for an entire task boundary. This task adds `jobLiveness` and rewires `server.go` together, so `cmd/bwfs` compiles and all its tests pass by the end of this single task.

- [ ] **Step 1: Delete the old tracker and its test**

```bash
rm src/cmd/bwfs/jobtracker.go src/cmd/bwfs/jobtracker_test.go
```

- [ ] **Step 2: Write the failing liveness test**

Create `src/cmd/bwfs/liveness_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestJobLiveness_TouchThenStaleJobsExcludesFreshJob(t *testing.T) {
	l := newJobLiveness()
	l.Touch("job-1")

	stale := l.StaleJobs(time.Hour)
	assert.Empty(t, stale, "a job touched moments ago must not be stale")
}

func TestJobLiveness_StaleJobsIncludesOldEntry(t *testing.T) {
	l := newJobLiveness()
	l.mu.Lock()
	l.lastSeen["job-old"] = time.Now().Add(-2 * time.Hour)
	l.mu.Unlock()

	stale := l.StaleJobs(time.Hour)
	assert.Equal(t, []string{"job-old"}, stale)
}

func TestJobLiveness_CompleteMarksFinalizedAndRemovesFromLastSeen(t *testing.T) {
	l := newJobLiveness()
	l.Touch("job-1")
	l.Complete("job-1")

	assert.True(t, l.IsFinalized("job-1"))
	l.mu.Lock()
	_, stillTracked := l.lastSeen["job-1"]
	l.mu.Unlock()
	assert.False(t, stillTracked, "a completed job must not still count as active for staleness checks")
}

func TestJobLiveness_IsFinalizedFalseForUnknownJob(t *testing.T) {
	l := newJobLiveness()
	assert.False(t, l.IsFinalized("never-seen"))
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd src && go test ./cmd/bwfs/... -run TestJobLiveness -v`
Expected: FAIL — the package doesn't compile yet. You'll likely see two independent errors at once: `newJobLiveness` undefined (liveness.go doesn't exist yet), and `server.go`'s reference to the now-deleted `jobTracker` type plus its call to `server.store.FinishBackupJob` (removed from the interface in Task 1). Both are expected at this point — Steps 4–6 below fix all of it in this same task.

- [ ] **Step 4: Implement jobLiveness**

Create `src/cmd/bwfs/liveness.go`:

```go
package main

import (
	"sync"
	"time"
)

// jobLiveness tracks, per backup job, when the server last saw any activity
// (a stream opening or any FileRequest received) and whether the job has
// already been finalized (success or failure). The stall watchdog uses
// StaleJobs to find jobs that have gone silent; the stream handler uses
// IsFinalized as a cheap in-memory check to reject further messages for a
// job whose outcome has already been decided, without hitting the database
// on every message.
type jobLiveness struct {
	mu        sync.Mutex
	lastSeen  map[string]time.Time
	finalized map[string]bool
}

func newJobLiveness() *jobLiveness {
	return &jobLiveness{
		lastSeen:  make(map[string]time.Time),
		finalized: make(map[string]bool),
	}
}

// Touch records activity for jobID now. Must not be called after Complete
// for the same jobID — callers check IsFinalized first.
func (l *jobLiveness) Touch(jobID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastSeen[jobID] = time.Now()
}

// Complete marks jobID as finalized and stops tracking its liveness.
func (l *jobLiveness) Complete(jobID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.lastSeen, jobID)
	l.finalized[jobID] = true
}

// IsFinalized reports whether Complete has been called for jobID.
func (l *jobLiveness) IsFinalized(jobID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.finalized[jobID]
}

// StaleJobs returns the IDs of jobs whose last recorded activity is older
// than timeout.
func (l *jobLiveness) StaleJobs(timeout time.Duration) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-timeout)
	var stale []string
	for jobID, seen := range l.lastSeen {
		if seen.Before(cutoff) {
			stale = append(stale, jobID)
		}
	}
	return stale
}
```

- [ ] **Step 5: Rewire server.go**

In `src/cmd/bwfs/server.go`, change the struct field and constructor:

```go
type backupServer struct {
	pb.UnimplementedBackupServiceServer
	config   *config.Config
	store    storage.BackupStore
	logger   *slog.Logger
	liveness *jobLiveness
}

func NewBackupServer(ctx context.Context, logger *slog.Logger, storagePath string) (*backupServer, error) {
	conf := config.GetConfigFromContext(ctx)

	store, err := wfs.New(storagePath)
	if err != nil {
		return nil, err
	}
	return &backupServer{
		logger:   logger,
		config:   conf,
		store:    store,
		liveness: newJobLiveness(),
	}, nil
}
```

Replace the job-tracking block in `ProcessBackupStream` (removes the refcount start/defer-finish, adds an initial `Touch`):

```go
	if err := server.store.EnsureBackupJob(jobID, sourceHost); err != nil {
		return status.Errorf(codes.Internal, "ensure backup job: %v", err)
	}
	server.liveness.Touch(jobID)
```

Update the receive loop to reject further messages for an already-finalized job (checked in-memory via `liveness`, not a DB read per message) and to `Touch` on every accepted message:

```go
	h := newStreamHandler(ctx, logger, server.store, jobID)

	for {
		request, err := stream.Recv()
		if err == io.EOF {
			h.logger.Info("Client stopped sending")
			return nil
		}
		if err != nil {
			h.logger.Error("Error receiving", "error", err)
			return err
		}
		if request == nil {
			continue
		}
		if server.liveness.IsFinalized(jobID) {
			return status.Errorf(codes.FailedPrecondition, "job %s already finalized", jobID)
		}
		server.liveness.Touch(jobID)
		if err := h.handleRequest(ctx, stream, request); err != nil {
			h.logger.Error("Error handling request", "error", err)
		}
		if h.EOF {
			if err := h.fileWritten(ctx, stream); err != nil {
				h.logger.Error("Error finalizing file", "error", err)
			}
		}
	}
```

- [ ] **Step 6: Update the existing tests that assumed refcount-driven finish**

In `src/cmd/bwfs/integration_test.go`, replace `TestIntegration_BackupJob_RecordedWithSourceHost` (it currently asserts `FinishedAt != nil` right after streams close, which is no longer true):

```go
// TestIntegration_BackupJob_RecordedWithSourceHost verifies a stream creates
// a backup_jobs row with the mTLS-verified source host, staying in_progress
// with finished_at nil after the stream closes — completion now requires an
// explicit BackupCommit (Task 6), not just the stream going away.
func TestIntegration_BackupJob_RecordedWithSourceHost(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)

	ctx := jobContext("job-source-host")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			_, err := backupOneFile(ctx, t, stream, f)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	require.Eventually(t, func() bool {
		record, err := backupJobRow(t, env, "job-source-host")
		return err == nil && record.SourceHost == "bwfs.internal"
	}, time.Second, 10*time.Millisecond, "backup job should be recorded with source host")

	record, err := backupJobRow(t, env, "job-source-host")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusInProgress, record.Status)
	assert.Nil(t, record.FinishedAt, "finished_at must stay nil until BackupCommit — streams closing alone is not completion")
}
```

Replace `TestIntegration_BackupJob_FinishedAtWaitsForAllStreams` (it asserts `finished_at` gets set once the last stream closes — that behavior is removed):

```go
// TestIntegration_BackupJob_StaysInProgressAfterAllStreamsClose verifies
// that closing every stream of a job does NOT, by itself, finalize it —
// completion now requires an explicit BackupCommit call (Task 6) or the
// stall watchdog (Task 7), not just stream closure.
func TestIntegration_BackupJob_StaysInProgressAfterAllStreamsClose(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-multi-stream")

	stream1, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	stream2, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	_, err = backupOneFile(ctx, t, stream1, target)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := backupJobRow(t, env, "job-multi-stream")
		return err == nil
	}, time.Second, 10*time.Millisecond, "backup job row should exist once the first stream starts")

	require.NoError(t, stream1.CloseSend())
	_, err = stream1.Recv()
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, stream2.CloseSend())
	_, err = stream2.Recv()
	require.ErrorIs(t, err, io.EOF)

	record, err := backupJobRow(t, env, "job-multi-stream")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusInProgress, record.Status, "job must stay in_progress after streams close with no BackupCommit sent")
	assert.Nil(t, record.FinishedAt)
}
```

Add `"github.com/alex-sviridov/miniprotector/storage"` to the import block of `integration_test.go` (needed for `storage.JobStatusInProgress` above).

- [ ] **Step 7: Run tests to verify everything passes**

Run: `cd src && go test ./cmd/bwfs/... -tags integration -v`
Expected: PASS for every test in the package, including `TestJobLiveness_*` and the two rewritten `TestIntegration_BackupJob_*` tests. This is also the point where `cmd/bwfs` compiles cleanly again for the first time since Task 1.

Run: `grep -rn "FinishBackupJob\|jobTracker" src/cmd/bwfs/`
Expected: no matches.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/bwfs/liveness.go src/cmd/bwfs/liveness_test.go src/cmd/bwfs/server.go src/cmd/bwfs/integration_test.go
git rm src/cmd/bwfs/jobtracker.go src/cmd/bwfs/jobtracker_test.go
git commit -m "feat(bwfs): add jobLiveness tracker, drive job completion from it instead of stream refcount"
```

---

### Task 6: bwfs — BackupCommit RPC handler

**Files:**
- Create: `src/cmd/bwfs/commit.go`
- Test: `src/cmd/bwfs/integration_test.go`

**Interfaces:**
- Consumes: `pb.BackupCommitRequest/Response` (Task 4), `store.GetBackupJob/FileVersionsForJob/FinalizeBackupJob` (Tasks 1–3), `server.liveness.Complete/IsFinalized` (Task 5), `mtls.PeerHostname`, `jobIDFromMetadata` (existing, `server.go`).
- Produces: `func (server *backupServer) BackupCommit(ctx context.Context, req *pb.BackupCommitRequest) (*pb.BackupCommitResponse, error)` — the method that makes `*backupServer` satisfy the now-larger `pb.BackupServiceServer` interface from Task 4.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/bwfs/integration_test.go`:

```go
// commitHash computes the same SHA256-over-sorted-newline-joined-IDs that
// brfs computes client-side (see cmd/brfs/commit.go, Task 8) — inlined here
// so this test file doesn't depend on the brfs package.
func commitHash(ids ...string) []byte {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return sum[:]
}

func TestIntegration_BackupCommit_MatchingHashSucceeds(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-success")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	resp, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(target.ID())})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	record, err := backupJobRow(t, env, "job-commit-success")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusSuccess, record.Status)
	require.NotNil(t, record.FinishedAt)
}

func TestIntegration_BackupCommit_MismatchedHashFailsAndPurges(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-mismatch")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	resp, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash("some-file-that-was-never-sent")})
	require.NoError(t, err)
	assert.False(t, resp.Success)

	record, err := backupJobRow(t, env, "job-commit-mismatch")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusFailure, record.Status)

	var count int64
	concrete, ok := env.store.store.(*storagefs.Store)
	require.True(t, ok)
	require.NoError(t, concrete.RawDB().Model(&storagefs.FileVersionRecord{}).Where("job_id = ?", "job-commit-mismatch").Count(&count).Error)
	assert.Equal(t, int64(0), count, "mismatched job's file_versions must be purged")
}

func TestIntegration_BackupCommit_UnknownJobReturnsNotFound(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	ctx := jobContext("job-never-existed")
	_, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash("x")})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestIntegration_BackupCommit_WrongSourceHostRejected(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	// Seed a job as if it belonged to a different host, bypassing mTLS —
	// the test client cert always presents "bwfs.internal", so this
	// simulates a second host trying to commit a job it doesn't own.
	require.NoError(t, env.store.store.EnsureBackupJob("job-other-host", "some-other-host"))

	ctx := jobContext("job-other-host")
	_, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash("x")})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestIntegration_BackupCommit_RetriedCallAfterSuccessIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-retry")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	hash := commitHash(target.ID())
	resp1, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: hash})
	require.NoError(t, err)
	assert.True(t, resp1.Success)

	// Simulate brfs retrying because the first response was lost in transit.
	resp2, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: hash})
	require.NoError(t, err)
	assert.True(t, resp2.Success, "a retried commit call for an already-succeeded job must return the same outcome, not re-hash or error")
}

// TestIntegration_LateMessageAfterFinalize_Rejected verifies a message
// arriving for a job that BackupCommit already finalized is rejected rather
// than silently written.
func TestIntegration_LateMessageAfterFinalize_Rejected(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var first, second wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			if first.ID() == "" {
				first = f
			} else if second.ID() == "" {
				second = f
			}
		}
	}
	require.NotEmpty(t, first.ID())

	ctx := jobContext("job-late-message")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, first)
	require.NoError(t, err)

	// Commit the job while stream is still open (simulating brfs committing
	// after its WaitGroup joins, even though this test keeps one stream alive).
	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(first.ID())})
	require.NoError(t, err)

	// Now send another message on the still-open stream — must be rejected.
	require.NoError(t, stream.Send(&pb.FileRequest{
		RequestType: &pb.FileRequest_FileInfo{FileInfo: &pb.FileInfo{FileId: "late-file", Attributes: []byte("x")}},
	}))
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}
```

Add these imports to `src/cmd/bwfs/integration_test.go`: `"crypto/sha256"`, `"sort"`, `"strings"`, and `"github.com/alex-sviridov/miniprotector/storage"` (the last may already be present from Task 6).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/bwfs/... -tags integration -run TestIntegration_BackupCommit -v`
Expected: FAIL, but not to compile — `backupServer` embeds `pb.UnimplementedBackupServiceServer`, so it already satisfies `pb.BackupServiceServer` without a `BackupCommit` method of its own; the embedded stub returns `codes.Unimplemented` for any call to it. The new tests should compile fine and fail on assertions instead (e.g. `require.NoError(t, err)` failing because `err` is a `codes.Unimplemented` status error, or `resp` being nil).

- [ ] **Step 3: Implement the handler**

Create `src/cmd/bwfs/commit.go`:

```go
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sort"
	"strings"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/storage"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BackupCommit is brfs's final call for a job, after all of its streams
// have closed. bwfs independently recomputes the same hash from what it
// actually recorded in file_versions and only marks the job success if the
// two agree — the streams having closed is not, by itself, proof that
// everything brfs intended to send actually arrived.
func (server *backupServer) BackupCommit(ctx context.Context, req *pb.BackupCommitRequest) (*pb.BackupCommitResponse, error) {
	jobID, err := jobIDFromMetadata(ctx)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "job-id metadata required: %v", err)
	}

	sourceHost, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "resolve peer identity: %v", err)
	}

	job, err := server.store.GetBackupJob(jobID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "unknown job %s: %v", jobID, err)
	}
	if job.SourceHost != sourceHost {
		return nil, status.Errorf(codes.PermissionDenied, "job %s does not belong to host %s", jobID, sourceHost)
	}

	if job.Status != storage.JobStatusInProgress {
		// Already decided — by a prior commit call whose response was lost,
		// or by the stall watchdog racing ahead. Return the ground truth
		// instead of re-hashing or erroring, so retries are idempotent.
		server.logger.Info("BackupCommit for already-finalized job", "job_id", jobID, "status", job.Status)
		return &pb.BackupCommitResponse{Success: job.Status == storage.JobStatusSuccess}, nil
	}

	objectIDs, err := server.store.FileVersionsForJob(jobID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list file versions for job: %v", err)
	}
	sort.Strings(objectIDs)
	computed := sha256.Sum256([]byte(strings.Join(objectIDs, "\n")))
	matched := bytes.Equal(computed[:], req.FileListHash)

	if _, err := server.store.FinalizeBackupJob(jobID, matched); err != nil {
		return nil, status.Errorf(codes.Internal, "finalize backup job: %v", err)
	}
	server.liveness.Complete(jobID)

	server.logger.Info("Backup job committed", "job_id", jobID, "matched", matched)
	return &pb.BackupCommitResponse{Success: matched}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/bwfs/... -tags integration -v`
Expected: PASS for the entire package.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/bwfs/commit.go src/cmd/bwfs/integration_test.go
git commit -m "feat(bwfs): implement BackupCommit RPC with hash verification"
```

---

### Task 7: bwfs — job-timeout config, stall watchdog, startup reconciliation

**Files:**
- Modify: `src/common/config/config.go`
- Test: `src/common/config/config_test.go`
- Create: `src/cmd/bwfs/watchdog.go`
- Modify: `src/cmd/bwfs/main.go`
- Modify: `bin/local.conf`
- Modify: `src/e2e/config.conf`
- Test: `src/cmd/bwfs/integration_test.go` (watchdog behavior, exercised against the real liveness tracker directly — not through main.go, which isn't otherwise unit-tested in this codebase)

**Interfaces:**
- Produces: `config.Config.JobTimeoutSec int` (default `30` when the key is absent); `func watchStaleJobs(ctx context.Context, server *backupServer, timeout time.Duration)`.
- Consumes: `server.liveness.StaleJobs/Complete` (Task 5), `server.store.FinalizeBackupJob` (Task 2), `server.store.FailStaleInProgressJobs` (Task 3).

- [ ] **Step 1: Write the failing config tests**

Add to `src/common/config/config_test.go`:

```go
func TestParseConfig_JobTimeoutSecDefaultsTo30(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 30, conf.JobTimeoutSec)
}

func TestParseConfig_JobTimeoutSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nJobTimeoutSec=90\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 90, conf.JobTimeoutSec)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run TestParseConfig_JobTimeoutSec -v`
Expected: FAIL — `conf.JobTimeoutSec` undefined field.

- [ ] **Step 3: Add the config field with its default**

In `src/common/config/config.go`, add the field to `Config`:

```go
type Config struct {
	DefaultPort              int
	DefaultStreams           int
	LogFolder                string
	ClientHashQueryBatchSize int
	ConnectionTimeOutSec     int
	FileLockTimeoutSec       int
	StopStreamOnFileError    bool
	CAHost                   string
	JobTimeoutSec            int
}
```

In `ParseConfig`, set the default before scanning and add the parse case. Change:

```go
	config := &Config{}
	foundFields := make(map[string]bool)
```

to:

```go
	config := &Config{JobTimeoutSec: 30}
	foundFields := make(map[string]bool)
```

and add a case alongside the other `...Sec` fields:

```go
		case "JobTimeoutSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid JobTimeoutSec value at line %d: %s", lineNum, value)
			}
			config.JobTimeoutSec = number
			foundFields["JobTimeoutSec"] = true
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS for the whole package.

- [ ] **Step 5: Add the watchdog and wire it plus startup reconciliation into main.go**

Create `src/cmd/bwfs/watchdog.go`:

```go
package main

import (
	"context"
	"time"
)

// watchStaleJobs periodically fails any backup job that has gone silent for
// longer than timeout — the bound on how long a crashed brfs or a dead
// connection can leave a job ambiguously in_progress. Soft-fail: the job is
// marked failed in the database the instant the timeout fires; the stream
// goroutines that were serving it are left to end on their own (the
// FailedPrecondition check in ProcessBackupStream's receive loop rejects any
// further message they might still deliver).
func watchStaleJobs(ctx context.Context, server *backupServer, timeout time.Duration) {
	pollInterval := timeout / 6
	if pollInterval < 5*time.Second {
		pollInterval = 5 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, jobID := range server.liveness.StaleJobs(timeout) {
				if _, err := server.store.FinalizeBackupJob(jobID, false); err != nil {
					server.logger.Error("failed to finalize stale job", "job_id", jobID, "error", err)
					continue
				}
				server.liveness.Complete(jobID)
				server.logger.Warn("backup job timed out and was marked failed", "job_id", jobID, "timeout", timeout)
			}
		}
	}
}
```

In `src/cmd/bwfs/main.go`, inside `case "server":`, right after the `NewBackupServer` + startup-vacuum block and before the `listStore`/`restoreStore` setup, add:

```go
		staleCount, err := backupServer.store.FailStaleInProgressJobs()
		if err != nil {
			logger.Error("Startup job reconciliation failed", "error", err)
			os.Exit(1)
		}
		if staleCount > 0 {
			logger.Warn("Marked stale in-progress jobs as failed after restart", "count", staleCount)
		}

		go watchStaleJobs(ctx, backupServer, time.Duration(conf.JobTimeoutSec)*time.Second)
```

Add `"time"` to the import block of `main.go`.

- [ ] **Step 6: Add config keys to the fixture conf files**

In `bin/local.conf`, add after `StopStreamOnFileError=true`:

```
# Seconds of silence before an in_progress backup job is marked failed
JobTimeoutSec=30
```

In `src/e2e/config.conf`, add after `StopStreamOnFileError=true`:

```
JobTimeoutSec=30
```

- [ ] **Step 7: Add an integration test exercising the watchdog directly**

Add to `src/cmd/bwfs/integration_test.go`:

```go
// TestIntegration_StallWatchdog_FailsSilentJob verifies a job with no
// BackupCommit and no further stream activity is failed once its liveness
// entry exceeds the timeout — this exercises the same watchStaleJobs logic
// main.go runs on a ticker, called directly here with a near-zero timeout
// so the test doesn't need to sleep for main.go's real poll interval.
func TestIntegration_StallWatchdog_FailsSilentJob(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	require.NoError(t, env.store.store.EnsureBackupJob("job-stalled", "bwfs.internal"))
	env.store.liveness.Touch("job-stalled")

	// Directly invoke one watchdog pass with a timeout of 0 — everything
	// touched at least a nanosecond ago now counts as stale.
	stale := env.store.liveness.StaleJobs(0)
	require.Contains(t, stale, "job-stalled")
	for _, jobID := range stale {
		changed, err := env.store.store.FinalizeBackupJob(jobID, false)
		require.NoError(t, err)
		require.True(t, changed)
		env.store.liveness.Complete(jobID)
	}

	record, err := backupJobRow(t, env, "job-stalled")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusFailure, record.Status)
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... ./cmd/bwfs/... -tags integration -v`
Expected: PASS across both packages.

- [ ] **Step 9: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go src/cmd/bwfs/watchdog.go src/cmd/bwfs/main.go bin/local.conf src/e2e/config.conf src/cmd/bwfs/integration_test.go
git commit -m "feat(bwfs): add job-timeout config, stall watchdog, and startup reconciliation"
```

---

### Task 8: brfs — commit hash computation and retry-with-backoff

**Files:**
- Create: `src/cmd/brfs/commit.go`
- Test: `src/cmd/brfs/commit_test.go`

**Interfaces:**
- Produces: `func successFileHash(filesBackupState map[string]bool) []byte`; `func commitBackupJob(ctx context.Context, logger *slog.Logger, client pb.BackupServiceClient, hash []byte) (bool, error)`.
- Consumes: `pb.BackupServiceClient.BackupCommit` (Task 4), `filesBackupState` (existing map already built in `cmd/brfs/main.go`).

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/brfs/commit_test.go`:

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestSuccessFileHash_OnlyIncludesSuccessfulFilesAndIsOrderIndependent(t *testing.T) {
	stateA := map[string]bool{"b": true, "a": true, "c": false}
	stateB := map[string]bool{"a": true, "c": false, "b": true}

	assert.Equal(t, successFileHash(stateA), successFileHash(stateB))
}

func TestSuccessFileHash_DiffersWhenSuccessSetDiffers(t *testing.T) {
	withB := map[string]bool{"a": true, "b": true}
	withoutB := map[string]bool{"a": true, "b": false}

	assert.NotEqual(t, successFileHash(withB), successFileHash(withoutB))
}

// fakeBackupCommitClient implements just enough of pb.BackupServiceClient to
// drive commitBackupJob's retry loop; every other method panics if called.
type fakeBackupCommitClient struct {
	pb.BackupServiceClient
	calls    int
	failN    int // number of leading calls that return an error
	response *pb.BackupCommitResponse
}

func (f *fakeBackupCommitClient) BackupCommit(ctx context.Context, req *pb.BackupCommitRequest, opts ...grpc.CallOption) (*pb.BackupCommitResponse, error) {
	f.calls++
	if f.calls <= f.failN {
		return nil, errors.New("transport error")
	}
	return f.response, nil
}

func TestCommitBackupJob_SucceedsAfterTransientFailures(t *testing.T) {
	client := &fakeBackupCommitClient{failN: 1, response: &pb.BackupCommitResponse{Success: true}}
	logger := slog.Default()

	success, err := commitBackupJob(context.Background(), logger, client, []byte("hash"))
	require.NoError(t, err)
	assert.True(t, success)
	assert.Equal(t, 2, client.calls, "should have retried once after the first transient failure")
}

func TestCommitBackupJob_ReturnsErrorAfterExhaustingRetries(t *testing.T) {
	client := &fakeBackupCommitClient{failN: commitMaxAttempts}
	logger := slog.Default()

	_, err := commitBackupJob(context.Background(), logger, client, []byte("hash"))
	require.Error(t, err)
	assert.Equal(t, commitMaxAttempts, client.calls)
}

func TestCommitBackupJob_PropagatesServerRejection(t *testing.T) {
	client := &fakeBackupCommitClient{response: &pb.BackupCommitResponse{Success: false}}
	logger := slog.Default()

	success, err := commitBackupJob(context.Background(), logger, client, []byte("hash"))
	require.NoError(t, err, "a clean false response is not a transport error, must not retry or error")
	assert.False(t, success)
	assert.Equal(t, 1, client.calls)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/brfs/... -run 'TestSuccessFileHash|TestCommitBackupJob' -v`
Expected: FAIL — `successFileHash`, `commitBackupJob`, `commitMaxAttempts` undefined.

- [ ] **Step 3: Implement commit.go**

Create `src/cmd/brfs/commit.go`:

```go
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
)

const (
	commitMaxAttempts = 3
	commitBaseDelay   = 2 * time.Second
)

// successFileHash computes the SHA256 over the sorted, newline-joined IDs
// of every file brfs believes it backed up successfully this run — the same
// computation bwfs performs server-side from its own file_versions rows.
func successFileHash(filesBackupState map[string]bool) []byte {
	ids := make([]string, 0, len(filesBackupState))
	for id, ok := range filesBackupState {
		if ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return sum[:]
}

// commitBackupJob calls BackupCommit, retrying a few times with backoff on
// transport error — this call is the only positive signal that a whole
// backup succeeded, so it's worth insulating from a single flaky blip. A
// clean response (even Success: false, meaning the server rejected the
// backup as incomplete) is returned immediately without retrying — only
// transport-level errors trigger a retry.
func commitBackupJob(ctx context.Context, logger *slog.Logger, client pb.BackupServiceClient, hash []byte) (bool, error) {
	var lastErr error
	for attempt := 1; attempt <= commitMaxAttempts; attempt++ {
		resp, err := client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: hash})
		if err == nil {
			return resp.Success, nil
		}
		lastErr = err
		logger.Warn("BackupCommit failed, retrying", "attempt", attempt, "error", err)
		if attempt < commitMaxAttempts {
			time.Sleep(commitBaseDelay * time.Duration(attempt))
		}
	}
	return false, fmt.Errorf("BackupCommit failed after %d attempts: %w", commitMaxAttempts, lastErr)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/brfs/... -run 'TestSuccessFileHash|TestCommitBackupJob' -v`
Expected: PASS. (This test uses `commitBaseDelay * 1` and `* 2` second sleeps in the retry test — `TestCommitBackupJob_SucceedsAfterTransientFailures` will take ~2s and `TestCommitBackupJob_ReturnsErrorAfterExhaustingRetries` ~2s+4s=6s to run; that's acceptable for this package's test suite, consistent with the codebase not having a fake-clock abstraction elsewhere.)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/brfs/commit.go src/cmd/brfs/commit_test.go
git commit -m "feat(brfs): add BackupCommit hash computation and retry-with-backoff"
```

---

### Task 9: brfs — wire BackupCommit into the main run

**Files:**
- Modify: `src/cmd/brfs/main.go`

**Interfaces:**
- Consumes: `successFileHash`, `commitBackupJob` (Task 8).

This task has no new automated test — `cmd/brfs/main.go` isn't unit-tested anywhere else in this codebase (no `main_test.go` exists for either `brfs` or `bwfs`); its wiring is exercised end-to-end by the bwfs integration tests already added in Tasks 6–8 plus the `make test-e2e` Docker suite. Manual verification is Step 3 below.

- [ ] **Step 1: Add the commit call after the stream results are drained**

In `src/cmd/brfs/main.go`, replace the tail of `main()` (from `// Final analysis` through the end) with:

```go
	// Final analysis
	successCount := 0
	failedCount := 0

	for _, success := range filesBackupState {
		if success {
			successCount++
		} else {
			failedCount++
		}
	}

	state := "failed"
	if failedCount == 0 {
		state = "success"
	}
	logger.Info("Backup finished",
		"state", state,
		"count.success", successCount,
		"count.failed", failedCount,
	)

	if len(filesList) == 0 {
		// Nothing discovered, no streams ever opened, no job exists server-side to commit.
		return
	}

	hash := successFileHash(filesBackupState)
	committed, err := commitBackupJob(ctx, logger, client, hash)
	if err != nil {
		logger.Error("Backup commit failed", "error", err)
		os.Exit(1)
	}
	if !committed {
		logger.Error("Server rejected backup as incomplete")
		os.Exit(1)
	}
	logger.Info("Backup job committed successfully")
}
```

- [ ] **Step 2: Build to verify it compiles**

Run: `cd src && go build ./...`
Expected: success, no errors.

- [ ] **Step 3: Manually verify against a live bwfs**

Run:
```bash
cd src && go run ./cmd/bwfs server --port 15722 --storage /tmp/mp-manual-test-storage &
sleep 1
mkdir -p /tmp/mp-manual-test-src && echo "hello" > /tmp/mp-manual-test-src/hello.txt
go run ./cmd/brfs /tmp/mp-manual-test-src --destination localhost:15722 --job-id manual-test-1
```
Expected: brfs logs end with `"Backup job committed successfully"` and exits 0. Then kill the background bwfs process.

- [ ] **Step 4: Commit**

```bash
git add src/cmd/brfs/main.go
git commit -m "feat(brfs): call BackupCommit after all streams close"
```

---

### Task 10: Documentation updates

**Files:**
- Modify: `docs/protocols/backup.md`
- Modify: `docs/components/bwfs.md`
- Modify: `docs/components/brfs.md`

Per `.claude/CLAUDE.md`, feature changes and wire-level protocol changes require doc updates before committing. No tests — this is documentation only.

- [ ] **Step 1: Update docs/protocols/backup.md**

Replace the "Backup Job Tracking" section (currently lines 39–67, ending right before the ` ```mermaid ` block) with:

```markdown
## **Backup Job Tracking & Completion Verification**

Every `ProcessBackupStream` call carries a `job-id` gRPC metadata key, attached by `brfs` when it
opens the stream (not a message in the `FileRequest`/`FileResponse` protobuf — this is transport
metadata, so it requires no `.proto` changes). A stream with no `job-id` metadata is rejected
immediately with `codes.InvalidArgument`, before any file is processed.

One `brfs` invocation is one backup job: `brfs` generates a UUID at startup, or uses the value
passed via `--job-id`, and attaches it to every one of its `--streams` concurrent streams.

On the `bwfs` side, the first stream carrying a given `job-id` causes a `backup_jobs` row to be
created (idempotently — every stream of the job attempts this, only the first succeeds) with
`status=in_progress`; the row's `source_host` is read from the client's mTLS certificate (first SAN
entry, falling back to CommonName), not from anything the client reports in-band.

Every file version `bwfs` records (`file_versions` table) carries the `job_id` of the stream that
produced it. A duplicate observation of the same object within the same job (e.g. a future retry
re-sending a file) is a safe no-op — the first write for a given `(job_id, object_id)` pair wins.

**Completion is no longer inferred from streams closing.** After all of its streams have closed,
`brfs` computes a SHA256 over the sorted IDs of every file it believes it sent successfully this
run, and submits it via a new unary RPC:

```proto
rpc BackupCommit(BackupCommitRequest) returns (BackupCommitResponse);

message BackupCommitRequest {
  bytes file_list_hash = 1;
}
message BackupCommitResponse {
  bool success = 1;
}
```

`bwfs` independently recomputes the same hash from its own `file_versions` rows for that job and
compares. A match sets `status=success`; a mismatch sets `status=failure` and purges the job's
`file_versions` rows (raw chunk/file data is reclaimed later by the existing vacuum path). Either
way `finished_at` is set. `brfs` retries the commit call a few times with backoff on transport
error before giving up, since this one small call is now the only positive signal that a
(potentially large) transfer succeeded.

Two mechanisms bound how long a job can be left `in_progress` if `brfs` never gets to send a
commit (crash, network death):

- **Stall watchdog**: `bwfs` tracks, per job, the last time it saw any activity (a stream opening,
  or any message received on it). A background loop fails any job silent for longer than the
  configured `JobTimeoutSec` (default 30s) — `status=failure`, `file_versions` purged, same as a
  hash mismatch. This is a soft-fail: the stream goroutines that were serving the job are not
  forcibly disconnected, they're just rejected (`codes.FailedPrecondition`) if they try to deliver
  another message after the job's been decided.
- **Startup reconciliation**: when `bwfs` starts, any job still `status=in_progress` from a
  previous, uncleanly-terminated process is immediately failed the same way, before the server
  starts accepting new connections.

See [bwfs](../components/bwfs.md) for the schema and config key, and [brfs](../components/brfs.md)
for the commit-with-retry behavior.

Note on the sequence diagram below: the `START_STREAM:jobId:streamId` step shown there is
conceptual — in the actual gRPC transport this is the `job-id` metadata described above, attached
when the stream is opened, not a discrete message exchanged over the stream. The diagram also
predates `BackupCommit` and doesn't show it.
```

- [ ] **Step 2: Update docs/components/bwfs.md**

Find the "Backup Job Tracking" subsection (around line 38) and replace it with:

```markdown
#### Backup Job Tracking & Completion Verification

Every stream `bwfs` accepts must carry `job-id` gRPC metadata (sent by `brfs` — see
[brfs](brfs.md)). `bwfs` records each job in a `backup_jobs` table (`job_id`, `source_host`,
`started_at`, `finished_at`, `status`) and tags every row in `file_versions` with the `job_id` of
the run that produced it. `source_host` is read from the client's verified mTLS identity, not
anything the client reports in-band.

A job starts `status=in_progress` and is only finalized (`success` or `failure`, with
`finished_at` set) by one of three paths:

1. `brfs` calls the unary `BackupCommit` RPC after all its streams close; `bwfs` recomputes a
   SHA256 over its own `file_versions` for that job and compares it to the hash `brfs` submits —
   match is `success`, mismatch is `failure` (and purges that job's `file_versions`).
2. The stall watchdog fails any job with no activity for longer than the `JobTimeoutSec` config
   key (default 30 seconds).
3. On startup, `bwfs` fails any job left `in_progress` by a previous, uncleanly-terminated process,
   before accepting new connections.

See [Backup Protocol](../protocols/backup.md) for the full RPC and lifecycle.
```

Also add `JobTimeoutSec` to whatever list of server config keys already exists in this file (grep the file for where `ConnectionTimeOutSec`/`FileLockTimeoutSec` are documented and add a matching line):

```markdown
- `JobTimeoutSec` — seconds of no activity before an in_progress backup job is marked failed *(default: 30)*
```

- [ ] **Step 3: Update docs/components/brfs.md**

Find the `--job-id` description (around line 22-28) and add a note about the commit step after it:

```markdown
After all of its streams close, `brfs` computes a SHA256 over the sorted IDs of every file it
believes it sent successfully and submits it to `bwfs` via the `BackupCommit` RPC, retrying a few
times with backoff on transport error. `brfs` exits non-zero if the commit call ultimately fails to
reach the server, or if the server reports the hash didn't match what it actually received — see
[Backup Protocol](../protocols/backup.md) for the full mechanism.
```

- [ ] **Step 4: Commit**

```bash
git add docs/protocols/backup.md docs/components/bwfs.md docs/components/brfs.md
git commit -m "docs: document BackupCommit, stall watchdog, and startup reconciliation"
```

---

### Task 11: Full test suite and lint pass

**Files:** none (verification only)

- [ ] **Step 1: Run the full unit + integration suite**

Run: `make test`
Expected: all packages pass, including every test added in Tasks 1–10.

- [ ] **Step 2: Run go vet**

Run: `make lint`
Expected: no findings.

- [ ] **Step 3: Confirm no dangling references to removed symbols**

Run: `grep -rn "FinishBackupJob\|jobTracker\|newJobTracker" src/`
Expected: no matches.

- [ ] **Step 4: Run the Docker-based e2e suite**

Run: `make test-e2e`
Expected: passes (requires Docker daemon, ~3 min). If this reveals any e2e assertion that depended on the old finished-on-stream-close behavior, fix it following the same pattern as Task 6's integration test updates.

No commit for this task — it's a verification gate. If Steps 1–4 all pass, the feature is complete and ready for `superpowers:finishing-a-development-branch`.
