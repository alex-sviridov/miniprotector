# Restore: Per-File Retry on Transient Stream Errors — Design

> **Builds on:** `docs/superpowers/specs/2026-08-17-restore-file-content-design.md` (phase 2 — file
> content restore, the "do this exact job correctly or not at all" abort-on-first-failure model this
> design does not change) and `docs/superpowers/specs/2026-08-23-restore-policy-lifecycle-design.md`
> (agent's restore/verify tasks now run at most once, ever — success or failure, no policy-level
> retry).

## Problem

`rwfs verify` retries a transient `RestoreFile` stream error per file, up to `--retries` times with
capped-doubling backoff (`verifyFileWithRetry`, `verify.go`). `rwfs restore`'s file-content phase has
no retry at all: `writeRestoreFile` fails a file on the first stream error, and `restoreFileContent`
(`restore.go`) then cancels every other in-flight transfer and aborts the whole run.

That asymmetry was a deliberate choice on 2026-08-17 ("restore's job here is 'do this exact job
correctly or not at all'"), reasonable at the time because a failed restore task was retried
automatically by `agent` on its next reconcile tick. On 2026-08-23 that safety net was removed:
restore/verify policies now run at most once ever (`agent/restore.go`'s `Due: LastAttemptAt == nil`),
and `policy-server` auto-deletes the policy once Loki shows a `finish` event, regardless of whether
it was `success` or `failure`.

The result: a single transient network blip on one file, in a restore touching thousands, now
permanently aborts the whole policy with no automatic recovery — the operator must notice the
`failure` in the Jobs UI and manually resubmit the entire restore cart.

## Goals

- A transient per-file stream error during `rwfs restore` is retried, same backoff policy `verify`
  already uses, before it's allowed to abort the run.
- `verify` and `restore` share one retry implementation, not two copies that can drift.
- The retry count defaults from config (so an operator can raise it fleet-wide without touching every
  invocation) but stays overridable per-invocation via `--retries`, for both commands.

## Non-Goals

- **No change to the abort-on-failure model.** A file that exhausts its retries still fails the whole
  restore run immediately, cancelling other in-flight transfers — unchanged from the 2026-08-17
  design. This is retry-before-abort, not abort-avoidance.
- **No change to what's retryable.** Integrity failures (BLAKE3/CRC mismatch) and destination-side
  problems (existing directory at the destination, local disk I/O errors) are never retried in either
  command — retrying can't fix them, and `verify` already draws this line for BLAKE3/CRC.
- **No change to agent's one-shot policy semantics** (`Due: LastAttemptAt == nil`) or the
  policy-server cleanup sweep. This design only makes a single `rwfs` invocation more resilient to
  transient blips before that outer one-shot boundary is ever reached.
- **No retry across separate `rwfs` invocations** — only within one run's per-file attempts.

## Architecture

### 1. Config-driven default: `RwfsRetries`

`common/config.Config` gains `RwfsRetries int`, following the exact pattern
`RestoreCleanupIntervalSec`/`RestoreCleanupGracePeriodSec` established for the 2026-08-23 design:
defaulted to `3` in the struct-literal defaults, parsed under `case "RwfsRetries":`, **not** added to
`requiredFields` — an existing `local.conf` missing the key keeps working, silently getting the
built-in default of 3. This mirrors `brfs/arguments.go`'s existing `conf.DefaultStreams` ->
`--streams` default wiring, applied to retries instead of streams.

`arguments.go`: both `verifyCmd` and `restoreCmd` register `--retries` with default `conf.RwfsRetries`
(replacing the currently-hardcoded `3` on `verifyCmd`, and adding the flag to `restoreCmd`, which has
never had one). The existing `--retries must be at least 1` validation, currently gated on
`args.Action == "verify"` only, widens to cover `restore` too — one check, not two copies.

### 2. Shared `withRetry`: one implementation, not two

New `retry.go` (alongside `workerpool.go`, which already establishes generics as this codebase's way
to kill duplicated concurrency plumbing rather than copy-pasting it):

```go
// withRetry runs attempt up to maxRetries times over ctx, retrying only
// while isRetryable(result) is true, backing off with capped doubling
// between attempts (retryBackoffInitial to retryBackoffCap). Stops
// immediately -- no further attempts, no backoff wait -- the moment
// isRetryable returns false, so a terminal failure surfaces without
// delay. logger should already be scoped with per-item fields (path,
// file_uuid, etc.) via .With(); each retried attempt logs "reason"
// (from the reason func) and "attempt" at Warn. maxRetries must be >= 1
// (enforced at the CLI layer); 1 means no retry.
func withRetry[R any](
    ctx context.Context,
    logger *slog.Logger,
    maxRetries int,
    attempt func(context.Context) R,
    isRetryable func(R) bool,
    reason func(R) string,
) R
```

`retryBackoffInitial`/`retryBackoffCap` move from `verify.go` into `retry.go` (same values, same
place a shared helper's constants belong).

- **`verifyFileWithRetry`** (`verify.go`) becomes a thin wrapper:
  `withRetry(ctx, logger.With("path", row.Path, "file_uuid", row.FileUuid), maxRetries, func(ctx) verifyResult { return verifyFile(ctx, client, row) }, func(r) bool { return !r.ok && r.reason != "blake3_mismatch" && r.reason != "crc_mismatch" }, func(r) string { return r.reason })`
  — same retry set, same backoff, same log line shape as today; purely a refactor.
- **`writeRestoreFileWithRetry`** (new, `restorefile.go`) is the analogous wrapper for restore:
  `withRetry(ctx, logger.With("source", f.Source, "path", f.Path, "dest_path", f.DestPath), maxRetries, func(ctx) restoreFileResult { return writeRestoreFile(ctx, client, f, overwrite) }, func(r) bool { return r.Err != nil && r.Retryable }, func(r) string { return r.Err.Error() })`.
  `restoreFileContent` (`restore.go`) calls this instead of `writeRestoreFile` directly, and gains a
  `retries int` parameter threaded from `runRestoreWithConn`/`runRestore`/`main.go`, exactly the path
  `verify`'s `retries` already follows.

### 3. Classifying restore's retryable failures: `restoreFileResult.Retryable`

`restoreFileResult` gains `Retryable bool`. `writeRestoreFile` sets it `true` only on the
network/RPC-facing branches — the ones currently wrapped `fmt.Errorf("stream error: %w", err)`:
the initial `RestoreFile` call, the first `Recv` (and its meta-type check), and each subsequent
`Recv` (and its chunk-type check). It stays `false` (the zero value) everywhere else: the
pre-existing-directory conflict, local `os.Stat`/`Open`/`Truncate`/`Write`/`Flush`/`Close` errors, and
both integrity mismatches (`blake3_mismatch`, `crc_mismatch`). This draws the same line `verify`
already draws (retry the stream to `bwfs`, never retry a correctness or local-filesystem problem) —
restore just has a few extra terminal categories `verify` has no equivalent of, since `verify` never
touches the destination filesystem.

Retrying is safe with no new cleanup logic: `writeRestoreFile`'s existing `defer` already removes any
partial destination file before returning on failure, so each retry attempt starts from a clean slate
(re-stats, re-opens, re-truncates).

## Testing

- `retry_test.go` (new): `withRetry`'s own behavior in isolation — recovers after N retryable
  failures, exhausts `maxRetries` and returns the last failure, stops immediately (no backoff wait) on
  a non-retryable result, respects `ctx` cancellation during a backoff wait.
- `verify_test.go`: existing `TestVerifyFileWithRetry_BacksOffBetweenAttempts` and friends continue to
  exercise `verifyFileWithRetry` end-to-end — behavior unchanged, now proving the thin wrapper composes
  correctly with the shared helper.
- `restore_test.go` (new cases): a fake server that fails a file N times then succeeds (retry
  recovers, run completes), fails all `maxRetries` attempts (final failure surfaces, run still aborts
  and cancels other in-flight transfers — `TestRestoreFileContent_FirstFailureCancelsOtherInFlightTransfers`
  stays valid), and a `blake3_mismatch`/`crc_mismatch`/pre-existing-directory case (fails immediately,
  `Retryable == false`, no backoff delay incurred).
- `arguments_test.go`: `--retries` accepted and validated (`>= 1`) for `restore`, not just `verify`;
  default sourced from `conf.RwfsRetries` when the flag is omitted.
- `config_test.go`: `RwfsRetries` parses when present, defaults to `3` when absent, and is not in
  `requiredFields`.

## Documentation Impact

Per `.claude/CLAUDE.md`:

- `docs/components/rwfs.md`: the Flags table's `--retries` row currently lives under `verify` only,
  default hardcoded "3" — update to note it also applies to `restore`, and that the default now comes
  from config (`RwfsRetries`) rather than being flag-hardcoded. The existing paragraph describing
  `verifyFileWithRetry`'s backoff behavior extends to mention `restore` now shares the same retry path.
- `demo/local.conf`: the canonical multi-component example config (already carries
  `RestoreCleanupIntervalSec`/`RestoreCleanupGracePeriodSec` from the last restore-lifecycle change) —
  add `RwfsRetries` so it stays a complete, current reference.
- `bin/local.conf`: a tracked personal/dev config mirroring `default_port`/`default_streams` — add
  `RwfsRetries` for consistency (not load-bearing, since the field is optional).
- `CHANGELOG.md`: entry before merge.

## Open Questions / Follow-ups

- None — this is a small, additive change with no unresolved design questions. Grace-period/interval
  style config defaults for other rwfs behaviors are out of scope here.
