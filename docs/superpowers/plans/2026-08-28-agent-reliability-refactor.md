# Agent Reliability/Readability/Performance Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix two latent shutdown-timing bugs found by comparing `agent`'s two independently-written process supervisors, unify them into one implementation, remove the redundant per-tick reads of `policies-cache.json`, de-globalize backoff/stability tuning out of test-mutated package vars, fix `main.go`'s `os.Exit` paths bypassing deferred logfile cleanup, and (repo-wide) replace string-keyed `context.Context` values with typed keys.

**Architecture:** Five sequential, independently-committable pieces, in dependency order: (0) typed context keys in `common/logging`, adopted by all 14 `cmd/*/main.go` files; (1) `cmd/agent/main.go`'s `serve`/`list-policies` cases become exit-code-returning functions instead of calling `os.Exit` inline; (2) a new `backoffPolicy` type replaces `reconcile.go`'s package-level `backoffBase`/`backoffMax` vars for policy retry, with `run()` gaining an explicit parameter for it; (3) a new `processSupervisor` (new `supervisor.go`) replaces the near-duplicate `storageSupervisor`/`vectorSupervisor`, fixing both bugs by construction, after which the now-fully-unused `backoff()`/`backoffBase`/`backoffMax`/`storageStabilityWindow` globals are deleted; (4) `backupTasks`/`storageTasks`/`restoreTasks` take an already-parsed `[]cachedPolicy` instead of a path, and `main.go`'s reconcile loop reads `policies-cache.json` once per tick instead of three times.

**Tech Stack:** Go 1.26, Cobra (CLI flags), `log/slog`, `robfig/cron/v3`, `testify` (assert/require), `lumberjack` (log rotation).

## Global Constraints

- No CLI, config-key, cache-file-format, or `list-policies` output changes.
- No change to `isDue`/backoff *semantics*, only to where backoff *values* come from.
- Two intentional behavior changes, both from unifying the supervisors, called out in the final `CHANGELOG.md` entry: `agent`'s bundled Vector process now honors `Stop()` promptly even mid-backoff-wait, and a `Stop()`/spawn race can no longer leave an unsignalled Vector process running.
- Every existing test must keep passing with unchanged behavior/timing unless a task explicitly says otherwise (e.g. deleting a test made redundant by a signature change).
- Every task must leave `go build ./...` and the affected package's `go test` green before its commit — these are five sequentially-landed pieces, and each commit must stand on its own.

---

## File Map

| File | Change |
|---|---|
| `src/common/logging/logging.go` | New unexported `ctxKey` type + `WithAppName`/`WithDebugMode`/`WithQuietMode`/`WithJobID` constructors; `NewLogger` reads via them |
| `src/common/logging/logging_test.go` | `testContext` uses the new constructors; new job-id round-trip test |
| `src/cmd/{agent,brfs,certclient,issuer,bwfs,rwfs,catalog,api-server,catalogsync,clientmanager-api,clientmanager-admin-api,policy-server,log-gateway,policyclient}/main.go` | String-keyed `context.WithValue` calls replaced with `logging.With...` calls |
| `src/cmd/agent/main.go` | `serve`/`list-policies` cases extracted into `serve(...) int` / `listPolicies(...) int`; `main()` becomes `os.Exit(...)`-at-the-end shaped |
| `src/cmd/agent/reconcile.go` | New `backoffPolicy` type + `defaultBackoffPolicy`; `reconcileState` gains a `backoff` field; `recordOutcome` uses it; `run()` gains a `backoff backoffPolicy` parameter |
| `src/cmd/agent/reconcile_test.go` | `TestBackoff_*` renamed/rewritten against `backoffPolicy` directly; every `run(...)` call site updated |
| `src/cmd/agent/integration_test.go` | Both `run(...)` call sites gain the trailing backoff arg (Task 4); `storageStabilityWindow` mutation replaced with `mgr.stabilityWindow` (Task 6); `policiesFunc`/`storageTasksFunc` collapse into `derivedFunc` (Task 9) |
| `src/cmd/agent/supervisor.go` | **New.** `supervisorConfig` + `processSupervisor`, replacing the supervisor portions of `storage.go`/`vector.go` |
| `src/cmd/agent/supervisor_test.go` | **New.** Generic `processSupervisor` coverage, including the two bug-regression tests |
| `src/cmd/agent/storage.go` | `storageSupervisor` deleted; `storageManager` builds `processSupervisor`s instead; gains `backoff`/`stabilityWindow` fields |
| `src/cmd/agent/storage_test.go` | `TestStorageSupervisor_*` deleted (superseded by `supervisor_test.go`); `TestStorageManager_*` updated to poke `mgr.backoff`/`mgr.stabilityWindow` directly instead of mutating globals; task-derivation tests updated in Task 8 |
| `src/cmd/agent/vector.go` | `vectorSupervisor` deleted; `newVectorSupervisor` now returns `*processSupervisor` |
| `src/cmd/agent/vector_test.go` | `TestVectorSupervisor_*` deleted (superseded by `supervisor_test.go`) |
| `src/cmd/agent/backup.go` | `backupTasks` takes `[]cachedPolicy` instead of a path, returns `[]Policy` (no `ok`) |
| `src/cmd/agent/backup_test.go` | New `mustReadCachedPolicies` helper; two now-redundant tests deleted; remaining call sites wrapped |
| `src/cmd/agent/storage.go` | (again, Task 9) `storageTasks` takes `[]cachedPolicy`, returns `[]storageTask` (no `ok`) |
| `src/cmd/agent/storage_test.go` | (again, Task 9) One redundant test deleted; remaining call sites wrapped |
| `src/cmd/agent/restore.go` | `restoreTasks` takes `[]cachedPolicy`, returns `[]Policy` (no `ok`) |
| `src/cmd/agent/restore_test.go`, `src/cmd/agent/rwfs_exec_test.go` | One redundant test deleted; remaining call sites wrapped |
| `src/cmd/agent/main.go` | (again, Task 9) `policiesFunc`/`storageTasksFunc` collapse into one `derivedFunc`; `run()`'s signature updated to match |
| `docs/components/agent.md` | Reviewed at end; no behavior-prose changes expected beyond the two Vector fixes |
| `CHANGELOG.md` | One consolidated entry |

---

### Task 1: `common/logging` — typed context keys

**Files:**
- Modify: `src/common/logging/logging.go:1-13` (imports, unchanged — `context` already imported), `:68-80` (add types/constructors, update `NewLogger`), `:129` (job-id read)
- Test: `src/common/logging/logging_test.go:15-21` (`testContext`)

**Interfaces:**
- Produces: `logging.WithAppName(ctx, string) context.Context`, `logging.WithDebugMode(ctx, bool) context.Context`, `logging.WithQuietMode(ctx, bool) context.Context`, `logging.WithJobID(ctx, string) context.Context` — consumed by Task 2's 14 `main.go` files.

- [ ] **Step 1: Run the existing logging suite to confirm the baseline passes**

Run: `cd src && go test ./common/logging/...`
Expected: PASS (baseline, before refactor)

- [ ] **Step 2: Add the typed key type and constructors**

In `src/common/logging/logging.go`, insert immediately before `func getLevel`:

```go
// ctxKey is an unexported context key type so values set here can never
// collide with another package's string-keyed context.WithValue call.
type ctxKey int

const (
	appNameKey ctxKey = iota
	debugModeKey
	quietModeKey
	jobIDKey
)

// WithAppName, WithDebugMode, WithQuietMode, and WithJobID attach the
// values NewLogger reads back out. Every cmd/*/main.go calls these instead
// of context.WithValue(ctx, "appName", ...) directly.
func WithAppName(ctx context.Context, appName string) context.Context {
	return context.WithValue(ctx, appNameKey, appName)
}

func WithDebugMode(ctx context.Context, debugMode bool) context.Context {
	return context.WithValue(ctx, debugModeKey, debugMode)
}

func WithQuietMode(ctx context.Context, quietMode bool) context.Context {
	return context.WithValue(ctx, quietModeKey, quietMode)
}

func WithJobID(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, jobIDKey, jobID)
}
```

- [ ] **Step 3: Update `NewLogger` to read the typed keys**

Replace (line 78-80):

```go
	level := getLevel(ctx.Value("debugMode").(bool))
	quietMode := ctx.Value("quietMode").(bool)
	appName := ctx.Value("appName").(string)
```

with:

```go
	level := getLevel(ctx.Value(debugModeKey).(bool))
	quietMode := ctx.Value(quietModeKey).(bool)
	appName := ctx.Value(appNameKey).(string)
```

Replace (line 129):

```go
	if jobId := ctx.Value("jobId"); jobId != nil {
```

with:

```go
	if jobId := ctx.Value(jobIDKey); jobId != nil {
```

- [ ] **Step 4: Update `logging_test.go`'s `testContext` and add a job-id test**

Replace `testContext` (lines 15-21):

```go
func testContext(logDir, appName string) context.Context {
	ctx := logging.WithAppName(context.Background(), appName)
	ctx = context.WithValue(ctx, config.ContextKey, &config.Config{LogDir: logDir})
	ctx = logging.WithDebugMode(ctx, false)
	ctx = logging.WithQuietMode(ctx, true)
	return ctx
}
```

Wait — this file is `package logging` itself (see its `package logging` line 1), so it calls the constructors unqualified, not via a `logging.` prefix. Use instead:

```go
func testContext(logDir, appName string) context.Context {
	ctx := WithAppName(context.Background(), appName)
	ctx = context.WithValue(ctx, config.ContextKey, &config.Config{LogDir: logDir})
	ctx = WithDebugMode(ctx, false)
	ctx = WithQuietMode(ctx, true)
	return ctx
}
```

Add, after `TestNewLogger_DifferentBinariesGetDifferentFiles`:

```go
func TestNewLogger_JobIDAttachedWhenSet(t *testing.T) {
	dir := t.TempDir()
	ctx := WithJobID(testContext(dir, "testbinary"), "job-123")

	logger, closer := NewLogger(ctx)
	logger.Info("hello")
	require.NoError(t, closer.Close())

	data, err := os.ReadFile(filepath.Join(dir, "testbinary.log"))
	require.NoError(t, err)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(data, &entry))
	assert.Equal(t, "job-123", entry["job_id"])
}
```

- [ ] **Step 5: Build and run the full logging suite**

Run: `cd src && go build ./common/logging/... && go test ./common/logging/... -v`
Expected: PASS, including the new `TestNewLogger_JobIDAttachedWhenSet`

- [ ] **Step 6: Commit**

```bash
git add src/common/logging/logging.go src/common/logging/logging_test.go
git commit -m "refactor(logging): typed context keys instead of raw strings"
```

---

### Task 2: Migrate all 14 `cmd/*/main.go` files to the typed constructors

This is purely mechanical — every one of these 14 files already imports `common/logging` (for `logging.NewLogger`), so no import changes are needed beyond that. `go build` after the edit will immediately flag any missed call site, since the old string-keyed values no longer match `NewLogger`'s new typed reads (Task 1) — a missed file would fail at runtime with a type-assertion panic, not a compile error, so **do not skip verification** — actually run each binary's own test suite, not just `go build`, since this is a runtime-only mismatch class of bug.

**Files:** all 14 `src/cmd/*/main.go` listed in the File Map.

**Interfaces:**
- Consumes: `logging.WithAppName`/`WithDebugMode`/`WithQuietMode`/`WithJobID` (Task 1).

- [ ] **Step 1: Apply the identical substitution to every file**

For each line matching `context.WithValue(ctx, "appName", X)` (or `context.WithValue(context.Background(), "appName", X)`), replace with `logging.WithAppName(ctx, X)` (or `logging.WithAppName(context.Background(), X)`); same pattern for `"debugMode"` → `logging.WithDebugMode`, `"quietMode"` → `logging.WithQuietMode`, `"jobId"` → `logging.WithJobID`. Exact current lines (`context.WithValue(ctx, ...)` unless noted):

| File | `appName` line | `debugMode` line | `quietMode` line | `jobId` line |
|---|---|---|---|---|
| `agent/main.go` | 56 (`context.Background()`) | 58 | 59 | — |
| `brfs/main.go` | 33 | 58 | 59 | 63 |
| `certclient/main.go` | 44 (`context.Background()`, `"certclient"`) | 46 | 47 | 48 |
| `issuer/main.go` | 66 (`context.Background()`) | 68 | 69 | — |
| `bwfs/main.go` | 22 (`context.Background()`) | 43 | 44 | — |
| `rwfs/main.go` | 16 (`context.Background()`) | 36 | 41 (var `quietForLogger`) | 47 |
| `catalog/main.go` | 40 (`context.Background()`) | 42 | 43 | — |
| `api-server/main.go` | 47 (`context.Background()`) | 49 | 50 | — |
| `catalogsync/main.go` | 41 (`context.Background()`) | 43 | 44 | — |
| `clientmanager-api/main.go` | 43 (`context.Background()`) | 45 | 46 | — |
| `clientmanager-admin-api/main.go` | 46 (`context.Background()`) | 48 | 49 | — |
| `policy-server/main.go` | 48 (`context.Background()`) | 50 | 51 | — |
| `log-gateway/main.go` | 49 (`context.Background()`) | 51 | 52 | — |
| `policyclient/main.go` | 51 (`context.Background()`, `"policyclient"`) | 53 | 54 | 55 |

Example (`certclient/main.go:44-48`), before:

```go
	ctx := context.WithValue(context.Background(), "appName", "certclient")
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)
	ctx = context.WithValue(ctx, "jobId", jobID)
```

after:

```go
	ctx := logging.WithAppName(context.Background(), "certclient")
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = logging.WithDebugMode(ctx, args.Debug)
	ctx = logging.WithQuietMode(ctx, false)
	ctx = logging.WithJobID(ctx, jobID)
```

(Note `config.ContextKey` is untouched — it's already its own typed key, unrelated to this change.) Apply the equivalent substitution in all 14 files.

- [ ] **Step 2: Build everything to catch any compile-visible mistake**

Run: `cd src && go build ./...`
Expected: PASS

- [ ] **Step 3: Run every touched binary's own package test suite**

Run: `cd src && go test ./cmd/agent/... ./cmd/brfs/... ./cmd/certclient/... ./cmd/issuer/... ./cmd/bwfs/... ./cmd/rwfs/... ./cmd/catalog/... ./cmd/api-server/... ./cmd/catalogsync/... ./cmd/clientmanager-api/... ./cmd/clientmanager-admin-api/... ./cmd/policy-server/... ./cmd/log-gateway/... ./cmd/policyclient/...`
Expected: PASS across the board — since `main()` itself isn't unit-tested in any of these packages, this mainly confirms nothing else broke; the real proof is Step 4.

- [ ] **Step 4: Manually smoke-test one binary end to end**

Run: `cd src && go run ./cmd/agent list-policies` against a throwaway config (or whatever minimal invocation each binary supports without real infrastructure) and confirm it runs without panicking on a `nil`-interface type assertion inside `NewLogger` — that specific panic (`interface conversion: interface {} is nil, not bool`) is exactly the failure mode a missed call site produces, and it would not be caught by Step 2's build.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/main.go src/cmd/brfs/main.go src/cmd/certclient/main.go src/cmd/issuer/main.go src/cmd/bwfs/main.go src/cmd/rwfs/main.go src/cmd/catalog/main.go src/cmd/api-server/main.go src/cmd/catalogsync/main.go src/cmd/clientmanager-api/main.go src/cmd/clientmanager-admin-api/main.go src/cmd/policy-server/main.go src/cmd/log-gateway/main.go src/cmd/policyclient/main.go
git commit -m "refactor: adopt common/logging's typed context-key constructors everywhere"
```

---

### Task 3: `agent/main.go` — exit codes instead of inline `os.Exit`

Fixes: several `os.Exit(1)` calls inside the `"serve"` case run after the logfile is opened (and its `Close()` deferred) but bypass that defer, since `os.Exit` doesn't run deferred functions — the very error message explaining the fatal startup failure can be lost.

**Files:**
- Modify: `src/cmd/agent/main.go` (whole `serve`/`list-policies` bodies, described below)

**Interfaces:**
- Produces: `serve(conf *config.Config, arguments *Arguments, varDir, cachePath, policiesCachePath string) int`, `listPolicies(conf *config.Config, cachePath, policiesCachePath string) int` — both return a process exit code (`0` success, `1` failure); `main()` is the only caller of either.

There is no existing `main_test.go` in this package (main()'s exit-code behavior isn't unit-testable the normal way — `os.Exit` terminates the test process too), so this task's safety net is the existing `agent` package suite (nothing in it calls `main()`, but the extraction must not change any function these tests *do* exercise) plus a manual smoke test.

- [ ] **Step 1: Extract the `"serve"` case into `serve(...)`**

Replace `main.go`'s entire `case "serve":` block (lines 50-131) with a call to a new function, and define that function separately. Full new `main.go` body for this section:

```go
	switch arguments.Action {
	case "serve":
		os.Exit(serve(conf, arguments, varDir, cachePath, policiesCachePath))

	case "list-policies":
		os.Exit(listPolicies(conf, cachePath, policiesCachePath))
	}
}

// serve runs the "serve" subcommand: sets up logging, Vector, and the
// reconcile loop, returning a process exit code rather than calling
// os.Exit directly -- every defer registered along the way (most
// importantly logfile.Close()) must run before the process actually exits,
// which os.Exit alone does not guarantee.
func serve(conf *config.Config, arguments *Arguments, varDir, cachePath, policiesCachePath string) int {
	const appName = "agent"

	if err := os.MkdirAll(varDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create var directory %s: %v\n", varDir, err)
		return 1
	}

	ctx := logging.WithAppName(context.Background(), appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = logging.WithDebugMode(ctx, arguments.Debug)
	ctx = logging.WithQuietMode(ctx, false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	// policiesFunc combines the three static policies with the dynamic
	// backup tasks derived from policies-cache.json -- called fresh every
	// reconcile tick (not resolved once here) so agent serve notices
	// policy-update's cache changing over time without needing a restart.
	// ok is false whenever backupTasks's own read of policies-cache.json
	// failed this tick -- see reconcile.go's prune, which must not treat a
	// failed read as "every backup task was removed."
	policiesFunc := func() ([]Policy, bool) {
		backupTaskList, backupOk := backupTasks(policiesCachePath, logger, conf)
		restoreTaskList, restoreOk := restoreTasks(policiesCachePath, logger)
		all := append(policies(conf), backupTaskList...)
		all = append(all, restoreTaskList...)
		return all, backupOk && restoreOk
	}

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		return 1
	}

	vectorBinary, err := resolveVectorBinary()
	if err != nil {
		logger.Error("vector binary resolution failed", "error", err)
		return 1
	}
	bwfsBinary := resolveExecPath("bwfs")
	catalogsyncBinary := resolveExecPath("catalogsync")
	storageMgr := newStorageManager(logger)
	storageTasksFunc := func() ([]storageTask, bool) {
		return storageTasks(policiesCachePath, logger, bwfsBinary, catalogsyncBinary)
	}
	hostname, err := hostnameFromBootstrapCert(certsDir)
	if err != nil {
		logger.Error("hostname resolution from bootstrap credential failed", "error", err)
		return 1
	}
	vectorConfig, err := renderVectorConfig(conf.LogDir, varDir, certsDir, conf.LogGatewayHost, conf.LogGatewayPort, hostname)
	if err != nil {
		logger.Error("vector config render failed", "error", err)
		return 1
	}
	vectorConfigPath := filepath.Join(varDir, "vector-config.yaml")
	if err := os.WriteFile(vectorConfigPath, []byte(vectorConfig), 0o644); err != nil {
		logger.Error("vector config write failed", "path", vectorConfigPath, "error", err)
		return 1
	}

	reconcileInterval := time.Duration(conf.ReconcileIntervalSec) * time.Second
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	vectorSup := newVectorSupervisor(vectorBinary, vectorConfigPath, logger)
	vectorSup.Start(signalCtx)
	defer vectorSup.Stop()
	defer storageMgr.StopAll()

	onSuccess := func(policyID string) {
		if policyID == "operating-refresh" {
			vectorSup.TriggerRestart()
		}
	}

	logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath, "vector_config", vectorConfigPath)
	if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs, onSuccess, storageTasksFunc, storageMgr); err != nil {
		logger.Error("agent exited with error", "error", err)
		return 1
	}
	return 0
}

// listPolicies runs the "list-policies" subcommand: reads and renders
// agent-state.json without executing anything.
func listPolicies(conf *config.Config, cachePath, policiesCachePath string) int {
	// list-policies never executes anything -- a silent logger here keeps
	// backupTasks'/storageTasks' own skip-with-log warnings out of stdout's
	// table, matching this command's existing read-only, no-noise character.
	silentLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	backupTaskList, _ := backupTasks(policiesCachePath, silentLogger, conf)
	restoreTaskList, _ := restoreTasks(policiesCachePath, silentLogger)
	allPolicies := append(policies(conf), backupTaskList...)
	allPolicies = append(allPolicies, restoreTaskList...)
	bwfsBinary := resolveExecPath("bwfs")
	catalogsyncBinary := resolveExecPath("catalogsync")
	storageTaskList, _ := storageTasks(policiesCachePath, silentLogger, bwfsBinary, catalogsyncBinary)
	if err := renderPolicies(os.Stdout, cachePath, time.Now(), allPolicies, storageTaskList); err != nil {
		fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
		return 1
	}
	return 0
}
```

`main()` above this now reads, in full:

```go
func main() {
	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	arguments, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Var directory resolution failed: %v\n", err)
		os.Exit(1)
	}
	cachePath := filepath.Join(varDir, "agent-state.json")
	policiesCachePath := filepath.Join(varDir, "policies-cache.json")

	switch arguments.Action {
	case "serve":
		os.Exit(serve(conf, arguments, varDir, cachePath, policiesCachePath))

	case "list-policies":
		os.Exit(listPolicies(conf, cachePath, policiesCachePath))
	}
}
```

(These four early `os.Exit(1)` calls in `main()` itself are unchanged and correct as-is — no logfile is open yet at that point, so there's nothing for them to bypass.)

- [ ] **Step 2: Build**

Run: `cd src && go build ./cmd/agent/...`
Expected: PASS

- [ ] **Step 3: Run the full agent package suite**

Run: `cd src && go test ./cmd/agent/...`
Expected: PASS — nothing in the existing suite calls `main()`, `serve()`, or `listPolicies()` directly, so this mainly confirms the extraction didn't accidentally change any function the tests *do* call (`run`, `backupTasks`, `renderPolicies`, etc. are all untouched).

- [ ] **Step 4: Manual smoke test**

Run `go run ./cmd/agent list-policies` and `timeout 3 go run ./cmd/agent serve --debug` (or equivalent) against a local dev config, confirming both still behave as before (table output for the former; startup log lines, then a clean-looking timeout-kill for the latter). This is the only practical way to confirm the exit-code plumbing behaves identically to the old inline `os.Exit` calls, since none of it is unit-tested.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/main.go
git commit -m "fix(agent): serve/list-policies return exit codes so os.Exit no longer bypasses deferred logfile cleanup"
```

---

### Task 4: `reconcile.go` — de-globalize backoff tuning for policy retry

`backoffBase`/`backoffMax`/`backoff()` are today shared by three consumers: `reconcileState.recordOutcome` (policy retry, this task), and `storageSupervisor`/`vectorSupervisor` (Task 6/7). This task gives only the first consumer its own injectable value; the free `backoff()` function and its package vars stay in place afterward (now serving only `storage.go`/`vector.go`) until Task 7 finishes migrating both and deletes them for good — removing them now would break `storage.go`/`vector.go`'s build before their own migration lands.

**Files:**
- Modify: `src/cmd/agent/reconcile.go:17-22` (new type, near existing vars), `:106-112` (`reconcileState` struct), `:223-246` (`recordOutcome`), `:287` (`run` signature), `:292` (`rs` construction)
- Modify: `src/cmd/agent/main.go` (Task 3's `serve()`, the `run(...)` call at its end)
- Test: `src/cmd/agent/reconcile_test.go`, `src/cmd/agent/integration_test.go`

**Interfaces:**
- Produces: `backoffPolicy{Base, Max time.Duration}` with method `next(failures int) time.Duration`; `defaultBackoffPolicy` (production default: `Base: 30 * time.Second, Max: 10 * time.Minute`, matching the old `backoffBase`/`backoffMax` defaults exactly); `run(..., backoff backoffPolicy) error` (one new trailing parameter).
- Consumes (unchanged): everything else about `run`'s existing signature.

- [ ] **Step 1: Run the existing reconcile suite to confirm the baseline passes**

Run: `cd src && go test ./cmd/agent/... -run 'TestBackoff|TestRun|TestRecordOutcome|TestIsDue'`
Expected: PASS (baseline, before refactor)

- [ ] **Step 2: Add `backoffPolicy` and `defaultBackoffPolicy`**

In `reconcile.go`, immediately after the existing `backoffBase`/`backoffMax` var block (after line 22), insert:

```go
// backoffPolicy is an injectable version of the backoff() computation
// below, so each consumer (reconcileState here; processSupervisor from
// Task 6/7) can be given its own value instead of sharing package-level
// vars -- the old backoffBase/backoffMax/backoff() stay in place for now,
// still used by storage.go/vector.go until they migrate too (Task 6/7),
// at which point they're deleted for good.
type backoffPolicy struct {
	Base, Max time.Duration
}

// next returns a jittered retry delay for the given number of consecutive
// failures -- identical computation to backoff() below, just parameterized
// instead of reading package vars. Must be called exactly once per failure
// and the result stored (see reconcileState.recordOutcome), not recomputed
// on every isDue check.
func (b backoffPolicy) next(failures int) time.Duration {
	exp := min(max(failures-1, 0), 8)
	d := b.Base * time.Duration(1<<exp)
	if d > b.Max {
		d = b.Max
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// defaultBackoffPolicy is the production default for reconcileState (and,
// from Task 6/7, every processSupervisor) -- same values as backoffBase/
// backoffMax below. A plain default, never mutated by tests: tests that
// need different timing construct their own backoffPolicy{...} value
// instead.
var defaultBackoffPolicy = backoffPolicy{Base: 30 * time.Second, Max: 10 * time.Minute}
```

- [ ] **Step 3: Give `reconcileState` a `backoff` field and use it in `recordOutcome`**

Modify the struct (lines 106-112):

```go
type reconcileState struct {
	mu        sync.Mutex
	cachePath string
	cache     Cache
	logger    *slog.Logger
	inFlight  map[string]bool
	backoff   backoffPolicy
}
```

In `recordOutcome`, replace (line 236):

```go
		retryAt := attemptTime.Add(backoff(state.ConsecutiveFailures))
```

with:

```go
		retryAt := attemptTime.Add(rs.backoff.next(state.ConsecutiveFailures))
```

- [ ] **Step 4: Give `run()` a `backoff backoffPolicy` parameter**

Replace the signature (line 287):

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int, onSuccess func(policyID string), storageTasksFunc func() ([]storageTask, bool), storageMgr *storageManager) error {
```

with:

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int, onSuccess func(policyID string), storageTasksFunc func() ([]storageTask, bool), storageMgr *storageManager, backoff backoffPolicy) error {
```

Replace the `rs` construction (line 292):

```go
	rs := &reconcileState{cachePath: cachePath, cache: cache, logger: logger}
```

with:

```go
	rs := &reconcileState{cachePath: cachePath, cache: cache, logger: logger, backoff: backoff}
```

- [ ] **Step 5: Update `main.go`'s real call to `run()`**

In `serve()` (Task 3's version of `main.go`), replace:

```go
	if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs, onSuccess, storageTasksFunc, storageMgr); err != nil {
```

with:

```go
	if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs, onSuccess, storageTasksFunc, storageMgr, defaultBackoffPolicy); err != nil {
```

- [ ] **Step 6: Build to find every other broken call site**

Run: `cd src && go build ./cmd/agent/...`
Expected: FAIL — every `run(...)` call in `reconcile_test.go` and `integration_test.go` is now missing an argument. The compiler output lists each one by file:line.

- [ ] **Step 7: Update every `run(...)` call site in `reconcile_test.go`**

Twelve of the fourteen calls don't exercise the failure/backoff path at all — append `, defaultBackoffPolicy` before the closing `)`. Current lines (verify against the compiler's own list from Step 6 if any have drifted): 147, 237, 288, 327, 356, 439, 478, 500, 549, 613, 725, 775. Example (line 147), before:

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
```

after:

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil, defaultBackoffPolicy)
```

Apply the same trailing `, defaultBackoffPolicy` addition at the other eleven lines listed above.

The remaining two calls need genuinely fast backoff, and lose their old global-mutation boilerplate entirely. `TestRun_FailedExecutionRecordsFailureAndRetriesAfterBackoff` (around line 162-173), replace:

```go
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 20*time.Millisecond, 50*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	fr := &fakeRunner{failN: 1} // fails once, then succeeds
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
```

with:

```go
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	fr := &fakeRunner{failN: 1} // fails once, then succeeds
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil, backoffPolicy{Base: 20 * time.Millisecond, Max: 50 * time.Millisecond})
```

`TestRun_CallsOnSuccessAfterASuccessfulExecOnly` (around line 740-758), replace:

```go
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 20*time.Millisecond, 50*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	var mu sync.Mutex
	var succeeded []string
	onSuccess := func(policyID string) {
		mu.Lock()
		defer mu.Unlock()
		succeeded = append(succeeded, policyID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec, func() ([]Policy, bool) { return testPolicies, true }, 2, onSuccess, nil, nil)
```

with:

```go
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	var mu sync.Mutex
	var succeeded []string
	onSuccess := func(policyID string) {
		mu.Lock()
		defer mu.Unlock()
		succeeded = append(succeeded, policyID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec, func() ([]Policy, bool) { return testPolicies, true }, 2, onSuccess, nil, nil, backoffPolicy{Base: 20 * time.Millisecond, Max: 50 * time.Millisecond})
```

- [ ] **Step 8: Rewrite `TestBackoff_*` to test `backoffPolicy` directly**

Replace both tests:

```go
func TestBackoff_JitterWithinHalfToFullRange(t *testing.T) {
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Second, time.Minute
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	for failures := 1; failures <= 5; failures++ {
		exp := min(max(failures-1, 0), 8)
		full := backoffBase * time.Duration(1<<exp)
		if full > backoffMax {
			full = backoffMax
		}
		d := backoff(failures)
		assert.GreaterOrEqual(t, d, full/2)
		assert.LessOrEqual(t, d, full)
	}
}

func TestBackoff_CappedAtMax(t *testing.T) {
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Second, 30*time.Second
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	d := backoff(20) // huge failure count, must clamp to backoffMax
	assert.LessOrEqual(t, d, backoffMax)
}
```

with:

```go
func TestBackoffPolicy_JitterWithinHalfToFullRange(t *testing.T) {
	bp := backoffPolicy{Base: 10 * time.Second, Max: time.Minute}
	for failures := 1; failures <= 5; failures++ {
		exp := min(max(failures-1, 0), 8)
		full := bp.Base * time.Duration(1<<exp)
		if full > bp.Max {
			full = bp.Max
		}
		d := bp.next(failures)
		assert.GreaterOrEqual(t, d, full/2)
		assert.LessOrEqual(t, d, full)
	}
}

func TestBackoffPolicy_CappedAtMax(t *testing.T) {
	bp := backoffPolicy{Base: 10 * time.Second, Max: 30 * time.Second}
	d := bp.next(20) // huge failure count, must clamp to Max
	assert.LessOrEqual(t, d, bp.Max)
}
```

(`backoffBase`/`backoffMax`/`backoff()` themselves are untouched by this step — they still exist, still used by `storage.go`/`vector.go` until Task 6/7 — this step only removes the two tests' *dependency* on them.)

- [ ] **Step 9: Update both `run(...)` call sites in `integration_test.go`**

Line 56, before:

```go
	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr, policiesFunc, 2, nil, nil, nil)
```

after:

```go
	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr, policiesFunc, 2, nil, nil, nil, defaultBackoffPolicy)
```

Line 98, before:

```go
			done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec,
				func() ([]Policy, bool) { return nil, true }, 2, nil, storageTasksFunc, mgr)
```

after:

```go
			done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec,
				func() ([]Policy, bool) { return nil, true }, 2, nil, storageTasksFunc, mgr, defaultBackoffPolicy)
```

- [ ] **Step 10: Build and run the full agent suite**

Run: `cd src && go build ./cmd/agent/... && go test ./cmd/agent/... -v -run 'TestBackoffPolicy|TestRun|TestRecordOutcome|TestIsDue|TestStorageTaskFromRealCacheFile|TestBackupTaskFromRealCacheFile'`
Expected: PASS. Then the full package: `go test ./cmd/agent/...` — expected PASS (some `storage_test.go`/`vector_test.go` tests still reference `backoffBase`/`backoffMax` directly and are untouched by this task, so they must still compile and pass unchanged).

- [ ] **Step 11: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go src/cmd/agent/integration_test.go src/cmd/agent/main.go
git commit -m "refactor(agent): de-globalize backoff tuning for policy retry into an injectable backoffPolicy"
```

---

### Task 5: New `supervisor.go` — unified `processSupervisor`

Builds the replacement for both `storageSupervisor` and `vectorSupervisor` as new, additional code — this task does not yet touch `storage.go` or `vector.go` (Task 6/7). Fixes, by construction, the two bugs found comparing the two originals: `Stop()` during a crash-backoff wait now takes effect immediately (via `stopCh`, ported from `storageSupervisor`), and a `Stop()` racing a concurrent spawn can no longer leave an unsignalled process running (via the post-`Start()` recheck, also ported from `storageSupervisor` — `vectorSupervisor` had neither).

**Files:**
- Create: `src/cmd/agent/supervisor.go`
- Test: `src/cmd/agent/supervisor_test.go`

**Interfaces:**
- Consumes: `backoffPolicy` (Task 4).
- Produces: `supervisorConfig{Binary string, Args []string, Logger *slog.Logger, Backoff backoffPolicy, StabilityWindow time.Duration, OnOutcome func(error), Stdout, Stderr io.Writer}`; `newProcessSupervisor(cfg supervisorConfig) *processSupervisor` with methods `Start(ctx context.Context)`, `Stop()`, `TriggerRestart()` — consumed by Task 6 (`storage.go`) and Task 7 (`vector.go`).

- [ ] **Step 1: Write `supervisor.go`**

```go
// supervisor.go: a generic long-running child-process supervisor, shared
// by storage.go's bwfs/catalogsync ensure-running tasks and vector.go's
// bundled Vector process. Both used to be independent, hand-written copies
// of this same spawn/wait/backoff/Stop lifecycle (storageSupervisor,
// vectorSupervisor) that had quietly drifted apart -- see
// docs/superpowers/specs/2026-08-28-agent-reliability-refactor-design.md
// for the two bugs that drift produced in the Vector copy, both fixed here
// by construction since there is now only one implementation for both
// callers.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// supervisorConfig configures one processSupervisor. Binary/Args/Logger/
// Backoff are required; every other field has a documented zero-value
// behavior so a caller only sets what it needs.
type supervisorConfig struct {
	Binary string
	Args   []string
	Logger *slog.Logger
	// Backoff governs the delay before respawning after an unexpected
	// exit. A zero value retries with no delay at all -- callers should
	// always set this explicitly (production code uses defaultBackoffPolicy).
	Backoff backoffPolicy

	// StabilityWindow, when non-zero, delays a successful start's
	// OnOutcome(nil) report until the process has stayed running this long
	// -- storage's crash-loop detection (see storageManager). Zero
	// (Vector's case) means OnOutcome(nil) fires as soon as the process is
	// spawned.
	StabilityWindow time.Duration

	// OnOutcome, when non-nil, is called with nil once a start is
	// considered successful (immediately if StabilityWindow is zero, or
	// after it elapses), and with a non-nil error on an unexpected exit.
	// Never called for a deliberate Stop(). nil (Vector's case) means this
	// supervisor's outcomes aren't tracked in agent-state.json at all.
	OnOutcome func(error)

	// Stdout/Stderr default to os.Stdout/os.Stderr when nil.
	Stdout, Stderr io.Writer
}

// processSupervisor owns the lifecycle of one supervised child process:
// spawn, wait, and on an unexpected exit, respawn after Backoff -- until
// Stop is called. Replaces the formerly-separate storageSupervisor
// (storage.go) and vectorSupervisor (vector.go).
type processSupervisor struct {
	cfg supervisorConfig

	mu           sync.Mutex
	cmd          *exec.Cmd
	shuttingDown bool
	restarting   bool // set by TriggerRestart; only vector.go's caller uses this

	// onSpawnForTest, when non-nil, is called once per spawn attempt --
	// test-only instrumentation, never set in production.
	onSpawnForTest func()

	// loopDone is closed when superviseLoop returns -- a real signal for
	// callers (and tests) to synchronize on instead of guessing at a sleep
	// duration.
	loopDone chan struct{}

	// stopCh is closed exactly once, by Stop(), so a superviseLoop sitting
	// in its backoff wait (no live process to signal -- the supervisor is
	// between crashes) notices Stop() immediately instead of only via ctx
	// (which Stop() doesn't touch) or waiting out the full backoff.
	stopCh   chan struct{}
	stopOnce sync.Once
}

func newProcessSupervisor(cfg supervisorConfig) *processSupervisor {
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	return &processSupervisor{cfg: cfg, stopCh: make(chan struct{})}
}

// Start launches the supervise loop in its own goroutine and returns
// immediately; the loop itself runs until ctx is done, at which point the
// currently-running process (if any) is also signalled to exit.
func (s *processSupervisor) Start(ctx context.Context) {
	s.loopDone = make(chan struct{})
	go func() {
		defer close(s.loopDone)
		s.superviseLoop(ctx)
	}()
}

// TriggerRestart signals the currently-running process to exit and marks
// the next respawn as deliberate, so the supervise loop skips the
// crash-backoff delay for it. Only vector.go's caller uses this today (a
// fresh operating certificate landing); storage's tasks never call it.
func (s *processSupervisor) TriggerRestart() {
	s.mu.Lock()
	cmd := s.cmd
	s.restarting = true
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

// Stop signals the currently-running process to exit (SIGTERM) and tells
// the supervise loop not to respawn it -- takes effect immediately even if
// the loop is currently sitting out a crash-backoff wait between attempts.
func (s *processSupervisor) Stop() {
	s.mu.Lock()
	s.shuttingDown = true
	cmd := s.cmd
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stopCh) })
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (s *processSupervisor) superviseLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		err := s.spawnAndWait(ctx)

		s.mu.Lock()
		shuttingDown := s.shuttingDown
		deliberate := s.restarting
		s.restarting = false
		s.mu.Unlock()

		if shuttingDown || ctx.Err() != nil {
			return
		}
		if deliberate {
			failures = 0
			continue
		}

		failures++
		s.cfg.Logger.Error("supervised process exited unexpectedly, restarting with backoff", "binary", s.cfg.Binary, "failures", failures, "error", err)
		if s.cfg.OnOutcome != nil {
			s.cfg.OnOutcome(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-time.After(s.cfg.Backoff.next(failures)):
		}
	}
}

// spawnAndWait starts the process and blocks until it exits. A successful
// start's OnOutcome(nil) report is delayed until StabilityWindow has
// elapsed (StabilityWindow == 0 means "immediately") -- a process that
// crashes faster than that never reaches OnOutcome(nil), so a genuine
// crash loop's persisted failure count (reset to 0 by any nil outcome)
// climbs instead of bouncing back to "1 failure" on every restart attempt;
// the crash itself is still reported via superviseLoop's own
// OnOutcome(err) call for the exit.
//
// cmd.Start() runs under s.mu so s.cmd is updated atomically with the
// process actually starting, and the shuttingDown recheck immediately
// after covers the case where Stop() raced ahead of this spawn: it ran
// (and saw s.cmd == nil, so sent no signal) before cmd.Start() above
// completed. Without this check the process just-started would run
// forever unsignalled.
func (s *processSupervisor) spawnAndWait(ctx context.Context) error {
	cmd := exec.Command(s.cfg.Binary, s.cfg.Args...)
	cmd.Stdout = s.cfg.Stdout
	cmd.Stderr = s.cfg.Stderr

	s.mu.Lock()
	err := cmd.Start()
	shuttingDown := s.shuttingDown
	if err == nil {
		s.cmd = cmd
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start %s: %w", s.cfg.Binary, err)
	}
	if shuttingDown {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	if s.onSpawnForTest != nil {
		s.onSpawnForTest()
	}

	waitDone := make(chan struct{})
	defer close(waitDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case <-waitDone:
		}
	}()

	// Read StabilityWindow synchronously here, on the same goroutine that
	// will go on to close waitDone, rather than inside the goroutine below
	// -- avoids a race for a test that mutates cfg.StabilityWindow after
	// construction (none currently do, but this mirrors the original
	// storageSupervisor's own reasoning for this exact structure).
	stabilityWindow := s.cfg.StabilityWindow
	go func() {
		timer := time.NewTimer(stabilityWindow)
		defer timer.Stop()
		select {
		case <-timer.C:
			if s.cfg.OnOutcome != nil {
				s.cfg.OnOutcome(nil)
			}
		case <-waitDone:
			// Process exited before the stability window elapsed -- the
			// crash is reported by superviseLoop's own OnOutcome(err) call
			// instead, not here.
		}
	}()

	return cmd.Wait()
}
```

- [ ] **Step 2: Write `supervisor_test.go`**

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFakeScript(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-process.sh")
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))
	return script
}

const longRunningScript = "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"

func TestProcessSupervisor_StartsAndStopsCleanlyOnContextCancel(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	var spawns int64
	sup := newProcessSupervisor(supervisorConfig{Binary: script, Logger: testLogger(), Backoff: defaultBackoffPolicy})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	sup.Start(ctx)

	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt64(&spawns))
	cancel()

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context cancellation")
	}
	assert.EqualValues(t, 1, atomic.LoadInt64(&spawns), "no respawn should happen once ctx is cancelled")
}

func TestProcessSupervisor_RestartsOnUnexpectedExitAndRecordsFailure(t *testing.T) {
	script := writeFakeScript(t, "#!/bin/sh\nexit 1\n")

	var spawns int64
	var mu sync.Mutex
	var outcomes []error
	sup := newProcessSupervisor(supervisorConfig{
		Binary:  script,
		Logger:  testLogger(),
		Backoff: backoffPolicy{Base: 10 * time.Millisecond, Max: 30 * time.Millisecond},
		OnOutcome: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			outcomes = append(outcomes, err)
		},
	})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Start(ctx)

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context timeout")
	}

	assert.GreaterOrEqual(t, atomic.LoadInt64(&spawns), int64(2), "a persistently crashing process must be respawned more than once")

	mu.Lock()
	defer mu.Unlock()
	var sawFailure bool
	for _, err := range outcomes {
		if err != nil {
			sawFailure = true
		}
	}
	assert.True(t, sawFailure, "at least one crash must be recorded as a failure")
}

func TestProcessSupervisor_ZeroStabilityWindowReportsSuccessImmediately(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	outcomes := make(chan error, 1)
	sup := newProcessSupervisor(supervisorConfig{
		Binary:    script,
		Logger:    testLogger(),
		Backoff:   defaultBackoffPolicy,
		OnOutcome: func(err error) { outcomes <- err },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	select {
	case err := <-outcomes:
		assert.NoError(t, err, "a zero StabilityWindow must report success as soon as the process spawns")
	case <-time.After(time.Second):
		t.Fatal("onOutcome was never called")
	}
	sup.Stop()
}

func TestProcessSupervisor_SuccessfulStartRecordsSuccessAfterStabilityWindow(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	outcomes := make(chan error, 1)
	sup := newProcessSupervisor(supervisorConfig{
		Binary:          script,
		Logger:          testLogger(),
		Backoff:         defaultBackoffPolicy,
		StabilityWindow: 20 * time.Millisecond,
		OnOutcome:       func(err error) { outcomes <- err },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	select {
	case err := <-outcomes:
		t.Fatalf("onOutcome fired before the stability window elapsed: %v", err)
	case <-time.After(5 * time.Millisecond):
	}

	select {
	case err := <-outcomes:
		assert.NoError(t, err, "a start that stays up past the stability window must record success")
	case <-time.After(time.Second):
		t.Fatal("onOutcome was never called after the stability window elapsed")
	}
	sup.Stop()
}

func TestProcessSupervisor_CrashBeforeStabilityWindowNeverRecordsSuccess(t *testing.T) {
	// Exits almost immediately -- well before the 200ms stability window.
	script := writeFakeScript(t, "#!/bin/sh\nsleep 0.01\nexit 1\n")

	var mu sync.Mutex
	var outcomes []error
	sup := newProcessSupervisor(supervisorConfig{
		Binary:          script,
		Logger:          testLogger(),
		Backoff:         backoffPolicy{Base: 10 * time.Millisecond, Max: 30 * time.Millisecond},
		StabilityWindow: 200 * time.Millisecond,
		OnOutcome: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			outcomes = append(outcomes, err)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Start(ctx)

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context timeout")
	}

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, outcomes, "a persistently crashing process must record at least one outcome")
	for _, err := range outcomes {
		assert.Error(t, err, "a process that crashes before the stability window elapses must never be recorded as a success")
	}
}

func TestProcessSupervisor_DeliberateStopDoesNotRecordFailure(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	var mu sync.Mutex
	var outcomes []error
	sup := newProcessSupervisor(supervisorConfig{
		Binary:          script,
		Logger:          testLogger(),
		Backoff:         defaultBackoffPolicy,
		StabilityWindow: 20 * time.Millisecond,
		OnOutcome: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			outcomes = append(outcomes, err)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	time.Sleep(100 * time.Millisecond) // let it start and clear the stability window, recording one nil outcome

	sup.Stop()
	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after Stop()")
	}

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, outcomes, "the process should have stayed up past the stability window and recorded a success before Stop()")
	for _, err := range outcomes {
		assert.NoError(t, err, "a deliberate Stop() must never record a failure outcome")
	}
}

// TestProcessSupervisor_StopDuringBackoffWaitReturnsPromptly is a
// regression test for bug #1 found comparing storageSupervisor and
// vectorSupervisor: vectorSupervisor's Stop() didn't take effect until a
// pending crash-backoff wait elapsed (up to backoffMax, 10 minutes in
// production) because it had no dedicated stop signal for that wait --
// only storageSupervisor did (stopCh). processSupervisor has it
// unconditionally now, so both former callers get the fix.
func TestProcessSupervisor_StopDuringBackoffWaitReturnsPromptly(t *testing.T) {
	script := writeFakeScript(t, "#!/bin/sh\nexit 1\n")

	failed := make(chan struct{}, 1)
	sup := newProcessSupervisor(supervisorConfig{
		Binary:  script,
		Logger:  testLogger(),
		Backoff: backoffPolicy{Base: 10 * time.Second, Max: 10 * time.Second}, // large -- must not be waited out
		OnOutcome: func(err error) {
			if err != nil {
				select {
				case failed <- struct{}{}:
				default:
				}
			}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	// Wait until the first crash has been recorded as a failure -- by then
	// superviseLoop has already passed its shuttingDown check for this
	// iteration and is heading into (or already sitting in) the 10s
	// backoff select, exactly the state this fix targets.
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("first crash was never recorded as a failure")
	}

	start := time.Now()
	sup.Stop()

	select {
	case <-sup.loopDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop() during backoff wait did not stop the supervisor promptly")
	}
	assert.Less(t, time.Since(start), 500*time.Millisecond, "Stop() must interrupt the backoff wait, not wait out the full 10s backoff")
}

// TestProcessSupervisor_StopRacingSpawnDoesNotLeakProcess is a regression
// test for bug #2: vectorSupervisor was missing the post-cmd.Start()
// shuttingDown recheck storageSupervisor already had. Without it, a Stop()
// landing in the narrow window between another goroutine's cmd.Start()
// returning and s.cmd being assigned would see s.cmd == nil, signal
// nothing, and leave the just-spawned process running forever unsignalled.
// This test can't hit that exact nanosecond window deterministically, but
// it proves the mechanism that closes it: spawnAndWait's own recheck fires
// whenever shuttingDown is already true by the time cmd.Start() returns,
// regardless of exactly when Stop() ran relative to it.
func TestProcessSupervisor_StopRacingSpawnDoesNotLeakProcess(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	sup := newProcessSupervisor(supervisorConfig{Binary: script, Logger: testLogger(), Backoff: defaultBackoffPolicy})

	// Simulate Stop() having already run (shuttingDown = true, s.cmd still
	// nil -- exactly the state a real race would leave behind) before
	// spawnAndWait's own cmd.Start() call happens.
	sup.mu.Lock()
	sup.shuttingDown = true
	sup.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- sup.spawnAndWait(context.Background()) }()

	select {
	case <-done:
		// cmd.Wait() returned -- the process was signalled and exited,
		// proving the recheck caught the pre-set shuttingDown flag instead
		// of leaving the process running forever.
	case <-time.After(2 * time.Second):
		t.Fatal("spawnAndWait never returned -- the just-spawned process was left running unsignalled")
	}
}

func TestProcessSupervisor_TriggerRestartDoesNotApplyBackoff(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	// A large backoff window -- if TriggerRestart incorrectly went through
	// the crash-backoff path, the respawn would not happen within this
	// test's short assertion window.
	var spawns int64
	sup := newProcessSupervisor(supervisorConfig{
		Binary:  script,
		Logger:  testLogger(),
		Backoff: backoffPolicy{Base: 10 * time.Second, Max: 10 * time.Second},
	})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt64(&spawns))

	sup.TriggerRestart()
	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&spawns) >= 2
	}, time.Second, 20*time.Millisecond, "TriggerRestart must respawn promptly, not wait out the crash-backoff window")
}
```

- [ ] **Step 3: Build and run the new suite**

Run: `cd src && go build ./cmd/agent/... && go test ./cmd/agent/... -run TestProcessSupervisor -v`
Expected: PASS, all nine new tests.

- [ ] **Step 4: Run the full agent suite to confirm nothing else broke**

Run: `cd src && go test ./cmd/agent/...`
Expected: PASS (`storage.go`/`vector.go` are untouched by this task — `processSupervisor` exists but isn't wired to production yet).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/supervisor.go src/cmd/agent/supervisor_test.go
git commit -m "feat(agent): add processSupervisor, unifying storage/vector process supervision"
```

---

### Task 6: Migrate `storage.go` to `processSupervisor`

**Files:**
- Modify: `src/cmd/agent/storage.go:114-372` (delete `storageStabilityWindow` var and the whole `storageSupervisor` type; rewrite `storageManager`)
- Test: `src/cmd/agent/storage_test.go` (delete `TestStorageSupervisor_*`; update `TestStorageManager_*`)

**Interfaces:**
- Consumes: `newProcessSupervisor`/`supervisorConfig` (Task 5).
- Produces: `newStorageManager(logger *slog.Logger) *storageManager` — **signature unchanged**, so `main.go` needs no edit for this task.

- [ ] **Step 1: Run the existing storage suite to confirm the baseline passes**

Run: `cd src && go test ./cmd/agent/... -run 'TestStorage'`
Expected: PASS (baseline)

- [ ] **Step 2: Delete `storageStabilityWindow` and the whole `storageSupervisor` type**

In `storage.go`, delete lines 114-298 in full (the `storageStabilityWindow` var, its doc comment, and every method of `storageSupervisor`: `newStorageSupervisor`, `Start`, `Stop`, `superviseLoop`, `spawnAndWait`).

- [ ] **Step 3: Rewrite `storageManager`**

Replace the remaining `storageManager` section (former lines 300-372) with:

```go
// defaultStorageStabilityWindow is production's StabilityWindow for every
// storage-policy-derived processSupervisor -- see supervisor.go. A var
// (not const) only so it reads naturally next to storageManager's own
// backoff/stabilityWindow fields below; never mutated outside a test's own
// storageManager instance.
var defaultStorageStabilityWindow = 3 * time.Second

// storageManager holds one processSupervisor per current storage task,
// keyed by task ID, and reconciles that set against agent's latest read of
// policies-cache.json every tick (see reconcile.go's run(), which calls
// reconcile once per loop iteration). It has no knowledge of what any
// given task actually runs -- bwfs, catalogsync, or anything else -- it
// only ever sees (ID, Binary, Args) tuples and supervises whatever it's
// handed.
type storageManager struct {
	logger *slog.Logger

	// backoff/stabilityWindow configure every processSupervisor this
	// manager creates -- defaulted to production values by
	// newStorageManager, overridable directly (same package) by a test
	// that needs faster timing, replacing the old package-level
	// backoffBase/backoffMax/storageStabilityWindow var-mutation pattern.
	backoff         backoffPolicy
	stabilityWindow time.Duration

	mu          sync.Mutex
	supervisors map[string]*processSupervisor
	args        map[string][]string // last-started args, to detect a changed task
}

func newStorageManager(logger *slog.Logger) *storageManager {
	return &storageManager{
		logger:          logger,
		backoff:         defaultBackoffPolicy,
		stabilityWindow: defaultStorageStabilityWindow,
		supervisors:     map[string]*processSupervisor{},
		args:            map[string][]string{},
	}
}

// reconcile starts a supervisor for every newly-appeared task, stops and
// removes one for every task that disappeared or whose Args changed
// (port/path edited on the same policy -- the old process is stopped, a
// fresh one started with the new args), and leaves an unchanged task's
// supervisor running untouched. rs is the same reconcileState run()'s own
// loop already uses -- recordOutcome is mutex-guarded internally, so this
// is safe to call from a processSupervisor's own background goroutines
// concurrently with run()'s main loop, exactly like backup-task goroutines
// already do.
func (m *storageManager) reconcile(ctx context.Context, rs *reconcileState, tasks []storageTask) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wanted := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		wanted[t.ID] = t.Args
	}

	for id, sup := range m.supervisors {
		newArgs, stillWanted := wanted[id]
		if !stillWanted || !slices.Equal(newArgs, m.args[id]) {
			sup.Stop()
			delete(m.supervisors, id)
			delete(m.args, id)
		}
	}

	for _, t := range tasks {
		if _, exists := m.supervisors[t.ID]; exists {
			continue
		}
		id := t.ID
		sup := newProcessSupervisor(supervisorConfig{
			Binary:          t.Binary,
			Args:            t.Args,
			Logger:          m.logger,
			Backoff:         m.backoff,
			StabilityWindow: m.stabilityWindow,
			OnOutcome:       func(err error) { rs.recordOutcome(id, err, time.Now()) },
		})
		sup.Start(ctx)
		m.supervisors[t.ID] = sup
		m.args[t.ID] = t.Args
	}
}

// StopAll stops every currently-supervised process -- called on agent
// shutdown so none are orphaned.
func (m *storageManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sup := range m.supervisors {
		sup.Stop()
	}
}
```

Also update the package doc comment at the top of `storage.go` (lines 1-12): the sentence "It has no knowledge of what any given task actually runs" is preserved above; the file-level comment's line "and just gets crash-restarted like any other transient exec failure)" is unaffected and needs no change, but delete the now-dangling `"os"`, `"os/exec"`, and `"syscall"` imports — all three were only used inside the deleted `storageSupervisor.spawnAndWait` (`os.Stdout`/`os.Stderr`, `exec.Command`, `syscall.SIGTERM`); `storageManager` itself never calls any of the three directly.

- [ ] **Step 4: Build to find what's now unused or broken**

Run: `cd src && go build ./cmd/agent/...`
Expected: Likely FAIL on unused imports (`os`, `os/exec`, `syscall` in `storage.go` if nothing else in that file needs them) — remove any the compiler flags. Also expect failures in `storage_test.go` (Step 5 fixes these).

- [ ] **Step 5: Delete `TestStorageSupervisor_*` from `storage_test.go`**

Delete these six test functions in full (superseded by `supervisor_test.go`'s generic coverage, Task 5): `TestStorageSupervisor_StartsAndStopsCleanlyOnContextCancel`, `TestStorageSupervisor_RestartsOnUnexpectedExitAndRecordsFailure`, `TestStorageSupervisor_SuccessfulStartRecordsSuccessAfterStabilityWindow`, `TestStorageSupervisor_CrashBeforeStabilityWindowNeverRecordsSuccess`, `TestStorageSupervisor_DeliberateStopDoesNotRecordFailure`, `TestStorageSupervisor_StopDuringBackoffWaitReturnsPromptly`. Also delete the now-unused `osWriteExecutable` helper if nothing else in the file calls it after these deletions (check with `grep -n osWriteExecutable src/cmd/agent/storage_test.go` — if the only remaining references are inside `TestStorageManager_*`, keep it; those tests still use it for their own fixture scripts).

- [ ] **Step 6: Update `TestStorageManager_*` to configure timing via fields instead of globals**

Six tests construct `mgr := newStorageManager(testLogger())`. Two of them currently also mutate globals beforehand — update those two to poke the manager's own fields instead, immediately after construction:

`TestStorageManager_StartsSupervisorForNewTask`, replace:

```go
	origWindow := storageStabilityWindow
	storageStabilityWindow = 20 * time.Millisecond
	defer func() { storageStabilityWindow = origWindow }()

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(testLogger())
```

with:

```go
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger(), backoff: defaultBackoffPolicy}
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 20 * time.Millisecond
```

`TestStorageManager_TasksSuperviseFullyIndependently`, replace:

```go
	origWindow := storageStabilityWindow
	storageStabilityWindow = 20 * time.Millisecond
	defer func() { storageStabilityWindow = origWindow }()

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Millisecond, 30*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	dir := t.TempDir()
	healthyScript := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, healthyScript, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))
	crashingScript := filepath.Join(dir, "fake-catalogsync.sh")
	require.NoError(t, osWriteExecutable(t, crashingScript, "#!/bin/sh\nexit 1\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(testLogger())
```

with:

```go
	dir := t.TempDir()
	healthyScript := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, healthyScript, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))
	crashingScript := filepath.Join(dir, "fake-catalogsync.sh")
	require.NoError(t, osWriteExecutable(t, crashingScript, "#!/bin/sh\nexit 1\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger(), backoff: defaultBackoffPolicy}
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 20 * time.Millisecond
	mgr.backoff = backoffPolicy{Base: 10 * time.Millisecond, Max: 30 * time.Millisecond}
```

The other four `TestStorageManager_*` tests (`StopsSupervisorForRemovedTask`, `RestartsSupervisorWhenArgsChange`, `DoesNotDoubleStartAlreadySupervisedTask`, `StopAllStopsEverySupervisor`) don't depend on fast backoff/stability timing for their assertions — leave their `mgr := newStorageManager(testLogger())` lines as-is, but add `backoff: defaultBackoffPolicy` to each of their `&reconcileState{...}` literals for consistency (harmless — `rs.backoff` is only read on a failure path none of these four tests exercise).

- [ ] **Step 7: Fix `integration_test.go`'s own `storageStabilityWindow` mutation**

`TestRun_StorageTaskFromRealCacheFileStartsAndPrunesStorageSupervisors` (`integration_test.go`, around lines 68-71) also mutates the now-deleted global, ahead of its own `mgr := newStorageManager(testLogger())` (around line 93) — this must be fixed in this same commit, or the package fails to build starting here, not just later in Task 9. Replace:

```go
	origWindow := storageStabilityWindow
	storageStabilityWindow = 20 * time.Millisecond
	defer func() { storageStabilityWindow = origWindow }()

	dir := t.TempDir()
```

with:

```go
	dir := t.TempDir()
```

and, right after `storageTasksFunc := func() ([]storageTask, bool) { return storageTasks(policiesCachePath, testLogger(), script, script) }` / `mgr := newStorageManager(testLogger())`, add:

```go
	mgr.stabilityWindow = 20 * time.Millisecond
```

(This test's `run(...)` call and `storageTasksFunc` closure are otherwise untouched here — Task 8/9 change their shape further, in their own commits.)

- [ ] **Step 8: Build and run the full agent suite**

Run: `cd src && go build ./... && go test ./cmd/agent/... -run 'TestStorage|TestProcessSupervisor|TestRun_StorageTaskFromRealCacheFile' -v`
Expected: PASS. Then: `go test ./cmd/agent/...` (full package) — expected PASS.

- [ ] **Step 9: Commit**

```bash
git add src/cmd/agent/storage.go src/cmd/agent/storage_test.go src/cmd/agent/integration_test.go
git commit -m "refactor(agent): migrate storageManager onto the unified processSupervisor"
```

---

### Task 7: Migrate `vector.go` to `processSupervisor`; delete the now-unused backoff globals

**Files:**
- Modify: `src/cmd/agent/vector.go:168-349` (delete the whole `vectorSupervisor` type; rewrite `newVectorSupervisor`)
- Modify: `src/cmd/agent/reconcile.go` (delete `backoffBase`/`backoffMax`/`backoff()`, now fully unused)
- Test: `src/cmd/agent/vector_test.go` (delete `TestVectorSupervisor_*`)

**Interfaces:**
- Consumes: `newProcessSupervisor`/`supervisorConfig` (Task 5).
- Produces: `newVectorSupervisor(binary, configPath string, logger *slog.Logger) *processSupervisor` — same three parameters as the old constructor, different return type; `main.go`'s existing `vectorSup.Start(...)`/`.Stop()`/`.TriggerRestart()` calls all still compile unchanged since `processSupervisor` has all three methods.

- [ ] **Step 1: Run the existing vector suite to confirm the baseline passes**

Run: `cd src && go test ./cmd/agent/... -run 'TestVector'`
Expected: PASS (baseline)

- [ ] **Step 2: Delete the whole `vectorSupervisor` type, replace `newVectorSupervisor`**

In `vector.go`, delete lines 168-349 in full (the `vectorSupervisor` struct and every one of its methods: `newVectorSupervisor`, `Start`, `TriggerRestart`, `Stop`, `superviseLoop`, `spawnAndWait`) and replace with:

```go
// newVectorSupervisor builds a processSupervisor configured for agent's
// bundled Vector process: no StabilityWindow and no OnOutcome (Vector
// isn't tracked in agent-state.json/list-policies, unlike storage tasks --
// see storage.go's storageManager), and Vector's own stdout/stderr
// rotated to disk via lumberjack, since they were previously silently
// discarded and are the only way to see a Vector-side failure (a config
// problem, a sink healthcheck failure, a buffer error) without manually
// re-running the binary by hand.
func newVectorSupervisor(binary, configPath string, logger *slog.Logger) *processSupervisor {
	args := []string{}
	if configPath != "" {
		args = []string{"--config", configPath}
	}

	var stdout, stderr io.Writer
	if configPath != "" {
		ljLogger := &lumberjack.Logger{
			Filename:   filepath.Join(filepath.Dir(configPath), "vector-output.log"),
			MaxSize:    50, // megabytes
			MaxBackups: 5,
			MaxAge:     14, // days
			Compress:   true,
		}
		stdout, stderr = ljLogger, ljLogger
	}

	return newProcessSupervisor(supervisorConfig{
		Binary:  binary,
		Args:    args,
		Logger:  logger,
		Backoff: defaultBackoffPolicy,
		Stdout:  stdout,
		Stderr:  stderr,
	})
}
```

Update `vector.go`'s import block: add `"io"` (for the `io.Writer` return type above — `bytes`/`crypto/tls`/`crypto/x509`/`fmt`/`log/slog`/`os`/`path/filepath`/`text/template`/`gopkg.in/natefinch/lumberjack.v2` all stay, since `resolveVectorBinary`/`renderVectorConfig`/`hostnameFromBootstrapCert` are untouched); remove `"os/exec"`, `"sync"`, `"syscall"`, and `"time"` — all four were only used inside the deleted `vectorSupervisor` methods (`exec.Command`, `sync.Mutex`, `syscall.SIGTERM`, `time.After`/`backoff`); nothing else in this file references any of them.

- [ ] **Step 3: Delete the now-fully-unused backoff globals in `reconcile.go`**

`backoff()`/`backoffBase`/`backoffMax` (originally lines 17-22 and 90-98 of `reconcile.go`, before Task 4's `backoffPolicy` addition shifted line numbers) are no longer referenced anywhere — `reconcileState` uses its own `backoff` field (Task 4), `storageManager` uses its own `backoff` field (Task 6), and `vector.go` now uses `defaultBackoffPolicy` directly (Step 2 above). Delete:

```go
var (
	backoffBase = 30 * time.Second
	backoffMax  = 10 * time.Minute
)
```

and:

```go
func backoff(failures int) time.Duration {
	exp := min(max(failures-1, 0), 8)
	d := backoffBase * time.Duration(1<<exp)
	if d > backoffMax {
		d = backoffMax
	}
	// half jitter: never near-zero, still spreads retries across a fleet
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}
```

(Their doc comments go with them.) Leave `backoffPolicy`/`defaultBackoffPolicy`/`backoffPolicy.next` (Task 4) untouched — those are the permanent replacement, not part of this deletion.

- [ ] **Step 4: Build to confirm nothing else references the deleted names**

Run: `cd src && go build ./cmd/agent/...`
Expected: FAIL if any reference survives (there shouldn't be any outside `vector_test.go`, fixed next) — the compiler will name every remaining site.

- [ ] **Step 5: Delete `TestVectorSupervisor_*` from `vector_test.go`**

Delete these three test functions in full (superseded by `supervisor_test.go`'s generic coverage, Task 5, including its `TestProcessSupervisor_TriggerRestartDoesNotApplyBackoff` for this exact behavior): `TestVectorSupervisor_StartsAndStopsCleanlyOnContextCancel`, `TestVectorSupervisor_RestartsOnUnexpectedExitWithoutHangingForever`, `TestVectorSupervisor_TriggerRestartDoesNotApplyBackoff`. Every other test in this file (`TestResolveVectorBinary_*`, `TestRenderVectorConfig_*`, `TestHostnameFromBootstrapCert_*`) is untouched — none of them reference the deleted supervisor type or the deleted backoff globals.

- [ ] **Step 6: Build and run the full agent suite**

Run: `cd src && go build ./... && go test ./cmd/agent/... -v`
Expected: PASS across the entire package — this is the first point where every one of Tasks 4-7's changes are simultaneously live, so run the whole suite, not a filtered subset.

- [ ] **Step 7: Manual smoke test**

Run `timeout 5 go run ./cmd/agent serve --debug` against a local dev config and confirm `agent.log` shows Vector starting normally (no immediate crash-loop from a malformed `newVectorSupervisor` wiring) — the automated suite exercises `processSupervisor` and `renderVectorConfig` separately, but not the two wired together via a real Vector binary.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/agent/vector.go src/cmd/agent/vector_test.go src/cmd/agent/reconcile.go
git commit -m "refactor(agent): migrate vector supervision onto the unified processSupervisor, delete the now-unused legacy backoff globals"
```

---

### Task 8: `backupTasks`/`storageTasks`/`restoreTasks` take `[]cachedPolicy` instead of a path

**Files:**
- Modify: `src/cmd/agent/backup.go:212-278` (`backupTasks`), `src/cmd/agent/storage.go` (`storageTasks`), `src/cmd/agent/restore.go:85-137` (`restoreTasks`)
- Test: `src/cmd/agent/backup_test.go`, `src/cmd/agent/storage_test.go`, `src/cmd/agent/restore_test.go`, `src/cmd/agent/rwfs_exec_test.go`

**Interfaces:**
- Consumes: `readCachedPolicies(path string) ([]cachedPolicy, bool)` — **unchanged**, still the one place that touches the filesystem for `policies-cache.json`.
- Produces: `backupTasks(cachedPolicies []cachedPolicy, logger *slog.Logger, conf *config.Config) []Policy`, `storageTasks(cachedPolicies []cachedPolicy, logger *slog.Logger, bwfsBinary, catalogsyncBinary string) []storageTask`, `restoreTasks(cachedPolicies []cachedPolicy, logger *slog.Logger) []Policy` — all three drop their `bool` return; a failed read is now handled once, by the caller (Task 9), before any of them is invoked.

- [ ] **Step 1: Run the existing suites to confirm the baseline passes**

Run: `cd src && go test ./cmd/agent/... -run 'TestBackupTasks|TestStorageTasks|TestRestoreTasks|TestReadCachedPolicies|TestRestoreTask_RealRwfsBinary'`
Expected: PASS (baseline)

- [ ] **Step 2: Update `backupTasks`**

In `backup.go`, replace the signature and its first few lines (lines 212-219):

```go
func backupTasks(policiesCachePath string, logger *slog.Logger, conf *config.Config) ([]Policy, bool) {
	grace := time.Duration(conf.BackupWindowGraceSec) * time.Second

	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []Policy
```

with:

```go
func backupTasks(cachedPolicies []cachedPolicy, logger *slog.Logger, conf *config.Config) []Policy {
	grace := time.Duration(conf.BackupWindowGraceSec) * time.Second

	var tasks []Policy
```

At the end of the function, replace:

```go
	return tasks, true
}
```

with:

```go
	return tasks
}
```

Update the function's doc comment (lines 189-211): remove the two paragraphs discussing the `ok` return value's meaning (the "second return value is ok=false..." paragraph, and the "callers that need to notice policies-cache.json changing over time... must call this fresh every tick" framing can stay, since it's still true for the *caller's* read, just no longer this function's own concern).

- [ ] **Step 3: Update `storageTasks`**

In `storage.go`, replace the signature and its first lines:

```go
func storageTasks(policiesCachePath string, logger *slog.Logger, bwfsBinary, catalogsyncBinary string) ([]storageTask, bool) {
	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []storageTask
```

with:

```go
func storageTasks(cachedPolicies []cachedPolicy, logger *slog.Logger, bwfsBinary, catalogsyncBinary string) []storageTask {
	var tasks []storageTask
```

At the end, replace `return tasks, true` with `return tasks`. Update its doc comment the same way as Step 2 (drop the `ok`-semantics paragraph).

- [ ] **Step 4: Update `restoreTasks`**

In `restore.go`, replace the signature and its first lines (lines 85-90):

```go
func restoreTasks(policiesCachePath string, logger *slog.Logger) ([]Policy, bool) {
	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []Policy
```

with:

```go
func restoreTasks(cachedPolicies []cachedPolicy, logger *slog.Logger) []Policy {
	var tasks []Policy
```

At the end, replace `return tasks, true` with `return tasks`. Update its doc comment the same way.

- [ ] **Step 5: Build to find every broken call site**

Run: `cd src && go build ./cmd/agent/...`
Expected: FAIL — every call to `backupTasks`/`storageTasks`/`restoreTasks` in production code (`main.go`, fixed properly in Task 9 — for now just make it compile) and every test call site is now wrong (either wrong argument type, or destructuring a `bool` that no longer exists).

For `main.go` specifically (both `serve()` and `listPolicies()` call all three), make the **minimal** compiling change for this task only — wrap each call with `readCachedPolicies` inline, without yet doing Task 9's full single-read restructuring:

In `serve()`'s `policiesFunc`, replace:

```go
	policiesFunc := func() ([]Policy, bool) {
		backupTaskList, backupOk := backupTasks(policiesCachePath, logger, conf)
		restoreTaskList, restoreOk := restoreTasks(policiesCachePath, logger)
		all := append(policies(conf), backupTaskList...)
		all = append(all, restoreTaskList...)
		return all, backupOk && restoreOk
	}
```

with:

```go
	policiesFunc := func() ([]Policy, bool) {
		cachedPolicies, ok := readCachedPolicies(policiesCachePath)
		if !ok {
			// Bootstrap/self-heal: even on a failed read (missing cache
			// file on a fresh node, or a transient corrupt read), the
			// three static policies must still run -- policy-update is
			// what (re)creates policies-cache.json in the first place, so
			// suppressing it here would deadlock a fresh install forever
			// (never able to run the one thing that fixes the read).
			// Matches the pre-refactor code's actual behavior: it always
			// started from append(policies(conf), ...), so a failed
			// backupTasks/restoreTasks read (nil, false in the old
			// contract) still left the static policies in policyList --
			// only ok=false (suppressing prune, see reconcile.go) changed.
			return policies(conf), false
		}
		all := append(policies(conf), backupTasks(cachedPolicies, logger, conf)...)
		all = append(all, restoreTasks(cachedPolicies, logger)...)
		return all, true
	}
```

And `storageTasksFunc` — no equivalent fix needed here: storage tasks have no static-policy counterpart, so `nil, false` on a failed read matches `storageTasks`'s own old contract exactly:

```go
	storageTasksFunc := func() ([]storageTask, bool) {
		return storageTasks(policiesCachePath, logger, bwfsBinary, catalogsyncBinary)
	}
```

becomes:

```go
	storageTasksFunc := func() ([]storageTask, bool) {
		cachedPolicies, ok := readCachedPolicies(policiesCachePath)
		if !ok {
			return nil, false
		}
		return storageTasks(cachedPolicies, logger, bwfsBinary, catalogsyncBinary), true
	}
```

In `listPolicies()`, replace:

```go
	backupTaskList, _ := backupTasks(policiesCachePath, silentLogger, conf)
	restoreTaskList, _ := restoreTasks(policiesCachePath, silentLogger)
	allPolicies := append(policies(conf), backupTaskList...)
	allPolicies = append(allPolicies, restoreTaskList...)
	bwfsBinary := resolveExecPath("bwfs")
	catalogsyncBinary := resolveExecPath("catalogsync")
	storageTaskList, _ := storageTasks(policiesCachePath, silentLogger, bwfsBinary, catalogsyncBinary)
```

with:

```go
	cachedPolicies, _ := readCachedPolicies(policiesCachePath)
	allPolicies := append(policies(conf), backupTasks(cachedPolicies, silentLogger, conf)...)
	allPolicies = append(allPolicies, restoreTasks(cachedPolicies, silentLogger)...)
	bwfsBinary := resolveExecPath("bwfs")
	catalogsyncBinary := resolveExecPath("catalogsync")
	storageTaskList := storageTasks(cachedPolicies, silentLogger, bwfsBinary, catalogsyncBinary)
```

(A discarded `ok` here is fine and matches this command's existing "empty/best-effort on any read problem" character — `list-policies` already treats an unreadable cache as "show zero dynamic tasks," unchanged from before.) **Task 9 replaces this whole shape again** with the fully single-read version — this step exists only to keep the build green between Task 8 and Task 9's commits.

- [ ] **Step 6: Add `mustReadCachedPolicies` test helper**

In `backup_test.go`, immediately after `writeCachedPolicies`:

```go
// mustReadCachedPolicies reads path via readCachedPolicies and fails the
// test immediately if the read wasn't ok -- shared by every test that
// exercises backupTasks/storageTasks/restoreTasks against a valid fixture
// file, now that those three take already-parsed policies rather than a
// path (see TestReadCachedPolicies_* above for dedicated unreadable/
// corrupt-file coverage, which is what those specific failure modes are
// tested through now instead).
func mustReadCachedPolicies(t *testing.T, path string) []cachedPolicy {
	t.Helper()
	policies, ok := readCachedPolicies(path)
	require.True(t, ok, "fixture policies-cache.json must be readable")
	return policies
}
```

- [ ] **Step 7: Delete the three now-redundant tests**

These tested `backupTasks`/`storageTasks`/`restoreTasks`'s own file-read failure handling, which no longer exists on these functions (that behavior is now `readCachedPolicies`'s alone, already covered by `TestReadCachedPolicies_MissingFileReturnsOkFalse`/`TestReadCachedPolicies_CorruptFileReturnsOkFalse` in `backup_test.go`). Delete in full:
- `backup_test.go`: `TestBackupTasks_MissingCacheFileReturnsOkFalseWithNoTasks`, `TestBackupTasks_CorruptCacheFileReturnsOkFalseWithNoTasks`
- `storage_test.go`: `TestStorageTasks_MissingCacheFileReturnsOkFalse`, `TestStorageTasks_CorruptCacheFileReturnsOkFalse`
- `restore_test.go`: `TestRestoreTasks_UnreadableCacheReturnsNotOK`

- [ ] **Step 8: Wrap every remaining call site**

The transformation is identical everywhere: `X, ok := backupTasks(path, ...)` (or `storageTasks`/`restoreTasks`) becomes `X := backupTasks(mustReadCachedPolicies(t, path), ...)`, and the line immediately after it that asserted on `ok` (`require.True(t, ok)` or `assert.True(t, ok, "...")`) is deleted, since that assertion is now inside the helper. Apply this to every remaining call site — known starting line numbers (re-run `go build`/`go vet` after each file if any have drifted from earlier edits in this task):

- `backup_test.go`: lines 112, 137, 159, 185, 215, 247, 263, 279, 305, 322, 343, 362, 367, 384, 413, 435, 454
- `storage_test.go`: lines 25, 47, 61, 75, 91, 117, 547
- `restore_test.go`: lines 37, 60, 76, 93 (plus any others `grep -n 'restoreTasks(' src/cmd/agent/restore_test.go` finds)
- `rwfs_exec_test.go`: line 69

Example (`backup_test.go:112`), before:

```go
	tasks, ok := backupTasks(path, testLogger(), conf)
	require.True(t, ok)
```

after:

```go
	tasks := backupTasks(mustReadCachedPolicies(t, path), testLogger(), conf)
```

(If a given call site's `ok` assertion used `assert.True` instead of `require.True`, or was phrased slightly differently, delete whatever that line was — the helper's own `require.True` inside `mustReadCachedPolicies` replaces it exactly.)

- [ ] **Step 9: Build and run the full agent suite**

Run: `cd src && go build ./... && go test ./cmd/agent/... -v`
Expected: PASS across the board.

- [ ] **Step 10: Commit**

```bash
git add src/cmd/agent/backup.go src/cmd/agent/storage.go src/cmd/agent/restore.go src/cmd/agent/main.go src/cmd/agent/backup_test.go src/cmd/agent/storage_test.go src/cmd/agent/restore_test.go src/cmd/agent/rwfs_exec_test.go
git commit -m "refactor(agent): backupTasks/storageTasks/restoreTasks take already-parsed cached policies instead of a path"
```

---

### Task 9: Single `policies-cache.json` read per reconcile tick

**Files:**
- Modify: `src/cmd/agent/reconcile.go:287-320` (`run`'s signature and loop body)
- Modify: `src/cmd/agent/main.go` (`serve()`'s `policiesFunc`/`storageTasksFunc` collapse into one)
- Test: `src/cmd/agent/reconcile_test.go`, `src/cmd/agent/integration_test.go`

**Interfaces:**
- Consumes: `backupTasks`/`storageTasks`/`restoreTasks` (Task 8), `backoffPolicy` (Task 4).
- Produces: `run(ctx, logger, cachePath, reconcileInterval, execute, derivedFunc func() ([]Policy, []storageTask, bool), maxConcurrentBackgroundJobs int, onSuccess func(string), storageMgr *storageManager, backoff backoffPolicy) error` — replaces the separate `policiesFunc`/`storageTasksFunc` parameters with one combined function.

- [ ] **Step 1: Run the existing suite to confirm the baseline passes**

Run: `cd src && go test ./cmd/agent/...`
Expected: PASS (baseline, after Task 8)

- [ ] **Step 2: Update `run`'s signature and loop body**

In `reconcile.go`, replace the signature:

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int, onSuccess func(policyID string), storageTasksFunc func() ([]storageTask, bool), storageMgr *storageManager, backoff backoffPolicy) error {
```

with:

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, derivedFunc func() ([]Policy, []storageTask, bool), maxConcurrentBackgroundJobs int, onSuccess func(policyID string), storageMgr *storageManager, backoff backoffPolicy) error {
```

Replace the loop body's per-tick derivation (originally):

```go
		now := time.Now()
		policyList, ok := policiesFunc()

		var storageTaskList []storageTask
		storageOk := true
		if storageTasksFunc != nil {
			storageTaskList, storageOk = storageTasksFunc()
		}

		if ok && storageOk {
			currentIDs := make(map[string]struct{}, len(policyList)+len(storageTaskList))
			for _, p := range policyList {
				currentIDs[p.ID] = struct{}{}
			}
			for _, t := range storageTaskList {
				currentIDs[t.ID] = struct{}{}
			}
			rs.prune(currentIDs)
		}

		if storageMgr != nil && storageOk {
			storageMgr.reconcile(ctx, rs, storageTaskList)
		}
```

with:

```go
		now := time.Now()
		policyList, storageTaskList, ok := derivedFunc()

		if ok {
			currentIDs := make(map[string]struct{}, len(policyList)+len(storageTaskList))
			for _, p := range policyList {
				currentIDs[p.ID] = struct{}{}
			}
			for _, t := range storageTaskList {
				currentIDs[t.ID] = struct{}{}
			}
			rs.prune(currentIDs)
		}

		if storageMgr != nil && ok {
			storageMgr.reconcile(ctx, rs, storageTaskList)
		}
```

(The rest of the loop — the `for _, p := range policyList` due/execute section — is unchanged.)

- [ ] **Step 3: Collapse `main.go`'s `policiesFunc`/`storageTasksFunc` into one `derivedFunc`**

In `serve()`, replace (the Task 8 version of) both closures:

```go
	policiesFunc := func() ([]Policy, bool) {
		cachedPolicies, ok := readCachedPolicies(policiesCachePath)
		if !ok {
			return policies(conf), false
		}
		all := append(policies(conf), backupTasks(cachedPolicies, logger, conf)...)
		all = append(all, restoreTasks(cachedPolicies, logger)...)
		return all, true
	}
```

and, further down:

```go
	storageTasksFunc := func() ([]storageTask, bool) {
		cachedPolicies, ok := readCachedPolicies(policiesCachePath)
		if !ok {
			return nil, false
		}
		return storageTasks(cachedPolicies, logger, bwfsBinary, catalogsyncBinary), true
	}
```

with one combined closure, placed where `policiesFunc` used to be (after `bwfsBinary`/`catalogsyncBinary` are resolved — reorder the surrounding `certsDir`/`vectorBinary`/`bwfsBinary`/`catalogsyncBinary`/`storageMgr` block above it if needed so both are in scope by this point):

```go
	// derivedFunc reads policies-cache.json exactly once per reconcile
	// tick and derives every dynamic policy/task kind from that single
	// snapshot -- previously this was two separate reads (one inside
	// backupTasks+restoreTasks combined, one inside storageTasks), which
	// could observe two different snapshots of the file within the same
	// tick. ok is false whenever this tick's read failed -- see
	// reconcile.go's prune, which must not treat a failed read as "every
	// task was removed." On a failed read, the three static policies
	// still run (see policies(conf) below) -- policy-update is what
	// (re)creates policies-cache.json, so suppressing it on a missing/
	// unreadable cache would deadlock a fresh install forever. Only the
	// storage task list is genuinely empty on failure, matching
	// storageTasks's own old contract.
	derivedFunc := func() ([]Policy, []storageTask, bool) {
		cachedPolicies, ok := readCachedPolicies(policiesCachePath)
		if !ok {
			return policies(conf), nil, false
		}
		allPolicies := append(policies(conf), backupTasks(cachedPolicies, logger, conf)...)
		allPolicies = append(allPolicies, restoreTasks(cachedPolicies, logger)...)
		storageTaskList := storageTasks(cachedPolicies, logger, bwfsBinary, catalogsyncBinary)
		return allPolicies, storageTaskList, true
	}
```

Update the final `run(...)` call to match the new signature — replace:

```go
	if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs, onSuccess, storageTasksFunc, storageMgr, defaultBackoffPolicy); err != nil {
```

with:

```go
	if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, derivedFunc, conf.MaxConcurrentBackupJobs, onSuccess, storageMgr, defaultBackoffPolicy); err != nil {
```

- [ ] **Step 4: Build to find every broken test call site**

Run: `cd src && go build ./cmd/agent/...`
Expected: FAIL — every `run(...)` call in `reconcile_test.go`/`integration_test.go` still passes the old two-func shape.

- [ ] **Step 5: Update every `run(...)` call site in `reconcile_test.go`**

Of the fourteen calls, ten use a simple inline closure `func() ([]Policy, bool) { return testPolicies, true }` (eight of them at lines 147, 237, 288, 327, 356, 439, 725, 775 with `nil, nil` for storageTasksFunc/storageMgr and `defaultBackoffPolicy` trailing; the other two, at lines 173 and 758, are the backoff-timing tests from Task 4, Step 7, which keep their own custom trailing `backoffPolicy{...}` instead of `defaultBackoffPolicy`). All ten get the identical shape change: the closure's return type changes from `([]Policy, bool)` to `([]Policy, []storageTask, bool)` (adding a `nil` storage-task slice before the trailing `true`), and the call's own now-redundant `storageTasksFunc` argument slot disappears (since `derivedFunc` already carries what it used to provide) — `storageMgr`'s `nil` shifts left to take its place. For the eight plain ones, the pattern is:

before (line 147):

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil, defaultBackoffPolicy)
```

after:

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, []storageTask, bool) { return testPolicies, nil, true }, 2, nil, nil, defaultBackoffPolicy)
```

Apply the identical transformation (closure signature gains `[]storageTask` and its body gains a `nil,` before the trailing `true`; drop the redundant `nil` that used to be the separate `storageTasksFunc` argument, since `storageMgr`'s `nil` is now immediately after the closure) at lines 237, 288, 327, 356, 439, 725, 775 — each with its own `testPolicies` value but the identical shape change. The two backoff-timing tests at lines 173 and 758 get the same closure-shape change, keeping their custom trailing `backoffPolicy{...}` argument as-is instead of `defaultBackoffPolicy`.

The remaining four calls (lines 478, 500, 549, 613) don't fit this simple pattern — each needs its own treatment, below.

`TestRun_DisabledPolicyPrunedViaBackupTasks` (around line 477), which derives its policy list from the *real* `backupTasks` rather than a synthetic slice — replace:

```go
	err := run(ctx, testLogger(), stateCachePath, 10*time.Millisecond, fr.run,
		func() ([]Policy, bool) { return backupTasks(policiesCachePath, testLogger(), conf) }, 2, nil, nil, nil, defaultBackoffPolicy)
```

with:

```go
	err := run(ctx, testLogger(), stateCachePath, 10*time.Millisecond, fr.run,
		func() ([]Policy, []storageTask, bool) {
			cachedPolicies, ok := readCachedPolicies(policiesCachePath)
			if !ok {
				return nil, nil, false
			}
			return backupTasks(cachedPolicies, testLogger(), conf), nil, true
		}, 2, nil, nil, defaultBackoffPolicy)
```

And `TestRun_SkipsPruneWhenPoliciesFuncReportsNotOk` (around line 499), replace:

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run,
		func() ([]Policy, bool) { return nil, false }, 2, nil, nil, nil, defaultBackoffPolicy)
```

with:

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run,
		func() ([]Policy, []storageTask, bool) { return nil, nil, false }, 2, nil, nil, defaultBackoffPolicy)
```

And `TestRun_PruneRaceResurrectedEntryPrunedAgainNextTick`'s `policiesFunc` (around line 537), replace:

```go
	policiesFunc := func() ([]Policy, bool) {
		mu.Lock()
		defer mu.Unlock()
		if removed {
			return nil, true
		}
		return []Policy{{ID: "slow-backup", Binary: "slow", Interval: time.Hour, Background: true}}, true
	}
```

```go
	done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, policiesFunc, 2, nil, nil, defaultBackoffPolicy)
```

with:

```go
	derivedFunc := func() ([]Policy, []storageTask, bool) {
		mu.Lock()
		defer mu.Unlock()
		if removed {
			return nil, nil, true
		}
		return []Policy{{ID: "slow-backup", Binary: "slow", Interval: time.Hour, Background: true}}, nil, true
	}
```

```go
	done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, derivedFunc, 2, nil, nil, defaultBackoffPolicy)
```

And `TestRun_StdinIsPassedThroughToRunner`'s `policiesFunc` (around line 606), replace:

```go
	policiesFunc := func() ([]Policy, bool) { return []Policy{p}, true }
```

with:

```go
	derivedFunc := func() ([]Policy, []storageTask, bool) { return []Policy{p}, nil, true }
```

and its `run(...)` call's sixth argument from `policiesFunc` to `derivedFunc`.

- [ ] **Step 6: Update both `run(...)` call sites in `integration_test.go`**

`TestRun_BackupTaskFromRealCacheFileExecutesBrfsWithExpectedArgs`'s `policiesFunc`, replace:

```go
	policiesFunc := func() ([]Policy, bool) { return backupTasks(policiesCachePath, testLogger(), conf) }
```

```go
	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr, policiesFunc, 2, nil, nil, nil, defaultBackoffPolicy)
```

with:

```go
	derivedFunc := func() ([]Policy, []storageTask, bool) {
		cachedPolicies, ok := readCachedPolicies(policiesCachePath)
		if !ok {
			return nil, nil, false
		}
		return backupTasks(cachedPolicies, testLogger(), conf), nil, true
	}
```

```go
	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr, derivedFunc, 2, nil, nil, defaultBackoffPolicy)
```

`TestRun_StorageTaskFromRealCacheFileStartsAndPrunesStorageSupervisors`'s `storageTasksFunc` — by this point (after Task 6's Step 7 fix) the test reads:

```go
	storageTasksFunc := func() ([]storageTask, bool) { return storageTasks(policiesCachePath, testLogger(), script, script) }
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 20 * time.Millisecond
```

```go
		done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec,
			func() ([]Policy, bool) { return nil, true }, 2, nil, storageTasksFunc, mgr, defaultBackoffPolicy)
```

replace with:

```go
	derivedFunc := func() ([]Policy, []storageTask, bool) {
		cachedPolicies, ok := readCachedPolicies(policiesCachePath)
		if !ok {
			return nil, nil, false
		}
		return nil, storageTasks(cachedPolicies, testLogger(), script, script), true
	}
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 20 * time.Millisecond
```

```go
		done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec, derivedFunc, 2, nil, mgr, defaultBackoffPolicy)
```

(The `mgr.stabilityWindow` line itself is unchanged by this task — only `storageTasksFunc`'s replacement by `derivedFunc` and the `run(...)` call's shape.)

- [ ] **Step 7: Build and run the full agent suite**

Run: `cd src && go build ./... && go test ./cmd/agent/... -v`
Expected: PASS across the board.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/main.go src/cmd/agent/reconcile_test.go src/cmd/agent/integration_test.go
git commit -m "perf(agent): read policies-cache.json once per reconcile tick instead of three times"
```

---

### Task 10: Documentation and changelog

**Files:**
- Modify: `docs/components/agent.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Review `docs/components/agent.md` for anything now stale**

Read through it once with Tasks 1-9 in mind. Nothing in it documents `agent`'s *internal* supervisor implementation (`storageSupervisor`/`vectorSupervisor`/`processSupervisor` are never named in this file — it only documents externally-observable behavior: task IDs, `list-policies` columns, config keys, backoff *semantics*), so no prose changes should be needed except one: the "Storage-policy supervision" section's line "a start is recorded as success... only once the process has stayed running for a short stability window (a few seconds)" and "An unexpected exit is recorded as a failure with the same jittered `backoff()` reconcile.go already uses elsewhere" — confirm both still read true (they do: `defaultStorageStabilityWindow` is still ~3s, and `storageManager.backoff` still defaults to `defaultBackoffPolicy`, the same values `backoff()` used to produce). If the wording literally says `backoff()` by name anywhere, update it to say `backoffPolicy` since that identifier no longer exists after Task 7. Also confirm the "agent also bundles, configures, and directly supervises a Vector process... crash-restarted with backoff otherwise, the same `backoff()` failing policies already use" sentence in "Logging and correlation" — update this `backoff()` reference too if present.

- [ ] **Step 2: Add the `CHANGELOG.md` entry**

Add, at the top (most recent first, matching this repo's existing convention):

```markdown
## 2026-08-28 — Agent reliability/readability/performance refactor

Unified `agent`'s two independently-written process supervisors (for its bundled Vector process and
for `bwfs`/`catalogsync` storage-policy supervision) into one shared implementation, fixing two bugs
the drift between them had introduced: `Stop()` no longer waits out a pending crash-backoff window
before taking effect, and a `Stop()` racing a concurrent respawn can no longer leave an unsignalled
process running. Also: `agent` now reads `policies-cache.json` once per reconcile tick instead of
three times; backoff/stability-window tuning moved out of test-mutated package globals into
per-instance config; `main.go`'s fatal startup errors are now guaranteed to reach the log file before
the process exits; and every binary in the repo now uses typed `context.Context` keys for its
app-name/debug/quiet/job-id logging values instead of raw strings.
```

- [ ] **Step 3: Commit**

```bash
git add docs/components/agent.md CHANGELOG.md
git commit -m "docs: changelog and agent.md touch-ups for the reliability refactor"
```
