# Backup Job Tracking — Design

## Problem

Backups happen through discrete `brfs` runs, but nothing today records that a run happened, which
files it touched, or when it started/finished. `jobId` exists only as a hardcoded logging string
(`"BackupJob"` in `src/cmd/brfs/main.go`) — it's never sent to `bwfs` and never persisted. This
makes it impossible to answer "what did backup run X do?" or "when did the last backup from host Y
complete?".

## Goals

- Every `brfs` invocation is a distinct, identifiable backup job.
- `bwfs` records each job (who ran it, when it started, when it finished) and which file versions
  it produced.
- The mechanism is reliable under partial failure (client crash, server restart) and safe under
  concurrency (multiple streams per job, multiple jobs at once, duplicate file sends).

## Non-Goals

- No job cancellation, pause/resume, or scheduling — that's a future control-plane concern.
- No `bwfs jobs` CLI/list surface for querying job history — out of scope for this change; the data
  will exist in the DB for a future component to read.
- No retry-at-the-file-level orchestration — this only makes the write path safe *if* a duplicate
  arrives, it doesn't introduce new retry behavior.

## Architecture

A backup job = one `brfs` invocation.

1. **Job ID origin**: `brfs` generates a UUID `job_id` at startup. An optional `--job-id` flag lets
   a caller (e.g. a future scheduler) supply their own instead, for correlation with an external
   system.
2. **Wire transmission**: `brfs` attaches `job_id` as outgoing gRPC metadata (`job-id` key) when
   opening each of its `--streams` concurrent `ProcessBackupStream` calls. No `.proto` changes —
   metadata is set once per stream at creation, read once server-side.
3. **Job creation**: `bwfs`'s `newStreamHandler` (already invoked once per stream) reads `job-id`
   from `stream.Context()` and idempotently ensures a `backup_jobs` row exists
   (`INSERT ... ON CONFLICT(jobid) DO NOTHING`). Every stream of the job attempts this — cheap,
   and safe because the store already serializes all writes through a single SQLite connection
   (`SetMaxOpenConns(1)` in `db.go`).
4. **Source host**: read from the client's verified mTLS certificate
   (`peer.FromContext(ctx)` → `TLSInfo.State.PeerCertificates[0].DNSNames[0]`), not from
   client-reported data. `certrequest` always puts the primary hostname first in the cert's SAN
   list (`sans := append([]string{hostname}, extraSANs...)` in `certrequest/main.go`), so this is
   a verified identity, not something `brfs` could spoof by embedding a different hostname string
   in its file paths. Falls back to `Subject.CommonName` if `DNSNames` is empty; if neither is
   present, the stream is rejected (see Error Handling).
5. **Job completion**: `backupServer` (the long-lived per-process struct, not `streamHandler`)
   holds an in-memory `map[jobID]int` reference count, guarded by a mutex. Incremented when a
   stream starts (in `ProcessBackupStream`, after the job-id is validated), decremented via
   `defer` when the stream ends (EOF or error). When a job's count reaches zero, `bwfs` writes
   `finished_at = now()`.
6. **File versions**: `streamHandler` reads `job-id` once (same place it reads it for job
   creation) and threads it into every `EnsureFileVersion` call, replacing the current
   `CreateFileVersion`.

## Data Flow

```
brfs startup
  → job_id = uuid.New() (or --job-id override)
  → for each of N streams: open ProcessBackupStream with metadata "job-id": job_id
       bwfs: newStreamHandler
         → read job-id from context; reject stream if absent/unparseable
         → EnsureBackupJob(job_id, source_host_from_mtls)   [idempotent, every stream]
         → refcount[job_id]++
       ... normal file/chunk protocol, unchanged ...
       → on file skip or successful write: EnsureFileVersion(job_id, object_id, metadata, ctime)
       stream ends (EOF/error)
         → defer: refcount[job_id]--; if 0, FinishBackupJob(job_id) sets finished_at
```

## Schema

```go
type BackupJobRecord struct {
    JobID      string `gorm:"primaryKey"`
    SourceHost string
    StartedAt  time.Time
    FinishedAt *time.Time // nil until the job's last stream closes
}

type FileVersionRecord struct {
    UUID      string `gorm:"primaryKey"`
    ObjectID  string `gorm:"uniqueIndex:idx_job_object"`
    JobID     string `gorm:"uniqueIndex:idx_job_object"`
    Metadata  []byte
    Ctime     int64
    CreatedAt time.Time
}
```

**Why no junction table** (`backup_job_file_versions`, as originally proposed): every existing
call to `CreateFileVersion` generates a brand-new UUID row — there's no code path that looks up
and reuses an existing `file_versions` row across two different jobs. The true relationship is
one job → many file versions, not many-to-many. A `job_id` column with an index is simpler,
avoids a second table, and avoids needing to wrap two inserts in a transaction to keep them
consistent.

**Why unique on `(job_id, object_id)`**: without it, a file reported twice in the same job (e.g. a
future retry re-sending a file after a stream error) would create two `file_versions` rows for the
same object in the same job. The unique index plus upsert makes a duplicate send a safe no-op.

`db.go`'s `AutoMigrate` call gains `&BackupJobRecord{}`.

## Store Interface Changes (`src/storage/interface.go`)

```go
EnsureBackupJob(jobID, sourceHost string) error
FinishBackupJob(jobID string) error
EnsureFileVersion(jobID, objectID string, metadata []byte, ctime int64) error // replaces CreateFileVersion
```

`EnsureFileVersion` drops the `(string, error)` return of the old `CreateFileVersion` — both
existing call sites in `handler.go` already discard the returned UUID (`_, err := ...`), so there's
no caller that needs it.

`EnsureBackupJob` uses `clause.OnConflict{Columns: []clause.Column{{Name: "job_id"}}, DoNothing: true}`.
`EnsureFileVersion` uses the same pattern keyed on `(job_id, object_id)`. Both are first-write-wins:
the first observation in a job stands; duplicates are no-ops.

## Error Handling

- **Missing/unparseable `job-id` metadata**: `newStreamHandler`/`ProcessBackupStream` rejects the
  stream immediately (before any file processing), returning an error to the client rather than
  falling back to a default or no-job mode. This is a hard requirement going forward, not an
  optional enhancement — every stream must belong to a job.
- **No SAN and no CommonName on peer cert**: stream rejected the same way. Shouldn't happen given
  `mtls.go` requires `RequireAndVerifyClientCert` and `certrequest` always sets a SAN, but it's a
  defensive check, not a load-bearing assumption.
- **`brfs` crash mid-run**: some streams never send EOF/close; the refcount for that job never
  reaches zero; `finished_at` stays `NULL` forever. This is treated as correct signal (job did not
  complete), not a bug to fix.
- **`bwfs` restart mid-job**: in-memory refcounts are lost. Any jobs with open streams at restart
  time behave the same as a client crash — `finished_at` never gets set for them. `started_at`
  and any `file_versions` already written before the restart remain intact (they're durable, only
  the completion signal is in-memory).
- **Duplicate file send within a job**: handled by the `EnsureFileVersion` upsert — no error, no
  duplicate row, first write wins.

## Testing

- Unit: `EnsureBackupJob` called twice with the same `job_id` results in exactly one row, original
  `started_at` preserved.
- Unit: `EnsureFileVersion` called twice with the same `(job_id, object_id)` results in exactly one
  row, first `metadata`/`ctime` preserved.
- Unit: refcount transitions — two streams open for the same job, `finished_at` stays nil until
  both close; the second close sets it.
- Integration (extending the existing e2e harness in `src/e2e/`): run `brfs --streams N` against a
  live `bwfs`, assert one `backup_jobs` row with correct `source_host` (matching the mTLS cert
  identity, not any client-reported hostname) and non-nil `finished_at` after the run completes,
  and that `file_versions.job_id` is populated for both the skip-path and write-path files.
- Integration: a stream opened without `job-id` metadata is rejected by the server.

## Documentation Impact

Per `.claude/CLAUDE.md`, this changes wire-level behavior (new required metadata key) even without
a `.proto` diff, so before committing:
- Update `docs/protocols/backup.md` — document the `job-id` metadata requirement and the job
  lifecycle (creation on first stream, completion on last stream close).
- Update `docs/components/brfs.md` — document the new `--job-id` flag.
- Update `docs/components/bwfs.md` — document the `backup_jobs`/`file_versions.job_id` schema
  addition and that `bwfs` now requires `job-id` metadata on every stream.
