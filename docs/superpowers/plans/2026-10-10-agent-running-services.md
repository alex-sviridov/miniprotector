# agent `running_service=` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `agent` supervises the local services listed as repeated `running_service=` lines in `local.conf`, so a control-plane container runs only `agent`.

**Architecture:** `common/config` parses and validates the lines (env expansion, no quoting). `cmd/agent` starts them once the operating certificate exists, using a dedicated `storageManager` independent of the policy cache. The four control-plane entrypoints shrink to bootstrap/renew + `exec ./agent serve`.

**Tech Stack:** Go 1.26, testify, POSIX sh, Docker compose.

**Spec:** `docs/superpowers/specs/2026-10-10-agent-running-services-design.md`

## Global Constraints

- Config key `running_service`, repeatable; value `<binary> [args...]`; `<binary>` is a bare name; args split on whitespace; `"`/`'` rejected; `$VAR`, `${VAR}`, `${VAR:-default}` expanded from the environment before splitting; duplicate binary rejected.
- Nothing starts before `<certs>/client.crt` exists; poll every 500 ms.
- A node with no `running_service` lines behaves exactly as before.
- Static services are not recorded in `agent-state.json`.
- Work on branch `agent-running-services`; run Go commands from `/home/alex/miniprotector/src`.
- Every commit ends with `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr` (as in each commit step).
- Docs obligations (`.claude/CLAUDE.md`): `docs/components/agent.md`, `README.md` only if affected, `docs/ARCHITECTURE.md`, `CHANGELOG.md`.

---

### Task 1: Parse `running_service=` in `common/config`

**Files:** Modify `src/common/config/config.go`, `src/common/config/config_test.go`.

**Interfaces:** Produces `type RunningService struct{ Binary string; Args []string }` and `Config.RunningServices []RunningService`.

- [ ] **Step 1: Failing tests** — append to `config_test.go`:

```go
func parseWith(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n" + extra
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return ParseConfig(path)
}

func TestParseConfig_RunningServicesInOrder(t *testing.T) {
	conf, err := parseWith(t, "running_service=listener --debug=false\nrunning_service=catalog /data/storage\nrunning_service=agentless\n")
	require.NoError(t, err)
	assert.Equal(t, []RunningService{
		{Binary: "listener", Args: []string{"--debug=false"}},
		{Binary: "catalog", Args: []string{"/data/storage"}},
		{Binary: "agentless", Args: nil},
	}, conf.RunningServices)
}

func TestParseConfig_RunningServicesAbsentByDefault(t *testing.T) {
	conf, err := parseWith(t, "")
	require.NoError(t, err)
	assert.Empty(t, conf.RunningServices)
}

func TestParseConfig_RunningServiceExpandsEnvironment(t *testing.T) {
	t.Setenv("MP_TEST_DEBUG", "true")
	t.Setenv("MP_TEST_EMPTY", "")
	t.Setenv("MP_TEST_UNSET_GUARD", "")
	os.Unsetenv("MP_TEST_UNSET")
	conf, err := parseWith(t, "running_service=svc --a=$MP_TEST_DEBUG --b=${MP_TEST_DEBUG} --c=${MP_TEST_UNSET:-fallback} --d=${MP_TEST_EMPTY:-empty-uses-default} --e=${MP_TEST_DEBUG:-ignored}\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"--a=true", "--b=true", "--c=fallback", "--d=empty-uses-default", "--e=true"}, conf.RunningServices[0].Args)
}

func TestParseConfig_RunningServiceRejections(t *testing.T) {
	cases := map[string]string{
		"empty value":        "running_service=\n",
		"only spaces":        "running_service=   \n",
		"path separator":     "running_service=./catalog\n",
		"absolute path":      "running_service=/usr/bin/catalog\n",
		"double quote":       "running_service=svc --msg \"a b\"\n",
		"single quote":       "running_service=svc 'a b'\n",
		"duplicate binary":   "running_service=svc\nrunning_service=svc --x\n",
		"expands to nothing": "running_service=${MP_TEST_UNSET_FOR_REJECT}\n",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseWith(t, line)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "running_service")
		})
	}
}
```

- [ ] **Step 2: Run, expect compile failure** — `go test ./common/config/ -run RunningService` → `undefined: RunningService`.

- [ ] **Step 3: Implement** — in `config.go`: add `"os"` and `"strings"` imports if missing; add the type and field; add the case; add helpers.

```go
// RunningService is one `running_service=` line: a binary agent keeps running.
type RunningService struct {
	Binary string
	Args   []string
}
```

Field on `Config` (near `RunDir`): `RunningServices []RunningService`.

Case next to `case "run_dir":`:

```go
		case "running_service":
			svc, err := parseRunningService(value)
			if err != nil {
				return nil, fmt.Errorf("invalid running_service at line %d: %w", lineNum, err)
			}
			for _, existing := range config.RunningServices {
				if existing.Binary == svc.Binary {
					return nil, fmt.Errorf("invalid running_service at line %d: %s is listed twice", lineNum, svc.Binary)
				}
			}
			config.RunningServices = append(config.RunningServices, svc)
```

Helpers (after `ResolveRunDir`):

```go
// parseRunningService expands environment references in value, then splits it
// into a bare binary name and its arguments. Quotes are rejected rather than
// mis-split: arguments are whitespace-separated, nothing more.
func parseRunningService(value string) (RunningService, error) {
	if strings.ContainsAny(value, `"'`) {
		return RunningService{}, fmt.Errorf("quote characters are not supported (arguments are split on whitespace)")
	}
	fields := strings.Fields(os.Expand(value, expandVar))
	if len(fields) == 0 {
		return RunningService{}, fmt.Errorf("empty value (want: <binary> [args...])")
	}
	if strings.ContainsAny(fields[0], `/\`) {
		return RunningService{}, fmt.Errorf("binary %q must be a bare name, resolved next to the agent executable", fields[0])
	}
	return RunningService{Binary: fields[0], Args: fields[1:]}, nil
}

// expandVar resolves $VAR, ${VAR} and ${VAR:-default}; the default applies
// when the variable is unset or empty. Unset without a default expands to "".
func expandVar(name string) string {
	name, def, hasDefault := strings.Cut(name, ":-")
	if v := os.Getenv(name); v != "" || !hasDefault {
		return v
	}
	return def
}
```

Note `fields[1:]` of a one-field slice is an empty non-nil slice; make `Args` nil in that case so the `{Binary: "agentless", Args: nil}` expectation holds: `args := fields[1:]; if len(args) == 0 { args = nil }`.

- [ ] **Step 4: Run** — `go test ./common/config/` → PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add src/common/config
git commit -m "feat(config): repeatable running_service= lines" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 2: agent starts the listed services once the operating certificate exists

**Files:** Create `src/cmd/agent/services.go`, `src/cmd/agent/services_test.go`; modify `src/cmd/agent/storage.go` (nil-safe `rs`), `src/cmd/agent/main.go`.

**Interfaces:**
- Consumes: `config.RunningService`, `newStorageManager`, `storageManager.reconcile(ctx, rs, tasks)` (rs may now be nil), `resolveExecPath`.
- Produces: `runningServiceTasks(services []config.RunningService) []storageTask` (ID `service:<binary>`, Binary resolved); `superviseRunningServices(ctx, logger, mgr *storageManager, certsDir string, tasks []storageTask, poll time.Duration)` (blocks until started or ctx done).

- [ ] **Step 1: Failing tests** — `services_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunningServiceTasks(t *testing.T) {
	tasks := runningServiceTasks([]config.RunningService{
		{Binary: "listener", Args: []string{"--debug=false"}},
		{Binary: "catalog", Args: []string{"/data"}},
	})
	require.Len(t, tasks, 2)
	assert.Equal(t, "service:listener", tasks[0].ID)
	assert.Equal(t, []string{"--debug=false"}, tasks[0].Args)
	assert.Equal(t, "service:catalog", tasks[1].ID)
	assert.True(t, filepath.Base(tasks[1].Binary) == "catalog", "binary resolved from the bare name: %s", tasks[1].Binary)
}

// markerScript writes its PID line to marker each time it starts, then idles
// until SIGTERM.
func markerScript(t *testing.T, dir, marker string) string {
	t.Helper()
	script := filepath.Join(dir, "svc.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\necho started >> "+marker+"\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))
	return script
}

func lines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

func TestSuperviseRunningServices_WaitsForOperatingCertificate(t *testing.T) {
	dir := t.TempDir()
	certs := filepath.Join(dir, "certs")
	require.NoError(t, os.MkdirAll(certs, 0o755))
	marker := filepath.Join(dir, "marker")
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer mgr.StopAll()

	done := make(chan struct{})
	go func() {
		superviseRunningServices(ctx, testLogger(), mgr, certs, []storageTask{{ID: "service:svc", Binary: markerScript(t, dir, marker)}}, 10*time.Millisecond)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, 0, lines(t, marker), "must not start before client.crt exists")

	require.NoError(t, os.WriteFile(filepath.Join(certs, "client.crt"), []byte("x"), 0o600))
	require.Eventually(t, func() bool { return lines(t, marker) == 1 }, 2*time.Second, 10*time.Millisecond)
	<-done
}

func TestSuperviseRunningServices_ReturnsWhenContextCancelledBeforeCertificate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		superviseRunningServices(ctx, testLogger(), newStorageManager(testLogger()), t.TempDir(), nil, 10*time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after cancel")
	}
}

func TestSuperviseRunningServices_RestartsACrashingService(t *testing.T) {
	dir := t.TempDir()
	certs := filepath.Join(dir, "certs")
	require.NoError(t, os.MkdirAll(certs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(certs, "client.crt"), []byte("x"), 0o600))
	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "crash.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\necho started >> "+marker+"\nexit 1\n"))

	mgr := newStorageManager(testLogger())
	mgr.backoff = backoffPolicy{Base: 10 * time.Millisecond, Max: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer mgr.StopAll()

	superviseRunningServices(ctx, testLogger(), mgr, certs, []storageTask{{ID: "service:crash", Binary: script}}, 10*time.Millisecond)
	require.Eventually(t, func() bool { return lines(t, marker) >= 3 }, 3*time.Second, 20*time.Millisecond, "a crashing service must be restarted repeatedly")
}

func TestStorageManager_ReconcileAcceptsNilReconcileState(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer mgr.StopAll()

	mgr.reconcile(ctx, nil, []storageTask{{ID: "service:svc", Binary: markerScript(t, dir, marker)}})
	require.Eventually(t, func() bool { return lines(t, marker) == 1 }, 2*time.Second, 10*time.Millisecond)
}
```

- [ ] **Step 2: Run, expect compile failure** — `go test ./cmd/agent/ -run 'RunningService|NilReconcileState'`.

- [ ] **Step 3: Implement**

`storage.go`, in `reconcile`'s supervisor construction, replace the `OnOutcome` line with a nil-guarded one:

```go
			OnOutcome: func(err error) {
				if rs != nil {
					rs.recordOutcome(id, err, time.Now())
				}
			},
```

and extend the doc comment: `// rs may be nil (static running_service tasks are not recorded in agent-state.json).`

Create `services.go`:

```go
package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
)

// runningServiceTasks turns `running_service=` config lines into supervised
// tasks. IDs are "service:<binary>"; the binary is resolved next to agent.
func runningServiceTasks(services []config.RunningService) []storageTask {
	tasks := make([]storageTask, 0, len(services))
	for _, s := range services {
		tasks = append(tasks, storageTask{
			ID:     "service:" + s.Binary,
			Binary: resolveExecPath(s.Binary),
			Args:   s.Args,
		})
	}
	return tasks
}

// superviseRunningServices starts tasks under mgr once the operating
// certificate (certsDir/client.crt) exists, polling every poll, and returns
// right after starting them (or when ctx is cancelled first). Static services
// are deliberately not driven by run()'s per-tick storage reconcile, which is
// skipped whenever the policy cache is unreadable -- a fresh policy-server must
// start before any policy can be fetched from it.
func superviseRunningServices(ctx context.Context, logger *slog.Logger, mgr *storageManager, certsDir string, tasks []storageTask, poll time.Duration) {
	if len(tasks) == 0 {
		return
	}
	crt := filepath.Join(certsDir, "client.crt")
	logged := false
	for ctx.Err() == nil {
		if _, err := os.Stat(crt); err == nil {
			mgr.reconcile(ctx, nil, tasks)
			logger.Info("running services started", "count", len(tasks))
			return
		}
		if !logged {
			logger.Info("waiting for the operating certificate before starting running services", "path", crt, "count", len(tasks))
			logged = true
		}
		if !sleepOrDone(ctx, poll) {
			return
		}
	}
}
```

`main.go`, in `serve` after `storageMgr := newStorageManager(logger)` add:

```go
	servicesMgr := newStorageManager(logger)
```

after `defer storageMgr.StopAll()` add `defer servicesMgr.StopAll()`, and just before the `logger.Info("agent started", ...)` line add:

```go
	go superviseRunningServices(signalCtx, logger, servicesMgr, certsDir, runningServiceTasks(conf.RunningServices), 500*time.Millisecond)
```

- [ ] **Step 4: Run** — `go test -race ./cmd/agent/ ./common/config/` → PASS; `gofmt -l cmd/agent common/config` prints nothing.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/agent
git commit -m "feat(agent): supervise running_service processes once the operating cert exists" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 3: Control-plane containers run only agent

**Files:** Modify `deploy/control-plane/{catalog,policy-server,api-server,clientmanager-api}/{entrypoint.sh,local.conf}`.

- [ ] **Step 1: Entrypoints.** Read each current file first. In each, keep the bootstrap/renew block and any directory setup (`catalog`: `mkdir -p "$STORAGE_PATH"`), delete the `./agent serve &` line, the 60 s certificate wait loop, the listener restart loop and the service start (`exec ./<svc>` / backgrounded services / `wait`), and end with `exec ./agent serve --debug="${DEBUG:-false}"`. Result for `api-server`:

```sh
#!/bin/sh
set -e

if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent keeps the credentials fresh and, once the operating certificate exists,
# starts and supervises the processes listed as running_service= in local.conf.
exec ./agent serve --debug="${DEBUG:-false}"
```

(`catalog` keeps its `mkdir -p "$STORAGE_PATH"` first; add a comment line there that the services come from `local.conf`.)

- [ ] **Step 2: local.conf.** Append to each file (after the `listener_port` block), with a short comment above the lines:

`catalog/local.conf`:
```
running_service=listener --debug=${DEBUG:-false}
running_service=catalog ${STORAGE_PATH:-/data/storage} --debug=${DEBUG:-false}
```
`policy-server/local.conf`:
```
running_service=listener --debug=${DEBUG:-false}
running_service=policy-server --debug=${DEBUG:-false}
```
`api-server/local.conf`:
```
running_service=listener --debug=${DEBUG:-false}
running_service=api-server --debug=${DEBUG:-false}
```
`clientmanager-api/local.conf` (flags copied from its current entrypoint):
```
running_service=listener --debug=${DEBUG:-false}
running_service=clientmanager-api --debug=${DEBUG:-false}
running_service=clientmanager-admin-api --debug=${DEBUG:-false} --ca-url https://step-ca:9000 --root /data/root_ca.crt --provisioner admin@backup.internal --password-file /data/secrets/password
```
Check what `STORAGE_PATH` the catalog entrypoint/compose actually uses (`grep -rn STORAGE_PATH deploy demo`) and make the default in `local.conf` match it; do not guess.

- [ ] **Step 3: Validate.** `for f in deploy/control-plane/*/entrypoint.sh; do sh -n "$f"; done`; a throwaway Go test in `common/config` (delete after) that `ParseConfig`s every `deploy/control-plane/*/local.conf` and `demo/local.conf`; `docker compose -f deploy/control-plane/docker-compose.yml config -q` and the demo one.

- [ ] **Step 4: Commit** (`build(deploy): control-plane containers run only agent, which supervises their services`).

---

### Task 4: Docs, end-to-end verification, changelog

- [ ] **Step 1: Docs.** `docs/components/agent.md`: new "Running services" section (format, expansion, no quotes, cert gate, independence from the policy cache, restart behaviour, not in `agent-state.json`, bare-name binaries) and a mention in the intro and config keys. `docs/components/listener.md` "Start order" bullet: `agent` starts it from `running_service=`. `docs/ARCHITECTURE.md`: the `agent` paragraph and the control-plane containers description (containers run only `agent`). `deploy/control-plane/README.md`: entrypoint/`running_service` note. Amend the listener spec's supervision bullet to say "started and supervised by `agent` from `running_service=` lines (see the running-services spec)".

- [ ] **Step 2: Full checks.** `cd src && go build ./... && go test ./... && go vet ./cmd/agent/ ./common/config/` and `gofmt -l cmd/agent common/config`.

- [ ] **Step 3: Demo end to end.** `make demo-up` (log to the scratchpad, run in background, wait with a monitor), then check `docker compose -f demo/docker-compose.yml logs <svc>` for each of catalog, policy-server, api-server, clientmanager-api shows `running services started` and the listener/service sockets; run `cd src && go test -tags=e2e -count=1 -timeout=240s ./e2e/...`; kill a service process inside a container (`docker compose exec catalog pkill listener`) and confirm the supervisor restarts it (`supervised process exited unexpectedly` then a new `listener started`); then `make demo-down`.

- [ ] **Step 4: Changelog + commit.** Add a dated entry (most recent first) summarizing the feature and its effect on the entrypoints; commit docs and changelog.
