# Restore: File Content Phase — Design

> **Builds on:** `docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md` (phase 1 —
> directory structure creation, real and disk-mutating as of that design) and
> `docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md` (the resolution pipeline,
> `restoreDestPath` rename logic, and `streamResolvedRows` both phases already consume). This design
> adds phase 2: actually writing file content to the destination filesystem. `rwfs restore` was, until
> now, log-only for files — it resolved and logged what a restore would do but never called
> `RestoreFile` or wrote a byte. This is the first round that does.

## Problem

Phase 1 proved out real filesystem mutation for directories. File content is the much larger,
much more consequential remaining piece: `rwfs restore` must fetch each resolved file's chunks
from `bwfs` via `RestoreFile` (the same RPC `rwfs verify` already uses to *check* files, never
previously called by `restore`) and write them to the (`dest_path`-renamed) destination path,
respecting `--overwrite`, verifying integrity as it writes, and aborting the whole job immediately
on any failure — then reporting what happened.

## Goals

- For every resolved file row, fetch its chunks via `RestoreFile` and write them to
  `restoreDestPath`-computed destination, verifying per-chunk BLAKE3 and whole-file CRC32 exactly as
  `rwfs verify` already does (bwfs's own documented contract: it sends chunks as-is and "the caller
  is responsible for any integrity checks" — see `docs/protocols/restore.md`).
- `--overwrite` becomes real: a pre-existing destination file is skipped (not an error) when false,
  overwritten when true. A pre-existing *non*-file (a directory) at the destination is always a hard
  error, regardless of `--overwrite` — mirrors phase 1's directory-vs-non-directory rule exactly.
- Multiple files transfer concurrently, reusing the existing generic `runWorkerPool`, sized by a new
  `--streams` flag (default 4, same default and validation as `verify --streams`).
- Any failure (stream error, integrity mismatch, disk write error, or an illegal pre-existing
  non-file) aborts the whole restore: the file being written is cleaned up (best-effort delete of
  the partial/corrupt file), every other in-flight file transfer is cancelled immediately, and no
  new file transfer starts.
- A single default file permission (`0o644`) is used for every created/overwritten file — a stub,
  exactly like phase 1's directories (`0o755`), pending real captured-permission restore in a future
  round.
- On full success, one structured summary line reports `files_written`, `bytes_written`, `skipped`.
- Per-file success is logged at `Debug` level only (see Logging below) — not gated by `--quiet`,
  since it's off by default regardless.

## Non-Goals (this round)

- **Permissions/ownership restoration** — still a stub, for both files and directories. No metadata
  blob is threaded onto the wire this round (same deferral phase 1 already made).
- **Retries.** Unlike `verify`, a transient `RestoreFile` stream error is not retried — any failure
  aborts immediately, per the instruction that drove this design. (`verify`'s retry-with-backoff
  exists because verify's job is "check everything, report all failures"; restore's job here is "do
  this exact job correctly or not at all.")
- **Destination-collision detection** between two rules resolving to the same file path — same
  deferral phase 1 already made for directories, now extended to files. `--overwrite` governs
  whichever rule's write happens to land second.
- **An overlapped receive/write pipeline** (separate goroutine overlapping network receive with disk
  write within one file). Considered and explicitly deferred — cross-file concurrency (`--streams`)
  already provides the bulk of the available parallelism; revisit only if profiling shows a single
  large file's restore is disk-write-bound.
- **`file.Sync()` / explicit durability guarantees.** No part of this codebase calls `Sync` on
  written data (bwfs's own chunk writes don't either) — matching that existing convention, restore
  relies on normal OS buffering.
- **OS-specific write paths.** Considered and rejected — see Architecture, "Why no build-tag split."

## Architecture

### 1. Collecting file rows: `files []restoreFile`

`runRestoreWithConn`'s existing streaming loop over `rowsCh` already branches on `row.GetType()`,
appending directory rows to `dirs[]` and merely logging file rows. It now also appends each file row
to a new `files []restoreFile` slice:

```go
type restoreFile struct {
	FileUUID string
	Source   string
	Path     string
	DestPath string
}
```

The existing `logger.Info("resolved", ...)` line for file rows, and its `--quiet` gating, is
unchanged — this design doesn't touch phase 0's logging.

This buffers every resolved file row in memory before phase 2 starts, the same tradeoff phase 1
already accepted for `dirs[]`, now extended to a set that is normally far larger. It's required
here, not just convenient: phase 2 cannot safely start writing any file until phase 1 has finished
creating every directory that file's path depends on, so the two phases cannot be interleaved
row-by-row as the stream arrives.

### 2. Phase ordering (unchanged before phase 2)

1. Resolve stream drains → `dirs[]` and `files[]` collected.
2. `resolver.NotFound()` checked — any failure aborts before anything is touched (unchanged).
3. Phase 1 (directories) runs exactly as today — abort on first failure (unchanged).
4. **New:** Phase 2 (file content) runs only once phase 1 has fully succeeded.

### 3. Phase 2 driver

```go
func restoreFileContent(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, files []restoreFile, overwrite bool, streams int) error
```

- Logs `logger.Info("restoring file content")` once at start (mirrors phase 1's start line).
- `writeCtx, cancel := context.WithCancel(ctx)`, `defer cancel()`.
- Feeds `files` into a `workCh chan restoreFile` via a small goroutine (`select` on `writeCtx.Done()`
  so it stops feeding once cancelled — mirrors `verify.go`'s existing producer pattern).
- `resultCh := runWorkerPool(writeCtx, streams, workCh, func(ctx context.Context, f restoreFile) restoreFileResult { return writeRestoreFile(ctx, client, f, overwrite) })`.
- `writeRestoreFile` is a pure worker function — it does no logging itself (mirrors `verifyFile`'s
  existing shape: it returns a result, the driver decides what to log based on it). `restoreFileResult`
  therefore carries everything the driver needs to log without re-deriving it: `Source`, `Path`,
  `DestPath`, `Bytes`, `Skipped`, `Err`.
- Consumes `resultCh` fully (required — the pool's `out` channel only closes once every worker has
  drained `workCh`, and walking away early would leak goroutines). For each result, in order of
  precedence:
  - `Err != nil` and this is the **first** error seen: `logger.Error("failed to restore file", "source", ..., "path", ..., "dest_path", ..., "reason", err)`,
    call `cancel()`, remember this as the error to return.
  - `Err != nil` after cancellation has already begun: not logged individually (expected fallout of
    cancelling in-flight transfers, not a new independent failure) — not counted as written.
  - `Skipped`: `logger.Debug("file skipped, already exists", "source", ..., "path", ..., "dest_path", ...)`,
    increments the `skipped` counter.
  - otherwise (success): `logger.Debug("file written", "source", ..., "path", ..., "dest_path", ..., "bytes", result.Bytes)`,
    increments `files_written` and `bytes_written`.
- On full success (no error ever seen): `logger.Info("restore complete", "files_written", n, "bytes_written", bytes, "skipped", skipped)`.
- **On failure: no summary line is logged** — mirrors phase 1's existing, already-tested convention
  (`TestRunRestore_AbortsOnDirectoryCreationFailureBeforeSummary`) of not reaching the summary when a
  phase aborts partway. The one detailed `Error` line above carries the diagnostic.
- Returns the remembered error (wrapped, e.g. `fmt.Errorf("restore file content: %w", err)`), or nil.

### 4. Per-file work: `writeRestoreFile`

```go
type restoreFileResult struct {
	Source, Path, DestPath string
	Bytes                  int64
	Skipped                bool
	Err                    error
}

const defaultRestoreFilePerm = 0o644
const restoreWriteBufferSize = 1 << 20 // 1MB -- coalesces 64KB chunks (see chunker.go) into far fewer syscalls

func writeRestoreFile(ctx context.Context, client pb.RestoreServiceClient, f restoreFile, overwrite bool) restoreFileResult
```

Every `restoreFileResult` returned carries `f.Source`/`f.Path`/`f.DestPath` through unchanged, regardless
of which path below produced it, so the driver never needs a side-lookup back into `f`.

1. `os.Stat(f.DestPath)`:
   - exists and is a directory → hard error (`"path exists and is a directory: %s"`), regardless of
     `--overwrite` — mirrors phase 1's inverse case exactly.
   - exists and is a regular file, `!overwrite` → return `restoreFileResult{Skipped: true, ...}`, no
     RPC call made at all (cheaper than dispatching a doomed-to-be-thrown-away fetch).
   - exists and is a regular file, `overwrite` → proceed (will truncate).
   - doesn't exist → proceed (will create).
   - any other `Stat` error → hard error.
2. Stall watchdog exactly like `verifyFile` (`withStallWatchdog`, no pause/resume — every `Recv`
   below is followed by pure in-process work: hashing and a buffered write, never a blocking
   hand-off to another goroutine).
3. `client.RestoreFile(ctx, &pb.RestoreRequest{FileUuid: f.FileUUID})`; first event must be `Meta`
   (same shape check `verifyFile` already does).
4. `os.OpenFile(f.DestPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, defaultRestoreFilePerm)`, then
   `out.Truncate(meta.Size)` — portable preallocation to the file's final size (see "Why no
   build-tag split" below).
5. `bufio.NewWriterSize(out, restoreWriteBufferSize)` wraps `out`. For each chunk event: `Recv`,
   `touch()`, verify `BLAKE3(chunk.Data) == chunk.Hash` (mismatch → fail, reason `blake3_mismatch`,
   same as verify), `bufw.Write(chunk.Data)` (a write error here is the literal "file write error"
   this design's abort behavior is named for), `checksum.FeedChunk` into a running CRC32, break on
   `chunk.Eof`.
6. `bufw.Flush()`; compare the accumulated CRC32 against `meta.ExpectedChecksum` (mismatch → fail,
   reason `crc_mismatch`, same as verify).
7. **Cleanup on any failure path** (steps 3–6): `out.Close()` first (Windows disallows removing an
   open file handle), then `os.Remove(f.DestPath)` best-effort — a corrupted or half-written file
   must never be left looking like a successfully restored one. The removal's own error is not
   itself logged (best-effort cleanup, not diagnostically critical) and never shadows the original
   failure, which is what's returned in `Err`.
8. On success: `out.Close()`, return `restoreFileResult{Source: f.Source, Path: f.Path, DestPath: f.DestPath, Bytes: meta.Size}`.
   `writeRestoreFile` itself never logs — see the driver's per-result handling above, which mirrors
   `verifyFile`'s existing split between a pure worker function and a logging driver loop.

### Why no build-tag split (Windows vs. Linux)

The repo does split OS-specific code exactly once today —
`src/workload/filesystem/fileinfo_{windows,linux}.go` — but only because reading *native permission
bits/ACLs* genuinely requires different APIs per OS (`golang.org/x/sys/windows` vs.
`syscall.Stat_t`/`golang.org/x/sys/unix`). Permission/ownership restore is explicitly out of scope
this round (Non-Goals above), so that divergence never comes up here. Everything phase 2 actually
does — `os.OpenFile`, `bufio.Writer`, `Write`, `Truncate`, `Close`, `Remove` — is standard-library
`os`/`bufio` with no OS-specific behavior to branch on; Go's runtime already handles path separators
and file semantics transparently for this pattern. `Truncate(meta.Size)`, in particular, is a single
portable call that preallocates on both NTFS and Linux filesystems — no OS-specific syscall (e.g.
`fallocate`/`SetEndOfFile`) needed to get most of that benefit. One Windows-specific correctness
detail (not a performance one) is handled by ordering alone: `Close()` before `Remove()` in the
cleanup path, since Windows generally refuses to delete a file that's still open.

### 5. CLI: new `--streams` flag on `restore`

`arguments.go`: `restoreCmd.Flags().IntVar(&args.Streams, "streams", 4, "Number of concurrent file restore workers")`,
validated the same way `verify`'s already is (`common.ValidateStreamsCount`). `runRestore` /
`runRestoreWithConn` gain a `streams int` parameter (same position verify's has), threaded down to
`restoreFileContent`.

`--overwrite`'s existing flag help text ("logged only, not yet enforced") is updated — it's now
enforced.

## Logging

| Line | Level | Gating |
|---|---|---|
| `"resolved"` (phase 0, per file) | Info | `--quiet` (unchanged, pre-existing) |
| `"creating restored directory structure"` / `"restored directory structure created"` (phase 1) | Info | none (unchanged) |
| `"restoring file content"` (phase 2 start) | Info | none |
| `"file written"` / `"file skipped, already exists"` (phase 2, per file) | **Debug** | `--debug` only, via slog's level filter — unconditional call, no manual `if` (matches `brfs`'s existing per-item logging convention, e.g. `onefile.go`'s `logger.Debug("Sending chunk data")`) |
| `"failed to restore file"` (phase 2, first failure only) | Error | none |
| `"restore complete"` (phase 2, full success only) | Info | none |

## Error Handling

- Pre-existing non-file at a file's destination: hard error, same severity/handling as any other
  write failure.
- `--overwrite=false` and file exists: not an error — skipped, counted, RPC never called.
- Stream error, BLAKE3 mismatch, CRC32 mismatch, or local disk write error: hard error, aborts the
  whole restore. The partial file is removed. Every other in-flight transfer is cancelled.
- Every failure surfaces through the one `logger.Error("failed to restore file", ...)` line for the
  triggering file; no summary line follows.

## Testing

Reuses existing test infrastructure in `verify_test.go` directly (same package): `realRestoreServer`
(a working fake `RestoreServiceServer` that actually serves valid chunk streams for a seeded file),
`seedRestorableFile`, `expectedCRC32`.

- A folder selection with real seeded file content: file is actually written to
  `dest_path`-renamed disk location; content matches; `files_written`/`bytes_written` summary is
  correct; `Debug`-level per-file line only appears when the test logger is configured at Debug
  level (assert absence at Info level, presence at Debug level).
- `--overwrite=false`, destination file already exists: file is left untouched (assert original
  content unchanged), counted in `skipped`, `RestoreFile` never called for it (assert via
  `realRestoreServer.Requested()`).
- `--overwrite=true`, destination file already exists with different content: file is overwritten;
  new content matches.
- Destination path is an existing directory: hard error, no partial file created.
- A chunk with a tampered/mismatched hash (BLAKE3 mismatch): write aborts, partial file is removed
  from disk, restore returns an error, no summary line logged.
- Two files, `--streams` ≥ 2, one engineered to fail (e.g. missing/corrupt chunk): the other transfer
  is observably cancelled or does not complete successfully; overall restore returns an error.
- `os.OpenFile`/`os.Truncate` failure (e.g. parent directory missing — a file-level rule with no
  accompanying folder rule, restoring into a destination whose parent was never created): hard
  error, same handling as any other write failure.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule:

- **`docs/components/rwfs.md`** — `## restore` section: file content restore is now real, not
  log-only; document `--overwrite` as enforced, add `--streams` to the flags table, document the new
  per-phase log lines and the Debug-only per-file line, update the top summary sentence ("File
  content restore is still log-only" is no longer true).
- **`docs/protocols/restore.md`** — CLI → RPC Mapping: `rwfs restore --rules-stdin` now does call
  `RestoreFile` for each resolved file (the line stating it never does is no longer accurate).
- **`docs/ARCHITECTURE.md`** — update the `rwfs`/`agent` restore-execution description: file content
  restore is now real.
- **`CHANGELOG.md`** — entry before merge, flagging that `rwfs restore` now writes real file content
  to the destination filesystem, in addition to directory structure.

## Relationship to Prior Work

The 2026-08-16 log-only design and the same-day directory-structure design deliberately proved out
resolution, precedence, and one small, low-risk real write (directories, no content) before
committing to the largest remaining piece. This design spends that groundwork on the actual
payload — file bytes — reusing the exact resolution pipeline, the exact `dest_path` rename logic,
and the exact per-chunk integrity-checking logic `rwfs verify` already established, so the only
genuinely new code is the write path itself and its concurrency/cancellation/cleanup contract.
