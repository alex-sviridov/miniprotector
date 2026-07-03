# Changelog

All notable changes to this project are documented here, most recent first.

## 2026-07-02 — Async catalog replication (catalogsync)

Added `catalogsync`, a new standalone component that tails a `bwfs` node's `file_versions` table
and forwards new rows to a future backup catalog, independently of `bwfs`'s own availability.
`catalogsync` opens `bwfs`'s SQLite database strictly read-only and tracks its own replication
progress in a small local cursor file, retrying with backoff whenever the catalog (represented
today by a logging stand-in `Sender`) is unreachable — nothing is marked replicated until a batch
is confirmed sent, so an outage or restart never loses data. This required replacing
`file_versions`' synthetic `UUID` primary key with a real `INTEGER PRIMARY KEY AUTOINCREMENT`
`seq` column (immune to the row-number reuse a bare SQLite `rowid` allows after a failed job's
rows are purged) and its natural `(job_id, object_id)` identity for external consumers.

## 2026-07-02 — Backup job completion verification

`bwfs` no longer treats a job as finished just because its streams closed. Added a `BackupCommit`
RPC: after a backup run's streams close, `brfs` submits a hash of the files it believes it sent,
and `bwfs` independently recomputes the same hash from its own catalog before marking the job
`success` — a mismatch marks it `failure` and purges that job's incomplete catalog entries. A
background watchdog now fails jobs that go silent past a configurable timeout (`JobTimeoutSec`,
default 30s), and `bwfs` reconciles any jobs left `in_progress` by an unclean shutdown on restart.
`backup_jobs` gained an explicit `status` column (`in_progress`/`success`/`failure`) as the source
of truth for job outcome.
