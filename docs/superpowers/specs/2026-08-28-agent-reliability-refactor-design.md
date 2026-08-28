# Design: `agent` reliability/readability/performance refactor

**Date:** 2026-08-28
**Status:** Approved for planning

## Problem

`src/cmd/agent` has grown across many small feature-driven changes (backup tasks, storage
supervision, restore verification/execution, Vector shipping) without a pass to consolidate what
those additions have in common. A full read of the package (`main.go`, `reconcile.go`, `backup.go`,
`storage.go`, `restore.go`, `policy.go`, `cache.go`, `list.go`, `vector.go`) surfaced five concrete
issues, two of which are latent bugs rather than pure style:

1. **`storageSupervisor` (storage.go) and `vectorSupervisor` (vector.go) are near-identical,
   independently-written process supervisors that have drifted.** Comparing them line-by-line finds
   two real bugs in `vectorSupervisor` that `storageSupervisor` already fixed for its own case but
   the fix was never ported over:
   - `vectorSupervisor.Stop()`, called while `superviseLoop` is sitting in its crash-backoff
     `time.After(backoff(failures))` wait, does not take effect until that wait elapses (up to
     `backoffMax`, 10 minutes) — because `vectorSupervisor`'s backoff `select` only watches
     `ctx.Done()`, not a dedicated stop signal. `storageSupervisor` has exactly this problem solved
     already via its `stopCh`/`stopOnce`.
   - `vectorSupervisor.spawnAndWait` is missing the shutting-down recheck `storageSupervisor.
     spawnAndWait` performs immediately after `cmd.Start()` (still under the mutex). Without it, a
     `Stop()` that lands in the narrow window between another goroutine's `cmd.Start()` returning and
     `v.cmd` being assigned can leave a freshly-spawned Vector process running with nothing left to
     signal it — a real, if narrow-probability, process leak for the rest of the node's uptime.

2. **`policies-cache.json` is read from disk and JSON-unmarshaled independently, up to three times,
   every reconcile tick** — once each inside `backupTasks`, `storageTasks`, and `restoreTasks`. The
   absolute cost is negligible (a small local file, read every `ReconcileIntervalSec`, default 30s),
   but it's three near-identical "read the file, bail out on any error" blocks doing the same I/O,
   and nothing guarantees the three reads observe the same snapshot within one tick.

3. **`main.go` stores request-scoped values in `context.Context` under raw string keys**
   (`"appName"`, `"debugMode"`, `"quietMode"`) instead of a typed key type — collision-prone and
   without compile-time safety, and inconsistent with idiomatic Go context-key usage. This turns out
   not to be `agent`-specific: `common/logging.NewLogger` (used by every `cmd/*` binary) reads these
   same string keys, plus a fourth (`"jobId"`), and all 14 binaries' `main.go` files set them the
   same string-keyed way — `agent` is one of fourteen identical copies of this pattern, not an
   outlier. Fixing it for `agent` alone isn't possible without breaking `agent`'s own logging (the
   shared `NewLogger` would still read the old string keys), so this item is necessarily repo-wide.

4. **Several `os.Exit(1)` calls inside `main.go`'s `serve` setup path run after the logfile has
   already been opened (and its `Close()` deferred), but `os.Exit` does not run deferred functions.**
   A fatal error during certs-dir resolution, Vector binary resolution, hostname resolution, or
   Vector config render/write is logged and then the process exits without ever flushing/closing
   that log — so the very message explaining the failure can be lost.

5. **Backoff/stability tuning lives in mutable package-level `var`s** (`backoffBase`, `backoffMax` in
   `reconcile.go`; `storageStabilityWindow` in `storage.go`) that tests mutate in place for the
   duration of one test function, then restore via `defer`. This works today only because this
   package's tests never run in parallel; it's shared mutable state with no compile-time signal
   protecting a future test from breaking it by adding `t.Parallel()`.

## Scope

Five independent, sequentially-landed changes. PR0 is repo-wide (`common/logging` plus all 14
`cmd/*/main.go` files); PR1 through PR4 touch `src/cmd/agent` only. No CLI, config-key,
cache-file-format, or `list-policies` output changes, except the two Vector behavior fixes named
above (folded into PR3, called out explicitly in that PR's `CHANGELOG.md` entry).

### PR0 — Typed context keys (repo-wide)

- In `common/logging/logging.go`, introduce an unexported `type ctxKey int` with four named
  constants (`appNameKey`, `debugModeKey`, `quietModeKey`, `jobIDKey`) and four exported
  constructors — `WithAppName`, `WithDebugMode`, `WithQuietMode`, `WithJobID` (each
  `func(context.Context, T) context.Context`) — mirroring the shape `common/config.ContextKey`
  already established for `*config.Config`. `NewLogger` reads all four via the unexported keys
  instead of `ctx.Value("appName")`/`"debugMode"`/`"quietMode"`/`"jobId"`.
- Every one of the 14 `cmd/*/main.go` files that currently does
  `ctx = context.WithValue(ctx, "appName", appName)` (and the matching `debugMode`/`quietMode`, plus
  `jobId` in the four binaries that set it: `brfs`, `certclient`, `policyclient`, `rwfs`) switches to
  the matching `logging.With...` call. Purely mechanical, three-or-four-line diff per file, no
  behavior change. `agent`'s own `main.go` is one of these 14 — this is the only piece of PR0 that
  touches `cmd/agent` at all.
- `common/logging/logging_test.go` — the only test file referencing the string keys — updated to
  build its contexts via the new constructors.

### PR1 — `main.go` cleanup (`agent`-only)

- Extract the body of the `"serve"` case into a function (e.g. `serve(conf *config.Config,
  arguments *Arguments, varDir, cachePath, policiesCachePath string) int`) that owns the logfile and
  every other deferred cleanup, returning an exit code instead of calling `os.Exit` internally.
  `main()` becomes `os.Exit(run(...))`-shaped: a single exit point at the very end, after every
  defer in the call chain has had a chance to run. `"list-policies"` gets the same treatment for
  consistency, though it has no defers to protect today.

### PR2 — De-globalize backoff tuning

- Introduce:
  ```go
  type backoffPolicy struct {
      Base, Max time.Duration
  }
  func (b backoffPolicy) next(failures int) time.Duration { ... } // body unchanged from today's backoff()
  ```
  with a package-level `defaultBackoffPolicy = backoffPolicy{Base: 30 * time.Second, Max: 10 * time.Minute}`
  used by production constructors.
- `reconcileState` gains a `backoff backoffPolicy` field, set by its constructor (defaulting to
  `defaultBackoffPolicy`); `recordOutcome` calls `rs.backoff.next(...)` instead of the free function.
- Every test currently doing:
  ```go
  origBase, origMax := backoffBase, backoffMax
  backoffBase, backoffMax = 20*time.Millisecond, 50*time.Millisecond
  defer func() { backoffBase, backoffMax = origBase, origMax }()
  ```
  instead constructs its `reconcileState` (or, after PR3, its supervisor) with an explicit
  `backoffPolicy{Base: 20 * time.Millisecond, Max: 50 * time.Millisecond}` — no shared state, no
  restore-on-defer needed. This touches `reconcile_test.go`, `storage_test.go`, `vector_test.go`,
  and `integration_test.go` (every call site listed under "Backoff vars" during design review).

### PR3 — Unify `storageSupervisor` and `vectorSupervisor`

- New `supervisor.go` replacing the supervisor portions of `storage.go` and `vector.go` with one
  `processSupervisor` type:
  ```go
  type supervisorConfig struct {
      Binary          string
      Args            []string
      Logger          *slog.Logger
      Backoff         backoffPolicy
      StabilityWindow time.Duration // 0 disables stability-gated success reporting
      OnOutcome       func(error)   // nil disables outcome reporting entirely
      Stdout, Stderr  io.Writer     // default os.Stdout/os.Stderr when nil
  }
  ```
  with `Start(ctx)`, `Stop()`, and `TriggerRestart()` (always present; only Vector's caller invokes
  it) methods, and the `stopCh`/`stopOnce` + post-`Start()` shutting-down recheck folded in
  unconditionally — both supervisors get both fixes, not just storage.
- `storage.go` keeps `storageTask`, `storageTaskID`/`catalogsyncTaskID`, `storageConfig`, and
  `storageTasks()`. `storageManager` gains a `stabilityWindow time.Duration` field, set by its
  constructor to a package-level `defaultStorageStabilityWindow = 3 * time.Second` in production;
  `storageStabilityWindow` as a mutable package var disappears entirely — tests construct
  `storageManager` with an explicit small window instead of mutating a global. `storageManager.
  reconcile` passes that field as `StabilityWindow` into each `processSupervisor`'s config, along
  with `OnOutcome:` wired to `reconcileState.recordOutcome` and default `Stdout`/`Stderr`.
- `vector.go` keeps binary resolution, config templating, and `hostnameFromBootstrapCert`, but its
  supervisor section shrinks to constructing one `processSupervisor` with `StabilityWindow: 0`,
  `OnOutcome: nil`, and `Stdout`/`Stderr` set to the rotating `lumberjack.Logger` it already builds.
  `main.go`'s `vectorSup.TriggerRestart()` call on a successful `operating-refresh` is unchanged.
- New regression tests (in whichever test file ends up owning `processSupervisor` — likely a new
  `supervisor_test.go`) covering the two fixed races directly, using the existing
  `onSpawnForTest` hook pattern: (a) `Stop()` called during a backoff wait returns promptly rather
  than waiting out the backoff; (b) a `Stop()` racing a concurrent spawn never leaves a process
  running past the test.

### PR4 — Single `policies-cache.json` read per reconcile tick

- `readCachedPolicies(path) ([]cachedPolicy, bool)` stays exactly as-is (still the one place that
  touches the filesystem for this file).
- `backupTasks`, `storageTasks`, `restoreTasks` change signature from `(policiesCachePath string,
  ...) ([]Policy, bool)` to `(cachedPolicies []cachedPolicy, ...) []Policy` — they become pure
  functions over already-loaded data; the `ok bool` collapses away since a failed read is now
  handled once, by the caller, before any of the three is invoked.
- `main.go`'s `policiesFunc`/`storageTasksFunc` closures collapse into one function that calls
  `readCachedPolicies` once per tick and, only on success, calls all three derivers against the same
  slice; on failure, all three consumers see "no update this tick" (via the existing `ok` mechanism
  at that single call site) exactly as today.
- Test call sites in `backup_test.go`, `storage_test.go`, `restore_test.go` (`restore_test.go` and
  `rwfs_exec_test.go` both call `restoreTasks`), `reconcile_test.go`, and `integration_test.go` change
  from `backupTasks(path, logger, conf)` to `backupTasks(readCachedPoliciesOrFatal(t, path), logger,
  conf)`-shaped calls, or equivalently inline the two-line read-then-call — exact helper shape is an
  implementation detail for the plan/execution step, not fixed here.

## Out of scope

- Any change to `isDue`/backoff *semantics*, the cache file's on-disk JSON shape, `list-policies`
  output formatting, or any config key.
- Caching parsed `cron.Schedule`s across ticks — evaluated during design and explicitly deferred:
  the per-tick cost (parsing a handful of short cron strings) is noise next to PR4's win, and a
  correct cache would need its own invalidation story keyed off `policies-cache.json` changing.
- Any behavior change to `bwfs`, `catalogsync`, or Vector itself — only `agent`'s supervision of
  them.

## Testing plan

- PR0: `common/logging/logging_test.go` updated to use the new constructors and confirms the
  round-trip (app name/debug/quiet/job-id all still land in emitted log records correctly); full
  `go build ./...` plus each touched binary's own test suite run once, since 14 `main.go` files
  change — a compile failure in any one would be caught immediately, and none of their behavior
  is otherwise affected.
- PR1: existing `main`-adjacent coverage (if any) continues to pass.
- PR2: `reconcile_test.go` backoff-related tests updated to construct explicit `backoffPolicy`
  values; no behavior change, so existing assertions are unchanged, only the setup/teardown shape.
- PR3: `storage_test.go` and `vector_test.go`'s existing supervisor tests move to exercise
  `processSupervisor` (via `storageManager`/vector's construction path respectively) with the same
  assertions; two new tests added for the previously-Vector-only-missing fixes, written generically
  against `processSupervisor` so both callers are covered by construction.
- PR4: `backup_test.go`/`storage_test.go`/`restore_test.go`/`rwfs_exec_test.go` updated to read-then-
  call; `reconcile_test.go`/`integration_test.go`'s end-to-end tests confirm the combined single-read
  wiring in `main.go` still prunes/derives tasks identically to before.
- Full `go test ./src/cmd/agent/...` run at the end of each PR before moving to the next.

## Documentation

- `CHANGELOG.md`: one entry per PR (this repo's convention — dated heading, short paragraph). PR0's
  entry names it as an internal, behavior-preserving cleanup spanning all binaries (no per-component
  doc changes needed — context keys aren't user-facing behavior). PR3's entry explicitly names
  the two Vector behavior fixes (prompt shutdown during backoff; no more possible
  unsignalled-process leak on a `Stop()`/spawn race).
- `docs/components/agent.md`: no behavioral prose changes expected (nothing in scope changes
  documented behavior other than the two Vector fixes, which are about failure-path timing, not
  anything currently documented there) — reviewed at the end of PR3 to confirm.
- `storage.go`'s existing doc-comment cross-reference to "vector.go's vectorSupervisor" (the
  rationale for storage's supervisor not needing `TriggerRestart`) updated to point at the unified
  `processSupervisor` in PR3, since the type it currently names goes away.
