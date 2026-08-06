# E2E Lifecycle Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a second e2e test alongside the existing demo-web-UI smoke test: revoke and reissue an
enrolled client's certificate, create a fast-recurring backup policy, and confirm a real backup job
and its catalog entry both appear — all against the already-running demo lab (`make demo-up`).

**Architecture:** One new file, `src/e2e/lifecycle_test.go`, same package and `//go:build e2e` tag as
the existing `src/e2e/e2e_test.go`. A single test function, `TestE2E_ClientLifecycle`, with three
ordered `t.Run` subtests sharing state via outer-scope variables: revoke/verify-locked-out/reissue,
create-policy-and-observe-job, and observe-catalog-entry. Two categories of helper: plain `net/http`
calls to `api-server`'s REST API (bearer-token authenticated), and `docker compose exec` calls into
the already-running `database` container for the two actions with no host-reachable API
(`certclient operating-refresh`, `policyclient fetch`).

**Tech Stack:** Go 1.26, `net/http` (stdlib), `github.com/stretchr/testify v1.11.1` (already a
dependency, used via `require`), `os/exec` (stdlib) to shell out to `docker compose`.

## Global Constraints

- Module: `github.com/alex-sviridov/miniprotector` (`src/go.mod`).
- New file keeps the `//go:build e2e` tag and `package e2e` — same convention as
  `src/e2e/e2e_test.go`, keyed off by the Makefile's `-tags=e2e` flag and `./e2e/...` path.
- Target node is `database` (already enrolled, has sample data at `/var/lib/dbdata`). No new
  container, docker-compose service, or infrastructure is created.
- `api-server` REST base URL is `http://localhost:8090`; bearer token is
  `dev-placeholder-token-change-me` (demo lab placeholder, `demo/local.conf`).
- Every step in this plan that runs the test against live infrastructure assumes `make demo-up` has
  already been run and the stack is healthy. If a step's expected result doesn't match, check
  `docker compose -f demo/docker-compose.yml ps` before assuming the new code is wrong.
- Binaries inside the `database` container run from `/app` and are **not** on `$PATH` — invoke them
  as `./certclient`, `./policyclient` (confirmed live: `docker compose exec -T database sh -c 'which
  certclient'` fails, but `./certclient operating-refresh` from `/app` succeeds).

---

### Task 1: Shared helpers + revoke/verify-locked-out/reissue subtest

**Files:**
- Create: `src/e2e/lifecycle_test.go`

**Interfaces:**
- Consumes: nothing from other tasks — this is the first task.
- Produces: `apiRequest`, `requireStatus`, `decodeJSON`, `composeFile`, `dockerComposeExec` (all
  package-private, used by every later task), and `TestE2E_ClientLifecycle` with its first subtest.

- [ ] **Step 1: Write the file**

```go
//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	apiBaseURL = "http://localhost:8090"
	apiToken   = "dev-placeholder-token-change-me"
)

// apiRequest sends an authenticated request to api-server's REST API and
// returns the raw response -- callers decide whether to decode the body.
func apiRequest(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, apiBaseURL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// requireStatus closes resp's body and asserts its status code.
func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	require.Equal(t, want, resp.StatusCode)
}

// decodeJSON decodes resp's body into v and closes it.
func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(v))
}

// composeFile returns the absolute path to demo/docker-compose.yml,
// resolved relative to this source file so it works regardless of the
// test binary's working directory.
func composeFile() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "demo", "docker-compose.yml")
}

// dockerComposeExec runs a command inside an already-running demo service
// container and returns its combined output. A non-nil error means the
// command exited non-zero.
func dockerComposeExec(t *testing.T, service string, args ...string) (string, error) {
	t.Helper()
	cmdArgs := append([]string{"compose", "-f", composeFile(), "exec", "-T", service}, args...)
	out, err := exec.Command("docker", cmdArgs...).CombinedOutput()
	return string(out), err
}

// TestE2E_ClientLifecycle walks a realistic operator flow against the
// already-running demo lab (`make demo-up`): revoke and reissue a client's
// certificate, create a fast-recurring backup policy, and confirm a real
// backup job and its catalog entry both appear. Subtests run in order and
// share state -- a failure partway through makes later subtests
// meaningless.
func TestE2E_ClientLifecycle(t *testing.T) {
	const hostname = "database"

	t.Cleanup(func() {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)
	})

	t.Run("revoke_locks_out_then_reissue_restores", func(t *testing.T) {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/revoke", nil), http.StatusOK)

		out, err := dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.Error(t, err, "certclient operating-refresh should fail while revoked, output: %s", out)

		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)

		out, err = dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.NoError(t, err, "certclient operating-refresh should succeed after unrevoke, output: %s", out)
	})
}
```

- [ ] **Step 2: Build-check under the e2e tag**

```bash
cd /home/alex/miniprotector/src
go build -tags=e2e ./e2e/...
go vet -tags=e2e ./e2e/...
```

Expected: both succeed with no errors.

- [ ] **Step 3: Run against the live demo lab**

```bash
cd /home/alex/miniprotector
docker compose -f demo/docker-compose.yml ps database   # confirm it's Up before running
cd src && go test -tags=e2e -count=1 -timeout=60s -run TestE2E_ClientLifecycle -v ./e2e/...
```

Expected: `PASS`. The subtest name `revoke_locks_out_then_reissue_restores` should show as passed in
`-v` output. If it fails on the first `certclient operating-refresh` call with a *success* instead of
an error, `database` was likely already left revoked by a previous failed run — check
`GET /api/v1/clients/database` and unrevoke manually before re-running.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector
git add src/e2e/lifecycle_test.go
git commit -m "$(cat <<'EOF'
test(e2e): add client revoke/reissue lifecycle subtest

First step of a new e2e test that exercises credential lifecycle,
policy-driven backup, job visibility, and catalog replication against
the already-running demo lab: revoke an enrolled client's certificate,
confirm certclient operating-refresh is refused while revoked, then
confirm it succeeds again after unrevoke.

See docs/superpowers/specs/2026-08-05-e2e-lifecycle-test-design.md.
EOF
)"
```

---

### Task 2: Create-minute-policy-and-observe-backup-job subtest

**Files:**
- Modify: `src/e2e/lifecycle_test.go`

**Interfaces:**
- Consumes: `apiRequest`, `requireStatus`, `decodeJSON`, `dockerComposeExec` from Task 1.
- Produces: `fetchStoragePolicyID(t *testing.T, name string) string`, `waitForBackupJob(t
  *testing.T, sourceHost, policyName string, since int64)` — `waitForBackupJob` is consumed by Task
  3 indirectly only through the `policyName` variable it validates, not called directly by Task 3.

- [ ] **Step 1: Add `fmt` and `strings` to the import block**

```go
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 2: Replace the tail of the file** — this adds policy-deletion cleanup, the second
  subtest, and its two new helper functions

Find this block (the end of `TestE2E_ClientLifecycle` from Task 1):

```go
func TestE2E_ClientLifecycle(t *testing.T) {
	const hostname = "database"

	t.Cleanup(func() {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)
	})

	t.Run("revoke_locks_out_then_reissue_restores", func(t *testing.T) {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/revoke", nil), http.StatusOK)

		out, err := dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.Error(t, err, "certclient operating-refresh should fail while revoked, output: %s", out)

		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)

		out, err = dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.NoError(t, err, "certclient operating-refresh should succeed after unrevoke, output: %s", out)
	})
}
```

Replace it with:

```go
func TestE2E_ClientLifecycle(t *testing.T) {
	const hostname = "database"

	var policyID string
	t.Cleanup(func() {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)
	})
	t.Cleanup(func() {
		if policyID == "" {
			return
		}
		requireStatus(t, apiRequest(t, http.MethodDelete, "/api/v1/policies/"+policyID, nil), http.StatusNoContent)
	})

	t.Run("revoke_locks_out_then_reissue_restores", func(t *testing.T) {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/revoke", nil), http.StatusOK)

		out, err := dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.Error(t, err, "certclient operating-refresh should fail while revoked, output: %s", out)

		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)

		out, err = dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.NoError(t, err, "certclient operating-refresh should succeed after unrevoke, output: %s", out)
	})

	var policyName string
	t.Run("create_minute_policy_triggers_backup_job", func(t *testing.T) {
		storagePolicyID := fetchStoragePolicyID(t, "store")

		policyName = fmt.Sprintf("e2e-lifecycle-%d", time.Now().Unix())
		createdAt := time.Now().Unix()

		resp := apiRequest(t, http.MethodPost, "/api/v1/policies", map[string]any{
			"name":              policyName,
			"client_filters":    map[string]any{"hostnames": []string{hostname}},
			"object_filters":    []map[string]any{{"path": "/var/lib/dbdata"}},
			"rpo":               "1m",
			"backup_window":     []string{"* * * * *"},
			"storage_policy_id": storagePolicyID,
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var created struct {
			ID string `json:"id"`
		}
		decodeJSON(t, resp, &created)
		require.NotEmpty(t, created.ID)
		policyID = created.ID

		out, err := dockerComposeExec(t, hostname, "./policyclient", "fetch")
		require.NoError(t, err, "policyclient fetch failed, output: %s", out)

		waitForBackupJob(t, hostname, policyName, createdAt)
	})
}

// fetchStoragePolicyID looks up an existing storage-typed policy by name
// and returns its ID -- resolved live rather than hardcoded, since a
// hardcoded UUID would silently break if the demo's storage policy is ever
// recreated with a new ID.
func fetchStoragePolicyID(t *testing.T, name string) string {
	t.Helper()

	resp := apiRequest(t, http.MethodGet, "/api/v1/policies?type=storage", nil)
	var listResp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	decodeJSON(t, resp, &listResp)

	for _, p := range listResp.Data {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("no storage policy named %q found", name)
	return ""
}

// waitForBackupJob polls GET /api/v1/jobs until a backup job produced by
// policyName for sourceHost reaches a terminal "success" state, or fails
// the test after a 90s timeout -- generous because it spans a full
// reconcile tick (ReconcileIntervalSec, 30s in the demo) plus the brfs
// exec and Loki ingest.
func waitForBackupJob(t *testing.T, sourceHost, policyName string, since int64) {
	t.Helper()

	prefix := "backup:" + policyName + ":"
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp := apiRequest(t, http.MethodGet, fmt.Sprintf(
			"/api/v1/jobs?kind=backup&source_host=%s&since=%d", sourceHost, since), nil)
		var jobsResp struct {
			Data []struct {
				JobID string `json:"job_id"`
				State string `json:"state"`
			} `json:"data"`
		}
		decodeJSON(t, resp, &jobsResp)

		for _, j := range jobsResp.Data {
			if strings.HasPrefix(j.JobID, prefix) && j.State == "success" {
				return
			}
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("no successful backup job for source_host=%s policy=%s within 90s", sourceHost, policyName)
}
```

- [ ] **Step 3: Build-check under the e2e tag**

```bash
cd /home/alex/miniprotector/src
go build -tags=e2e ./e2e/...
go vet -tags=e2e ./e2e/...
```

Expected: both succeed with no errors.

- [ ] **Step 4: Run against the live demo lab**

```bash
cd /home/alex/miniprotector
docker compose -f demo/docker-compose.yml ps database
cd src && go test -tags=e2e -count=1 -timeout=150s -run TestE2E_ClientLifecycle -v ./e2e/...
```

Expected: `PASS`, both subtests shown in `-v` output. This run takes up to ~90s in the worst case
(waiting on `database`'s next 30s reconcile tick to notice the freshly-fetched policy, then the
`brfs` exec, then Loki ingest) — don't interrupt it early. If `create_minute_policy_triggers_backup_job`
fails with "no successful backup job," first check that step 4 of Task 1 didn't leave `database`
revoked (a revoked node's `agent` still runs but its `operating-refresh` policy will keep failing,
though existing valid certs continue working until TTL expiry — check
`GET /api/v1/clients/database` shows `"revoked": false`).

- [ ] **Step 5: Verify test-created policy was cleaned up**

```bash
curl -s -H "Authorization: Bearer dev-placeholder-token-change-me" http://localhost:8090/api/v1/policies?type=backup | python3 -c "import json,sys; print([p['name'] for p in json.load(sys.stdin)['data']])"
```

Expected: only the demo's original three policies (`audit-logs`, `database-backup`,
`webserver-backup`) — no `e2e-lifecycle-*` policy left behind, confirming `t.Cleanup` deleted it.

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector
git add src/e2e/lifecycle_test.go
git commit -m "$(cat <<'EOF'
test(e2e): add minute-policy backup job subtest to client lifecycle

Extends TestE2E_ClientLifecycle: creates a 1-minute recurring backup
policy targeting database's /var/lib/dbdata, forces an immediate
policy fetch (policyclient fetch) rather than waiting on the agent's
15-minute PolicyFetchIntervalSec, and polls GET /api/v1/jobs for the
resulting successful backup job. The created policy is deleted via
t.Cleanup.

See docs/superpowers/specs/2026-08-05-e2e-lifecycle-test-design.md.
EOF
)"
```

---

### Task 3: Catalog-entry subtest

**Files:**
- Modify: `src/e2e/lifecycle_test.go`

**Interfaces:**
- Consumes: `policyName` (set by Task 2's subtest), `apiRequest`, `decodeJSON` from Task 1.
- Produces: `waitForCatalogEntry(t *testing.T, sourceHost, jobName string)` — not consumed by any
  later task.

- [ ] **Step 1: Insert the third subtest**

Find this block (the end of Task 2's subtest and the function's closing brace):

```go
		waitForBackupJob(t, hostname, policyName, createdAt)
	})
}
```

Replace it with:

```go
		waitForBackupJob(t, hostname, policyName, createdAt)
	})

	t.Run("catalog_entry_appears_for_backup", func(t *testing.T) {
		waitForCatalogEntry(t, hostname, policyName)
	})
}
```

- [ ] **Step 2: Append the `waitForCatalogEntry` helper**

Find this block (the end of `waitForBackupJob`):

```go
	t.Fatalf("no successful backup job for source_host=%s policy=%s within 90s", sourceHost, policyName)
}
```

Replace it with:

```go
	t.Fatalf("no successful backup job for source_host=%s policy=%s within 90s", sourceHost, policyName)
}

// waitForCatalogEntry polls GET /api/v1/catalog until at least one entry
// tied to jobName appears for sourceHost, or fails the test after a 30s
// timeout -- catalog replication (catalogsync) polls every
// CatalogSyncPollIntervalSec (5s) once the backup job has already
// completed successfully.
func waitForCatalogEntry(t *testing.T, sourceHost, jobName string) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp := apiRequest(t, http.MethodGet, fmt.Sprintf(
			"/api/v1/catalog?source_host=%s&job_names=%s", sourceHost, jobName), nil)
		var catalogResp struct {
			Data []struct {
				ID int64 `json:"id"`
			} `json:"data"`
		}
		decodeJSON(t, resp, &catalogResp)

		if len(catalogResp.Data) > 0 {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("no catalog entry for source_host=%s job_names=%s within 30s", sourceHost, jobName)
}
```

- [ ] **Step 3: Build-check under the e2e tag**

```bash
cd /home/alex/miniprotector/src
go build -tags=e2e ./e2e/...
go vet -tags=e2e ./e2e/...
```

Expected: both succeed with no errors.

- [ ] **Step 4: Run the full test against the live demo lab**

```bash
cd /home/alex/miniprotector
docker compose -f demo/docker-compose.yml ps database
cd src && go test -tags=e2e -count=1 -timeout=150s -run TestE2E_ClientLifecycle -v ./e2e/...
```

Expected: `PASS`, all three subtests shown in `-v` output:
`revoke_locks_out_then_reissue_restores`, `create_minute_policy_triggers_backup_job`,
`catalog_entry_appears_for_backup`.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add src/e2e/lifecycle_test.go
git commit -m "$(cat <<'EOF'
test(e2e): add catalog entry subtest, completing client lifecycle test

Completes TestE2E_ClientLifecycle: after the backup job succeeds,
polls GET /api/v1/catalog for the replicated entry tied to the test
policy's name. All three subtests (revoke/reissue, policy+job,
catalog) now run as one ordered flow against the live demo lab.

See docs/superpowers/specs/2026-08-05-e2e-lifecycle-test-design.md.
EOF
)"
```

---

### Task 4: Bump `test-e2e` timeout and verify the full suite

**Files:**
- Modify: `Makefile:172`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing consumed by other tasks — purely a Makefile change.

- [ ] **Step 1: Update the timeout**

Current (`Makefile:171-172`):

```makefile
test-e2e: ## Run e2e smoke test against the running demo lab (run `make demo-up` first)
	cd src && go test -tags=e2e -count=1 -timeout=30s ./e2e/...
```

Replace with:

```makefile
test-e2e: ## Run e2e tests against the running demo lab (run `make demo-up` first)
	cd src && go test -tags=e2e -count=1 -timeout=120s ./e2e/...
```

(The help comment drops "smoke" since the suite is no longer just the one smoke test.)

- [ ] **Step 2: Run the full e2e suite via the Makefile target**

```bash
cd /home/alex/miniprotector
make test-e2e
```

Expected: `PASS` for both `TestE2E_WebUIAvailable` and `TestE2E_ClientLifecycle` (and its three
subtests).

- [ ] **Step 3: Commit**

```bash
cd /home/alex/miniprotector
git add Makefile
git commit -m "$(cat <<'EOF'
build: raise test-e2e timeout for the new client lifecycle test

The e2e suite is no longer a single sub-second smoke check -- the new
TestE2E_ClientLifecycle waits on real reconcile/policy-fetch/catalog-
sync intervals and can take up to ~90s. Raises -timeout from 30s to
120s and drops "smoke" from the help text since the suite covers more
than the original web-UI check.
EOF
)"
```

---

### Task 5: CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing.

- [ ] **Step 1: Add a new dated entry at the top, above the `2026-08-05 — catalog: ...` entry**

```markdown
## 2026-08-06 — e2e: add client lifecycle test (revoke/reissue, policy, job, catalog)

Adds `TestE2E_ClientLifecycle` alongside the existing demo-web-UI smoke test: revokes and reissues
an enrolled client's certificate (confirming `certclient operating-refresh` is refused while
revoked and succeeds again after unrevoke), creates a 1-minute recurring backup policy, and
confirms both a real backup job (`GET /api/v1/jobs`) and its replicated catalog entry
(`GET /api/v1/catalog`) appear. Runs against the already-running demo lab, same precondition as the
original smoke test. `make test-e2e`'s timeout grows from 30s to 120s to accommodate the new test's
real reconcile/policy-fetch/catalog-sync wait times.
```

- [ ] **Step 2: Commit**

```bash
cd /home/alex/miniprotector
git add CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: add changelog entry for e2e client lifecycle test
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** revoke/verify-locked-out/reissue (Task 1), 1-minute policy creation +
  `policyclient fetch` forcing + job polling (Task 2), catalog polling (Task 3), `t.Cleanup` for both
  unrevoke and policy deletion (introduced across Tasks 1–2, present for the life of the test),
  Makefile timeout bump (Task 4), CHANGELOG entry (Task 5) — every section of
  `docs/superpowers/specs/2026-08-05-e2e-lifecycle-test-design.md` maps to a task.
- **Placeholder scan:** no TBD/TODO; every step has literal code, file paths, or shell commands. The
  storage-policy-ID lookup is resolved live in code (`fetchStoragePolicyID`), not hardcoded.
- **Type/name consistency:** `hostname`, `policyID`, `policyName` are declared once in
  `TestE2E_ClientLifecycle` (Task 1/2) and referenced identically by every later task; no
  redeclaration. `fetchStoragePolicyID`, `waitForBackupJob`, `waitForCatalogEntry` each declared
  exactly once, called exactly where declared. `apiRequest`/`requireStatus`/`decodeJSON`/
  `dockerComposeExec` signatures (Task 1) are used unchanged in Tasks 2–3.
- **Live-verified assumptions:** every timing figure and JSON field name in this plan
  (`storage_policy_id`, `job_id` prefix format, `state`, catalog `job_names` filter, revoke/unrevoke
  status codes, `./certclient`/`./policyclient` invocation path, ~90s job / ~30s catalog windows) was
  confirmed by hand against the running demo lab before writing this plan, including diagnosing and
  clearing an unrelated pre-existing issue (host disk pressure was throttling Loki's writes, breaking
  `/api/v1/jobs` for any caller) that would otherwise have made Task 2/3's verification steps fail
  for reasons unrelated to this code.
