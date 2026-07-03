# Agent v1 (Embedded Cert-Refresh Reconciliation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a new `agent` binary that runs a reconcile loop with one embedded policy (mTLS cert renewal via `certclient`), replacing the bare cron entry, plus a `list-policies` read-only inspection command.

**Architecture:** `agent serve` ticks on a fixed interval; each tick compares an embedded `[]Policy` against a locally persisted `Cache` (JSON, atomic write) to decide if a policy is due, execs it if so, and records the outcome (with jittered backoff on failure). `agent list-policies` reads the same cache file with no daemon/IPC required. A new `var_path` config key (in `common/config`) generalizes where this kind of runtime data lives.

**Tech Stack:** Go 1.26, `spf13/cobra` (subcommands, already a dependency), stdlib only otherwise (`encoding/json`, `os/exec`, `text/tabwriter`, `math/rand/v2`).

## Global Constraints

- No new dependencies — this plan introduces zero new `go.mod` entries.
- Follow existing `src/cmd/<binary>/` conventions exactly: `arguments.go` uses `cobra` the same way `rwfs`/`bwfs`/`certclient` do; `main.go` wires `config.ResolveConfigPath` → `config.ParseConfig` → `logging.NewLogger`, matching `catalogsync/main.go`.
- All persisted local files use the atomic write pattern already established by `catalogsync`'s `cursor.go`: write to `<path>.tmp`, then `os.Rename` over the target.
- Per `.claude/CLAUDE.md`, any new component or config key requires doc updates (`docs/components/`, `README.md`, `docs/ARCHITECTURE.md` where topology changes) before the work is considered done — this plan's final tasks cover that; don't skip it.
- Design reference: `docs/superpowers/specs/2026-07-03-agent-v1-cert-refresh-design.md`. One correction from that doc applied in this plan: `backoff()` must be computed **once**, at the moment a failure happens, and stored (`PolicyState.NextRetryAt`) rather than recomputed on every `isDue` check — recomputing it with fresh jitter on every check would make the due-ness threshold unstable (different random value each tick). This plan's `PolicyState` has a `NextRetryAt` field the design doc's sketch didn't; `isDue` and `estimatedNextRun` both read it directly instead of calling `backoff()` themselves.
- `go test ./cmd/agent/...` works throughout this plan even before `main.go` exists (Go test binaries synthesize their own `main`, they don't need the package's own `func main`) — only Tasks 5 and 7 need `go build`/`go vet` at the repo root to succeed.

---

### Task 1: `common/config` — `var_path` and `ReconcileIntervalSec` config keys, `ResolveVarDir` helper

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.VarPath string`, `Config.ReconcileIntervalSec int` (default `30`), `func ResolveVarDir(cfg *Config) (string, error)`

- [ ] **Step 1: Write the failing tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_VarPathOptional(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "", conf.VarPath)
}

func TestParseConfig_VarPathParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nvar_path=/var/lib/miniprotector\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "/var/lib/miniprotector", conf.VarPath)
}

func TestParseConfig_ReconcileIntervalSecDefaultsTo30(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 30, conf.ReconcileIntervalSec)
}

func TestParseConfig_ReconcileIntervalSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nReconcileIntervalSec=15\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 15, conf.ReconcileIntervalSec)
}

func TestResolveVarDir_ReturnsConfiguredPathWhenSet(t *testing.T) {
	got, err := ResolveVarDir(&Config{VarPath: "/var/lib/miniprotector"})
	require.NoError(t, err)
	assert.Equal(t, "/var/lib/miniprotector", got)
}

func TestResolveVarDir_DefaultsToExecutableDir(t *testing.T) {
	got, err := ResolveVarDir(&Config{})
	require.NoError(t, err)

	exePath, err := os.Executable()
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(exePath), got)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run 'VarPath|ReconcileIntervalSec|ResolveVarDir' -v`
Expected: FAIL — `conf.VarPath`/`conf.ReconcileIntervalSec` undefined, `ResolveVarDir` undefined.

- [ ] **Step 3: Implement `var_path` and `ReconcileIntervalSec` in `config.go`**

In `src/common/config/config.go`, add two fields to the `Config` struct (after `CatalogPort`):

```go
	CatalogHost                string
	CatalogPort                int
	VarPath                    string
	ReconcileIntervalSec       int
}
```

Add the default to the literal inside `ParseConfig`:

```go
	config := &Config{
		JobTimeoutSec:              30,
		CatalogSyncBatchSize:       500,
		CatalogSyncPollIntervalSec: 5,
		CatalogSyncMaxBackoffSec:   60,
		CatalogPort:                15723,
		ReconcileIntervalSec:       30,
	}
```

Add two cases to the `switch key` block, alongside `catalog_port`:

```go
		case "var_path":
			config.VarPath = value
			foundFields["var_path"] = true
		case "ReconcileIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid ReconcileIntervalSec value at line %d: %s", lineNum, value)
			}
			config.ReconcileIntervalSec = number
			foundFields["ReconcileIntervalSec"] = true
```

- [ ] **Step 4: Implement `ResolveVarDir`**

Add below `ResolveCertsDir` in the same file:

```go
// ResolveVarDir determines the directory for variable/runtime data (cache
// files, state files). Returns cfg.VarPath if set, otherwise the directory
// containing the running binary — the same fallback ResolveBaseDir uses,
// but resolved independently of MP_CONFIG_PATH, since variable data and
// config-file location are orthogonal concerns that happen to share a
// default.
func ResolveVarDir(cfg *Config) (string, error) {
	if cfg.VarPath != "" {
		return cfg.VarPath, nil
	}
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to determine executable path: %w", err)
	}
	return filepath.Dir(exePath), nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS — all tests in the package, including the pre-existing ones.

- [ ] **Step 6: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add var_path and ReconcileIntervalSec, ResolveVarDir helper"
```

---

### Task 2: `cmd/agent` — `policy.go` and `cache.go`

**Files:**
- Create: `src/cmd/agent/policy.go`
- Create: `src/cmd/agent/cache.go`
- Test: `src/cmd/agent/cache_test.go`

**Interfaces:**
- Produces: `type Policy struct{ ID, Binary string; Args []string; Interval time.Duration }`, `var policies []Policy` (one entry: `{ID: "cert-refresh", Binary: "certclient", Interval: 5*time.Minute}`), `type PolicyState struct{ LastSuccessAt, LastAttemptAt, NextRetryAt *time.Time; ConsecutiveFailures int }`, `type Cache map[string]PolicyState`, `func readCache(path string) (Cache, error)`, `func writeCache(path string, c Cache) error`

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/agent/cache_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCache_MissingFileReturnsEmptyCache(t *testing.T) {
	dir := t.TempDir()
	c, err := readCache(filepath.Join(dir, "agent-state.json"))
	require.NoError(t, err)
	assert.Empty(t, c)
}

func TestReadCache_CorruptFileReturnsEmptyCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

	c, err := readCache(path)
	require.NoError(t, err)
	assert.Empty(t, c)
}

func TestWriteCacheThenReadCache_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")

	now := time.Now().UTC().Truncate(time.Second)
	c := Cache{
		"cert-refresh": {LastSuccessAt: &now, ConsecutiveFailures: 0},
	}
	require.NoError(t, writeCache(path, c))

	got, err := readCache(path)
	require.NoError(t, err)
	require.Contains(t, got, "cert-refresh")
	assert.True(t, got["cert-refresh"].LastSuccessAt.Equal(now))
	assert.Equal(t, 0, got["cert-refresh"].ConsecutiveFailures)
}

func TestWriteCache_CreatesParentDirectoryIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "agent-state.json")

	require.NoError(t, writeCache(path, Cache{}))

	_, err := os.Stat(path)
	assert.NoError(t, err)
}

func TestWriteCache_OverwritesPreviousValueAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")

	require.NoError(t, writeCache(path, Cache{"a": {ConsecutiveFailures: 1}}))
	require.NoError(t, writeCache(path, Cache{"a": {ConsecutiveFailures: 2}}))

	got, err := readCache(path)
	require.NoError(t, err)
	assert.Equal(t, 2, got["a"].ConsecutiveFailures)

	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err), "no leftover temp file after a successful write")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: FAIL to compile — `readCache`, `writeCache`, `Cache` undefined.

- [ ] **Step 3: Implement `policy.go`**

Create `src/cmd/agent/policy.go`:

```go
package main

import "time"

// Policy is a single reconcilable unit: run Binary with Args whenever more
// than Interval has elapsed since the last successful run. v1 has exactly
// one, compiled in here — no policy is fetched over the network yet.
type Policy struct {
	ID       string
	Binary   string
	Args     []string
	Interval time.Duration
}

var policies = []Policy{
	{ID: "cert-refresh", Binary: "certclient", Interval: 5 * time.Minute},
}
```

- [ ] **Step 4: Implement `cache.go`**

Create `src/cmd/agent/cache.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PolicyState is one policy's reconciliation history, persisted as part of
// Cache. NextRetryAt is only meaningful when ConsecutiveFailures > 0 — it's
// set once when a failure happens (see run in reconcile.go) rather than
// recomputed from backoff() on every check, so the retry threshold can't
// drift between checks, or between the daemon and `list-policies`.
type PolicyState struct {
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
}

// Cache is keyed by Policy.ID and persisted as one JSON file.
type Cache map[string]PolicyState

// readCache returns an empty Cache if the file is missing or unparseable —
// every policy then looks "never run", which is the fail-safe direction:
// on any doubt, assume not yet done, never assume done.
func readCache(path string) (Cache, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Cache{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, nil
	}
	if c == nil {
		c = Cache{}
	}
	return c, nil
}

// writeCache persists c atomically: write to a temp file in the same
// directory, then rename over the target, so a crash mid-write never
// leaves a torn cache file. Creates the parent directory if it doesn't
// exist yet.
func writeCache(path string, c Cache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp cache: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename cache into place: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS — all 5 cache tests.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/policy.go src/cmd/agent/cache.go src/cmd/agent/cache_test.go
git commit -m "feat(agent): add Policy/Cache types and atomic cache read/write"
```

---

### Task 3: `cmd/agent` — `reconcile.go` (due-ness, backoff, the reconcile loop)

**Files:**
- Create: `src/cmd/agent/reconcile.go`
- Test: `src/cmd/agent/reconcile_test.go`

**Interfaces:**
- Consumes: `Policy`, `PolicyState`, `Cache`, `readCache`, `writeCache` (Task 2); `policies` (Task 2, package var — tests may temporarily reassign it)
- Produces: `type runner func(binary string, args []string) error`, `func realExec(binary string, args []string) error`, `func isDue(p Policy, s PolicyState, now time.Time) bool`, `func backoff(failures int) time.Duration` (reads package vars `backoffBase`, `backoffMax` — tests may temporarily reassign them), `func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner) error`, `func sleepOrDone(ctx context.Context, d time.Duration) bool`

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/agent/reconcile_test.go`:

```go
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeRunner struct {
	mu    sync.Mutex
	calls int
	failN int // number of subsequent calls to fail before succeeding
}

func (f *fakeRunner) run(binary string, args []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failN > 0 {
		f.failN--
		return errors.New("simulated failure")
	}
	return nil
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestIsDue_NeverRunIsDue(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	assert.True(t, isDue(p, PolicyState{}, time.Now()))
}

func TestIsDue_HealthyPolicyNotDueBeforeInterval(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	now := time.Now()
	last := now.Add(-1 * time.Minute)
	assert.False(t, isDue(p, PolicyState{LastSuccessAt: &last}, now))
}

func TestIsDue_HealthyPolicyDueAfterInterval(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	now := time.Now()
	last := now.Add(-6 * time.Minute)
	assert.True(t, isDue(p, PolicyState{LastSuccessAt: &last}, now))
}

func TestIsDue_FailingPolicyIgnoresIntervalUsesNextRetryAt(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	now := time.Now()
	last := now.Add(-1 * time.Minute)      // within Interval — would be "not due" if healthy
	retryAt := now.Add(-1 * time.Second)   // but the retry threshold already passed
	state := PolicyState{LastSuccessAt: &last, ConsecutiveFailures: 1, NextRetryAt: &retryAt}
	assert.True(t, isDue(p, state, now))
}

func TestIsDue_FailingPolicyNotDueBeforeNextRetryAt(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	now := time.Now()
	retryAt := now.Add(1 * time.Minute)
	state := PolicyState{ConsecutiveFailures: 1, NextRetryAt: &retryAt}
	assert.False(t, isDue(p, state, now))
}

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

func TestRun_ExecutesDuePolicyAndDoesNotRetriggerWithinInterval(t *testing.T) {
	origPolicies := policies
	policies = []Policy{{ID: "test-policy", Binary: "true", Interval: time.Hour}}
	defer func() { policies = origPolicies }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	fr := &fakeRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run)
	require.NoError(t, err)

	assert.Equal(t, 1, fr.callCount(), "a healthy 1-hour-interval policy must not re-trigger within the test window")

	cache, err := readCache(cachePath)
	require.NoError(t, err)
	state := cache["test-policy"]
	require.NotNil(t, state.LastSuccessAt)
	assert.Equal(t, 0, state.ConsecutiveFailures)
}

func TestRun_FailedExecutionRecordsFailureAndRetriesAfterBackoff(t *testing.T) {
	origPolicies := policies
	policies = []Policy{{ID: "test-policy", Binary: "false", Interval: time.Hour}}
	defer func() { policies = origPolicies }()

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 20*time.Millisecond, 50*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	fr := &fakeRunner{failN: 1} // fails once, then succeeds
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr.run)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, fr.callCount(), 2, "must retry after the backoff window elapses")

	cache, err := readCache(cachePath)
	require.NoError(t, err)
	state := cache["test-policy"]
	assert.Equal(t, 0, state.ConsecutiveFailures, "resets to 0 after the eventual success")
	require.NotNil(t, state.LastSuccessAt)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: FAIL to compile — `runner`, `isDue`, `backoff`, `backoffBase`, `backoffMax`, `run` undefined.

- [ ] **Step 3: Implement `reconcile.go`**

Create `src/cmd/agent/reconcile.go`:

```go
package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"os/exec"
	"time"
)

// backoffBase and backoffMax are vars (not consts) so tests can shrink them
// temporarily instead of waiting out real multi-minute backoff windows.
var (
	backoffBase = 30 * time.Second
	backoffMax  = 10 * time.Minute
)

// runner executes a policy's binary; production code uses realExec, tests
// substitute a fake so they don't actually invoke certclient.
type runner func(binary string, args []string) error

func realExec(binary string, args []string) error {
	return exec.Command(binary, args...).Run()
}

// isDue reports whether p should run now, given its last recorded state.
// A healthy policy (no consecutive failures) is due strictly on its own
// Interval. A failing policy is due once NextRetryAt has passed instead —
// decoupled from Interval, so a persistent failure doesn't get retried on
// every tick, and doesn't wait a full Interval either.
func isDue(p Policy, s PolicyState, now time.Time) bool {
	if s.ConsecutiveFailures == 0 {
		if s.LastSuccessAt == nil {
			return true // never succeeded, run immediately
		}
		return !now.Before(s.LastSuccessAt.Add(p.Interval))
	}
	return s.NextRetryAt == nil || !now.Before(*s.NextRetryAt)
}

// backoff returns a jittered retry delay for the given number of
// consecutive failures. It must be called exactly once per failure and the
// result stored (see run, PolicyState.NextRetryAt) rather than recomputed
// on every isDue check — recomputing it would redraw the jitter each time
// and make the due-ness threshold unstable.
func backoff(failures int) time.Duration {
	exp := min(max(failures-1, 0), 8)
	d := backoffBase * time.Duration(1<<exp)
	if d > backoffMax {
		d = backoffMax
	}
	// half jitter: never near-zero, still spreads retries across a fleet
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// run polls the embedded policy list every reconcileInterval, executing
// and recording the outcome of any policy isDue reports as due. It runs
// until ctx is cancelled, at which point it returns nil.
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		now := time.Now()
		changed := false
		for _, p := range policies {
			state := cache[p.ID]
			if !isDue(p, state, now) {
				continue
			}

			attemptErr := execute(p.Binary, p.Args)
			attemptTime := now
			state.LastAttemptAt = &attemptTime

			if attemptErr == nil {
				successTime := now
				state.LastSuccessAt = &successTime
				state.ConsecutiveFailures = 0
				state.NextRetryAt = nil
			} else {
				state.ConsecutiveFailures++
				retryAt := now.Add(backoff(state.ConsecutiveFailures))
				state.NextRetryAt = &retryAt
				logger.Error("policy execution failed", "policy", p.ID, "error", attemptErr)
			}

			cache[p.ID] = state
			changed = true
		}

		if changed {
			if err := writeCache(cachePath, cache); err != nil {
				logger.Error("failed to persist cache", "error", err)
			}
		}

		if !sleepOrDone(ctx, reconcileInterval) {
			return nil
		}
	}
}

// sleepOrDone sleeps for d, or returns false immediately if ctx is
// cancelled first.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS — all tests, including Task 2's cache tests and Task 3's new tests.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go
git commit -m "feat(agent): add reconcile loop with jittered-backoff retry"
```

---

### Task 4: `cmd/agent` — `list.go` (read-only policy state rendering)

**Files:**
- Create: `src/cmd/agent/list.go`
- Test: `src/cmd/agent/list_test.go`

**Interfaces:**
- Consumes: `Policy`, `PolicyState`, `readCache`, `writeCache`, `policies` (Task 2)
- Produces: `func estimatedNextRun(p Policy, s PolicyState) time.Time`, `func renderPolicies(w io.Writer, cachePath string, now time.Time) error`

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/agent/list_test.go`:

```go
package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimatedNextRun_NeverRunReturnsZeroValue(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	got := estimatedNextRun(p, PolicyState{})
	assert.True(t, got.IsZero())
}

func TestEstimatedNextRun_HealthyUsesLastSuccessPlusInterval(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	last := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	got := estimatedNextRun(p, PolicyState{LastSuccessAt: &last})
	assert.Equal(t, last.Add(5*time.Minute), got)
}

func TestEstimatedNextRun_FailingUsesStoredNextRetryAt(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	retryAt := time.Date(2026, 7, 3, 12, 5, 0, 0, time.UTC)
	got := estimatedNextRun(p, PolicyState{ConsecutiveFailures: 2, NextRetryAt: &retryAt})
	assert.Equal(t, retryAt, got)
}

func TestRenderPolicies_MissingCacheShowsNeverRunAndDueNow(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, time.Now()))

	out := buf.String()
	assert.Contains(t, out, "cert-refresh")
	assert.Contains(t, out, "never run")
	assert.Contains(t, out, "due now")
}

func TestRenderPolicies_HealthyPolicyShowsOkAndNotNeverRun(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"cert-refresh": {LastSuccessAt: &now},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now))

	out := buf.String()
	assert.Contains(t, out, "ok")
	assert.NotContains(t, out, "never run")
}

func TestRenderPolicies_FailingPolicyShowsRetryingWithCount(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	now := time.Now()
	retryAt := now.Add(time.Minute)
	require.NoError(t, writeCache(cachePath, Cache{
		"cert-refresh": {LastAttemptAt: &now, ConsecutiveFailures: 3, NextRetryAt: &retryAt},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now))

	assert.Contains(t, buf.String(), "retrying (3 failures)")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: FAIL to compile — `estimatedNextRun`, `renderPolicies` undefined.

- [ ] **Step 3: Implement `list.go`**

Create `src/cmd/agent/list.go`:

```go
package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// estimatedNextRun mirrors isDue's own comparisons exactly (see
// reconcile.go) so this display can never disagree with what the daemon
// would actually do. Returns the zero time.Time for "due now".
func estimatedNextRun(p Policy, s PolicyState) time.Time {
	if s.ConsecutiveFailures == 0 {
		if s.LastSuccessAt == nil {
			return time.Time{}
		}
		return s.LastSuccessAt.Add(p.Interval)
	}
	if s.NextRetryAt == nil {
		return time.Time{}
	}
	return *s.NextRetryAt
}

func health(s PolicyState) string {
	if s.LastSuccessAt == nil && s.LastAttemptAt == nil {
		return "never run"
	}
	if s.ConsecutiveFailures > 0 {
		return fmt.Sprintf("retrying (%d failures)", s.ConsecutiveFailures)
	}
	return "ok"
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// formatNextRun renders a due-now policy (zero value, or already past) as
// "due now" instead of a stale-looking timestamp.
func formatNextRun(t time.Time, now time.Time) string {
	if t.IsZero() || !t.After(now) {
		return "due now"
	}
	return t.Format("2006-01-02 15:04:05")
}

// renderPolicies reads cachePath and writes a table of every embedded
// policy's reconciliation state to w. It never executes a policy — purely
// a read-only view of what `agent serve` last recorded.
func renderPolicies(w io.Writer, cachePath string, now time.Time) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "POLICY\tSTATE\tLAST SUCCESS\tLAST ATTEMPT\tFAILURES\tNEXT RUN")
	for _, p := range policies {
		s := cache[p.ID]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n",
			p.ID,
			health(s),
			formatTime(s.LastSuccessAt),
			formatTime(s.LastAttemptAt),
			s.ConsecutiveFailures,
			formatNextRun(estimatedNextRun(p, s), now),
		)
	}
	return tw.Flush()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS — all tests from Tasks 2, 3, and 4.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/list.go src/cmd/agent/list_test.go
git commit -m "feat(agent): add read-only list-policies table rendering"
```

---

### Task 5: `cmd/agent` — `arguments.go`, `main.go`, and the `agent` Makefile target

**Files:**
- Create: `src/cmd/agent/arguments.go`
- Create: `src/cmd/agent/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `run` (Task 3), `renderPolicies` (Task 4), `realExec` (Task 3), `config.ResolveConfigPath`, `config.ParseConfig`, `config.ResolveVarDir`, `config.ContextKey` (Task 1 / existing `common/config`), `logging.NewLogger` (existing `common/logging`)
- Produces: the buildable `agent` binary; no new exported functions consumed elsewhere.

This task has no new unit tests of its own — `arguments.go`/`main.go` are argument-parsing and
wiring code, matching every other `cmd/<binary>` in this repo (none of which have an
`arguments_test.go`). Verification is a manual smoke test (Step 5) plus the full suite passing.

- [ ] **Step 1: Implement `arguments.go`**

Create `src/cmd/agent/arguments.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "serve" | "list-policies"
	Debug  bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "agent <command>",
		Short: "Node agent: reconciles local state against embedded policies",
	}

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the reconcile loop",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "serve" },
	}
	serveCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	listCmd := &cobra.Command{
		Use:   "list-policies",
		Short: "Show configured policies and their reconciliation state",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "list-policies" },
	}

	rootCmd.AddCommand(serveCmd, listCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: serve, list-policies")
	}

	return args, nil
}
```

- [ ] **Step 2: Implement `main.go`**

Create `src/cmd/agent/main.go`:

```go
// agent is a node-level process that reconciles local state against a
// small set of policies compiled into the binary. v1 has exactly one:
// renew this node's mTLS identity via certclient on a fixed interval.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
	const appName = "agent"

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
	if err := os.MkdirAll(varDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create var directory %s: %v\n", varDir, err)
		os.Exit(1)
	}
	cachePath := filepath.Join(varDir, "agent-state.json")

	switch arguments.Action {
	case "serve":
		ctx := context.WithValue(context.Background(), "appName", appName)
		ctx = context.WithValue(ctx, config.ContextKey, conf)
		ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
		ctx = context.WithValue(ctx, "quietMode", false)

		logger, logfile := logging.NewLogger(ctx)
		defer logfile.Close()

		reconcileInterval := time.Duration(conf.ReconcileIntervalSec) * time.Second
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath)
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec); err != nil {
			logger.Error("agent exited with error", "error", err)
			os.Exit(1)
		}

	case "list-policies":
		if err := renderPolicies(os.Stdout, cachePath, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
	}
}
```

- [ ] **Step 3: Add the `agent` Makefile target**

In `Makefile`, add a new variable next to the other `*_CMD` definitions (after `CATALOG_CMD := cmd/catalog`):

```makefile
CATALOG_CMD := cmd/catalog
AGENT_CMD := cmd/agent
```

Add `agent` to the `.PHONY` line:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certrequest certclient catalogsync catalog agent test test-e2e lint control-plane-up
```

Add a new target after the `catalog:` target block (before `test:`):

```makefile
agent: $(BINARY_DIR) ## Build agent binary
	@printf "$(BLUE)Building agent...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/agent ./$(AGENT_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/agent"
```

- [ ] **Step 4: Run the full test suite and vet**

Run: `cd src && go build ./... && go vet ./... && go test ./...`
Expected: all PASS, no build/vet errors. This is the first point in the plan where `cmd/agent` compiles as a full binary (Tasks 2–4 only needed `go test`, not `go build`).

- [ ] **Step 5: Manual smoke test**

```bash
cd /home/alex/miniprotector && make agent
MP_CONFIG_PATH=$(mktemp -d) bash -c '
  echo "default_port=8080
default_streams=4
logfolder=/tmp" > $MP_CONFIG_PATH/local.conf
  ./bin/agent list-policies
'
```

Expected output: a table with one row, `cert-refresh`, state `never run`, `-` for both timestamps, `0` failures, `due now`.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/arguments.go src/cmd/agent/main.go Makefile
git commit -m "feat(agent): wire serve/list-policies subcommands and add build target"
```

---

### Task 6: Documentation

**Files:**
- Create: `docs/components/agent.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Create `docs/components/agent.md`**

```markdown
# agent

Node-level agent that reconciles local state against a small set of policies compiled into the
binary. **v1** has exactly one embedded policy — renew this node's mTLS identity via `certclient`
on a fixed interval — replacing the bare cron entry used previously. `agent` does not fetch
policies over the network yet; see the
[design doc](../superpowers/specs/2026-07-03-agent-v1-cert-refresh-design.md) for how this grows
into policy-server-fetched and queue-dispatched work in later iterations.

## Usage

```bash
# Run the reconcile loop (long-lived)
agent serve

# Inspect policy state without running anything
agent list-policies
```

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` (serve only) | false | Enable debug logging |

## Behavior

`agent serve` ticks every `ReconcileIntervalSec` seconds. On each tick, for every embedded policy
it checks whether the policy is due — a healthy policy is due once its own `Interval` has elapsed
since the last success; a policy that's currently failing is due once a jittered backoff period
(computed once per failure, not re-derived on every check) has elapsed instead, decoupled from
`Interval`. When due, `agent` execs the policy's binary (`certclient`, with no arguments, for the
embedded `cert-refresh` policy) and records the outcome — success or failure, and a running count
of consecutive failures — to a local JSON cache file.

`agent list-policies` reads that same cache file and prints each policy's health and estimated
next run time, without executing anything or requiring a running `agent serve` process:

```
POLICY         STATE               LAST SUCCESS         LAST ATTEMPT         FAILURES  NEXT RUN
cert-refresh   ok                  2026-07-03 14:32:10  2026-07-03 14:32:10  0         2026-07-03 14:37:10
```

The cache file lives at `<var_dir>/agent-state.json`, where `<var_dir>` is `var_path` from
`local.conf` if set, otherwise the directory containing the running binary (see `common/config`).
A missing or corrupt cache is treated as empty — every policy then looks "never run" and executes
on the next tick, the same fail-safe direction used everywhere else in this component.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `var_path` | binary's own directory | Directory for runtime/variable data (the cache file) |
| `ReconcileIntervalSec` | 30 | How often `agent serve` checks whether any policy is due |

## Building

```bash
make agent
```

## See Also

- [certclient](./certclient.md) — the binary `agent`'s embedded `cert-refresh` policy execs
- [Architecture](../ARCHITECTURE.md)
- [Design: Agent v1](../superpowers/specs/2026-07-03-agent-v1-cert-refresh-design.md)
```

- [ ] **Step 2: Update `docs/ARCHITECTURE.md`**

In the `## Components` table at the top, add a row after the `catalog` row:

```markdown
| catalog | Backup Catalog — receives catalogsync's replicated file_versions over gRPC | Implemented |
| agent | Node Agent — reconciles local state against embedded policies | Implemented (v1: cert renewal only) |
```

In the `## Control Plane vs. Agents` table, change the `Agents` column's `Components` cell from:

```markdown
| Components | `deploy/control-plane/ca/` (step-ca container), `certrequest`, `catalog` | `bwfs`, `brfs`, `rwfs`, `certclient` |
```

to:

```markdown
| Components | `deploy/control-plane/ca/` (step-ca container), `certrequest`, `catalog` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
```

Add a short paragraph after the table (in the same place the existing `catalog` explanatory
paragraph lives), right before `## Backup Process`:

```markdown
`agent` is a node-level process that wraps `certclient` — instead of a bare cron entry invoking
`certclient` directly, `agent serve` runs a reconcile loop that periodically execs `certclient`
and tracks the outcome in a local cache (`agent list-policies` inspects it). It has no network
role of its own in v1; all network behavior is `certclient`'s, unchanged.
```

- [ ] **Step 3: Update `README.md`**

In the `## Components` list, add a bullet right after the `certclient` bullet:

```markdown
- **[certclient](docs/components/certclient.md)** - Bootstraps or renews a node's mTLS identity from the CA
- **[agent](docs/components/agent.md)** - Node agent — reconciles local state against embedded policies (v1: mTLS certificate renewal via `certclient`)
```

- [ ] **Step 4: Add a `CHANGELOG.md` entry**

Insert a new entry at the top, immediately after the intro paragraph and before the existing
`## 2026-07-03 — Backup catalog service (catalog)` entry:

```markdown
## 2026-07-03 — Node agent v1 (embedded cert-refresh reconciliation)

Added `agent`, a node-level process that replaces the bare cron entry for `certclient` with a
small reconcile loop: on a configurable interval it checks whether the (currently single,
compiled-in) `cert-refresh` policy is due, execs `certclient` if so, and records the outcome to a
local JSON cache — failures back off with jittered delays instead of retrying every tick. `agent
list-policies` reads that same cache to show each policy's health and estimated next run without
needing a running daemon. Also added `var_path` to `common/config`, a general directory for this
kind of runtime/variable data, defaulting to the running binary's own directory when unset. This
is the first concrete slice of a broader `agent` design that will later add queue-dispatched and
policy-server-fetched work on top of the same reconcile primitives.
```

- [ ] **Step 5: Commit**

```bash
git add docs/components/agent.md docs/ARCHITECTURE.md README.md CHANGELOG.md
git commit -m "docs: document agent v1, var_path, and ReconcileIntervalSec"
```

---

### Task 7: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Full build**

Run: `make build`
Expected: all binaries, including `agent`, build successfully into `bin/`.

- [ ] **Step 2: Full test suite**

Run: `make test`
Expected: PASS, no failures, including every test added in Tasks 1–4.

- [ ] **Step 3: Vet**

Run: `make lint`
Expected: no output, clean exit.

- [ ] **Step 4: Confirm no stray files**

Run: `git status`
Expected: working tree clean (everything from Tasks 1–6 already committed).
