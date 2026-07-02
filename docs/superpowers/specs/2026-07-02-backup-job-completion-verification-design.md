# Backup Job Completion Verification — Design

## Problem

Backup job tracking (see `2026-07-02-backup-job-tracking-design.md`) records that a job happened,
but `bwfs` currently declares a job "finished" purely because its stream refcount reached zero —
i.e. because the streams went away, not because it verified the job actually completed correctly.
A dropped connection, a client crash, or a network partition all look identical to a clean finish:
`finished_at` gets set either way. There is no positive signal that every file `brfs` intended to
send actually landed, and no bound on how long a job can sit ambiguously unfinished if `brfs` dies
mid-run without ever closing its streams.

## Goals

- `bwfs` only marks a job `success` after independently verifying, via a content-addressed hash
  comparison, that every file `brfs` believes it sent successfully is recorded as a `file_versions`
  row for that job.
- A job that goes silent for longer than a configured timeout is proactively marked `failure`
  instead of remaining `in_progress` indefinitely.
- A `bwfs` restart cleans up any jobs left `in_progress` by a previous, uncleanly-terminated process.
- `backup_jobs` gains an explicit `status` (`in_progress` / `success` / `failure`) as the single
  source of truth for job outcome. `finished_at` is no longer driven by stream-refcount-hits-zero.

## Non-Goals

- No automatic resend of missing files on hash mismatch — `brfs` exits with an error; re-running
  `brfs` (with the same or a new `--job-id`) is the recovery path. Consistent with the original
  job-tracking design's "no retry-at-the-file-level orchestration" non-goal.
- No forced mid-flight cancellation of a stalled job's gRPC streams. The watchdog soft-fails the
  job in the database the moment its timeout is hit; the stream goroutines that were serving it are
  left to end on their own (client-side error, or gRPC/OS keepalive eventually reclaiming a dead
  connection). Actually pre-empting a blocked `stream.Recv()` requires restructuring every stream's
  receive loop into a goroutine-plus-select pattern — out of scope for this change.
- No `bwfs jobs` CLI/list surface — still out of scope, same as the original design.
- No change to the existing dual-layer (file + chunk) integrity verification. This feature checks
  *completeness* (did all intended files arrive for this job), not per-file/per-chunk correctness,
  which the wire protocol already guarantees independently.

## Architecture

### Schema

```go
type BackupJobRecord struct {
    JobID      string `gorm:"primaryKey"`
    SourceHost string
    StartedAt  time.Time
    FinishedAt *time.Time
    Status     string `gorm:"default:in_progress"` // in_progress | success | failure
}
```

`EnsureBackupJob` sets `Status: "in_progress"` explicitly on its initial insert; its
`ON CONFLICT(job_id) DO NOTHING` semantics are otherwise unchanged.

### Store Interface Changes (`src/storage/interface.go`)

Replaces `FinishBackupJob`:

```go
// Object IDs of every file_versions row recorded for a job, for hash verification.
FileVersionsForJob(jobID string) ([]string, error)

// The job record, for source-host verification in BackupCommit. Nil if not found.
GetBackupJob(jobID string) (*BackupJob, error)

// Atomically transitions a job from in_progress to success/failure. On failure, also purges
// the job's file_versions rows in the same transaction. Returns false (no-op) if the job was
// already finalized — guards the race between BackupCommit arriving and the stall watchdog
// firing concurrently, and makes duplicate/retried BackupCommit calls idempotent.
FinalizeBackupJob(jobID string, success bool) (bool, error)

// Startup reconciliation: marks every in_progress job as failure (purging their file_versions),
// for jobs orphaned by a previous bwfs process that never cleanly finished them. Returns count.
FailStaleInProgressJobs() (int64, error)
```

`FinalizeBackupJob` is a CAS:
`UPDATE backup_jobs SET status=?, finished_at=? WHERE job_id=? AND status='in_progress'`,
checked via `RowsAffected`. When `success=false`, the same transaction also runs
`DELETE FROM file_versions WHERE job_id=?`. Raw file/chunk data is untouched — it is reclaimed
later by the existing `Vacuum()` path, out of scope here.

`jobtracker.go` (the in-memory stream refcount) is deleted entirely, along with the old
`FinishBackupJob` method and its call site in `ProcessBackupStream`.

### BackupCommit RPC

New unary RPC on the existing service (`src/api/backup.proto`):

```proto
service BackupService {
  rpc ProcessBackupStream(stream FileRequest) returns (stream FileResponse);
  rpc BackupCommit(BackupCommitRequest) returns (BackupCommitResponse);
}

message BackupCommitRequest {
  bytes file_list_hash = 1; // SHA256 over the sorted, newline-joined object IDs brfs believes it sent successfully
}

message BackupCommitResponse {
  bool success = 1;
}
```

`job_id` travels the same way it already does for streams: outgoing gRPC metadata (`"job-id"`) on
the same `ctx` — no new plumbing needed for job identity.

**`brfs` side** (`main.go`, after the stream `WaitGroup` joins):
1. Collect `file.ID()` for every entry where the locally-tracked `filesBackupState[id] == true`
   (already computed today from `resultChan`), sort lexicographically, join with `\n`, SHA256.
2. Call `BackupCommit` with that hash, retrying a few times with backoff on transport error — this
   call is now the linchpin confirming a large transfer's success, worth insulating from a single
   flaky network blip.
3. If the call ultimately fails to reach the server, or `resp.Success == false`, log an error and
   exit non-zero. The job stays `in_progress` server-side until the stall watchdog or a restart
   eventually fails it.
4. If zero files were discovered (nothing to send, no streams ever opened, no job row created),
   skip calling `BackupCommit` entirely — there is no job to reference.

**`bwfs` side** (new `src/cmd/bwfs/commitserver.go`, same pattern as `listserver.go`):
1. Read `job_id` from metadata (reuse `jobIDFromMetadata`); reject (`InvalidArgument`) if absent.
2. Read the caller's verified hostname via `mtls.PeerHostname(ctx)`; look up the job via
   `GetBackupJob`; reject (`NotFound`) if no such job exists, reject (`PermissionDenied`) if the
   hostname doesn't match `job.SourceHost` — same defense-in-depth as the original job-creation
   check, preventing one host from committing/forging another host's job.
3. If the job is already finalized (not `in_progress` — e.g. the watchdog raced ahead, or this is a
   retried commit call whose earlier response was lost), skip re-hashing and return the job's
   actual current status. This makes retries and races idempotent.
4. Otherwise, fetch `FileVersionsForJob(jobID)`, sort, SHA256, compare to `req.FileListHash`.
5. `store.FinalizeBackupJob(jobID, matched)`.
6. Remove the job from the stall watchdog's liveness tracking (`liveness.Complete(jobID)`) — a
   finalized job is no longer a stall candidate.
7. Return `{success: matched}`.

### Stall Watchdog & Startup Reconciliation

**Liveness tracker** (new `src/cmd/bwfs/liveness.go`, replaces `jobtracker.go`):

```go
type jobLiveness struct {
    mu       sync.Mutex
    lastSeen map[string]time.Time
}
func (l *jobLiveness) Touch(jobID string)
func (l *jobLiveness) Complete(jobID string)
func (l *jobLiveness) StaleJobs(timeout time.Duration) []string
```

`ProcessBackupStream`'s receive loop calls `Touch(jobID)` on every successfully received
`FileRequest` (and once at stream open) — the "any piece of data for this job" marker, shared
across however many concurrent streams belong to the job since they all key on the same `jobID`.

**Background watchdog**, started alongside the gRPC server in `bwfs`'s `main.go` and stopped on
shutdown: polls on a fixed interval well below the configured timeout so a stalled job isn't
detected much later than necessary (proposing `job-timeout / 6`, floored at 5s, which works out to
5s at the 30s default) and, for each job whose `lastSeen` exceeds the configured timeout, calls
`store.FinalizeBackupJob(jobID, false)` then `liveness.Complete(jobID)`.

This is the **soft-fail** approach: the job is dead in the database the instant the timeout fires.
The stream goroutines serving it are *not* forcibly cancelled — they unblock on their own (client
eventually errors out, or the connection is reclaimed by gRPC/OS keepalive). Any further message
that arrives for an already-finalized job is rejected by the handler (checks
`GetBackupJob(jobID).Status != "in_progress"` before persisting) rather than silently writing to a
job whose outcome has already been decided.

**Config**: `job-timeout` (duration) is read from `bwfs`'s existing config, not a CLI flag —
default **30 seconds**.

**Startup reconciliation** (`bwfs` `main.go`, right after store init, before the gRPC server starts
accepting): `store.FailStaleInProgressJobs()` bulk-transitions every `in_progress` job to `failure`
(purging their `file_versions` in the same transaction) and logs the count — cleaning up jobs
orphaned by an unclean previous shutdown before the process accepts new work.

## Data Flow

```
brfs: discover files (full walk, upfront) → open N streams → per-file protocol (unchanged)
  → WaitGroup.Wait() for all streams to join
  → successIDs = sorted [id for id, ok in filesBackupState if ok]
  → hash = SHA256(join(successIDs, "\n"))
  → BackupCommit(hash), retry w/ backoff on transport error
      bwfs: BackupCommit handler
        → validate job-id, source host (mTLS) matches job.SourceHost
        → if job already finalized: return its current status (idempotent)
        → objectIDs = sorted FileVersionsForJob(jobID); serverHash = SHA256(join(objectIDs, "\n"))
        → matched = (serverHash == hash)
        → FinalizeBackupJob(jobID, matched)   [sets status+finished_at; purges file_versions if !matched]
        → liveness.Complete(jobID)
        → return {success: matched}
  → if !success or RPC never succeeds: brfs exits non-zero; job stays in_progress until watchdog/restart

meanwhile, concurrently:
  bwfs watchdog (every job-timeout/6 poll): for jobID with lastSeen older than job-timeout
    → FinalizeBackupJob(jobID, false); liveness.Complete(jobID)

bwfs startup: FailStaleInProgressJobs() — cleans up jobs left in_progress by prior process instance
```

## Error Handling

- Missing/unparseable `job-id` on `BackupCommit` → `codes.InvalidArgument`, same as streams.
- Unknown `job_id` (no `backup_jobs` row) → `codes.NotFound`. Only occurs for a bogus job-id;
  `FinalizeBackupJob` never deletes the `backup_jobs` row itself, only `file_versions`.
- Source host on the calling cert doesn't match `job.SourceHost` → `codes.PermissionDenied`.
- Job already finalized when `BackupCommit` arrives (watchdog raced ahead, or a retried commit call
  arrives after an earlier one already succeeded but its response was lost in transit): the CAS
  no-ops; the handler returns the job's actual current status instead of re-computing.
- Hash mismatch → `status=failure`, `file_versions` purged, `{success: false}`; `brfs` exits
  non-zero.
- Zero files discovered (empty source tree) → no streams open, no job row created; `brfs` skips
  calling `BackupCommit`.
- `brfs` crash before commit, or `bwfs` restart mid-job → bounded instead of unbounded: the stall
  watchdog (30s default) or startup reconciliation flips the job to `failure` shortly after, rather
  than leaving it ambiguously `in_progress` forever.
- **Known accepted risk**: the per-message `liveness.IsFinalized` check in the stream receive loop
  is not atomic with the write it gates — if a job is finalized (by `BackupCommit` or the stall
  watchdog) while one of its messages is already past that check and mid-processing, `fileWritten`'s
  `EnsureFileVersion` call can still land afterward, leaving one stray `file_versions` row on a job
  already marked `failure`. This requires a narrow timing coincidence (a legitimately slow transfer
  racing the watchdog's timeout, or a misbehaving client) and is harmless when it happens — the
  underlying file data is real and complete, and the job's `success`/`failure` verdict is unaffected;
  it is not a path to a false `success`. Accepted as-is rather than fixed, given the narrowness and
  low impact.

## Testing

- Unit: `FinalizeBackupJob` in_progress→success (sets `finished_at`, leaves `file_versions` intact)
  and in_progress→failure (sets `finished_at`, purges only that job's `file_versions`; other jobs'
  rows untouched).
- Unit: `FinalizeBackupJob` called twice for the same job — second call is a no-op (`changed=false`),
  doesn't clobber the first outcome.
- Unit: `FailStaleInProgressJobs` flips multiple `in_progress` jobs to `failure` and purges their
  `file_versions`; jobs already `success`/`failure` are untouched.
- Unit: `jobLiveness.StaleJobs(timeout)` returns only jobs whose last `Touch` predates the timeout.
- Integration: full `brfs` run against a live `bwfs` → `backup_jobs.status=success`, `finished_at`
  set, after `BackupCommit`.
- Integration: induced hash mismatch (e.g. a `file_versions` row removed after transfer but before
  commit) → `status=failure`, `file_versions` purged, `brfs` exits non-zero.
- Integration: a job goes silent past a test-shortened `job-timeout` with no `BackupCommit` ever
  sent → watchdog marks it `failure` on its own.
- Integration: restart `bwfs` with a pre-seeded `in_progress` job row + `file_versions` → startup
  reconciliation flips it to `failure` and purges its `file_versions` before serving new requests.
- Integration: `BackupCommit` against an unknown `job_id` → `NotFound`; against a job owned by a
  different source host → `PermissionDenied`.

## Documentation Impact

Per `.claude/CLAUDE.md`, before committing:
- Update `docs/protocols/backup.md` — document the `BackupCommit` RPC, the `status` lifecycle, and
  the stall-timeout/reconciliation behavior, replacing the now-superseded refcount description.
- Update `docs/components/bwfs.md` — document the `job-timeout` config key, the `status` column,
  and watchdog/reconciliation behavior.
- Update `docs/components/brfs.md` — document the commit-with-retry step after streaming completes.
- Update `docs/ARCHITECTURE.md` if the job lifecycle diagram changes.
