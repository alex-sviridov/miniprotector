# Fleet Log Aggregation Phase 1: Standardized Logging & Cross-Host Correlation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Standardize local structured logging to one rotated file per binary, and extend the existing `brfs`→`bwfs` job-id correlation pattern to every other cross-host call `agent` drives (`certclient`→`issuer`, `policyclient`→`policy-server`), plus give `agent`'s own log a start/completion line for every exec it dispatches. This phase produces working, independently-verifiable improvements to local/cross-host logging with **no** new network component, no Loki, no `log-gateway`, no Vector — those are later phases against the same spec.

**Architecture:** `common/logging` gains a rotation-aware, one-file-per-binary writer (`gopkg.in/natefinch/lumberjack.v2`). A new `common/jobid` package centralizes the "resolve-or-generate, attach to outgoing gRPC metadata, require on incoming metadata" pattern `brfs`/`bwfs` already pioneered, and every other cross-host caller (`certclient`, `policyclient`) and callee (`issuer`, `policy-server`) adopts it. `agent` gains a `Policy.JobID` field (mirroring the per-invocation ID `backup.go` already builds for backup tasks) and a start/completion log line for every dispatched exec.

**Tech Stack:** Go, `log/slog`, `gopkg.in/natefinch/lumberjack.v2` (new dependency), `google.golang.org/grpc/metadata` (existing), `github.com/google/uuid` (existing).

## Global Constraints

- No new binaries, no new network listeners, no new `local.conf` keys beyond the `logfolder`→`log_dir` rename. `log-gateway`, Loki, Vector, and `agent`'s Vector-supervision are out of scope for this plan (see `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`'s Architecture — this plan covers "Standardized local logging" and "Correlation IDs, extended uniformly" only).
- Every change must leave the full existing test suite passing (`cd src && go test ./...`) — this phase is a uniform extension of existing, already-proven patterns (`brfs`'s job-id, `bwfs`'s `jobIDFromMetadata`), not new architecture.
- `job_id` is always request/log content, never a label or index key — nothing in this plan makes it part of any structured storage key beyond what `bwfs`'s `FileVersionRecord.JobID` already does.
- Historical spec/plan documents under `docs/superpowers/specs/` and `docs/superpowers/plans/` that mention `logfolder` are a record of what was true when written — do not edit them. Only living reference docs (`docs/components/*.md`, `docs/ARCHITECTURE.md`, `docs/SECURITY.md`) and actual deployment config files get updated.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/common/config/config.go`, `config_test.go` (modify) | Rename `logfolder`/`LogFolder` → `log_dir`/`LogDir` |
| `src/common/logging/logging.go` (modify), `logging_test.go` (new) | One rotated file per binary name, not one file per process invocation |
| `src/common/jobid/jobid.go`, `jobid_test.go` (new) | Shared `Resolve`/`Outgoing`/`FromIncoming` job-id helpers |
| `src/cmd/bwfs/server.go` (modify) | Adopt `common/jobid.FromIncoming`, drop local `jobIDFromMetadata` |
| `src/cmd/brfs/main.go` (modify) | Adopt `common/jobid.Resolve`/`Outgoing`, drop inline UUID/metadata code |
| `src/cmd/certclient/arguments.go`, `arguments_test.go` (new), `main.go`, `operatingrefresh.go` (modify) | `--job-id` flag on `renew`/`operating-refresh`, propagated to `issuer` |
| `src/cmd/issuer/server.go`, `server_test.go` (modify) | Require + log `job-id` on `RequestOperatingCert` |
| `src/cmd/policyclient/arguments.go`, `main.go`, `fetch.go` (modify) | `--job-id` flag on `fetch`, propagated to `policy-server` |
| `src/cmd/policy-server/server.go`, `server_test.go` (modify) | Require + log `job-id` on `GetPolicies` |
| `src/cmd/agent/policy.go`, `backup.go` (modify) | `Policy.JobID` field; static policies and backup tasks both populate it |
| `src/cmd/agent/reconcile.go`, `reconcile_test.go` (modify) | Start/completion log line for every dispatched exec |
| `deploy/control-plane/{catalog,issuer,policy-server,client-manager}/local.conf`, `demo/local.conf`, `demo/ca/clientmanager-local.conf` (modify) | `logfolder=` → `log_dir=` |
| `docs/components/agent.md` (modify) | Document the standardized log path and uniform `--job-id` convention |

---

### Task 1: `common/config` — rename `logfolder` to `log_dir`

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.LogDir string` (was `LogFolder`), parsed from `local.conf`'s `log_dir` key (was `logfolder`).

This is a straight rename with no behavior change — every existing test already exercises the required-field/parsing machinery via the `logfolder=/tmp` boilerplate line every test's config content includes; renaming both the boilerplate and the production code together keeps them in sync, so no new test is needed.

- [ ] **Step 1: Rename the boilerplate key in every test's config content**

In `src/common/config/config_test.go`, replace every occurrence of `logfolder=` with `log_dir=` (43 occurrences, all `logfolder=/tmp` or `logfolder=` inside a larger content string). Use a single find-and-replace across the whole file — every occurrence is the same literal substring, `logfolder=`, always immediately followed by a value in a `local.conf`-style content string.

- [ ] **Step 2: Rename the field, switch case, and required-field entry**

In `src/common/config/config.go`, change the `Config` struct field:

```go
	DefaultPort                      int
	DefaultStreams                   int
	LogDir                           string
```

(was `LogFolder`)

Change the switch case:

```go
		case "log_dir":
			config.LogDir = value
			foundFields["log_dir"] = true
```

(was `case "logfolder": config.LogFolder = value; foundFields["logfolder"] = true`)

Change the required-fields list:

```go
	requiredFields := []string{"default_port", "default_streams", "log_dir"}
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests — the rename is internally consistent, nothing else references the old names yet).

- [ ] **Step 4: Confirm nothing else in `src/` still references the old names**

Run: `cd src && grep -rn "LogFolder\|logfolder" .`
Expected: only `common/logging/logging.go` (updated in Task 2, next).

- [ ] **Step 5: Commit**

```bash
git add src/common/config/
git commit -m "refactor(config): rename logfolder/LogFolder to log_dir/LogDir"
```

---

### Task 2: `common/logging` — one rotated file per binary

**Files:**
- Modify: `src/common/logging/logging.go`
- Create: `src/common/logging/logging_test.go`
- Modify: `src/go.mod`, `src/go.sum` (via `go get`)

**Interfaces:**
- Consumes: `Config.LogDir` (Task 1).
- Produces: `NewLogger(ctx) (*slog.Logger, io.Closer)` — same signature as today, but the file handler now writes to `<LogDir>/<appName>.log` (stable, rotated), not `<LogDir>/<appName>-<date>.<pid>.log`.

- [ ] **Step 1: Add the rotation dependency**

Run: `cd src && go get gopkg.in/natefinch/lumberjack.v2`
Expected: `go.mod`/`go.sum` gain `gopkg.in/natefinch/lumberjack.v2`, exit code 0.

- [ ] **Step 2: Write the failing tests**

Create `src/common/logging/logging_test.go`:

```go
package logging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testContext(logDir, appName string) context.Context {
	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, &config.Config{LogDir: logDir})
	ctx = context.WithValue(ctx, "debugMode", false)
	ctx = context.WithValue(ctx, "quietMode", true)
	return ctx
}

func TestNewLogger_WritesToStableBinaryNamedFile(t *testing.T) {
	dir := t.TempDir()
	logger, closer := NewLogger(testContext(dir, "testbinary"))
	defer closer.Close()

	logger.Info("hello")

	data, err := os.ReadFile(filepath.Join(dir, "testbinary.log"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "hello")
}

func TestNewLogger_WritesValidJSONLines(t *testing.T) {
	dir := t.TempDir()
	logger, closer := NewLogger(testContext(dir, "testbinary"))
	logger.Info("structured", "key", "value")
	require.NoError(t, closer.Close())

	data, err := os.ReadFile(filepath.Join(dir, "testbinary.log"))
	require.NoError(t, err)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(data, &entry))
	assert.Equal(t, "structured", entry["msg"])
	assert.Equal(t, "value", entry["key"])
}

func TestNewLogger_TwoLoggersSameBinaryAppendSameFile(t *testing.T) {
	dir := t.TempDir()

	logger1, closer1 := NewLogger(testContext(dir, "testbinary"))
	logger1.Info("first")
	require.NoError(t, closer1.Close())

	logger2, closer2 := NewLogger(testContext(dir, "testbinary"))
	logger2.Info("second")
	require.NoError(t, closer2.Close())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "both loggers must write to one stable file, not one per invocation")

	data, err := os.ReadFile(filepath.Join(dir, "testbinary.log"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "first")
	assert.Contains(t, string(data), "second")
}

func TestNewLogger_CloserIsNilSafeWhenLogDirEmpty(t *testing.T) {
	_, closer := NewLogger(testContext("", "testbinary"))
	assert.NoError(t, closer.Close(), "Close must be safe to call even when no file handler was created")
}

func TestNewLogger_DifferentBinariesGetDifferentFiles(t *testing.T) {
	dir := t.TempDir()

	logger1, closer1 := NewLogger(testContext(dir, "binary-a"))
	logger1.Info("from a")
	require.NoError(t, closer1.Close())

	logger2, closer2 := NewLogger(testContext(dir, "binary-b"))
	logger2.Info("from b")
	require.NoError(t, closer2.Close())

	_, err := os.Stat(filepath.Join(dir, "binary-a.log"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "binary-b.log"))
	assert.NoError(t, err)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd src && go test ./common/logging/... -v`
Expected: FAIL — `testbinary.log` not found (current code writes `testbinary-<date>.<pid>.log`), or a `nil` `io.Closer` panic on `TestNewLogger_CloserIsNilSafeWhenLogDirEmpty` (current code returns a typed-nil `*os.File` welded to the exact same nil-safety trick this test would still pass on today — expected failure is specifically the filename-mismatch tests).

- [ ] **Step 4: Implement**

Replace `src/common/logging/logging.go`'s imports and `NewLogger` function:

```go
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/config"
	"gopkg.in/natefinch/lumberjack.v2"
)
```

(drops `"fmt"` and `"time"` — no longer needed once the filename stops embedding a date/pid)

Add, near the top of the file (after the `multiHandler` type, before `getLevel`):

```go
// nopCloser satisfies io.Closer with a no-op Close -- returned by NewLogger
// when no file handler was created (log_dir unset or unwritable), so
// callers can always safely `defer logfile.Close()` without risking a
// nil-interface panic.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }
```

Replace the body of `NewLogger` (keep `getLevel` unchanged):

```go
func NewLogger(ctx context.Context) (*slog.Logger, io.Closer) {
	conf := config.GetConfigFromContext(ctx)

	level := getLevel(ctx.Value("debugMode").(bool))
	quietMode := ctx.Value("quietMode").(bool)
	appName := ctx.Value("appName").(string)

	var logFile io.Closer = nopCloser{}
	handler := &multiHandler{}

	// Console output (logfmt format, only if not quiet)
	if !quietMode {
		handler.consoleHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level:     level,
			AddSource: level == slog.LevelDebug,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					return slog.String(a.Key, a.Value.Time().Format("15:04:05"))
				}
				return a
			},
		})
	}

	// File output (JSON format): one stable, rotated file per binary name --
	// <log_dir>/<appName>.log -- not one file per process invocation.
	// Optional: don't fail startup if the directory is unavailable.
	if conf.LogDir != "" {
		if err := os.MkdirAll(conf.LogDir, 0755); err == nil {
			ljLogger := &lumberjack.Logger{
				Filename:   filepath.Join(conf.LogDir, appName+".log"),
				MaxSize:    50, // megabytes
				MaxBackups: 5,
				MaxAge:     14, // days
				Compress:   true,
			}
			handler.fileHandler = slog.NewJSONHandler(ljLogger, &slog.HandlerOptions{
				Level:     level,
				AddSource: level == slog.LevelDebug,
			})
			logFile = ljLogger
		}
	}

	// Fallback to discard if no handlers
	if handler.consoleHandler == nil && handler.fileHandler == nil {
		handler.consoleHandler = slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})
	}

	logger := slog.New(handler).With(
		slog.String("app", appName),
		slog.Int("pid", os.Getpid()),
	)

	if jobId := ctx.Value("jobId"); jobId != nil {
		logger = logger.With(slog.String("job_id", jobId.(string)))
	}

	return logger, logFile
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./common/logging/... -v`
Expected: PASS (all 6 tests).

- [ ] **Step 6: Run the full test suite**

Run: `cd src && go build ./... && go test ./... 2>&1 | tail -40`
Expected: every package `ok` — every existing caller of `logging.NewLogger` (every `cmd/*/main.go`) is source-compatible, since the function signature is unchanged.

- [ ] **Step 7: Commit**

```bash
git add src/common/logging/ src/go.mod src/go.sum
git commit -m "feat(logging): one rotated file per binary instead of one per process invocation"
```

---

### Task 3: `common/jobid` — shared correlation-ID helpers

**Files:**
- Create: `src/common/jobid/jobid.go`
- Create: `src/common/jobid/jobid_test.go`

**Interfaces:**
- Produces: `jobid.Resolve(id string) string`, `jobid.Outgoing(ctx context.Context, id string) context.Context`, `jobid.FromIncoming(ctx context.Context) (string, error)`.

- [ ] **Step 1: Write the failing tests**

Create `src/common/jobid/jobid_test.go`:

```go
package jobid

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestResolve_ReturnsGivenIDWhenNonEmpty(t *testing.T) {
	assert.Equal(t, "explicit-id", Resolve("explicit-id"))
}

func TestResolve_GeneratesDistinctUUIDsWhenEmpty(t *testing.T) {
	first := Resolve("")
	second := Resolve("")
	assert.NotEmpty(t, first)
	assert.NotEmpty(t, second)
	assert.NotEqual(t, first, second)
}

func TestOutgoing_AttachesJobIDMetadata(t *testing.T) {
	ctx := Outgoing(context.Background(), "my-job")
	md, ok := metadata.FromOutgoingContext(ctx)
	require.True(t, ok)
	assert.Equal(t, []string{"my-job"}, md.Get("job-id"))
}

func TestFromIncoming_ReturnsAttachedValue(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("job-id", "my-job"))
	id, err := FromIncoming(ctx)
	require.NoError(t, err)
	assert.Equal(t, "my-job", id)
}

func TestFromIncoming_NoMetadataReturnsError(t *testing.T) {
	_, err := FromIncoming(context.Background())
	assert.Error(t, err)
}

func TestFromIncoming_EmptyJobIDReturnsError(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("job-id", ""))
	_, err := FromIncoming(ctx)
	assert.Error(t, err)
}

func TestFromIncoming_MissingJobIDKeyReturnsError(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("other-key", "value"))
	_, err := FromIncoming(ctx)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/jobid/... -v`
Expected: FAIL — package `jobid` doesn't exist yet (compile error; no non-test file present).

- [ ] **Step 3: Implement**

Create `src/common/jobid/jobid.go`:

```go
// Package jobid provides the shared per-invocation correlation-ID
// convention used across this project: a caller resolves or generates one,
// attaches it to outgoing gRPC metadata, and the server requires and reads
// it back. brfs/bwfs originated this pattern (see
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md);
// this package is that pattern extracted so every other cross-host caller
// agent drives (certclient, policyclient) and callee (issuer,
// policy-server) can share one implementation instead of three copies.
package jobid

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

// metadataKey is the gRPC metadata key job-id rides under, on the wire.
const metadataKey = "job-id"

// Resolve returns id unchanged if non-empty, otherwise a freshly generated
// UUID -- the shared "auto-generate if a --job-id flag was omitted"
// behavior every caller in this project uses.
func Resolve(id string) string {
	if id != "" {
		return id
	}
	return uuid.New().String()
}

// Outgoing attaches id to ctx's outgoing gRPC metadata under the job-id
// key, returning the derived context callers must use for the RPC call.
func Outgoing(ctx context.Context, id string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, metadataKey, id)
}

// FromIncoming reads the job-id gRPC metadata key from ctx's incoming
// metadata. There is no default: a call missing it returns an error rather
// than being silently treated as jobless -- callers decide how to handle
// that (every server in this project rejects the request outright).
func FromIncoming(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("no metadata in request")
	}
	values := md.Get(metadataKey)
	if len(values) == 0 || values[0] == "" {
		return "", fmt.Errorf("missing job-id metadata")
	}
	return values[0], nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/jobid/... -v`
Expected: PASS (all 7 tests).

- [ ] **Step 5: Commit**

```bash
git add src/common/jobid/
git commit -m "feat(jobid): add shared Resolve/Outgoing/FromIncoming correlation-ID helpers"
```

---

### Task 4: `bwfs` and `brfs` adopt `common/jobid`

**Files:**
- Modify: `src/cmd/bwfs/server.go`
- Modify: `src/cmd/brfs/main.go`

**Interfaces:**
- Consumes: `jobid.Resolve`, `jobid.Outgoing`, `jobid.FromIncoming` (Task 3).

This is a pure refactor — `bwfs`'s existing `jobIDFromMetadata` and `brfs`'s inline UUID-generation/metadata-attachment are replaced by calls to the new shared package, with identical behavior. No new tests: `bwfs`'s existing `TestIntegration_MissingJobID_StreamRejected` and the rest of its integration suite already exercise this path end-to-end and must continue to pass unchanged.

- [ ] **Step 1: `bwfs` — replace `jobIDFromMetadata` with `jobid.FromIncoming`**

In `src/cmd/bwfs/server.go`, remove the `jobIDFromMetadata` function entirely (lines 45-58 in the current file):

```go
// jobIDFromMetadata reads the job-id gRPC metadata key that brfs attaches
// when it opens each stream. There is no default: a stream without it is
// rejected rather than silently treated as jobless.
func jobIDFromMetadata(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("no metadata in request")
	}
	values := md.Get("job-id")
	if len(values) == 0 || values[0] == "" {
		return "", fmt.Errorf("missing job-id metadata")
	}
	return values[0], nil
}
```

Change its one call site:

```go
	jobID, err := jobid.FromIncoming(ctx)
```

(was `jobID, err := jobIDFromMetadata(ctx)`)

Update the import block: remove `"google.golang.org/grpc/metadata"` (no longer used in this file) and add `"github.com/alex-sviridov/miniprotector/common/jobid"`.

- [ ] **Step 2: `brfs` — replace inline UUID/metadata code with `jobid.Resolve`/`jobid.Outgoing`**

In `src/cmd/brfs/main.go`, replace:

```go
	jobID := arguments.JobID
	if jobID == "" {
		jobID = uuid.New().String()
	}
	ctx = context.WithValue(ctx, "jobId", jobID)
	ctx = metadata.AppendToOutgoingContext(ctx, "job-id", jobID)
```

with:

```go
	jobID := jobid.Resolve(arguments.JobID)
	ctx = context.WithValue(ctx, "jobId", jobID)
	ctx = jobid.Outgoing(ctx, jobID)
```

Update the import block: remove `"github.com/google/uuid"` and `"google.golang.org/grpc/metadata"` (no longer used in this file), add `"github.com/alex-sviridov/miniprotector/common/jobid"`.

- [ ] **Step 3: Run both packages' full test suites**

Run: `cd src && go test ./cmd/bwfs/... ./cmd/brfs/... -v 2>&1 | tail -60`
Expected: PASS (every existing test, including `TestIntegration_MissingJobID_StreamRejected` and every other `bwfs` integration test) — behavior is identical, only the implementation moved.

- [ ] **Step 4: Run the full build to confirm no other breakage**

Run: `cd src && go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/bwfs/server.go src/cmd/brfs/main.go
git commit -m "refactor(bwfs,brfs): adopt common/jobid instead of duplicated local logic"
```

---

### Task 5: `certclient` — `--job-id` flag, propagated to `issuer`

**Files:**
- Modify: `src/cmd/certclient/arguments.go`
- Create: `src/cmd/certclient/arguments_test.go`
- Modify: `src/cmd/certclient/main.go`
- Modify: `src/cmd/certclient/operatingrefresh.go`

**Interfaces:**
- Consumes: `jobid.Resolve`, `jobid.Outgoing` (Task 3).
- Produces: `Arguments.JobID string`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/certclient/arguments_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	old := osArgsForTest()
	setOsArgsForTest(args)
	defer setOsArgsForTest(old)
	fn()
}

func TestParseArguments_RenewJobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"certclient", "renew", "--job-id", "custom-job-123"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "custom-job-123", args.JobID)
	})
}

func TestParseArguments_RenewJobIDFlag_DefaultsEmpty(t *testing.T) {
	withArgs(t, []string{"certclient", "renew"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Empty(t, args.JobID)
	})
}

func TestParseArguments_OperatingRefreshJobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"certclient", "operating-refresh", "--job-id", "custom-job-456"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "custom-job-456", args.JobID)
	})
}
```

`brfs/arguments_test.go` sets `os.Args` directly around each `cmd.Execute()` call rather than through a helper — mirror that exact pattern instead of introducing `osArgsForTest`/`setOsArgsForTest` (those don't exist anywhere in this codebase). Replace the test file above with this corrected version, which manipulates `os.Args` directly like `brfs/arguments_test.go` does:

```go
package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	old := os.Args
	os.Args = args
	defer func() { os.Args = old }()
	fn()
}

func TestParseArguments_RenewJobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"certclient", "renew", "--job-id", "custom-job-123"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "custom-job-123", args.JobID)
	})
}

func TestParseArguments_RenewJobIDFlag_DefaultsEmpty(t *testing.T) {
	withArgs(t, []string{"certclient", "renew"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Empty(t, args.JobID)
	})
}

func TestParseArguments_OperatingRefreshJobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"certclient", "operating-refresh", "--job-id", "custom-job-456"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "custom-job-456", args.JobID)
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/certclient/... -run TestParseArguments_.*JobID -v`
Expected: FAIL — `args.JobID` undefined (compile error).

- [ ] **Step 3: Add the flag**

In `src/cmd/certclient/arguments.go`, add `JobID` to the `Arguments` struct:

```go
// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "bootstrap" | "renew" | "operating-refresh"
	Token  string
	Debug  bool
	JobID  string
}
```

Add a package-level flag var alongside `args` construction isn't needed — cobra binds directly into `args.JobID` via `StringVar`, same as every other flag here. Add these two lines after `renewCmd`'s and `operatingRefreshCmd`'s existing flag/definition blocks:

```go
	renewCmd := &cobra.Command{
		Use:   "renew",
		Short: "Renew the existing bootstrap credential via step-ca's /renew",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "renew" },
	}
	renewCmd.Flags().StringVar(&args.JobID, "job-id", "", "Correlation ID for this invocation's logs (auto-generated if omitted)")

	operatingRefreshCmd := &cobra.Command{
		Use:   "operating-refresh",
		Short: "Obtain a fresh operating certificate from issuer",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "operating-refresh" },
	}
	operatingRefreshCmd.Flags().StringVar(&args.JobID, "job-id", "", "Correlation ID for this invocation's logs (auto-generated if omitted); sent to issuer as job-id metadata")
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/certclient/... -run TestParseArguments_.*JobID -v`
Expected: PASS (all 3 new tests).

- [ ] **Step 5: Wire job-id into `main.go` (local tagging) and `operatingRefresh` (metadata to `issuer`)**

In `src/cmd/certclient/main.go`, add after `configPath`/`conf` resolution and before `certsDir` resolution — insert right before `ctx := context.WithValue(...)`:

```go
	jobID := jobid.Resolve(args.JobID)

	ctx := context.WithValue(context.Background(), "appName", "certclient")
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)
	ctx = context.WithValue(ctx, "jobId", jobID)
	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()
```

(adds the `jobID := jobid.Resolve(args.JobID)` line and the `ctx = context.WithValue(ctx, "jobId", jobID)` line — this alone makes every log line `renew`/`operating-refresh` emit carry `job_id`, since `logging.NewLogger` already reads `ctx.Value("jobId")`)

Update the `operating-refresh` case's call site:

```go
	case "operating-refresh":
		if conf.IssuerHost == "" {
			fmt.Fprintln(os.Stderr, "Configuration error: issuer_host not set in local.conf")
			os.Exit(1)
		}
		if err := operatingRefresh(certsDir, conf.IssuerHost, conf.IssuerPort, conf.ConnectionTimeOutSec, jobID, logger); err != nil {
			logger.Error("operating refresh failed", "error", err)
			fmt.Fprintf(os.Stderr, "Operating refresh failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Operating certificate refreshed in", certsDir)
```

(adds `jobID` as a new parameter before `logger`)

Add `"github.com/alex-sviridov/miniprotector/common/jobid"` to the import block.

In `src/cmd/certclient/operatingrefresh.go`, change `operatingRefresh`'s signature and body:

```go
// operatingRefresh is the real, network-dialing entry point main.go calls:
// it authenticates to issuer with the bootstrap credential and delegates
// to runOperatingRefresh. jobID rides the RPC as outgoing job-id metadata,
// so issuer's own log for this exact refresh attempt is correlatable back
// to this process's local log.
func operatingRefresh(certsDir, issuerHost string, issuerPort, timeoutSec int, jobID string, logger *slog.Logger) error {
	conn, err := connection.ConnectWithIdentity(issuerHost, issuerPort, timeoutSec, certsDir, "bootstrap.crt", "bootstrap.key")
	if err != nil {
		return fmt.Errorf("connect to issuer: %w", err)
	}
	defer conn.Close()

	client := pb.NewIssuerServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	ctx = jobid.Outgoing(ctx, jobID)

	return runOperatingRefresh(ctx, certsDir, client, logger)
}
```

Add `"github.com/alex-sviridov/miniprotector/common/jobid"` to the import block. `runOperatingRefresh` itself is unchanged — it already receives `ctx` and just needs it to carry the metadata, which the caller now attaches.

- [ ] **Step 6: Run the full `certclient` test suite**

Run: `cd src && go test ./cmd/certclient/... -v 2>&1 | tail -60`
Expected: PASS (every existing test — `runOperatingRefresh`'s own tests pass an explicit `ctx` already and are unaffected by `operatingRefresh`'s signature change, since they call `runOperatingRefresh` directly, not `operatingRefresh`).

- [ ] **Step 7: Run the full build**

Run: `cd src && go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/certclient/
git commit -m "feat(certclient): add --job-id, propagate to issuer as outgoing metadata"
```

---

### Task 6: `issuer` — require and log `job-id` on `RequestOperatingCert`

**Files:**
- Modify: `src/cmd/issuer/server.go`
- Modify: `src/cmd/issuer/server_test.go`

**Interfaces:**
- Consumes: `jobid.FromIncoming` (Task 3).

- [ ] **Step 1: Update the test helper to carry job-id metadata, and write the new failing test**

In `src/cmd/issuer/server_test.go`, split `fakeAuthContext` into a peer-only builder plus a metadata-adding wrapper, and add one new test for the missing-job-id path. Replace:

```go
// fakeAuthContext mirrors cmd/catalog/server_test.go's and cmd/certrequest/
// broker_server_test.go's helper of the same name: a self-signed cert with
// the given hostname as its SAN, simulating a verified mTLS peer identity
// without a real handshake.
func fakeAuthContext(t *testing.T, hostname string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}
```

with:

```go
// peerCertContext builds a context carrying only a verified mTLS peer
// certificate for hostname, with no gRPC metadata attached. fakeAuthContext
// (below) layers job-id metadata on top for the common case; this is used
// directly by TestRequestOperatingCert_MissingJobIDRejectedWithoutMinting
// to exercise the "no job-id metadata at all" path.
func peerCertContext(t *testing.T, hostname string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

// fakeAuthContext mirrors cmd/catalog/server_test.go's helper of the same
// name: a self-signed cert with the given hostname as its SAN, plus job-id
// metadata every RequestOperatingCert test needs by default now that it's
// required -- simulating a verified mTLS peer identity and an already-
// job-id-tagged call, without a real handshake.
func fakeAuthContext(t *testing.T, hostname string) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(peerCertContext(t, hostname), metadata.Pairs("job-id", "test-job-id"))
}
```

Add `"google.golang.org/grpc/metadata"` to the import block.

Add this new test after `TestRequestOperatingCert_NoPeerIdentityRejected`:

```go
func TestRequestOperatingCert_MissingJobIDRejectedWithoutMinting(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	called := false
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		called = true
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(peerCertContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)
	assert.False(t, called, "mintSign must not be called when job-id metadata is missing")
}
```

- [ ] **Step 2: Run tests to verify the new one fails and the rest still compile**

Run: `cd src && go test ./cmd/issuer/... -v 2>&1 | tail -40`
Expected: `TestRequestOperatingCert_MissingJobIDRejectedWithoutMinting` FAILs (`RequestOperatingCert` doesn't check job-id yet, so `mintSign` gets called and the test's `assert.Error`/`assert.False` fail); every other test still PASSes (they already carry job-id metadata via the updated `fakeAuthContext`, and `RequestOperatingCert` doesn't check it yet either way).

- [ ] **Step 3: Implement**

In `src/cmd/issuer/server.go`, add the job-id check and logging to `RequestOperatingCert`:

```go
func (s *issuerServer) RequestOperatingCert(ctx context.Context, req *pb.RequestOperatingCertRequest) (*pb.RequestOperatingCertResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, fmt.Errorf("determine caller identity: %w", err)
	}

	jobID, err := jobid.FromIncoming(ctx)
	if err != nil {
		return nil, fmt.Errorf("job-id metadata required: %w", err)
	}

	client, err := s.store.GetClient(hostname)
	if err != nil {
		return nil, fmt.Errorf("hostname %s not tracked: %w", hostname, err)
	}
	if client.Revoked {
		return nil, fmt.Errorf("hostname %s is revoked", hostname)
	}

	attrRecords, err := s.store.KV(hostname, clientmanagerstore.KindAttribute)
	if err != nil {
		return nil, fmt.Errorf("load attributes for %s: %w", hostname, err)
	}
	attributes := make(map[string]string, len(attrRecords))
	for _, a := range attrRecords {
		attributes[a.Key] = a.Value
	}

	csr, err := x509.ParseCertificateRequest(req.GetCsrDer())
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}

	chainPEM, err := s.mintSign(hostname, client.SANsList(), attributes, csr)
	if err != nil {
		return nil, fmt.Errorf("issue certificate for %s: %w", hostname, err)
	}

	if err := s.store.UpdateLastSeen(hostname, time.Now()); err != nil {
		s.logger.Error("failed to update last_seen", "hostname", hostname, "job_id", jobID, "error", err)
	}

	s.logger.Info("operating certificate issued", "hostname", hostname, "job_id", jobID)
	return &pb.RequestOperatingCertResponse{CertChainPem: chainPEM}, nil
}
```

Add `"github.com/alex-sviridov/miniprotector/common/jobid"` to the import block. `DescribeSANs` is intentionally left unchanged — it's a read-only lookup that reveals nothing the caller isn't already entitled to (see its existing doc comment) and doesn't mint anything; requiring job-id there is out of scope for this plan.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/issuer/... -v`
Expected: PASS (all tests, including the new one).

- [ ] **Step 5: Run the full build**

Run: `cd src && go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/issuer/
git commit -m "feat(issuer): require and log job-id on RequestOperatingCert"
```

---

### Task 7: `policyclient` — `--job-id` flag, propagated to `policy-server`

**Files:**
- Modify: `src/cmd/policyclient/arguments.go`
- Create: `src/cmd/policyclient/arguments_test.go`
- Modify: `src/cmd/policyclient/main.go`
- Modify: `src/cmd/policyclient/fetch.go`
- Modify: `src/cmd/policyclient/fetch_test.go`

**Interfaces:**
- Consumes: `jobid.Resolve`, `jobid.Outgoing` (Task 3).
- Produces: `Arguments.JobID string`.

- [ ] **Step 1: Write the failing argument-parsing tests**

Create `src/cmd/policyclient/arguments_test.go`:

```go
package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	old := os.Args
	os.Args = args
	defer func() { os.Args = old }()
	fn()
}

func TestParseArguments_JobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"policyclient", "fetch", "--job-id", "custom-job-123"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "custom-job-123", args.JobID)
	})
}

func TestParseArguments_JobIDFlag_DefaultsEmpty(t *testing.T) {
	withArgs(t, []string{"policyclient", "fetch"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Empty(t, args.JobID)
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/policyclient/... -run TestParseArguments_JobID -v`
Expected: FAIL — `args.JobID` undefined (compile error).

- [ ] **Step 3: Add the flag**

In `src/cmd/policyclient/arguments.go`:

```go
// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "fetch"
	Debug  bool
	JobID  string
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "policyclient <command>",
		Short: "Fetch backup policies from policy-server into a local cache",
	}
	rootCmd.PersistentFlags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	fetchCmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch current policies from policy-server and update the local cache",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "fetch" },
	}
	fetchCmd.Flags().StringVar(&args.JobID, "job-id", "", "Correlation ID for this invocation's logs (auto-generated if omitted); sent to policy-server as job-id metadata")

	rootCmd.AddCommand(fetchCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: fetch")
	}
	return args, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policyclient/... -run TestParseArguments_JobID -v`
Expected: PASS (both new tests).

- [ ] **Step 5: Wire job-id into `main.go` and `fetchAndCache`**

In `src/cmd/policyclient/main.go`, insert before the `ctx` construction:

```go
	jobID := jobid.Resolve(args.JobID)

	ctx := context.WithValue(context.Background(), "appName", "policyclient")
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)
	ctx = context.WithValue(ctx, "jobId", jobID)
	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()
```

Update the `fetch` case's call site:

```go
	case "fetch":
		if conf.PolicyServerHost == "" {
			fmt.Fprintln(os.Stderr, "Configuration error: policy_server_host not set in local.conf")
			os.Exit(1)
		}
		if err := fetchAndCache(certsDir, conf.PolicyServerHost, conf.PolicyServerPort, conf.ConnectionTimeOutSec, cachePath, jobID, logger); err != nil {
			logger.Error("fetch failed", "error", err)
			fmt.Fprintf(os.Stderr, "Fetch failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Policy cache updated at", cachePath)
```

Add `"github.com/alex-sviridov/miniprotector/common/jobid"` to the import block.

In `src/cmd/policyclient/fetch.go`, change `fetchAndCache`'s signature and body:

```go
// fetchAndCache is the real, network-dialing entry point main.go calls: it
// authenticates to policy-server with this node's operating credential
// (the default connection.Connect identity -- required, since policy-server
// matches policies against attribute labels embedded only in the operating
// certificate) and delegates to runFetch. jobID rides the RPC as outgoing
// job-id metadata, so policy-server's own log for this exact fetch is
// correlatable back to this process's local log.
func fetchAndCache(certsDir, host string, port, timeoutSec int, cachePath, jobID string, logger *slog.Logger) error {
	conn, err := connection.Connect(host, port, timeoutSec, certsDir)
	if err != nil {
		return fmt.Errorf("connect to policy-server: %w", err)
	}
	defer conn.Close()

	client := pb.NewPolicyServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	ctx = jobid.Outgoing(ctx, jobID)

	return runFetch(ctx, client, cachePath, logger)
}
```

Add `"github.com/alex-sviridov/miniprotector/common/jobid"` to the import block. `runFetch` itself is unchanged.

- [ ] **Step 6: Check `fetch_test.go` for a direct `fetchAndCache` caller**

Run: `cd src && grep -n "fetchAndCache(" cmd/policyclient/fetch_test.go`
If any call site exists, add a `"test-job-id"` argument in the right position to match the new signature. `runFetch`'s own tests (which call `runFetch` directly, not `fetchAndCache`) are unaffected.

- [ ] **Step 7: Run the full `policyclient` test suite**

Run: `cd src && go test ./cmd/policyclient/... -v 2>&1 | tail -60`
Expected: PASS (every existing test).

- [ ] **Step 8: Run the full build**

Run: `cd src && go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 9: Commit**

```bash
git add src/cmd/policyclient/
git commit -m "feat(policyclient): add --job-id, propagate to policy-server as outgoing metadata"
```

---

### Task 8: `policy-server` — require and log `job-id` on `GetPolicies`

**Files:**
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/server_test.go`

**Interfaces:**
- Consumes: `jobid.FromIncoming` (Task 3).

- [ ] **Step 1: Update the test helper to carry job-id metadata, and write the new failing test**

In `src/cmd/policy-server/server_test.go`, split `fakeAuthContext` the same way Task 6 did for `issuer`. Replace:

```go
// fakeAuthContext mirrors cmd/catalog/server_test.go's and cmd/issuer/
// server_test.go's helper of the same name: a self-signed cert with the
// given hostname as its SAN and attributes as its embedded extension,
// simulating a verified mTLS peer identity without a real handshake.
func fakeAuthContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	var extensions []pkix.Extension
	if attrs != nil {
		value, err := json.Marshal(attrs)
		require.NoError(t, err)
		extensions = []pkix.Extension{{Id: attributeExtensionOID, Critical: false, Value: value}}
	}

	template := &x509.Certificate{
		SerialNumber:    big.NewInt(1),
		Subject:         pkix.Name{CommonName: hostname},
		DNSNames:        []string{hostname},
		NotBefore:       time.Now(),
		NotAfter:        time.Now().Add(time.Hour),
		ExtraExtensions: extensions,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}
```

with:

```go
// peerCertContext builds a context carrying only a verified mTLS peer
// certificate (with attrs as its embedded extension) for hostname, with no
// gRPC metadata attached. fakeAuthContext (below) layers job-id metadata
// on top for the common case; TestGetPolicies_MissingJobIDRejected uses
// this directly to exercise the "no job-id metadata at all" path.
func peerCertContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	var extensions []pkix.Extension
	if attrs != nil {
		value, err := json.Marshal(attrs)
		require.NoError(t, err)
		extensions = []pkix.Extension{{Id: attributeExtensionOID, Critical: false, Value: value}}
	}

	template := &x509.Certificate{
		SerialNumber:    big.NewInt(1),
		Subject:         pkix.Name{CommonName: hostname},
		DNSNames:        []string{hostname},
		NotBefore:       time.Now(),
		NotAfter:        time.Now().Add(time.Hour),
		ExtraExtensions: extensions,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

// fakeAuthContext mirrors cmd/catalog/server_test.go's helper of the same
// name, plus job-id metadata every GetPolicies test needs by default now
// that it's required.
func fakeAuthContext(t *testing.T, hostname string, attrs map[string]string) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(peerCertContext(t, hostname, attrs), metadata.Pairs("job-id", "test-job-id"))
}
```

Add `"google.golang.org/grpc/metadata"` to the import block.

Add this new test (find an existing test using `newTestServerWithPolicies` for the pattern):

```go
func TestGetPolicies_MissingJobIDRejected(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	_, err := srv.GetPolicies(peerCertContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify the new one fails and the rest still compile**

Run: `cd src && go test ./cmd/policy-server/... -v 2>&1 | tail -40`
Expected: `TestGetPolicies_MissingJobIDRejected` FAILs (no check yet); every other test PASSes.

- [ ] **Step 3: Implement**

In `src/cmd/policy-server/server.go`:

```go
func (s *policyServerServer) GetPolicies(ctx context.Context, _ *pb.GetPoliciesRequest) (*pb.GetPoliciesResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not determine peer identity", "error", err)
		return nil, err
	}

	jobID, err := jobid.FromIncoming(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: job-id metadata required", "hostname", hostname, "error", err)
		return nil, err
	}

	labels, err := mtls.PeerAttributes(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not read peer attributes", "hostname", hostname, "job_id", jobID, "error", err)
		return nil, err
	}

	var matched []*pb.Policy
	for _, p := range s.cache.Policies() {
		if !p.Matches(hostname, labels) {
			continue
		}
		matched = append(matched, toProtoPolicy(p))
	}

	s.logger.Info("GetPolicies", "hostname", hostname, "job_id", jobID, "matched", len(matched))
	return &pb.GetPoliciesResponse{Policies: matched}, nil
}
```

Add `"github.com/alex-sviridov/miniprotector/common/jobid"` to the import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS (all tests, including the new one).

- [ ] **Step 5: Run the full build**

Run: `cd src && go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/
git commit -m "feat(policy-server): require and log job-id on GetPolicies"
```

---

### Task 9: `agent` — per-invocation `Policy.JobID`

**Files:**
- Modify: `src/cmd/agent/policy.go`
- Modify: `src/cmd/agent/policy_test.go`
- Modify: `src/cmd/agent/backup.go`
- Modify: `src/cmd/agent/backup_test.go`

**Interfaces:**
- Produces: `Policy.JobID string` — the same per-invocation value already embedded as `--job-id` in `Args`, now also directly accessible without re-parsing `Args` (Task 10 needs this for its own logging).

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/agent/policy_test.go` (check the file first for its existing test-helper conventions — likely `policies(&config.Config{...})` called directly):

```go
func TestPolicies_EachStaticPolicyGetsADistinctJobID(t *testing.T) {
	conf := &config.Config{
		BootstrapCertRefreshIntervalSec: 86400,
		OperatingCertFetchIntervalSec:   900,
		PolicyFetchIntervalSec:          900,
	}
	all := policies(conf)
	require.Len(t, all, 3)

	seen := make(map[string]bool)
	for _, p := range all {
		assert.NotEmpty(t, p.JobID, "policy %s must have a JobID", p.ID)
		assert.False(t, seen[p.JobID], "job IDs must be distinct across policies in the same call")
		seen[p.JobID] = true
		assert.Contains(t, p.Args, "--job-id", "policy %s's Args must include --job-id")
		assert.Contains(t, p.Args, p.JobID, "policy %s's Args must carry the same JobID exposed on the struct")
	}
}
```

Add to `src/cmd/agent/backup_test.go` (find an existing test constructing `backupTasks(...)` for the exact setup pattern to mirror):

```go
func TestBackupTasks_JobIDFieldMatchesArgsFlag(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	cached := []cachedPolicy{{
		Name:          "web-policy",
		ObjectFilters: []string{"/srv/web"},
		RPO:           "1h",
		BackupWindow:  []string{"* * * * *"},
		Destination:   "bwfs:9000",
	}}
	data, err := json.Marshal(cached)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachePath, data, 0o644))

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(cachePath, conf)
	require.True(t, ok)
	require.Len(t, tasks, 1)

	task := tasks[0]
	assert.NotEmpty(t, task.JobID)
	assert.Contains(t, task.Args, "--job-id")
	assert.Contains(t, task.Args, task.JobID)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run 'TestPolicies_EachStaticPolicyGetsADistinctJobID|TestBackupTasks_JobIDFieldMatchesArgsFlag' -v`
Expected: FAIL — `p.JobID`/`task.JobID` undefined (compile error).

- [ ] **Step 3: Add the field and populate it in `policy.go`**

In `src/cmd/agent/policy.go`, add `JobID` to the `Policy` struct:

```go
type Policy struct {
	ID         string
	Binary     string
	Args       []string
	JobID      string
	Interval   time.Duration
	Due        func(PolicyState, time.Time) bool
	NextRun    func(PolicyState, time.Time) time.Time
	Background bool
}
```

Replace `policies`:

```go
// policies returns agent's three embedded policies, their intervals read
// from conf rather than compiled in -- bootstrap-refresh (long-lived
// credential, infrequent), operating-refresh (short-lived credential,
// frequent), and policy-update (fetches this node's applicable backup
// policies from policy-server into a local cache). Each gets a fresh
// per-invocation JobID (also embedded in Args as --job-id) every time this
// function is called -- policiesFunc calls it fresh every reconcile tick,
// the same way backupTasks already does for backup jobs, so an unused
// policy's JobID (one not actually due this tick) is simply discarded.
func policies(conf *config.Config) []Policy {
	now := time.Now()
	bootstrapJobID := policyJobID("bootstrap-refresh", now)
	operatingJobID := policyJobID("operating-refresh", now)
	policyUpdateJobID := policyJobID("policy-update", now)

	return []Policy{
		{ID: "bootstrap-refresh", Binary: "certclient", JobID: bootstrapJobID,
			Args:     []string{"renew", "--job-id", bootstrapJobID},
			Interval: time.Duration(conf.BootstrapCertRefreshIntervalSec) * time.Second},
		{ID: "operating-refresh", Binary: "certclient", JobID: operatingJobID,
			Args:     []string{"operating-refresh", "--job-id", operatingJobID},
			Interval: time.Duration(conf.OperatingCertFetchIntervalSec) * time.Second},
		{ID: "policy-update", Binary: "policyclient", JobID: policyUpdateJobID,
			Args:     []string{"fetch", "--job-id", policyUpdateJobID},
			Interval: time.Duration(conf.PolicyFetchIntervalSec) * time.Second},
	}
}

// policyJobID builds a per-invocation correlation ID for a static policy's
// exec, shaped like backup.go's backupJobID (<id>:<unix-timestamp>) so
// static and dynamic (backup) job-ids follow one convention.
func policyJobID(policyID string, now time.Time) string {
	return fmt.Sprintf("%s:%d", policyID, now.Unix())
}
```

Add `"fmt"` to the import block.

- [ ] **Step 4: Populate it in `backup.go`**

In `src/cmd/agent/backup.go`, replace the loop body inside `backupTasks` that constructs each task:

```go
		policyName, destination := p.Name, p.Destination
		for _, path := range p.ObjectFilters {
			jobID := backupJobID(policyName, path, time.Now())
			tasks = append(tasks, Policy{
				ID:         backupTaskID(policyName, path),
				Binary:     "brfs",
				JobID:      jobID,
				Args:       []string{path, "--destination", destination, "--job-id", jobID},
				Background: true,
				Due: func(s PolicyState, now time.Time) bool {
					return windowOpen(schedules, now, grace) && rpoElapsed(s, now, rpo)
				},
				NextRun: func(s PolicyState, now time.Time) time.Time {
					return nextWindow(schedules, now)
				},
			})
		}
```

(the only change: `jobID := backupJobID(...)` is now computed once into a local variable and reused for both `JobID` and the `--job-id` flag value, instead of calling `backupJobID`/`time.Now()` a second time inline — this also fixes a latent inconsistency where, before this change, two separate `time.Now()` calls could theoretically produce different timestamps for what should be the same job-id)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run 'TestPolicies_EachStaticPolicyGetsADistinctJobID|TestBackupTasks_JobIDFieldMatchesArgsFlag' -v`
Expected: PASS (both new tests).

- [ ] **Step 6: Run the full `agent` test suite**

Run: `cd src && go test ./cmd/agent/... -v 2>&1 | tail -80`
Expected: PASS (every existing test — `Policy`'s new field is additive, and every other test that builds a `Policy` literal directly, e.g. in `reconcile_test.go`, simply leaves `JobID` at its zero value, which is valid).

- [ ] **Step 7: Commit**

```bash
git add src/cmd/agent/policy.go src/cmd/agent/policy_test.go src/cmd/agent/backup.go src/cmd/agent/backup_test.go
git commit -m "feat(agent): give every dispatched exec a per-invocation JobID, not just backup tasks"
```

---

### Task 10: `agent` — exec start/completion logging

**Files:**
- Modify: `src/cmd/agent/reconcile.go`
- Modify: `src/cmd/agent/reconcile_test.go`

**Interfaces:**
- Consumes: `Policy.JobID` (Task 9).

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/agent/reconcile_test.go` (near the top, alongside the existing `testLogger` helper, add a variant that captures output instead of discarding it):

```go
func testLoggerWithBuffer() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func TestLogExecOutcome_SuccessLogsStartAndCompletionWithJobID(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	logExecStart(logger, p)
	logExecCompletion(logger, p, nil, 250*time.Millisecond)

	out := buf.String()
	assert.Contains(t, out, "policy execution started")
	assert.Contains(t, out, "policy execution completed")
	assert.Contains(t, out, "test-policy:123")
}

func TestLogExecOutcome_FailureLogsExitCodeWhenAvailable(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	err := exec.Command("false").Run()
	require.Error(t, err, "exec.Command(\"false\") must fail with a real *exec.ExitError")

	logExecCompletion(logger, p, err, time.Second)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, float64(1), entry["exit_code"])
}

func TestLogExecOutcome_FailureWithoutExitErrorOmitsExitCode(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	logExecCompletion(logger, p, errors.New("simulated failure"), time.Second)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	_, hasExitCode := entry["exit_code"]
	assert.False(t, hasExitCode, "a non-exec.ExitError failure must not fabricate an exit_code field")
}

func TestRun_LogsStartAndCompletionForEveryDispatchedExec(t *testing.T) {
	testPolicies := []Policy{{ID: "test-policy", Binary: "true", JobID: "test-policy:456", Interval: time.Hour}}

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	logger, buf := testLoggerWithBuffer()

	fr := &fakeRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, logger, cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "policy execution started")
	assert.Contains(t, out, "policy execution completed")
	assert.Contains(t, out, "test-policy:456")
}
```

Add `"bytes"`, `"encoding/json"`, `"errors"`, and `"os/exec"` (already imported) to `reconcile_test.go`'s import block as needed — `"os/exec"` is likely already there via other tests in this file; add only what's missing.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run 'TestLogExecOutcome|TestRun_LogsStartAndCompletion' -v`
Expected: FAIL — `logExecStart`/`logExecCompletion` undefined (compile error).

- [ ] **Step 3: Implement**

In `src/cmd/agent/reconcile.go`, add two new functions (near `recordOutcome`, since they serve the same dispatch sites):

```go
// logExecStart logs that agent is about to dispatch p's exec. Called
// immediately before execute for both the synchronous and background
// dispatch paths in run(), so agent's own log always shows an exec
// starting even if it never finishes (e.g. agent is killed mid-exec).
func logExecStart(logger *slog.Logger, p Policy) {
	logger.Info("policy execution started", "policy", p.ID, "binary", p.Binary, "job_id", p.JobID)
}

// logExecCompletion logs the outcome of one exec attempt at Info level, on
// both success and failure -- unlike recordOutcome's existing Error-level
// line (which only fires on failure, for operators grepping specifically
// for errors), this gives agent's own log a complete start/end timeline
// for every dispatched exec. exit_code is included only when attemptErr is
// a real *exec.ExitError -- fabricating one for any other error type would
// be misleading.
func logExecCompletion(logger *slog.Logger, p Policy, attemptErr error, duration time.Duration) {
	if attemptErr == nil {
		logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration)
		return
	}
	var exitErr *exec.ExitError
	if errors.As(attemptErr, &exitErr) {
		logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "exit_code", exitErr.ExitCode(), "error", attemptErr)
		return
	}
	logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "error", attemptErr)
}
```

Add `"errors"` to the import block.

Update `run`'s dispatch loop to call both around every `execute` call. Replace:

```go
			if p.Background {
				if !rs.tryMarkInFlight(p.ID) {
					continue // still running from a previous tick; stays due, skip this tick
				}
				select {
				case sem <- struct{}{}:
				default:
					rs.clearInFlight(p.ID)
					continue // no free slot this tick; stays due, retried next tick
				}
				wg.Add(1)
				go func(p Policy) {
					defer wg.Done()
					defer func() { <-sem }()
					defer rs.clearInFlight(p.ID)
					attemptErr := execute(ctx, p.Binary, p.Args)
					rs.recordOutcome(p.ID, attemptErr, time.Now())
				}(p)
				continue
			}

			attemptErr := execute(ctx, p.Binary, p.Args)
			rs.recordOutcome(p.ID, attemptErr, now)
```

with:

```go
			if p.Background {
				if !rs.tryMarkInFlight(p.ID) {
					continue // still running from a previous tick; stays due, skip this tick
				}
				select {
				case sem <- struct{}{}:
				default:
					rs.clearInFlight(p.ID)
					continue // no free slot this tick; stays due, retried next tick
				}
				wg.Add(1)
				go func(p Policy) {
					defer wg.Done()
					defer func() { <-sem }()
					defer rs.clearInFlight(p.ID)
					logExecStart(rs.logger, p)
					start := time.Now()
					attemptErr := execute(ctx, p.Binary, p.Args)
					logExecCompletion(rs.logger, p, attemptErr, time.Since(start))
					rs.recordOutcome(p.ID, attemptErr, time.Now())
				}(p)
				continue
			}

			logExecStart(rs.logger, p)
			start := time.Now()
			attemptErr := execute(ctx, p.Binary, p.Args)
			logExecCompletion(rs.logger, p, attemptErr, time.Since(start))
			rs.recordOutcome(p.ID, attemptErr, now)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run 'TestLogExecOutcome|TestRun_LogsStartAndCompletion' -v`
Expected: PASS (all 4 new tests).

- [ ] **Step 5: Run the full `agent` test suite**

Run: `cd src && go test ./cmd/agent/... -v 2>&1 | tail -100`
Expected: PASS (every existing test, including every `TestRun_*` test — the new logging calls are additive and don't change `run`'s control flow, timing, or return values).

- [ ] **Step 6: Run the full build and full repo test suite**

Run: `cd src && go build ./... && go test ./... 2>&1 | tail -40`
Expected: `issuer` builds successfully implicitly via `go build ./...`; every package `ok`.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go
git commit -m "feat(agent): log start and completion (success and failure) for every dispatched exec"
```

---

### Task 11: Deployment config and documentation

**Files:**
- Modify: `deploy/control-plane/catalog/local.conf`
- Modify: `deploy/control-plane/issuer/local.conf`
- Modify: `deploy/control-plane/policy-server/local.conf`
- Modify: `deploy/control-plane/client-manager/local.conf`
- Modify: `demo/local.conf`
- Modify: `demo/ca/clientmanager-local.conf`
- Modify: `docs/components/agent.md`

- [ ] **Step 1: Rename `logfolder` to `log_dir` in every deployment config**

In `deploy/control-plane/catalog/local.conf`, `deploy/control-plane/issuer/local.conf`, and `deploy/control-plane/policy-server/local.conf`, each has the identical two lines:

```
# default_port/default_streams/logfolder are required by every miniprotector
# binary's shared config parser, even though catalog itself only uses
# catalog_port and ca_host below. Harmless placeholders.
default_port=15722
default_streams=4
logfolder=/data/log
```

Change `logfolder=/data/log` to `log_dir=/data/log`, and `default_port/default_streams/logfolder are required` to `default_port/default_streams/log_dir are required` in the comment (adjust the binary-specific wording per file — `catalog`/`issuer`/`policy-server` respectively, matching what's already there).

In `deploy/control-plane/client-manager/local.conf`, change `logfolder=/tmp` to `log_dir=/tmp`.

In `demo/local.conf`, change `logfolder=/var/log/miniprotector` to `log_dir=/var/log/miniprotector`.

In `demo/ca/clientmanager-local.conf`, change `logfolder=/tmp` to `log_dir=/tmp`.

- [ ] **Step 2: Confirm no deployment config still references the old key**

Run: `grep -rn "logfolder" deploy/ demo/`
Expected: no output.

- [ ] **Step 3: Update `docs/components/agent.md`**

In the "Configuration Keys" table, no row exists for `logfolder`/`log_dir` today (it's inherited from `common/config`, not agent-specific) — no table change needed. Instead, add a short paragraph after the existing "Policy-driven backup execution" section (before "## Configuration Keys"), documenting the standardized logging and job-id convention:

```markdown
## Logging and correlation

Every binary `agent` execs writes structured JSON logs to `<log_dir>/<binary-name>.log` (one
stable, rotated file per binary — not one file per invocation), and every exec `agent` dispatches
now carries a `--job-id` (auto-generated per invocation if not explicitly set): `<policy-id>:
<unix-timestamp>` for the three static policies, `backup:<policy>:<slug(path)>:<timestamp>` for
backup tasks (unchanged). That same job-id rides as outgoing gRPC metadata to whatever server the
exec calls (`issuer` for `certclient`, `policy-server` for `policyclient`, `bwfs` for `brfs`), and
each of those servers tags its own log lines with the identical value — so one job-id correlates
`agent`'s own start/completion log line, the exec's local log file, and the corresponding log line
on whichever remote host it called, end to end. `agent`'s own log
(`<log_dir>/agent.log`) records a start and a completion line (success or failure, with exit code
when available) for every dispatched exec, not just failures.
```

- [ ] **Step 4: Final verification**

Run: `cd src && go build ./... && go test ./... 2>&1 | tail -40`
Expected: every package `ok`.

Run: `grep -rn "logfolder" /home/alex/miniprotector --include="*.conf" --include="*.go" -l 2>/dev/null | grep -v superpowers`
Expected: no output (only historical spec/plan docs under `docs/superpowers/` still mention it, which is correct — see Global Constraints).

- [ ] **Step 5: Commit**

```bash
git add deploy/ demo/ docs/components/agent.md
git commit -m "docs,deploy: rename logfolder to log_dir; document logging/job-id conventions"
```

---

## Self-Review

**Spec coverage** (against `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`):
- "Standardized local logging" (path + rotation) → Task 2.
- `log_dir` renamed `local.conf` key → Task 1, Task 11.
- "Correlation IDs, extended uniformly — including across hosts" (shared helper, `certclient`→`issuer`, `policyclient`→`policy-server`) → Tasks 3, 5, 6, 7, 8.
- "No stdout capture in `agent`" → unchanged by this plan; `realExec` is never modified (confirmed no task touches it).
- "`agent` gains exec start/completion logging" → Tasks 9, 10.
- Explicitly out of scope for this plan (correctly, per its own stated scope): `log-gateway`, Loki, Vector, `agent`'s Vector supervision, per-job debug-level control — all deferred to later phases against the same spec.

**Placeholder scan:** every code block in every task is complete and directly usable — no `TODO`/`TBD`/"add appropriate handling" found on review. Task 5's Step 1 shows a first draft test file, flags the naming mismatch against this codebase's actual `os.Args`-based test pattern, and replaces it with the corrected version in the same step — both versions are complete code, not placeholders, but only the second is meant to be applied; this is called out explicitly in that step's prose so an implementer doesn't apply both.

**Type consistency:** `Policy.JobID string` (Task 9) is consumed identically by `logExecStart`/`logExecCompletion` (Task 10) and by the existing `Args []string` construction in both `policy.go` and `backup.go`. `jobid.Resolve`/`Outgoing`/`FromIncoming` (Task 3) signatures are used identically at every call site in Tasks 4–8. `fetchAndCache`'s and `operatingRefresh`'s new `jobID string` parameter position (appended before the trailing `logger` parameter) is consistent between their `main.go` call sites and their own definitions.

**Sequencing:** Task 3 (shared package) must land before Tasks 4–8 (its consumers) — the task list is already in that order. Task 9 (`Policy.JobID`) must land before Task 10 (which reads it) — already in order. Every task's own test suite is run before its commit, and each task additionally re-runs the affected package(s)' full suite (not just the new tests) to catch regressions in already-passing tests.

No gaps found.
