# Damaged data: flag, report, and keep restoring

Date: 2026-10-07. Branch: `storfs-reliability-perf`. Components: `src/storage/filesystem`, `src/cmd/bwfs`, `src/cmd/rwfs`, `src/common/listformat`.

## Problem

When `bwfs` finds a corrupt chunk (restore, compaction) it calls `MarkChunkCorrupted`, which deletes the chunk
and every `FileData` row that used it. Consequences:

- The damage is **invisible**: the restore resolver only considers finalized `FileData`, so a folder restore
  covering a damaged version lists no such file and can report success with that file missing. Nothing records
  which versions were lost.
- The restore stream fails with a generic `Internal`, a stream error `rwfs` treats as retryable; the retries
  then get `NotFound` and burn backoff.
- One damaged file **aborts the whole `rwfs restore`** (first failure cancels all workers and aborts the
  commit queue), so healthy files in the same folder are not restored either.
- A client `blake3_mismatch` (a fault in transit; the server already verified the bytes) is never retried.

## Decisions

- **Flag, do not delete.** `FileDataRecord` gains `damaged_at *time.Time` (nullable, indexed not needed).
  `MarkChunkCorrupted` still removes the chunk row and that chunk's links, but flags every dependent
  `FileData` that is not already flagged (`damaged_at = now`) instead of deleting it. Links of the file are
  otherwise kept: they are keyed by `file_id`, which a healthy re-upload of the same file shares.
- **Dedup ignores damaged rows.** `FileDataExists` and `FileData` require `damaged_at IS NULL`, so the next
  backup of an unchanged file re-uploads it. The new `FileData` row is newer, so the resolver's "latest finalized
  per file_id" picks the healthy one. The damaged row stays until its versions expire (vacuum already removes
  `FileData` with no versions).
- **Restore reports `DataLoss`.** `RestoreFile` on a flagged `FileData` returns `codes.DataLoss` ("backup data
  damaged") before any event. A chunk that fails with `ErrChunkCorrupt`/`ErrChunkNotFound` mid-stream is marked
  (flagging the file) and the stream ends with `codes.DataLoss` instead of `Internal`. Other read errors stay
  `Internal` and never mark. No `.proto` change.
- **`rwfs` treats `DataLoss` as final, and keeps going.** `DataLoss` is not retryable. During `restore`, a
  damaged file does not cancel the run: it is logged at Error with its path and counted; every other file is
  restored and committed; at the end `rwfs` returns an error naming the number of damaged files (non-zero exit).
  Every other failure keeps today's abort-on-first-failure behaviour. `verify` reports damaged files with their
  own reason (`data_loss`) in its summary.
- **One retry on client `blake3_mismatch`** (only a fault in transit can cause it): retryable once, then final.
- **Operator visibility.** `MarkChunkCorrupted` logs one Error line: chunk hash, number of file versions
  flagged, up to 5 paths. `bwfs list` marks damaged rows (JSON `"damaged": true` omitted when false; table adds a
  `DAMAGED` marker column only when at least one row is damaged). The gRPC list protocol is unchanged.
- Out of scope (backlog "Show damaged versions in the catalog and web UI"): catalog/web display; background
  scrub; a client-to-server corruption report RPC.

## Data flow (corrupt chunk on disk)

1. Restore reads the chunk, verification fails -> `ErrChunkCorrupt`.
2. `bwfs` drops the chunk row and links for that hash, flags all dependent `FileData` damaged, logs one line,
   ends the stream with `DataLoss`.
3. `rwfs` records the file as damaged (no retry), continues with the other files, exits non-zero with a count.
4. Later restores of that version fail immediately with `DataLoss`; `bwfs list` shows it as damaged.
5. The next backup of the same unchanged file re-uploads it (dedup ignores the damaged row); restore then
   serves the new `FileData`.

## Testing (TDD)

- store: mark flags instead of deleting; chunk row and that hash's links removed; already-flagged rows keep
  their original timestamp; other files untouched; `FileDataExists`/`FileData` ignore flagged rows; re-upload
  after damage yields a healthy row and the resolver picks it; vacuum removes a flagged row once its versions
  are gone; compaction's corrupt-record path flags as well.
- bwfs: `RestoreFile` on flagged data -> `DataLoss`, no events; mid-stream corrupt chunk -> `DataLoss` and
  flagged; transient error -> `Internal`, nothing flagged; resolver still lists a flagged file; `bwfs list`
  marks it (table and JSON).
- rwfs: `DataLoss` not retried (call count 1); a damaged file among healthy ones: healthy files restored and
  committed, error with count returned, exit non-zero; non-`DataLoss` failure still aborts; `blake3_mismatch`
  retried once then final; `verify` summary shows `data_loss`.
- Docs: `docs/protocols/restore.md`, `docs/protocols/backup.md` (corrupt-chunk recovery section),
  `docs/components/{bwfs,rwfs}.md`, `CHANGELOG.md`.
