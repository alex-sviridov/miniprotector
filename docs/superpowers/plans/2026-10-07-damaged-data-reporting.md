# Damaged data reporting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Flag (not delete) file data damaged by a corrupt chunk, report it as `DataLoss` on restore, let `rwfs restore` skip damaged files and finish the rest, and show damage in `bwfs list`.

**Architecture:** `FileDataRecord.damaged_at` replaces the delete in `MarkChunkCorrupted`; dedup and `FileData` ignore flagged rows; `bwfs` restore maps damage to `codes.DataLoss`; `rwfs` treats `DataLoss` as final and non-aborting; `blake3_mismatch` retried once.

**Tech Stack:** Go 1.26, GORM + modernc SQLite, gRPC status codes. No `.proto` change.

**Spec:** `docs/superpowers/specs/2026-10-07-damaged-data-reporting-design.md` (binding; read first).

## Global Constraints

- No `.proto` change; gRPC code `codes.DataLoss` only.
- `damaged_at` is a nullable `*time.Time` on `FileDataRecord`; a row is healthy iff `damaged_at IS NULL`.
- `MarkChunkCorrupted` still deletes the chunk row and that chunk's links; flags dependents (never overwrites an existing `damaged_at`); keeps all other links.
- Dedup (`FileDataExists`) and `FileData(fileID)` require `damaged_at IS NULL`. The resolver and `bwfs list` still include flagged rows.
- `rwfs`: `DataLoss` is never retried; during `restore` a damaged file does not cancel the run (other failures keep abort-on-first-failure); final error names the damaged count. `blake3_mismatch` is retried once.
- TDD for every behaviour. Before each commit: `cd src && go build ./... && go vet ./storage/... ./cmd/bwfs/... ./cmd/rwfs/... ./common/... && go test -race ./storage/... ./cmd/bwfs/... ./cmd/rwfs/... ./common/...` and `go test -race -tags integration ./cmd/bwfs/... ./cmd/rwfs/... ./storage/filesystem/...`.
- Update docs in the same task as the code (see each task). Plain readable Go; comments explain why.
- Commit trailer (every commit): `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr`.

---

### Task 1: Store flags damaged file data

**Files:**
- Modify: `src/storage/filesystem/models.go` (`DamagedAt *time.Time`), `src/storage/filesystem/chunks.go` (`MarkChunkCorrupted`, the shared `dropChunk` used by compaction), `src/storage/filesystem/filedata.go` (`FileDataExists`, `FileData`), `src/storage/interface.go` (comment for `MarkChunkCorrupted`)
- Test: `src/storage/filesystem/damaged_test.go` (+ adapt existing tests that assert FileData deletion, e.g. in `store_test.go`, `chunks_pack_test.go`, `compact_test.go`)

**Interfaces — Produces:** `FileDataRecord.DamagedAt *time.Time` (column `damaged_at`); `MarkChunkCorrupted(hash)` flags instead of deletes; log line `chunk marked corrupt` via `log/slog` default logger or an injected logger if the Store already has one (check; if none, use `slog.Error`) with attrs `chunk_hash`, `file_versions_damaged`, `paths` (up to 5).

- [ ] Test: after `MarkChunkCorrupted`, the dependent `FileData` row exists with non-nil `damaged_at`; the chunk row and that hash's links are gone; an unrelated file is untouched; marking again does not change `damaged_at`.
- [ ] Test: `FileDataExists` is false and `FileData` returns not-found for a flagged row; after a re-upload (CreateFileData + links + FinalizeFileData for the same file_id) `FileDataExists` is true and the resolver-style "latest finalized per file_id" query returns the new row.
- [ ] Test: vacuum (`Vacuum` and `VacuumOnline`) removes a flagged row once no file version references its file_id, and keeps it while one does.
- [ ] Test: compaction's corrupt-record path (`dropChunk`) flags dependents too (adapt the existing test).
- [ ] Implement, adapt old tests, run the gate, commit `feat(storfs): flag file data damaged instead of deleting it`.
- [ ] Docs: `docs/components/bwfs.md` (corrupt-chunk handling: flagged, dedup ignores, healed by next backup, removed with versions), `docs/protocols/backup.md` ("How does the system recover from a corrupted chunk?"), `src/storage/CLAUDE.md` if it describes deletion.

---

### Task 2: bwfs restore reports DataLoss; list marks damage

**Files:**
- Modify: `src/cmd/bwfs/restoreserver.go`, `src/cmd/bwfs/list.go`, `src/common/listformat/listformat.go`
- Test: `src/cmd/bwfs/restore_test.go` (and the existing restoreserver tests), `src/cmd/bwfs/list_test.go`, `src/common/listformat/*_test.go`
- Docs: `docs/protocols/restore.md`, `docs/components/bwfs.md`

**Interfaces — Consumes:** `FileDataRecord.DamagedAt`, `MarkChunkCorrupted` (Task 1). **Produces:** `RestoreFile` returns `status.Error(codes.DataLoss, ...)` for damaged data; `listformat.Row.Damaged bool` (JSON `"damaged":true` with `omitempty`; table shows a `DAMAGED` column only if any row is damaged).

- [ ] Test (real store, existing restore test helpers): `RestoreFile` on a flagged `FileData` returns `codes.DataLoss` before any event is sent.
- [ ] Test: a chunk corrupted on disk mid-stream -> stream ends with `DataLoss`, the file is flagged, the chunk marked; a not-found chunk row -> same `DataLoss` + marked; a transient read error -> `Internal`, nothing flagged/marked (keep existing behaviour).
- [ ] Test: the restore resolver still lists a flagged file (no change expected; assert it so a later change cannot hide damage).
- [ ] Test: `bwfs list` JSON and table mark a damaged row; table has no extra column when nothing is damaged (existing golden output unchanged).
- [ ] Implement (`restoreserver.go` select `damaged_at` with the file lookup and fail early; map marked-corrupt outcomes to `DataLoss`), run the gate, commit `feat(bwfs): report damaged backup data as DataLoss and in list`.
- [ ] Docs: restore protocol error table (DataLoss row; Internal only for transient), bwfs list description.

---

### Task 3: rwfs treats DataLoss as final and keeps restoring

**Files:**
- Modify: `src/cmd/rwfs/restorefile.go`, `src/cmd/rwfs/restore.go`, `src/cmd/rwfs/verify.go`, `src/cmd/rwfs/retry.go` if the retry predicate lives there
- Test: `src/cmd/rwfs/*_test.go` (restore, verify, retry)
- Docs: `docs/components/rwfs.md`, `docs/protocols/restore.md` (client behaviour), `CHANGELOG.md` (dated 2026-10-07 entry, most recent first)

**Interfaces — Consumes:** `codes.DataLoss` from Task 2. **Produces:** `restoreFileResult.Damaged bool` (true when the error is a gRPC `DataLoss`); verify reason string `data_loss`.

- [ ] Test: a `DataLoss` status from `RestoreFile` is not retried (fake client call count == 1, no backoff) in both `restore` and `verify`.
- [ ] Test: restore of several files where one returns `DataLoss`: the healthy files are written and committed (present at their final names with correct content), the damaged one is absent with no temp file left, the damaged path is logged at Error, and `restore` returns an error that states the damaged count; a non-`DataLoss` failure still aborts the run as before (existing tests stay green).
- [ ] Test: `blake3_mismatch` is retried once (fake client serving a bad chunk then a good one -> success; two bad -> final failure after exactly 2 calls); `crc_mismatch` stays final.
- [ ] Test: `verify` summary counts a `DataLoss` file as a failure with reason `data_loss` and continues with the rest.
- [ ] Implement, run the gate, commit `feat(rwfs): damaged files do not abort restore; retry blake3 mismatch once`.
- [ ] Docs: rwfs component (new behaviour and exit status), CHANGELOG entry (what changed and why: damaged data is flagged, reported, restore continues; catalog/web display is in the backlog).
