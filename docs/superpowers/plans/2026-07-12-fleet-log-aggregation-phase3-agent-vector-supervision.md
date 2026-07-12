# Fleet Log Aggregation Phase 3: Agent-Supervised Vector Shipping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire everything the first two phases built together: `agent` bundles, configures, and directly supervises a Vector process that tails the standardized log directory and ships to `log-gateway`, over mTLS using the node's own operating certificate — restarted immediately after every successful `operating-refresh` and crash-restarted with backoff otherwise. This is the final phase against `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`; after it, a log line from any agent-managed node is visible in Loki, queryable by hostname, end to end.

**Architecture:** A new `vectorSupervisor` type in `cmd/agent` owns a long-running Vector child process — a different lifecycle from the due-and-complete `Policy` model `agent` already has, so it's handled by its own small supervision loop rather than shoehorned into `reconcile.go`'s existing due/execute/record cycle. `run()` gains a minimal hook (`onSuccess func(policyID string)`) so the reconcile loop can tell the supervisor "a fresh operating cert just landed" without the supervisor needing to know anything about policies, and without `reconcile.go` needing to know anything about Vector.

**Tech Stack:** Go (`os/exec` process supervision, `text/template` for Vector config generation), Vector (`timberio/vector`, official Docker image, bundled binary — no Go build, a third-party Rust binary copied into each agent-bundled image).

## Global Constraints

- Assumes Phase 1 (`docs/superpowers/plans/2026-07-11-fleet-log-aggregation-phase1-logging-correlation.md`) and Phase 2 (`docs/superpowers/plans/2026-07-12-fleet-log-aggregation-phase2-log-gateway-loki.md`) are already merged: `log_dir` (not `logfolder`), `common/jobid`, and `log_gateway_host`/`log_gateway_port` all already exist by the time this plan's tasks run.
- The Vector binary is resolved exactly once, colocated with `agent`'s own executable (same directory `certclient`/`policyclient`/`brfs` already live in) — **no `$PATH` fallback**, unlike `realExec`'s existing behavior for those three. If the colocated binary is missing, `agent serve` fails loudly at startup rather than risking a silently mismatched host-installed Vector.
- Vector's generated config and its own on-disk data (positions, disk buffer) live under `var_dir` (`config.ResolveVarDir`), never the binary's own install directory — the same distinction this project already draws between "where the executable lives" and "where runtime-generated state lives."
- `agent`'s own network posture stays outbound-only: Vector's `api.enabled` is never set (left at its default `false`), so the supervised Vector process opens no listening socket, matching the same invariant every other part of `agent`'s own footprint already holds.
- Restarting Vector after a successful `operating-refresh` is **not** backed off (it's an expected, roughly-15-minute-interval event, not a failure); only a genuine unexpected exit goes through the existing jittered `backoff()` this package already has for failing policies.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/cmd/agent/vector.go` (new), `vector_test.go` (new) | Colocated-binary resolution (no `$PATH` fallback), Vector config template rendering, `vectorSupervisor` (start/restart/crash-restart-with-backoff/stop) |
| `src/cmd/agent/reconcile.go` (modify), `reconcile_test.go` (modify) | `run()` gains an `onSuccess func(policyID string)` hook, invoked after every successful exec |
| `src/cmd/agent/main.go` (modify) | Wire Vector resolution, config generation, supervisor start/stop, and the `onSuccess` hook into `serve` |
| `deploy/control-plane/{catalog,policy-server,log-gateway}/Dockerfile`, `demo/backup-host/Dockerfile` (modify) | Bundle the Vector binary via a multi-stage `COPY --from=timberio/vector:...` |
| `deploy/control-plane/{catalog,policy-server,log-gateway}/local.conf`, `demo/local.conf` (modify) | Set `log_gateway_host`/`log_gateway_port` |
| `demo/docker-compose.yml`, `deploy/control-plane/docker-compose.yml` (modify) | Wire `log_gateway_host` reachability between compose networks (control-plane's `log-gateway` service must be reachable from the demo compose project, or the demo gets its own `log-gateway`/`loki` pair — decided in Task 6) |
| `docs/components/agent.md`, `docs/ARCHITECTURE.md`, `docs/SECURITY.md`, `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md` (modify) | Document the completed pipeline; close out the design spec's "Not yet" framing |

---

### Task 1: `agent` — colocated Vector binary resolution

**Files:**
- Create: `src/cmd/agent/vector.go`
- Create: `src/cmd/agent/vector_test.go`

**Interfaces:**
- Produces: `resolveVectorBinary() (string, error)`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/agent/vector_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildAndRunAsOwnExecutable copies a trivial no-op script/binary to dir
// under name, then re-execs the current test binary with a temp
// GOOS-appropriate wrapper so os.Executable() resolves to a path inside
// dir -- mirrors TestRealExec_ResolvesBinaryColocatedWithOwnExecutable's
// existing technique in reconcile_test.go (same package, same trick).
func TestResolveVectorBinary_FindsColocatedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("colocated-binary resolution test assumes a POSIX layout")
	}
	dir := t.TempDir()
	vectorPath := filepath.Join(dir, "vector")
	require.NoError(t, os.WriteFile(vectorPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	got, err := resolveVectorBinaryIn(dir)
	require.NoError(t, err)
	assert.Equal(t, vectorPath, got)
}

func TestResolveVectorBinary_MissingBinaryFailsLoudly(t *testing.T) {
	dir := t.TempDir() // empty -- no vector binary present

	_, err := resolveVectorBinaryIn(dir)
	assert.Error(t, err, "must fail loudly, never fall back to $PATH")
}
```

(`resolveVectorBinaryIn` is the testable core, parameterized on the directory to look in, exactly the same split `realExec` doesn't have but this function benefits from — `resolveVectorBinary` itself, added in Step 3, derives that directory from `os.Executable()` and is not itself unit-testable without the same re-exec trickery `TestRealExec_ResolvesBinaryColocatedWithOwnExecutable` already uses; testing the pure, directory-parameterized core is sufficient and avoids duplicating that trickery here.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestResolveVectorBinary -v`
Expected: FAIL — `resolveVectorBinaryIn` undefined (compile error).

- [ ] **Step 3: Implement**

Create `src/cmd/agent/vector.go`:

```go
// vector.go: agent's ownership of the bundled Vector process's binary
// resolution, config generation, and supervision. See
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolveVectorBinary finds the Vector binary colocated with agent's own
// executable -- unlike realExec's resolution for certclient/policyclient/
// brfs, there is deliberately no $PATH fallback: Vector is a third-party
// tool that may already exist elsewhere on a host for an unrelated
// purpose, and silently picking up a different, unpinned version there
// would be a correctness landmine, not a convenience.
func resolveVectorBinary() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("determine own executable path: %w", err)
	}
	return resolveVectorBinaryIn(filepath.Dir(exePath))
}

// resolveVectorBinaryIn is resolveVectorBinary's testable core.
func resolveVectorBinaryIn(dir string) (string, error) {
	candidate := filepath.Join(dir, "vector")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("vector binary not found at %s (bundled alongside agent, no $PATH fallback): %w", candidate, err)
	}
	return candidate, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run TestResolveVectorBinary -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/vector.go src/cmd/agent/vector_test.go
git commit -m "feat(agent): resolve the colocated Vector binary, no \$PATH fallback"
```

---

### Task 2: `agent` — Vector config generation

**Files:**
- Modify: `src/cmd/agent/vector.go`
- Modify: `src/cmd/agent/vector_test.go`

**Interfaces:**
- Produces: `renderVectorConfig(logDir, varDir, certsDir, logGatewayHost string, logGatewayPort int) (string, error)`.

Vector's config depends entirely on that node's own `local.conf` (`log_dir`, `log_gateway_host`/`port`) and resolved paths (`certsDir`, `varDir`), all only known once `agent` has parsed its own config at startup — so it cannot be a static file baked into the image; `agent` renders it fresh at `serve` startup.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/agent/vector_test.go`:

```go
func TestRenderVectorConfig_IncludesLogDirGlob(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.Contains(t, got, `"/var/log/mp/*.log"`)
}

func TestRenderVectorConfig_PointsAtLogGatewayEndpoint(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.Contains(t, got, "https://log-gateway.internal:9400")
}

func TestRenderVectorConfig_UsesCertsDirForTLS(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.Contains(t, got, "/var/lib/mp/certs/client.crt")
	assert.Contains(t, got, "/var/lib/mp/certs/client.key")
	assert.Contains(t, got, "/var/lib/mp/certs/ca.crt")
}

func TestRenderVectorConfig_UsesVarDirForDataAndBuffer(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.Contains(t, got, "/var/lib/mp/vector-data")
}

func TestRenderVectorConfig_NeverEnablesTheHTTPAPI(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.NotContains(t, got, "api:", "must never enable Vector's own HTTP API/listener -- agent's own network footprint stays outbound-only")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestRenderVectorConfig -v`
Expected: FAIL — `renderVectorConfig` undefined (compile error).

- [ ] **Step 3: Implement**

Append to `src/cmd/agent/vector.go` (add `"bytes"`, `"text/template"` to the import block):

```go
// vectorConfigTemplate is Vector's own config format (YAML). Vector's
// `{{ binary }}` label templating syntax is escaped as a literal string so
// Go's text/template doesn't try to parse it as its own action.
const vectorConfigTemplate = `data_dir: {{ .VarDir }}/vector-data

sources:
  local_logs:
    type: file
    include:
      - "{{ .LogDir }}/*.log"

transforms:
  add_binary_label:
    type: remap
    inputs: ["local_logs"]
    source: |
      .binary = replace!(path.strip_dir!(.file), ".log", "")

sinks:
  loki_gateway:
    type: loki
    inputs: ["add_binary_label"]
    endpoint: "https://{{ .LogGatewayHost }}:{{ .LogGatewayPort }}"
    encoding:
      codec: json
    labels:
      binary: "{{"{{ binary }}"}}"
    tls:
      ca_file: "{{ .CertsDir }}/ca.crt"
      crt_file: "{{ .CertsDir }}/client.crt"
      key_file: "{{ .CertsDir }}/client.key"
    buffer:
      type: disk
      max_size: 268435488
      when_full: drop_newest
`

type vectorConfigData struct {
	LogDir         string
	VarDir         string
	CertsDir       string
	LogGatewayHost string
	LogGatewayPort int
}

// renderVectorConfig builds Vector's config from this node's own resolved
// paths and local.conf values -- never a static file, since all of these
// are deployment-specific and only known after agent has parsed its own
// config.
func renderVectorConfig(logDir, varDir, certsDir, logGatewayHost string, logGatewayPort int) (string, error) {
	tmpl, err := template.New("vector-config").Parse(vectorConfigTemplate)
	if err != nil {
		return "", fmt.Errorf("parse vector config template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vectorConfigData{
		LogDir:         logDir,
		VarDir:         varDir,
		CertsDir:       certsDir,
		LogGatewayHost: logGatewayHost,
		LogGatewayPort: logGatewayPort,
	}); err != nil {
		return "", fmt.Errorf("render vector config: %w", err)
	}
	return buf.String(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run TestRenderVectorConfig -v`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Validate the rendered config against Vector itself, if available locally**

This step is optional and manual — skip it if a `vector` binary isn't available in the current environment; Task 6 verifies the real, bundled binary against this config regardless.

If `vector` is available locally, temporarily add `t.Log(got)` to `TestRenderVectorConfig_IncludesLogDirGlob`, run it, copy the logged YAML into a file, and validate it:

```bash
cd src && go test ./cmd/agent/... -run TestRenderVectorConfig_IncludesLogDirGlob -v
# copy the logged YAML output into /tmp/vector-config-check.yaml, then:
vector validate /tmp/vector-config-check.yaml
```

Expected: Vector reports the config as valid. Remove the temporary `t.Log(got)` line afterward — it must not be committed.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/vector.go src/cmd/agent/vector_test.go
git commit -m "feat(agent): render Vector's config from local.conf and resolved paths"
```

---

### Task 3: `agent` — `vectorSupervisor`

**Files:**
- Modify: `src/cmd/agent/vector.go`
- Modify: `src/cmd/agent/vector_test.go`

**Interfaces:**
- Consumes: `backoff(failures int) time.Duration` (existing, `reconcile.go`).
- Produces: `newVectorSupervisor(binary, configPath string, logger *slog.Logger) *vectorSupervisor`, `(*vectorSupervisor).Start(ctx context.Context)`, `(*vectorSupervisor).TriggerRestart()`.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/agent/vector_test.go` (add `"context"`, `"sync/atomic"`, `"time"` to the import block):

```go
func TestVectorSupervisor_StartsAndStopsCleanlyOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-vector.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"), 0o755))

	sup := newVectorSupervisor(script, "", testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	sup.Start(ctx)

	time.Sleep(100 * time.Millisecond) // let it actually spawn
	cancel()

	// Start's supervise loop must observe ctx cancellation and stop
	// respawning within a bounded window -- no direct signal to assert on
	// besides giving it time and confirming no panic/hang; the crash-
	// restart tests below cover the respawn logic itself precisely.
	time.Sleep(200 * time.Millisecond)
}

func TestVectorSupervisor_RestartsOnUnexpectedExitWithoutHangingForever(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-vector.sh")
	// exits immediately every time -- simulates a persistent crash
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Millisecond, 30*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	var spawns int64
	sup := newVectorSupervisor(script, "", testLogger())
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Start(ctx)
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond) // let the loop observe ctx.Done and stop

	assert.GreaterOrEqual(t, atomic.LoadInt64(&spawns), int64(2), "a persistently crashing process must be respawned more than once")
}

func TestVectorSupervisor_TriggerRestartDoesNotApplyBackoff(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-vector.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"), 0o755))

	// A large backoff window -- if TriggerRestart incorrectly went through
	// the crash-backoff path, the respawn would not happen within this
	// test's short assertion window.
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Second, 10*time.Second
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	var spawns int64
	sup := newVectorSupervisor(script, "", testLogger())
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

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestVectorSupervisor -v`
Expected: FAIL — `newVectorSupervisor`/`vectorSupervisor` undefined (compile error).

- [ ] **Step 3: Implement**

Append to `src/cmd/agent/vector.go` (add `"context"`, `"os/exec"`, `"sync"`, `"syscall"`, `"time"` to the import block):

```go
// vectorSupervisor owns the lifecycle of agent's bundled Vector process: a
// long-running child, not a due-and-complete Policy exec, so it gets its
// own small supervision loop rather than being shoehorned into
// reconcile.go's due/execute/record cycle. It restarts Vector immediately
// (no backoff) whenever TriggerRestart is called -- the expected,
// roughly-15-minute-interval event of a fresh operating cert landing --
// and with the same jittered backoff() reconcile.go already uses for
// failing policies whenever Vector exits unexpectedly for any other
// reason.
type vectorSupervisor struct {
	binary     string
	configPath string
	logger     *slog.Logger

	mu           sync.Mutex
	cmd          *exec.Cmd
	shuttingDown bool
	restarting   bool

	// onSpawnForTest, when non-nil, is called once per spawn attempt --
	// test-only instrumentation, never set in production.
	onSpawnForTest func()
}

func newVectorSupervisor(binary, configPath string, logger *slog.Logger) *vectorSupervisor {
	return &vectorSupervisor{binary: binary, configPath: configPath, logger: logger}
}

// Start launches the supervise loop in its own goroutine and returns
// immediately; the loop itself runs until ctx is done.
func (v *vectorSupervisor) Start(ctx context.Context) {
	go v.superviseLoop(ctx)
}

// TriggerRestart signals the currently-running Vector process to exit
// (SIGTERM) and marks the next respawn as deliberate, so the supervise
// loop skips the crash-backoff delay for it.
func (v *vectorSupervisor) TriggerRestart() {
	v.mu.Lock()
	cmd := v.cmd
	v.restarting = true
	v.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

// Stop signals the currently-running Vector process to exit and tells the
// supervise loop not to respawn it.
func (v *vectorSupervisor) Stop() {
	v.mu.Lock()
	v.shuttingDown = true
	cmd := v.cmd
	v.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (v *vectorSupervisor) superviseLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		if err := v.spawnAndWait(); err != nil {
			v.logger.Error("vector process error", "error", err)
		}

		v.mu.Lock()
		shuttingDown := v.shuttingDown
		deliberate := v.restarting
		v.restarting = false
		v.mu.Unlock()

		if shuttingDown || ctx.Err() != nil {
			return
		}
		if deliberate {
			failures = 0
			continue
		}

		failures++
		v.logger.Error("vector exited unexpectedly, restarting with backoff", "failures", failures)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(failures)):
		}
	}
}

func (v *vectorSupervisor) spawnAndWait() error {
	args := []string{}
	if v.configPath != "" {
		args = []string{"--config", v.configPath}
	}
	cmd := exec.Command(v.binary, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start vector: %w", err)
	}
	if v.onSpawnForTest != nil {
		v.onSpawnForTest()
	}

	v.mu.Lock()
	v.cmd = cmd
	v.mu.Unlock()

	return cmd.Wait()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run TestVectorSupervisor -v`
Expected: PASS (all 3 tests).

- [ ] **Step 5: Run the full `agent` test suite**

Run: `cd src && go test ./cmd/agent/... -v 2>&1 | tail -100`
Expected: PASS (every existing test — `vectorSupervisor` is entirely new code with no existing call sites yet).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/vector.go src/cmd/agent/vector_test.go
git commit -m "feat(agent): add vectorSupervisor (start, crash-restart with backoff, deliberate restart, stop)"
```

---

### Task 4: `agent` — `run()` gains an `onSuccess` hook

**Files:**
- Modify: `src/cmd/agent/reconcile.go`
- Modify: `src/cmd/agent/reconcile_test.go`

**Interfaces:**
- Produces: `run`'s new trailing parameter, `onSuccess func(policyID string)`.

This is the one piece of wiring `reconcile.go` needs: a way for `run()`'s dispatch loop to tell something outside itself "this policy just succeeded," without `reconcile.go` knowing anything about Vector, and without `vectorSupervisor` knowing anything about policies.

- [ ] **Step 1: Write the failing test**

Append to `src/cmd/agent/reconcile_test.go`:

```go
func TestRun_CallsOnSuccessAfterASuccessfulExecOnly(t *testing.T) {
	testPolicies := []Policy{
		{ID: "ok-policy", Binary: "true", Interval: time.Hour},
		{ID: "fail-policy", Binary: "false", Interval: time.Hour},
	}

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

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec, func() ([]Policy, bool) { return testPolicies, true }, 2, onSuccess)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, succeeded, "ok-policy")
	assert.NotContains(t, succeeded, "fail-policy", "onSuccess must not fire for a failed exec")
}

func TestRun_NilOnSuccessIsSafe(t *testing.T) {
	testPolicies := []Policy{{ID: "test-policy", Binary: "true", Interval: time.Hour}}
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec, func() ([]Policy, bool) { return testPolicies, true }, 2, nil)
	assert.NoError(t, err, "run must not panic when onSuccess is nil")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run 'TestRun_CallsOnSuccessAfterASuccessfulExecOnly|TestRun_NilOnSuccessIsSafe' -v`
Expected: FAIL — too many arguments in call to `run` (compile error).

- [ ] **Step 3: Implement**

In `src/cmd/agent/reconcile.go`, change `run`'s signature and both dispatch sites:

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int, onSuccess func(policyID string)) error {
```

(adds `onSuccess func(policyID string)` as a new trailing parameter)

Replace the dispatch loop body:

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
					if attemptErr == nil && onSuccess != nil {
						onSuccess(p.ID)
					}
				}(p)
				continue
			}

			logExecStart(rs.logger, p)
			start := time.Now()
			attemptErr := execute(ctx, p.Binary, p.Args)
			logExecCompletion(rs.logger, p, attemptErr, time.Since(start))
			rs.recordOutcome(p.ID, attemptErr, now)
			if attemptErr == nil && onSuccess != nil {
				onSuccess(p.ID)
			}
```

(This assumes Phase 1's Task 10 — `logExecStart`/`logExecCompletion` — has already landed, per this plan's Global Constraints. If for any reason it hasn't, add the two `if attemptErr == nil && onSuccess != nil { onSuccess(p.ID) }` blocks directly after each `rs.recordOutcome(...)` call in whatever the current dispatch loop body looks like — the exact surrounding lines don't matter, only that `onSuccess` fires after `recordOutcome`, only on the nil-error path, at both the synchronous and background dispatch sites.)

- [ ] **Step 4: Update every existing `run(...)` call site in `reconcile_test.go`**

`reconcile_test.go` has several existing calls shaped like `run(ctx, testLogger(), cachePath, ..., 2)` (a trailing concurrency-limit argument). Add a trailing `, nil` (for "no `onSuccess` hook") to every one of them — this is a uniform, mechanical addition; find each existing `run(` call in the file and append `, nil)` where it currently ends in `, 2)` (or whatever concurrency value that test uses) followed by the closing paren. For example:

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2)
```

becomes:

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -v 2>&1 | tail -100`
Expected: PASS (every test, including the 2 new ones and every pre-existing `TestRun_*` test now passing an explicit `nil`).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go
git commit -m "feat(agent): give run() an onSuccess hook, fired after a policy's successful exec"
```

---

### Task 5: `agent` — wire Vector into `serve`

**Files:**
- Modify: `src/cmd/agent/main.go`

**Interfaces:**
- Consumes: `resolveVectorBinary` (Task 1), `renderVectorConfig` (Task 2), `newVectorSupervisor` (Task 3), `run`'s new `onSuccess` parameter (Task 4), `config.Config.LogGatewayHost`/`LogGatewayPort` (Phase 2).

- [ ] **Step 1: Implement**

In `src/cmd/agent/main.go`, inside the `case "serve":` block, after `os.MkdirAll(varDir, ...)` and before constructing `logger`/`logfile` (so a Vector setup failure is itself logged, not just printed to stderr — insert the Vector setup between `logging.NewLogger` and `signal.NotifyContext`):

```go
	case "serve":
		if err := os.MkdirAll(varDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create var directory %s: %v\n", varDir, err)
			os.Exit(1)
		}

		ctx := context.WithValue(context.Background(), "appName", appName)
		ctx = context.WithValue(ctx, config.ContextKey, conf)
		ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
		ctx = context.WithValue(ctx, "quietMode", false)

		logger, logfile := logging.NewLogger(ctx)
		defer logfile.Close()

		certsDir, err := config.ResolveCertsDir()
		if err != nil {
			logger.Error("certs directory resolution failed", "error", err)
			os.Exit(1)
		}

		vectorBinary, err := resolveVectorBinary()
		if err != nil {
			logger.Error("vector binary resolution failed", "error", err)
			os.Exit(1)
		}
		vectorConfig, err := renderVectorConfig(conf.LogDir, varDir, certsDir, conf.LogGatewayHost, conf.LogGatewayPort)
		if err != nil {
			logger.Error("vector config render failed", "error", err)
			os.Exit(1)
		}
		vectorConfigPath := filepath.Join(varDir, "vector-config.yaml")
		if err := os.WriteFile(vectorConfigPath, []byte(vectorConfig), 0o644); err != nil {
			logger.Error("vector config write failed", "path", vectorConfigPath, "error", err)
			os.Exit(1)
		}

		reconcileInterval := time.Duration(conf.ReconcileIntervalSec) * time.Second
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		vectorSup := newVectorSupervisor(vectorBinary, vectorConfigPath, logger)
		vectorSup.Start(signalCtx)
		defer vectorSup.Stop()

		onSuccess := func(policyID string) {
			if policyID == "operating-refresh" {
				vectorSup.TriggerRestart()
			}
		}

		logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath, "vector_config", vectorConfigPath)
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs, onSuccess); err != nil {
			logger.Error("agent exited with error", "error", err)
			os.Exit(1)
		}
```

(Note: `signalCtx`'s `defer stop()` and `vectorSup.Stop()`'s ordering — `defer` runs LIFO, so `defer vectorSup.Stop()` (registered after `defer stop()`) runs *before* `stop()` on the way out; since `vectorSup.Start(signalCtx)`'s own supervise loop already exits on `signalCtx.Done()` on its own, `vectorSup.Stop()` here is what actually triggers that by signaling the running process — the ordering is correct as written: `run(...)` returns after `signalCtx` is cancelled elsewhere (SIGTERM/interrupt), then `vectorSup.Stop()` fires, then `stop()` (the `signal.NotifyContext` cleanup) fires last, which is fine since it's idempotent context cleanup, not itself a shutdown trigger.)

Add `"path/filepath"` to the import block if not already present (it already is, per the existing `cachePath`/`policiesCachePath` construction earlier in `main`).

- [ ] **Step 2: Confirm it builds**

Run: `cd src && go build ./cmd/agent/...`
Expected: no output, exit code 0.

- [ ] **Step 3: Run the full `agent` test suite and full repo build**

Run: `cd src && go build ./... && go test ./... 2>&1 | tail -40`
Expected: every package `ok`.

- [ ] **Step 4: Commit**

```bash
git add src/cmd/agent/main.go
git commit -m "feat(agent): start, configure, and supervise Vector from serve"
```

---

### Task 6: Bundle the Vector binary into every agent-bundled image

**Files:**
- Modify: `deploy/control-plane/catalog/Dockerfile`
- Modify: `deploy/control-plane/policy-server/Dockerfile`
- Modify: `deploy/control-plane/log-gateway/Dockerfile`
- Modify: `demo/backup-host/Dockerfile`

- [ ] **Step 1: Add a Vector build stage and `COPY` to each Dockerfile**

In each of the four Dockerfiles, add a new stage before the final `FROM debian:bookworm-slim` stage:

```dockerfile
FROM timberio/vector:0.46.0-debian AS vector-source
```

Add `vector-source`'s binary to the final stage's `COPY --from=builder` line. For example, in `deploy/control-plane/catalog/Dockerfile`, change:

```dockerfile
COPY --from=builder /build/bin/catalog /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
```

to:

```dockerfile
COPY --from=builder /build/bin/catalog /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
```

Apply the equivalent change to `deploy/control-plane/policy-server/Dockerfile`, `deploy/control-plane/log-gateway/Dockerfile`, and `demo/backup-host/Dockerfile` (each already has its own `COPY --from=builder ...` line to add the `COPY --from=vector-source ...` line after).

**Verify the exact tag and in-image binary path before trusting this**: `0.46.0-debian` and `/usr/bin/vector` are this plan's best-effort values, not independently confirmed against a running container. Step 2 below is this task's own verification — if either is wrong, the build fails loudly and visibly, not silently.

- [ ] **Step 2: Build one image and verify Vector actually runs**

Run: `cd /home/alex/miniprotector && docker build -f deploy/control-plane/catalog/Dockerfile -t catalog-vector-test .`
Then: `docker run --rm --entrypoint ./vector catalog-vector-test --version`
Expected: the build succeeds, and the version command prints a real Vector version string. If the `COPY --from=vector-source /usr/bin/vector` line fails (wrong in-image path) or the base image tag doesn't pull, adjust the tag/path and re-run until this passes — then apply the same, now-confirmed values to the other three Dockerfiles.

- [ ] **Step 3: Commit**

```bash
git add deploy/control-plane/catalog/Dockerfile deploy/control-plane/policy-server/Dockerfile deploy/control-plane/log-gateway/Dockerfile demo/backup-host/Dockerfile
git commit -m "deploy: bundle the Vector binary into every agent-bundled image"
```

---

### Task 7: Configuration — `log_gateway_host`/`log_gateway_port` everywhere `agent` runs

**Files:**
- Modify: `deploy/control-plane/catalog/local.conf`
- Modify: `deploy/control-plane/policy-server/local.conf`
- Modify: `deploy/control-plane/log-gateway/local.conf`
- Modify: `demo/local.conf`

- [ ] **Step 1: Add `log_gateway_host`/`log_gateway_port` to each `local.conf`**

In each of `deploy/control-plane/catalog/local.conf`, `deploy/control-plane/policy-server/local.conf`, and `deploy/control-plane/log-gateway/local.conf`, add after the existing `policy_server_host=policy-server` line:

```
# Where this node's agent-managed Vector process pushes logs, via
# log-gateway's own mTLS-verifying proxy in front of Loki.
log_gateway_host=log-gateway
log_gateway_port=9400
```

In `demo/local.conf`, add the same two lines after its existing `policy_server_host=policy-server` line — pointing at whatever hostname the demo compose project's own `log-gateway` service resolves to (see Task 8, which decides whether the demo gets its own `log-gateway`/`loki` pair or reuses the control-plane compose project's).

- [ ] **Step 2: Confirm every agent-managed node's config parses**

Run, from the repo root:

```bash
make agent
for f in deploy/control-plane/catalog/local.conf deploy/control-plane/policy-server/local.conf deploy/control-plane/log-gateway/local.conf demo/local.conf; do
  echo "== $f =="
  MP_CONFIG_PATH=$(dirname "$f") ./bin/agent list-policies 2>&1 | head -1
done
```

Expected: each `== <file> ==` header is followed by `agent`'s normal `list-policies` output (a `POLICY ... STATE ...` table header, or an empty table), never a "missing required configuration field" or "unknown configuration key" error — `agent list-policies` parses config as its first step regardless of whether a real deployment's certs/state exist alongside it.

- [ ] **Step 3: Commit**

```bash
git add deploy/control-plane/catalog/local.conf deploy/control-plane/policy-server/local.conf deploy/control-plane/log-gateway/local.conf demo/local.conf
git commit -m "deploy: set log_gateway_host/log_gateway_port on every agent-managed node"
```

---

### Task 8: Demo wiring and end-to-end verification

**Files:**
- Modify: `demo/docker-compose.yml`

The demo (`demo/docker-compose.yml`) is a separate Docker Compose project from `deploy/control-plane/docker-compose.yml` — it has its own `ca`/`issuer`/`catalog`/`policy-server` services rather than reusing the control-plane ones, so it needs its own `loki`/`log-gateway` pair too, not a cross-project network reference (Compose projects don't share networks by default, and reaching across them is exactly the kind of fragile, unusual coupling this project's demo has consistently avoided elsewhere).

- [ ] **Step 1: Add `loki` and `log-gateway` services to `demo/docker-compose.yml`**

Add, after the existing `policy-server` service:

```yaml
  loki:
    image: grafana/loki:3.7.3
    volumes:
      - ./loki/loki-config.yaml:/etc/loki/local-config.yaml:ro
      - loki-data:/loki
    command: ["-config.file=/etc/loki/local-config.yaml"]
    restart: unless-stopped

  log-gateway:
    build:
      context: ..
      dockerfile: deploy/control-plane/log-gateway/Dockerfile
    depends_on:
      - ca
      - issuer
      - loki
    volumes:
      - log-gateway-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - LOKI_URL=http://loki:3100
    restart: unless-stopped
```

Add `loki-data:` and `log-gateway-data:` to the `volumes:` section at the bottom.

Create `demo/loki/loki-config.yaml` as a copy of `deploy/control-plane/loki/loki-config.yaml` (Phase 2, Task 5) — identical content, since both are the same single-binary filesystem-storage setup, just under the demo's own directory tree.

Add `log-gateway` to each of `database`'s, `webserver`'s, and `store`'s `depends_on:` lists (so Compose starts `log-gateway` before the nodes whose `agent`-supervised Vector will try to reach it — not strictly required for correctness, since Vector's own crash-restart-with-backoff already tolerates `log-gateway` not being up yet, but avoids a burst of early, expected-to-fail connection attempts in the logs on a fresh `docker compose up`).

- [ ] **Step 2: Bring the demo up and verify logs actually reach Loki**

Run: `cd demo && ./up.sh` (or the demo's documented startup command — check `demo/README.md` if `up.sh` requires arguments)
Wait for all services to report healthy/running: `docker compose ps`

Run: `docker compose exec loki wget -qO- 'http://localhost:3100/loki/api/v1/query_range?query={hostname=~".+"}&start=0&end=9999999999000000000' | head -c 2000`
Expected: a non-empty result containing log lines from at least `database`/`webserver`/`store`'s `hostname` labels (`database`, `webserver`, `store` respectively, or whatever hostnames `client-manager add` assigned them per `demo/README.md`) — confirming the full pipeline (subprocess log file → Vector tail → mTLS push → log-gateway hostname enforcement → Loki storage) works end to end in the actual demo environment, not just in isolated tests.

- [ ] **Step 3: Confirm revocation still cuts off logging within one refresh cycle**

Run whatever the demo's existing revoke procedure is (see `demo/README.md`) against one node (e.g. `database`), wait past `OperatingCertFetchIntervalSec` (900s default — consider temporarily lowering it in `demo/local.conf` for this manual check, then reverting), and confirm that node's logs stop appearing in new Loki query results while its already-ingested history remains queryable.

- [ ] **Step 4: Tear down and commit**

```bash
cd demo && docker compose down --volumes
cd ..
git add demo/docker-compose.yml demo/loki/loki-config.yaml
git commit -m "demo: wire log-gateway and loki, verify end-to-end log delivery"
```

---

### Task 9: Documentation

**Files:**
- Modify: `docs/components/agent.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/SECURITY.md`
- Modify: `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`

- [ ] **Step 1: Update `docs/components/agent.md`**

In the "Logging and correlation" section Phase 1 added, append:

```markdown
`agent` also bundles, configures, and directly supervises a Vector process that tails `log_dir`
and ships every line to `log-gateway` over mTLS, using this node's own operating certificate --
restarted immediately after every successful `operating-refresh` (so a rotated cert is always
picked up promptly) and crash-restarted with backoff otherwise, the same `backoff()` failing
policies already use. Vector's own HTTP API is never enabled, so this adds no listening socket to
`agent`'s footprint, which stays outbound-only. See
[Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md).
```

Add a row to "Configuration Keys":

```markdown
| `log_gateway_host` / `log_gateway_port` | none / 9400 | Where agent's supervised Vector process pushes logs, via `log-gateway` |
```

Add to "See Also": `- [log-gateway](./log-gateway.md) — receives this node's shipped logs`.

- [ ] **Step 2: Update `docs/ARCHITECTURE.md`**

Update the `log-gateway` components-table row (added in Phase 2) — change its "Status" cell from:

```
Implemented (agent/Vector integration is separate, later work)
```

to:

```
Implemented (agent bundles, configures, and supervises the Vector process that ships to it)
```

- [ ] **Step 3: Update `docs/SECURITY.md`**

After the "Revocation and its trust-model costs" section's existing paragraph about `attribute`/`issuer`, add:

```markdown
The same mechanism now also gates log shipping: `agent`'s supervised Vector process authenticates
to `log-gateway` with the node's operating credential, restarted immediately after every successful
`operating-refresh`. A revoked node's operating cert simply stops renewing (the existing mechanism,
above) -- once it expires, Vector can no longer authenticate, and that node's log-shipping ability
lapses within the same bound `OperatingCertFetchIntervalSec`/`OperatingCertTTLSec` already give
every other operating-cert-gated capability. No separate revocation path was built for logging.
```

- [ ] **Step 4: Update the design spec's own status**

In `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`, the header note (`> Builds on...`) and body already describe the target architecture accurately — no content changes needed there (specs describe design intent, not implementation status; `docs/ARCHITECTURE.md`'s components table, updated in Step 2, is this project's canonical "what's actually built" record, per its own stated convention). Confirm this by re-reading the spec's Non-Goals and Architecture sections once more: nothing in them was contingent on being rewritten post-implementation.

- [ ] **Step 5: Final verification**

Run: `cd src && go build ./... && go test ./... 2>&1 | tail -40` and `go vet ./...`
Expected: `ok` for every package; `go vet` shows only the pre-existing `cmd/brfs` warning, if any, not introduced by this plan.

- [ ] **Step 6: Commit**

```bash
git add docs/components/agent.md docs/ARCHITECTURE.md docs/SECURITY.md
git commit -m "docs: document agent-supervised Vector shipping, closing out the fleet log aggregation design"
```

---

## Self-Review

**Spec coverage** (against `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`):
- Vector bundled/config-generated/supervised by `agent`, restart-on-refresh event-driven (not a timer), crash-restart with backoff, no listening socket → Tasks 1, 2, 3, 5.
- Revocation cuts off log-shipping within one refresh cycle, reusing the existing credential/revocation machinery, no new revocation path → a direct consequence of Task 5's wiring (Vector authenticates with the operating cert `certclient operating-refresh` already manages); documented explicitly in Task 9.
- Fleet-wide, end-to-end log delivery, demonstrated in the demo → Task 8.
- Explicitly out of scope for this plan (correctly, per the design's own Non-Goals, unchanged by this phase): `issuer`/`client-manager` coverage, metrics/traces, a custom query UI, log-content redaction, HA for Loki/`log-gateway`, per-job debug-level control.

**Placeholder scan:** every code block is complete and directly usable. The one explicitly-flagged uncertainty (Vector's exact Docker image tag and in-image binary path) is called out as something Task 6's own Step 2 verifies by actually building and running the image, not silently assumed — the same discipline Phase 2 applied to the Loki image tag.

**Type consistency:** `run`'s new `onSuccess func(policyID string)` parameter (Task 4) is passed identically at every call site — the real one in `main.go` (Task 5) and every test call site in `reconcile_test.go` (Task 4, Step 4). `vectorSupervisor`'s methods (`Start`, `TriggerRestart`, `Stop`) are used with identical signatures in Task 3's own tests and Task 5's `main.go` wiring. `resolveVectorBinary`/`renderVectorConfig` (Tasks 1, 2) are consumed with identical signatures in Task 5.

**Sequencing:** Task 1 (binary resolution) and Task 2 (config rendering) are independent of each other but both precede Task 5 (which needs both) — order preserved. Task 3 (`vectorSupervisor`) and Task 4 (`run`'s `onSuccess` hook) are independent of each other but both precede Task 5 — order preserved. Tasks 6–8 (deployment/demo) depend on all of Tasks 1–5 being complete (the actual `agent` binary they bundle must already know how to supervise Vector) — already last in the task list.

No gaps found.
