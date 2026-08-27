# Restore Per-File Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `rwfs restore`'s file-content phase the same per-file retry-with-backoff on transient stream errors that `rwfs verify` already has, sharing one implementation and one config-driven default between the two commands.

**Architecture:** Extract `verifyFileWithRetry`'s existing retry loop into a generic `withRetry[R any]` helper (new `retry.go`, same spirit as the existing `runWorkerPool[T, R any]`). `verifyFileWithRetry` becomes a thin wrapper around it; a new `writeRestoreFileWithRetry` wraps `writeRestoreFile` the same way, classifying retryable failures via a new `Retryable bool` field on `restoreFileResult`. The `--retries` flag moves onto `restoreCmd` too, and both commands' flag defaults come from a new `RwfsRetries` config field instead of a hardcoded literal.

**Tech Stack:** Go 1.26, Cobra (CLI flags), `log/slog`, gRPC + `bufconn` (tests), `testify` (assert/require).

## Global Constraints

- `--retries` must be `>= 1` for both `verify` and `restore` (1 means "no retry") — unchanged validation semantics, just now enforced identically for both actions.
- Retry backoff is unchanged: starts at 500ms (`retryBackoffInitial`), doubles, caps at 5s (`retryBackoffCap`).
- What's retryable does not change for `verify` (still: any failure except `blake3_mismatch`/`crc_mismatch`). For `restore`, only network/RPC-facing failures (the RPC call, `Recv`, or an unexpected event type) are retryable — integrity mismatches, a pre-existing directory at the destination, and local disk I/O errors are never retried.
- No change to `restoreFileContent`'s abort-the-whole-run-on-first-failure behavior — retry happens *before* that failure is allowed to fire, not instead of it.
- `RwfsRetries` is an **optional** config field (default `3`), not added to `requiredFields` — an existing `local.conf` missing it must keep working unchanged.
- Every existing test must keep passing with unchanged behavior/timing; where a test's helper gains a new `retries` parameter, pass `1` (no retry) unless the test is specifically about retry, so nothing regresses.

---

## File Map

| File | Change |
|---|---|
| `src/common/config/config.go` | New `RwfsRetries int` field, default 3, optional parse case |
| `src/common/config/config_test.go` | New tests for `RwfsRetries` default/parsing |
| `src/cmd/rwfs/retry.go` | **New.** Generic `withRetry[R any]`; `retryBackoffInitial`/`retryBackoffCap` move here from `verify.go` |
| `src/cmd/rwfs/retry_test.go` | **New.** Unit tests for `withRetry` in isolation |
| `src/cmd/rwfs/verify.go` | `verifyFileWithRetry` becomes a thin `withRetry` wrapper; consts removed (moved to `retry.go`) |
| `src/cmd/rwfs/arguments.go` | `--retries` added to `restoreCmd`; both commands' `--retries` default from `conf.RwfsRetries`; restore validation gains the `>= 1` check |
| `src/cmd/rwfs/arguments_test.go` | `testConfig()` gains `RwfsRetries: 3`; new tests for restore's `--retries` |
| `src/cmd/rwfs/restorefile.go` | `restoreFileResult` gains `Retryable bool`; new `writeRestoreFileWithRetry` |
| `src/cmd/rwfs/restorefile_test.go` | New `flakyRestoreServer` fixture; new retry tests |
| `src/cmd/rwfs/restore.go` | `restoreFileContent`/`runRestoreWithConn`/`runRestore` gain a `retries int` parameter; worker pool calls `writeRestoreFileWithRetry` |
| `src/cmd/rwfs/main.go` | Passes `arguments.Retries` into `runRestore` |
| `src/cmd/rwfs/restore_test.go` | `runRestoreWithDialer` gains a `retries int` parameter (all 14 call sites pass `1`); the one direct `restoreFileContent` call passes `1`; new end-to-end retry-recovery test |
| `docs/components/rwfs.md` | `--retries` documented for both commands; config-driven default noted |
| `demo/local.conf`, `bin/local.conf` | Add `RwfsRetries` |
| `CHANGELOG.md` | New entry |

---

### Task 1: Config — `RwfsRetries`

**Files:**
- Modify: `src/common/config/config.go:82-126` (struct), `:149-177` (defaults), `:200-...` (parse switch, insert new case anywhere in the switch — alongside the other `*Sec`/count-style cases, e.g. right after the `RestoreCleanupGracePeriodSec` case)
- Test: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.RwfsRetries int` — read by `src/cmd/rwfs/arguments.go` (Task 4) as the default value for both commands' `--retries` flag.

- [ ] **Step 1: Write the failing tests**

Add to `src/common/config/config_test.go` (mirrors `TestParseConfig_BackupWindowGraceSecDefaultsTo3600`/`TestParseConfig_BackupWindowGraceSecParsesCorrectly` immediately above):

```go
func TestParseConfig_RwfsRetriesDefaultsTo3(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 3, conf.RwfsRetries)
}

func TestParseConfig_RwfsRetriesParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\nRwfsRetries=5\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 5, conf.RwfsRetries)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./common/config/... -run TestParseConfig_RwfsRetries -v`
Expected: FAIL — `conf.RwfsRetries` doesn't compile (`Config` has no such field yet).

- [ ] **Step 3: Implement**

In `src/common/config/config.go`, add the field to the `Config` struct (anywhere in the field list — put it at the end, after `RestoreCleanupGracePeriodSec`):

```go
	RestoreCleanupIntervalSec        int
	RestoreCleanupGracePeriodSec     int
	RwfsRetries                      int
}
```

Add the default to the struct literal in `ParseConfig` (after `RestoreCleanupGracePeriodSec: 900,`):

```go
		RestoreCleanupIntervalSec:        300,
		RestoreCleanupGracePeriodSec:     900,
		RwfsRetries:                      3,
	}
```

Add a parse case in the `switch key` block (after the `RestoreCleanupGracePeriodSec` case, before `default:`):

```go
		case "RwfsRetries":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid RwfsRetries value at line %d: %s", lineNum, value)
			}
			config.RwfsRetries = number
			foundFields["RwfsRetries"] = true
```

Do **not** add `"RwfsRetries"` to `requiredFields` (line ~471) — it must stay optional.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./common/config/... -run TestParseConfig_RwfsRetries -v`
Expected: PASS

- [ ] **Step 5: Run the full config package suite**

Run: `cd src && go test ./common/config/...`
Expected: PASS (no existing test regresses — the new field is optional and additive)

- [ ] **Step 6: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add RwfsRetries, an optional default for rwfs's --retries flag"
```

---

### Task 2: Shared `withRetry` generic helper

**Files:**
- Create: `src/cmd/rwfs/retry.go`
- Test: Create `src/cmd/rwfs/retry_test.go`

**Interfaces:**
- Produces: `withRetry[R any](ctx context.Context, logger *slog.Logger, maxRetries int, attempt func(context.Context) R, isRetryable func(R) bool, reason func(R) string) R`, and package-level consts `retryBackoffInitial = 500 * time.Millisecond`, `retryBackoffCap = 5 * time.Second` — both consumed by Task 3 (`verify.go`) and Task 5 (`restorefile.go`).

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/rwfs/retry_test.go`:

```go
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type retryTestResult struct {
	ok  bool
	err error
}

func TestWithRetry_RecoversAfterRetryableFailures(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	attempts := 0
	result := withRetry(context.Background(), logger, 3,
		func(ctx context.Context) retryTestResult {
			attempts++
			if attempts < 3 {
				return retryTestResult{err: errors.New("transient")}
			}
			return retryTestResult{ok: true}
		},
		func(r retryTestResult) bool { return !r.ok },
		func(r retryTestResult) string { return r.err.Error() },
	)
	require.True(t, result.ok)
	assert.Equal(t, 3, attempts)
}

func TestWithRetry_StopsAtMaxRetriesAndReturnsFinalResult(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	attempts := 0
	result := withRetry(context.Background(), logger, 3,
		func(ctx context.Context) retryTestResult {
			attempts++
			return retryTestResult{err: errors.New("always fails")}
		},
		func(r retryTestResult) bool { return !r.ok },
		func(r retryTestResult) string { return r.err.Error() },
	)
	assert.False(t, result.ok)
	assert.Equal(t, 3, attempts, "must attempt exactly maxRetries times, no more")
}

func TestWithRetry_NonRetryableStopsImmediately(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	attempts := 0
	start := time.Now()
	result := withRetry(context.Background(), logger, 3,
		func(ctx context.Context) retryTestResult {
			attempts++
			return retryTestResult{err: errors.New("terminal")}
		},
		func(r retryTestResult) bool { return false }, // never retryable
		func(r retryTestResult) string { return r.err.Error() },
	)
	elapsed := time.Since(start)
	assert.False(t, result.ok)
	assert.Equal(t, 1, attempts, "a non-retryable failure must not be retried")
	assert.Less(t, elapsed, 100*time.Millisecond, "no backoff wait should occur for a non-retryable failure")
}

func TestWithRetry_BacksOffBetweenAttempts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := time.Now()
	withRetry(context.Background(), logger, 3,
		func(ctx context.Context) retryTestResult { return retryTestResult{err: errors.New("fail")} },
		func(r retryTestResult) bool { return true },
		func(r retryTestResult) string { return r.err.Error() },
	)
	elapsed := time.Since(start)
	// 3 attempts -> 2 waits; backoff starts at 500ms and doubles, so the
	// floor is 500ms + 1s = 1.5s (well under the 5s cap). Assert a
	// slightly relaxed floor to absorb scheduler jitter -- mirrors
	// verify_test.go's TestVerifyFileWithRetry_BacksOffBetweenAttempts.
	if elapsed < 1300*time.Millisecond {
		t.Fatalf("expected at least ~1.5s of backoff across 2 waits, took %v", elapsed)
	}
}

func TestWithRetry_RespectsContextCancellationDuringBackoffWait(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	result := withRetry(ctx, logger, 5,
		func(ctx context.Context) retryTestResult {
			attempts++
			return retryTestResult{err: errors.New("fail")}
		},
		func(r retryTestResult) bool { return true },
		func(r retryTestResult) string { return r.err.Error() },
	)
	elapsed := time.Since(start)
	assert.False(t, result.ok)
	assert.Less(t, elapsed, 500*time.Millisecond, "cancellation during a backoff wait must return promptly, not wait out the full backoff")
	assert.Equal(t, 1, attempts, "cancellation during the first backoff wait must prevent a second attempt")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestWithRetry -v`
Expected: FAIL to compile — `withRetry` doesn't exist yet.

- [ ] **Step 3: Implement**

Create `src/cmd/rwfs/retry.go`:

```go
// retry.go provides a single generic retry-with-backoff loop, shared by
// verify's per-file verification (verifyFileWithRetry, verify.go) and
// restore's per-file content write (writeRestoreFileWithRetry,
// restorefile.go) -- one implementation so the two commands' retry
// behavior can't drift apart. See
// docs/superpowers/specs/2026-08-27-restore-retry-design.md.
package main

import (
	"context"
	"log/slog"
	"time"
)

const (
	retryBackoffInitial = 500 * time.Millisecond
	retryBackoffCap     = 5 * time.Second
)

// withRetry runs attempt up to maxRetries times over ctx, retrying only
// while isRetryable(result) is true, backing off with capped doubling
// (retryBackoffInitial, doubling, capped at retryBackoffCap) between
// attempts. Stops immediately -- no further attempts, no backoff wait --
// the moment isRetryable returns false, so a terminal (non-retryable)
// failure surfaces without delay.
//
// logger should already be scoped with per-item fields (path, file_uuid,
// dest_path, etc.) via .With() -- each retried attempt logs "stream
// error, retrying" with "attempt" and "reason" (from the reason func) at
// Warn, the same log line verifyFileWithRetry has always emitted.
//
// maxRetries must be >= 1 (enforced by --retries validation in
// arguments.go); 1 means no retry -- attempt runs once, whatever it
// returns is returned immediately regardless of isRetryable.
func withRetry[R any](
	ctx context.Context,
	logger *slog.Logger,
	maxRetries int,
	attempt func(context.Context) R,
	isRetryable func(R) bool,
	reason func(R) string,
) R {
	backoff := retryBackoffInitial
	var result R
	for i := 1; i <= maxRetries; i++ {
		result = attempt(ctx)
		if !isRetryable(result) {
			return result
		}
		if i < maxRetries {
			logger.Warn("stream error, retrying", "attempt", i, "reason", reason(result))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return result
			}
			backoff = min(backoff*2, retryBackoffCap)
		}
	}
	return result
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestWithRetry -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/cmd/rwfs/retry.go src/cmd/rwfs/retry_test.go
git commit -m "feat(rwfs): add withRetry, a generic retry-with-backoff helper"
```

---

### Task 3: Refactor `verifyFileWithRetry` onto `withRetry`

This is a pure refactor under an existing test as the safety net (`TestVerifyFileWithRetry_BacksOffBetweenAttempts`, `verify_test.go:846`) — no new test needed, but the existing suite must pass unchanged before and after.

**Files:**
- Modify: `src/cmd/rwfs/verify.go:22-25` (remove consts, now in `retry.go`), `:260-284` (`verifyFileWithRetry` body)

**Interfaces:**
- Consumes: `withRetry[R any]` (Task 2).
- Produces: `verifyFileWithRetry` keeps its exact existing signature — `func(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, row *pb.FileRow, maxRetries int) verifyResult` — unchanged for its one caller (`verify.go`'s `runVerifyWithConn`).

- [ ] **Step 1: Run the existing verify suite to confirm the baseline passes**

Run: `cd src && go test ./cmd/rwfs/... -run TestVerify -v`
Expected: PASS (baseline, before refactor)

- [ ] **Step 2: Remove the now-duplicated consts**

In `src/cmd/rwfs/verify.go`, delete lines 22-25:

```go
const (
	retryBackoffInitial = 500 * time.Millisecond
	retryBackoffCap     = 5 * time.Second
)

```

(They now live in `retry.go`, same package, so `verify.go` still sees them.)

- [ ] **Step 3: Replace `verifyFileWithRetry`'s body**

Replace (lines 260-284):

```go
func verifyFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, row *pb.FileRow, maxRetries int) verifyResult {
	backoff := retryBackoffInitial
	var result verifyResult
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result = verifyFile(ctx, client, row)
		if result.ok || result.reason == "blake3_mismatch" || result.reason == "crc_mismatch" {
			return result
		}
		if attempt < maxRetries {
			logger.Warn("stream error, retrying",
				"path", row.Path,
				"file_uuid", row.FileUuid,
				"attempt", attempt,
				"reason", result.reason,
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return result
			}
			backoff = min(backoff*2, retryBackoffCap)
		}
	}
	return result
}
```

with:

```go
func verifyFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, row *pb.FileRow, maxRetries int) verifyResult {
	return withRetry(ctx, logger.With("path", row.Path, "file_uuid", row.FileUuid), maxRetries,
		func(ctx context.Context) verifyResult { return verifyFile(ctx, client, row) },
		func(r verifyResult) bool { return !r.ok && r.reason != "blake3_mismatch" && r.reason != "crc_mismatch" },
		func(r verifyResult) string { return r.reason },
	)
}
```

Check whether `verify.go` still uses the `time` import elsewhere in the file (`verifyFileWithRetry` was its only direct user of `time.After`/`time.Now` in this specific loop, but `retryBackoffInitial = 500 * time.Millisecond`-style usage is gone from this file too). Run `goimports`/`go build` in the next step — if `time` is now unused in `verify.go`, remove it from that file's import block.

- [ ] **Step 4: Build and run the full verify suite**

Run: `cd src && go build ./cmd/rwfs/... && go test ./cmd/rwfs/... -run TestVerify -v`
Expected: PASS — in particular `TestVerifyFileWithRetry_BacksOffBetweenAttempts` must still show >= ~1.3s elapsed, proving the refactor preserved backoff timing exactly.

- [ ] **Step 5: Run the full rwfs package suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS (nothing else in the package references the removed consts or the old loop shape)

- [ ] **Step 6: Commit**

```bash
git add src/cmd/rwfs/verify.go
git commit -m "refactor(rwfs): verifyFileWithRetry composes the shared withRetry helper"
```

---

### Task 4: `--retries` on `restore`, config-driven default for both commands

**Files:**
- Modify: `src/cmd/rwfs/arguments.go:11-25` (struct comment), `:76-79` (verifyCmd flags), `:94-100` (restoreCmd flags), `:129-136` (restore validation)
- Test: `src/cmd/rwfs/arguments_test.go`

**Interfaces:**
- Consumes: `config.Config.RwfsRetries` (Task 1).
- Produces: `Arguments.Retries` is now populated for `restore` too (previously verify-only); `parseArguments` rejects `restore --retries 0` the same way it already rejects it for `verify`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/rwfs/arguments_test.go`, after `TestParseArguments_RestoreInvalidStreamsErrors`:

```go
func TestParseArguments_RestoreRetriesFlag_DefaultsFromConfig(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin"}, func() {
		conf := testConfig()
		conf.RwfsRetries = 7
		args, err := parseArguments(conf)
		require.NoError(t, err)
		assert.Equal(t, 7, args.Retries)
	})
}

func TestParseArguments_RestoreRetriesFlag_OverridesConfig(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin", "--retries", "9"}, func() {
		conf := testConfig()
		conf.RwfsRetries = 7
		args, err := parseArguments(conf)
		require.NoError(t, err)
		assert.Equal(t, 9, args.Retries)
	})
}

func TestParseArguments_RestoreInvalidRetriesErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin", "--retries", "0"}, func() {
		_, err := parseArguments(testConfig())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--retries must be at least 1")
	})
}

func TestParseArguments_VerifyRetriesFlag_DefaultsFromConfig(t *testing.T) {
	withArgs(t, []string{"rwfs", "verify", "localhost:8080"}, func() {
		conf := testConfig()
		conf.RwfsRetries = 7
		args, err := parseArguments(conf)
		require.NoError(t, err)
		assert.Equal(t, 7, args.Retries)
	})
}
```

Also update `testConfig()` itself (this is a required fixture fix, not a new test): every pre-existing `verify`/`restore` test in this file calls `parseArguments(testConfig())` without passing `--retries`, so once the flag default stops being a hardcoded `3` and starts coming from `conf.RwfsRetries`, an unset `RwfsRetries` on `testConfig()`'s zero-value `Config` would default the flag to `0` and fail every one of those tests against the new `--retries must be at least 1` check. Update it to:

```go
func testConfig() *config.Config {
	return &config.Config{DefaultPort: 8080, RwfsRetries: 3}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestParseArguments_Retries -v` and `-run TestParseArguments_RestoreInvalidRetries` and `-run TestParseArguments_VerifyRetries`
Expected: FAIL — restore has no `--retries` flag yet (`unknown flag: --retries` for the ones passing it explicitly; the default-from-config ones fail because `verifyCmd`'s flag is still hardcoded `3`, not reading `conf.RwfsRetries`, so `TestParseArguments_VerifyRetriesFlag_DefaultsFromConfig`'s `conf.RwfsRetries = 7` assertion of `7` fails against the actual `3`).

- [ ] **Step 3: Implement**

In `src/cmd/rwfs/arguments.go`, update the struct comment (line 22):

```go
	Retries    int  // verify, restore
```

Change `verifyCmd`'s flag registration (line 77) from a hardcoded default to the config-driven one:

```go
	verifyCmd.Flags().IntVar(&args.Retries, "retries", conf.RwfsRetries, "Max retry attempts per file on stream error")
```

Add the same flag to `restoreCmd`, alongside its other flags (after the `--streams` line, line 97):

```go
	restoreCmd.Flags().IntVar(&args.Streams, "streams", 4, "Number of concurrent file restore workers")
	restoreCmd.Flags().IntVar(&args.Retries, "retries", conf.RwfsRetries, "Max retry attempts per file on stream error")
```

Add the same validation restore's block already has for streams (lines 129-136), inserted before the `--rules-stdin` check:

```go
	if args.Action == "restore" {
		if err := common.ValidateStreamsCount(args.Streams); err != nil {
			return nil, fmt.Errorf("--streams: %w", err)
		}
		if args.Retries < 1 {
			return nil, fmt.Errorf("--retries must be at least 1, got: %d", args.Retries)
		}
		if !args.RulesStdin {
			return nil, fmt.Errorf("restore requires --rules-stdin")
		}
	}
```

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestParseArguments_Retries -v` and `-run TestParseArguments_RestoreInvalidRetries` and `-run TestParseArguments_VerifyRetries`
Expected: PASS

- [ ] **Step 5: Run the full arguments/rwfs suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS — every pre-existing verify/restore test still gets a valid `Retries` (3, from the updated `testConfig()`).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/rwfs/arguments.go src/cmd/rwfs/arguments_test.go
git commit -m "feat(rwfs): add --retries to restore, default both commands from RwfsRetries config"
```

---

### Task 5: `writeRestoreFileWithRetry`

**Files:**
- Modify: `src/cmd/rwfs/restorefile.go:1-20` (imports), `:45-50` (struct), `:83-175` (5 retryable-error sites), append new function at end of file
- Test: Modify `src/cmd/rwfs/restorefile_test.go`

**Interfaces:**
- Consumes: `withRetry[R any]` (Task 2).
- Produces: `restoreFileResult.Retryable bool`; `writeRestoreFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, f restoreFile, overwrite bool, maxRetries int) restoreFileResult` — consumed by Task 6's `restoreFileContent`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/rwfs/restorefile_test.go`. First, extend the import block to add `sync`, `status`, and `codes` (currently: `context`, `net`, `os`, `testing`, `pb`, `wfs`, `assert`, `require`, `grpc`, `insecure`, `bufconn`, `blake3`):

```go
import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"lukechampine.com/blake3"
)
```

Then add, after `TestWriteRestoreFile_MissingParentDirectoryIsHardError`:

```go
// flakyRestoreServer fails RestoreFile with a stream-level error the
// first failuresBeforeSuccess calls, then delegates to wrapped -- proving
// writeRestoreFileWithRetry actually recovers a transient failure rather
// than merely detecting one.
type flakyRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
	mu                    sync.Mutex
	calls                 int
	failuresBeforeSuccess int
	wrapped               pb.RestoreServiceServer
}

func (s *flakyRestoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	s.mu.Lock()
	s.calls++
	shouldFail := s.calls <= s.failuresBeforeSuccess
	s.mu.Unlock()
	if shouldFail {
		return status.Error(codes.Unavailable, "simulated transient failure")
	}
	return s.wrapped.RestoreFile(req, stream)
}

func (s *flakyRestoreServer) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestWriteRestoreFileWithRetry_RecoversAfterTransientFailures(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte("recovered content"))

	flakySrv := &flakyRestoreServer{failuresBeforeSuccess: 2, wrapped: &realRestoreServer{store: store}}
	client := dialRestoreClient(t, flakySrv)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFileWithRetry(context.Background(), logger, client, restoreFile{
		FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false, 3)

	require.NoError(t, result.Err)
	assert.Equal(t, 3, flakySrv.Calls(), "must have failed twice then succeeded on the third attempt")
	got, readErr := os.ReadFile(destPath)
	require.NoError(t, readErr)
	assert.Equal(t, "recovered content", string(got))
}

func TestWriteRestoreFileWithRetry_ExhaustsRetriesAndReturnsFinalError(t *testing.T) {
	restoreSrv := &recordingRestoreServer{} // always fails RestoreFile with codes.Unimplemented
	client := dialRestoreClient(t, restoreSrv)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFileWithRetry(context.Background(), logger, client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false, 3)

	require.Error(t, result.Err)
	assert.True(t, result.Retryable, "a stream error stays marked Retryable even once retries are exhausted")
	assert.Len(t, restoreSrv.Requested(), 3, "must attempt exactly maxRetries times")
	_, statErr := os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr))
}

func TestWriteRestoreFileWithRetry_IntegrityMismatchNeverRetries(t *testing.T) {
	client := dialRestoreClient(t, &hashMismatchRestoreServer{})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	destPath := t.TempDir() + "/a.txt"

	start := time.Now()
	result := writeRestoreFileWithRetry(context.Background(), logger, client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false, 3)
	elapsed := time.Since(start)

	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "blake3_mismatch")
	assert.False(t, result.Retryable)
	assert.Less(t, elapsed, 200*time.Millisecond, "an integrity mismatch must fail on the first attempt with no backoff wait")
}

func TestWriteRestoreFileWithRetry_BacksOffBetweenAttempts(t *testing.T) {
	restoreSrv := &recordingRestoreServer{}
	client := dialRestoreClient(t, restoreSrv)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	destPath := t.TempDir() + "/a.txt"

	start := time.Now()
	writeRestoreFileWithRetry(context.Background(), logger, client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false, 3)
	elapsed := time.Since(start)

	if elapsed < 1300*time.Millisecond {
		t.Fatalf("expected at least ~1.5s of backoff across 2 waits, took %v", elapsed)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestWriteRestoreFileWithRetry -v`
Expected: FAIL to compile — `writeRestoreFileWithRetry` and `restoreFileResult.Retryable` don't exist yet.

- [ ] **Step 3: Implement**

Add `"log/slog"` to `restorefile.go`'s import block (currently `bufio`, `bytes`, `context`, `encoding/binary`, `fmt`, `hash/crc32`, `os`, `pb`, `checksum`, `blake3`):

```go
import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/checksum"
	"lukechampine.com/blake3"
)
```

Add `Retryable` to `restoreFileResult` (lines 42-50):

```go
// restoreFileResult is writeRestoreFile's outcome. Source/Path/DestPath
// are carried through unchanged from the input restoreFile so the driver
// (restore.go's restoreFileContent) can log without a side lookup.
type restoreFileResult struct {
	Source, Path, DestPath string
	Bytes                  int64
	Skipped                bool
	Err                    error
	// Retryable is true only when Err comes from the network/RPC-facing
	// call to bwfs (connect, Recv, or an unexpected event type) -- the
	// same class of failure verifyFileWithRetry already retries. It
	// stays false (the zero value) for every other failure: integrity
	// mismatches (blake3_mismatch, crc_mismatch), a pre-existing
	// directory at the destination, and local disk I/O errors -- none of
	// which retrying can fix. See writeRestoreFileWithRetry.
	Retryable bool
}
```

Set `Retryable = true` on the five stream-error branches. Each is a small, surgical edit — old snippet followed by new:

```go
	stream, err := client.RestoreFile(ctx, &pb.RestoreRequest{FileUuid: f.FileUUID})
	if err != nil {
		base.Err = fmt.Errorf("stream error: %w", err)
		return base
	}
```
becomes:
```go
	stream, err := client.RestoreFile(ctx, &pb.RestoreRequest{FileUuid: f.FileUUID})
	if err != nil {
		base.Err = fmt.Errorf("stream error: %w", err)
		base.Retryable = true
		return base
	}
```

```go
	firstEvent, err := stream.Recv()
	if err != nil {
		base.Err = fmt.Errorf("stream error: %w", err)
		return base
	}
```
becomes:
```go
	firstEvent, err := stream.Recv()
	if err != nil {
		base.Err = fmt.Errorf("stream error: %w", err)
		base.Retryable = true
		return base
	}
```

```go
	meta := firstEvent.GetMeta()
	if meta == nil {
		base.Err = fmt.Errorf("stream error: expected RestoreFileMeta as first event")
		return base
	}
```
becomes:
```go
	meta := firstEvent.GetMeta()
	if meta == nil {
		base.Err = fmt.Errorf("stream error: expected RestoreFileMeta as first event")
		base.Retryable = true
		return base
	}
```

```go
	for {
		event, err := stream.Recv()
		if err != nil {
			base.Err = fmt.Errorf("stream error: %w", err)
			return base
		}
```
becomes:
```go
	for {
		event, err := stream.Recv()
		if err != nil {
			base.Err = fmt.Errorf("stream error: %w", err)
			base.Retryable = true
			return base
		}
```

```go
		chunk := event.GetChunk()
		if chunk == nil {
			base.Err = fmt.Errorf("stream error: expected RestoreChunk")
			return base
		}
```
becomes:
```go
		chunk := event.GetChunk()
		if chunk == nil {
			base.Err = fmt.Errorf("stream error: expected RestoreChunk")
			base.Retryable = true
			return base
		}
```

Every other error return in this file (pre-existing directory, `os.Stat`, `os.OpenFile`, `Truncate`, chunk `Write`, BLAKE3 mismatch, `Flush`, CRC mismatch, `Close`) is left unchanged — `Retryable` stays `false`, the zero value.

Append the new wrapper at the end of the file:

```go
// writeRestoreFileWithRetry retries writeRestoreFile up to maxRetries
// times on a retryable (network/RPC-facing) failure, sharing withRetry's
// backoff with verifyFileWithRetry (retry.go) so the two commands can't
// drift apart. A retry is safe with no extra cleanup: writeRestoreFile's
// own defer already removes any partial destination file before
// returning on failure, so each attempt starts from a clean slate (fresh
// stat, open, truncate). A non-retryable failure (integrity mismatch,
// pre-existing directory, local disk error) surfaces on the first
// attempt with no backoff wait.
func writeRestoreFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, f restoreFile, overwrite bool, maxRetries int) restoreFileResult {
	return withRetry(ctx, logger.With("source", f.Source, "path", f.Path, "dest_path", f.DestPath), maxRetries,
		func(ctx context.Context) restoreFileResult { return writeRestoreFile(ctx, client, f, overwrite) },
		func(r restoreFileResult) bool { return r.Err != nil && r.Retryable },
		func(r restoreFileResult) string { return r.Err.Error() },
	)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestWriteRestoreFileWithRetry -v`
Expected: PASS

- [ ] **Step 5: Run the full restorefile suite**

Run: `cd src && go test ./cmd/rwfs/... -run TestWriteRestoreFile`
Expected: PASS — every pre-existing `writeRestoreFile` test (called directly, not through the retry wrapper) is untouched by this task.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/rwfs/restorefile.go src/cmd/rwfs/restorefile_test.go
git commit -m "feat(rwfs): add writeRestoreFileWithRetry, retrying transient restore stream errors"
```

---

### Task 6: Wire `retries` through `restore.go`/`main.go`

**Files:**
- Modify: `src/cmd/rwfs/restore.go:35` (`runRestore`), `:41-47` (its body), `:55` (`runRestoreWithConn`), `:116` (its call to `restoreFileContent`), `:190` (`restoreFileContent`), `:217-219` (worker pool call)
- Modify: `src/cmd/rwfs/main.go:70` (the `"restore"` case)
- Modify: `src/cmd/rwfs/restore_test.go:32` (`runRestoreWithDialer`), all 14 call sites, and the direct `restoreFileContent` call in `TestRestoreFileContent_FirstFailureCancelsOtherInFlightTransfers`

**Interfaces:**
- Consumes: `writeRestoreFileWithRetry` (Task 5), `Arguments.Retries` (Task 4, now populated for `restore` too).
- Produces: `runRestore(logger, host, port, overwrite, stdin, quiet, streams, retries int, certsDir, jobID)`; `restoreFileContent(ctx, logger, client, files, overwrite bool, streams, retries int)`.

- [ ] **Step 1: Update `restore.go` signatures and the worker pool call**

`restoreFileContent` (line 190) — add `retries` and call the retry wrapper (lines 217-219):

```go
func restoreFileContent(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, files []restoreFile, overwrite bool, streams, retries int) error {
```

```go
	resultCh := runWorkerPool(writeCtx, streams, workCh, func(ctx context.Context, f restoreFile) restoreFileResult {
		return writeRestoreFileWithRetry(ctx, logger, client, f, overwrite, retries)
	})
```

`runRestoreWithConn` (line 55) — add `retries`, thread it to the call at line 116:

```go
func runRestoreWithConn(logger *slog.Logger, conn *grpc.ClientConn, overwrite bool, rules []RestoreRule, quiet bool, streams, retries int, jobID string) error {
```

```go
	return restoreFileContent(callCtx, logger, restoreClient, files, overwrite, streams, retries)
```

`runRestore` (line 35) — add `retries`, thread it to its call at line 47:

```go
func runRestore(logger *slog.Logger, host string, port int, overwrite bool, stdin io.Reader, quiet bool, streams, retries int, certsDir, jobID string) error {
```

```go
	return runRestoreWithConn(logger, conn, overwrite, rules, quiet, streams, retries, jobID)
```

- [ ] **Step 2: Update `main.go`'s `"restore"` case**

```go
	case "restore":
		if err := runRestore(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.Overwrite, os.Stdin, arguments.Quiet, arguments.Streams, arguments.Retries, certsDir, jobID); err != nil {
```

- [ ] **Step 3: Build to find every broken call site**

Run: `cd src && go build ./cmd/rwfs/...`
Expected: FAIL — compile errors in `restore.go` (fixed above) and `restore_test.go` (not yet fixed), listing every call site that needs the new `retries` argument.

- [ ] **Step 4: Update `restore_test.go`'s helper and call sites**

`runRestoreWithDialer` (line 32) gains a `retries int` parameter and threads it into `runRestoreWithConn`:

```go
func runRestoreWithDialer(t *testing.T, logger *slog.Logger, lis *bufconn.Listener, rulesJSON string, overwrite bool, streams, retries int) error {
	t.Helper()

	rules, err := parseRulesStdin(strings.NewReader(rulesJSON))
	require.NoError(t, err)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	return runRestoreWithConn(logger, conn, overwrite, rules, false, streams, retries, "test-job")
}
```

Update every one of the 14 existing call sites from `runRestoreWithDialer(t, logger, lis, rulesJSON, <overwrite>, 4)` to `runRestoreWithDialer(t, logger, lis, rulesJSON, <overwrite>, 4, 1)` — passing `retries=1` (no retry) preserves each test's exact original behavior and timing, since none of them exercise retry. The 14 lines (by current line number, `<overwrite>` varies per call but the trailing `4` -> `4, 1` edit is identical):

- `restore_test.go:79`: `err = runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4)` -> `err = runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4, 1)`
- `restore_test.go:112`: `err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)` -> `err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4, 1)`
- `restore_test.go:136`: same edit as above
- `restore_test.go:178`: same edit as above
- `restore_test.go:215`: same edit as above
- `restore_test.go:247`: same edit as above
- `restore_test.go:284`: same edit as above
- `restore_test.go:320`: same edit as above
- `restore_test.go:353`: same edit as above
- `restore_test.go:394`: same edit as above
- `restore_test.go:424`: same edit as above
- `restore_test.go:460`: `err = runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4)` -> `err = runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4, 1)`
- `restore_test.go:496`: `err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)` -> `err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4, 1)`
- `restore_test.go:605`: same edit as above

(Re-run `go vet ./cmd/rwfs/...` after this step — if line numbers have drifted from other edits, `vet`/`build` will point at the exact remaining call sites needing the same `, 1` addition; every one of the 14 gets the identical trailing-argument edit regardless of its exact line.)

Update the one direct `restoreFileContent` call in `TestRestoreFileContent_FirstFailureCancelsOtherInFlightTransfers`:

```go
	err = restoreFileContent(context.Background(), logger, client, files, false, 2)
```
becomes:
```go
	err = restoreFileContent(context.Background(), logger, client, files, false, 2, 1)
```

(`retries=1` here too — this test is specifically about cancel-on-first-failure timing, not retry, and `1` preserves its original immediate-failure behavior.)

- [ ] **Step 5: Add an end-to-end retry-recovery test**

Add to `restore_test.go`, after `TestRunRestore_DuplicateDestinationAcrossHostsIsHardError`:

```go
func TestRunRestore_RecoversFromTransientFileErrorViaRetry(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("vacation photo bytes"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &flakyRestoreServer{failuresBeforeSuccess: 1, wrapped: &realRestoreServer{store: store}}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destDir := t.TempDir() + "/photos_recovered"
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destDir)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4, 3)
	require.NoError(t, err, "a single transient failure must not abort the run when retries are available")

	got, readErr := os.ReadFile(destDir + "/vacation.jpg")
	require.NoError(t, readErr)
	assert.Equal(t, "vacation photo bytes", string(got))
	assert.Equal(t, 2, restoreSrv.Calls(), "one failed attempt, then one successful retry")
}
```

- [ ] **Step 6: Build and run the full rwfs suite**

Run: `cd src && go build ./cmd/rwfs/... && go test ./cmd/rwfs/... -v`
Expected: PASS — every existing test (unaffected by retry, since they pass `retries=1`) plus the new end-to-end recovery test.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/rwfs/restore.go src/cmd/rwfs/main.go src/cmd/rwfs/restore_test.go
git commit -m "feat(rwfs): retry restore's per-file content writes on transient stream errors"
```

---

### Task 7: Docs, example configs, changelog

**Files:**
- Modify: `docs/components/rwfs.md` (Flags table + surrounding paragraph, around lines 74-100)
- Modify: `demo/local.conf`
- Modify: `bin/local.conf`
- Modify: `CHANGELOG.md`

**Interfaces:** None — documentation only.

- [ ] **Step 1: Update `docs/components/rwfs.md`**

The `--retries` row currently sits only in `verify`'s Flags table (`| \`--retries\` | 3 | Max retry attempts per file on stream error |`). Change that row's default column, since `3` is no longer a flag-hardcoded literal:

```
| `--retries` | `RwfsRetries` config (default 3) | Max retry attempts per file on stream error |
```

Add the same flag row to `restore`'s own Flags table (find it by searching the file for `### Flags` under the `## restore` heading — mirror the exact table structure `verify`'s uses, inserting the `--retries` row after `--streams`).

Update the paragraph after `verify`'s Flags table (currently starts "`ResolveRestoreFiles` (used by `--rules-stdin`)... `verifyFileWithRetry` also waits between retry attempts..."). Replace the sentence:

```
`verifyFileWithRetry` also waits between retry attempts (capped, doubling backoff starting
at 500ms) instead of retrying immediately, so a struggling `bwfs` isn't hammered.
```

with:

```
Both `verify` and `restore` share one retry implementation (`withRetry`, `retry.go`) that waits
between retry attempts (capped, doubling backoff starting at 500ms) instead of retrying
immediately, so a struggling `bwfs` isn't hammered -- `restore` retries only network/RPC-facing
stream errors; an integrity mismatch or a local destination-side problem (existing directory,
disk error) still fails immediately, no retry, exactly as before. See
[Design: Restore Per-File Retry](../superpowers/specs/2026-08-27-restore-retry-design.md).
```

- [ ] **Step 2: Add `RwfsRetries` to `demo/local.conf`**

Append near the other restore-lifecycle keys (after `RestoreCleanupGracePeriodSec=5`):

```
RwfsRetries=3
```

- [ ] **Step 3: Add `RwfsRetries` to `bin/local.conf`**

Append near `default_streams=4` (this file uses the older lower-case-key style for the two required fields but PascalCase for everything else already, matching `ClientHashQueryBatchSize` etc. below it — add alongside those):

```
RwfsRetries=3
```

- [ ] **Step 4: Add a `CHANGELOG.md` entry**

Insert a new entry at the top of the changelog body, above the existing `## 2026-08-27 — Restore workflow UI clarity` entry (most-recent-first ordering):

```markdown
## 2026-08-27 — Restore retries transient per-file stream errors

`rwfs restore` now retries a transient `RestoreFile` stream error per file (network blip, momentary
`bwfs` hiccup) instead of immediately aborting the whole run -- the same retry-with-backoff `rwfs
verify` already had, now shared by both commands through one implementation (`withRetry`,
`cmd/rwfs/retry.go`). This matters more than it used to: since restore/verify policies now run at
most once ever (`agent`'s one-shot semantics, 2026-08-23), a single blip on one file out of
thousands used to permanently sacrifice the entire policy with no automatic recovery. Only
network/RPC-facing failures are retried -- an integrity mismatch (BLAKE3/CRC) or a local
destination-side problem still aborts immediately, unchanged. The retry count defaults from a new
`RwfsRetries` config key (default 3) for both commands, overridable per-invocation via `--retries`
on either. See `docs/superpowers/specs/2026-08-27-restore-retry-design.md`.
```

- [ ] **Step 5: Run the full test suite one last time**

Run: `cd src && go build ./... && go test ./...`
Expected: PASS across the whole module — confirms the docs/config-only changes in this task didn't accidentally break anything (they shouldn't, since none of them touch Go source).

- [ ] **Step 6: Commit**

```bash
git add docs/components/rwfs.md demo/local.conf bin/local.conf CHANGELOG.md
git commit -m "docs: document restore's per-file retry, add RwfsRetries to example configs"
```
