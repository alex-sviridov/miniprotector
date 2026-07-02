# Changelog

All notable changes to this project are documented here, most recent first.

## 2026-07-02 — Backup job completion verification

`bwfs` no longer treats a job as finished just because its streams closed. Added a `BackupCommit`
RPC: after a backup run's streams close, `brfs` submits a hash of the files it believes it sent,
and `bwfs` independently recomputes the same hash from its own catalog before marking the job
`success` — a mismatch marks it `failure` and purges that job's incomplete catalog entries. A
background watchdog now fails jobs that go silent past a configurable timeout (`JobTimeoutSec`,
default 30s), and `bwfs` reconciles any jobs left `in_progress` by an unclean shutdown on restart.
`backup_jobs` gained an explicit `status` column (`in_progress`/`success`/`failure`) as the source
of truth for job outcome.
