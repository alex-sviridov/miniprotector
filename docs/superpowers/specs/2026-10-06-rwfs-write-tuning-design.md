# Design: rwfs Write-Path Tuning

> Builds on `rwfs restore` phase 2 (`src/cmd/rwfs/restorefile.go`, see
> `2026-08-17-restore-file-content-design.md`) and the per-OS I/O split used by the reader
> (`2026-10-06-brfs-read-path-tuning-design.md`). No protocol change. Linux gets the full treatment;
> Windows gets portable fallbacks.

## Problem

`writeRestoreFile` opens the destination directly with `O_TRUNC`, calls `Truncate(size)`, writes through
a 1 MB buffer, and never fsyncs or renames. Consequences:

1. **`--overwrite` destroys the old file before the new content is verified.** A failed or interrupted
   restore loses both versions.
2. **A crash leaves torn files under their final names**, and a rerun with skip-existing trusts them.
3. **No durability at all**, and no control over dirty-page build-up or page-cache eviction during a
   large restore.
4. **Late `ENOSPC`.** `Truncate` makes a sparse file, so a full disk only shows up mid-stream.

## Decisions

- **Deferred commit.** A file appears under its real name only once its data is durable. Per-file
  fsync+rename is kept only as the measured baseline (`restore_commit_files=1`).
- **Batching lives in one shared per-run committer** (not per worker, not one `syncfs` at the end).
- **In scope:** temp file + atomic rename, `fallocate`, early writeback (`sync_file_range`), batched
  fsync, directory fsync, cache hygiene (`FADV_DONTNEED` after the data is clean).
- **Out of scope:** sparse-aware restore, `O_DIRECT`, buffer-size changes.
- **Measured, not assumed.** Each optimization is benchmarked with `mpbench`; one that does not move the
  numbers is recorded as not adopted, as with read-path tuning.

## Components (`src/cmd/rwfs`)

| File | Role |
|------|------|
| `restoresink.go` | Portable. `commitQueue`: collects verified temp files; flushes when pending files ≥ `restore_commit_files` (default 64) or pending bytes ≥ `restore_commit_bytes` (default 64 MiB), and once at end of run. `0` for either means flush after every file. |
| `restoreio_linux.go` | `preallocate` (`fallocate`; on `EOPNOTSUPP`/`ENOSYS` falls back to `Truncate`; real `ENOSPC` is an error). `startWriteback` (`sync_file_range(SYNC_FILE_RANGE_WRITE)`). `dropCache` (`FADV_DONTNEED`). `syncDir`. |
| `restoreio_windows.go` | `preallocate` = `Truncate`. `startWriteback` and `dropCache` no-ops. `syncDir` no-op (NTFS metadata is journaled; file `Sync` maps to `FlushFileBuffers`). |

## Per-file flow (`writeRestoreFile`)

1. Stat and skip/dir checks unchanged.
2. Create temp `<destdir>/.<name>.mptmp-<8 hex>` in the destination directory (same filesystem, so
   rename is atomic).
3. `preallocate(meta.Size)`. `ENOSPC` fails the file before any chunk is consumed (non-retryable, like
   other local errors).
4. Stream chunks into the 1 MB buffer, verifying BLAKE3 per chunk as today. After each buffer flush,
   `startWriteback` on the written range.
5. After the CRC32 check passes, submit `{file, tmpPath, destPath, size}` to the committer. The file stays
   open; no close or fsync on the worker.
6. On any failure before submit the worker closes and removes the temp file (unchanged cleanup contract).

## Checkpoint flush (committer)

For the pending batch: `fsync` each file (cheap, writeback already running) → `dropCache` each (pages are
clean now) → close each (Windows cannot rename an open file) → `rename` temp over destination (atomic
replace for `--overwrite`) → `syncDir` each distinct parent once → count each as restored. Pending files
hold their descriptors open until the checkpoint, so `restore_commit_files` is capped at 1024. `bytes`/file counts reported by `restoreFileContent`
move to the point of commit, so the summary only counts durable files.

## Errors and crashes

- First failure still aborts the run and cancels in-flight transfers. Pending uncommitted temp files are
  closed and removed; already-committed files stay.
- A flush failure (fsync, rename, dir sync) is a hard, non-retryable error for the files in that batch
  (their temp files are removed) and aborts the run.
- A crash leaves only `.mptmp-*` files. At start, `rwfs restore` removes stale `.mptmp-*` files in the
  destination directories it touches, so skip-existing remains trustworthy.
- `withRetry` is unaffected: each attempt starts from a fresh temp file.

## Configuration

| Key | Default | Meaning |
|-----|---------|---------|
| `restore_commit_files` | `64` | Files per checkpoint, 0–1024; `0` = every file |
| `restore_commit_bytes` | `67108864` | Bytes per checkpoint; `0` = every file |

Config keys only; no new CLI flags.

## Testing

- `commitQueue`: flush triggers (files, bytes, end of run); `0` flushes every file; ordering
  fsync → rename → dir sync via injected hooks.
- Crash ordering: a fault before the checkpoint leaves nothing at the final path; stale `.mptmp-*` are swept.
- `--overwrite`: failed restore keeps the old content intact; successful restore replaces it atomically.
- `preallocate` fallback on a filesystem without `fallocate`; `ENOSPC` surfaces before streaming (small
  tmpfs, skipped if the environment can't mount one).
- Existing restore tests (`restorefile_test.go`, `rwfs_exec_test.go`) pass unchanged in behavior.
- **Benchmark:** `mpbench --cold-cache` on `small`, `mixed`, `large` against baseline `main` and
  `restore_commit_files=1`; record restore-phase throughput and client RSS. Results go in this doc under
  a Status block with adopted / not adopted per optimization.

## Documentation

`docs/components/rwfs.md` (write contract, temp files, config keys), `docs/PERFORMANCE.md` and
`docs/components/mpbench.md` if the benchmark usage changes, `CHANGELOG.md` before merge. No proto change.
