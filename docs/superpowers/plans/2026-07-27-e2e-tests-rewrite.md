# E2E Test Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete all three existing e2e-tagged test suites and replace them with a single minimal test that requires the demo lab (`make demo-up`) to already be running and checks its web UI responds with 2xx.

**Architecture:** `src/e2e/` becomes a single-file package (`e2e_test.go`, `//go:build e2e`) containing one test that does a plain `http.Get("http://localhost:8091")` with a 5s client timeout. No `TestMain`, no container orchestration, no polling. `src/cmd/issuer/e2e_test.go` and `src/cmd/log-gateway/e2e_test.go` are deleted outright with no replacement — their coverage (real step-ca minting, real Loki push/query) is not preserved by this change per the approved spec.

**Tech Stack:** Go 1.26, `net/http` (stdlib), `github.com/stretchr/testify v1.11.1` (already a dependency).

## Global Constraints

- Module: `github.com/alex-sviridov/miniprotector` (`src/go.mod`).
- New test file keeps the `//go:build e2e` tag and `package e2e` — this is what the Makefile's `-tags=e2e` flag and `./e2e/...` path both key off.
- Demo web UI URL is hardcoded to `http://localhost:8091` (fixed port `demo/docker-compose.yml` publishes for the `web` service) — not configurable, per spec.
- Do not touch `docs/components/*.md`, `docs/ARCHITECTURE.md`, or `docs/protocols/` — spec confirms no protocol/component behavior changed.

---

### Task 1: Delete the three existing e2e suites

**Files:**
- Delete: `src/e2e/catalog_test.go`
- Delete: `src/e2e/catalog_validate.go`
- Delete: `src/e2e/config.conf`
- Delete: `src/e2e/Dockerfile`
- Delete: `src/e2e/docker.go`
- Delete: `src/e2e/e2e_test.go`
- Delete: `src/e2e/testdata/certs/ca.crt`
- Delete: `src/e2e/testdata/certs/client.crt`
- Delete: `src/e2e/testdata/certs/client.key`
- Delete: `src/e2e/testdata.go`
- Delete: `src/e2e/validate.go`
- Delete: `src/cmd/issuer/e2e_test.go`
- Delete: `src/cmd/log-gateway/e2e_test.go`
- Modify: `src/go.mod`, `src/go.sum` (via `go mod tidy`)

**Interfaces:**
- Consumes: nothing — this is pure deletion.
- Produces: an empty `src/e2e/` directory (git doesn't track empty dirs, so it will vanish from `git status` until Task 2 adds a file back into it) and a repo that builds clean without the `e2e` tag.

- [ ] **Step 1: Delete every file in `src/e2e/` and the two cmd-level e2e test files**

```bash
cd /home/alex/miniprotector
git rm -r src/e2e
git rm src/cmd/issuer/e2e_test.go src/cmd/log-gateway/e2e_test.go
```

- [ ] **Step 2: Confirm nothing outside these files referenced them**

```bash
cd /home/alex/miniprotector/src
go build ./...
go vet ./...
```

Expected: both succeed with no errors. (The deleted files were all `//go:build e2e`-tagged or self-contained within packages whose non-test files don't reference their test-only helpers, so the default build — which excludes the `e2e` tag — was never affected. This step is a correctness check, not expected to surface anything.)

- [ ] **Step 3: Run `go mod tidy` to drop the now-unused `docker/docker` SDK dependency**

```bash
cd /home/alex/miniprotector/src
go mod tidy
git diff go.mod go.sum
```

Expected: `go.mod` loses the `github.com/docker/docker v27.5.1+incompatible` require line (and `go.sum` loses its corresponding entries). No other require lines should change — `smallstep/certificates` and any Loki-related packages stay, since they're used by non-e2e production code (`cmd/issuer/mintsign.go`, `cmd/log-gateway/server.go`, `cmd/api-server/loki.go`, etc.), not just the deleted test files.

- [ ] **Step 4: Run the full non-e2e test suite to confirm nothing else broke**

```bash
cd /home/alex/miniprotector/src
go test ./...
```

Expected: PASS (or the same pre-existing pass/fail state as before this change — no new failures caused by the deletion).

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add -A src/e2e src/cmd/issuer/e2e_test.go src/cmd/log-gateway/e2e_test.go src/go.mod src/go.sum
git commit -m "$(cat <<'EOF'
test(e2e): remove existing e2e suites

Removes src/e2e (Docker-built brfs/bwfs flow), cmd/issuer/e2e_test.go
(real step-ca), and cmd/log-gateway/e2e_test.go (real Loki container)
ahead of replacing all three with a single minimal smoke test against
the demo lab's web UI.

See docs/superpowers/specs/2026-07-27-e2e-tests-rewrite-design.md.
EOF
)"
```

---

### Task 2: Add the minimal demo web UI smoke test

**Files:**
- Create: `src/e2e/e2e_test.go`

**Interfaces:**
- Consumes: `demo/docker-compose.yml`'s `web` service, expected reachable at `http://localhost:8091` (no Go-level dependency — this is a live HTTP precondition, not an import).
- Produces: `TestE2E_WebUIAvailable`, the only test in `package e2e`. No other task depends on symbols from this file.

- [ ] **Step 1: Write the test**

```go
//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestE2E_WebUIAvailable is a smoke test against an already-running demo lab
// (`make demo-up`) -- it does not start, wait for, or manage the stack
// itself. It only confirms the `web` service (published at localhost:8091 by
// demo/docker-compose.yml) is up and serving.
func TestE2E_WebUIAvailable(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get("http://localhost:8091")
	require.NoError(t, err, "demo web UI unreachable at http://localhost:8091 -- is `make demo-up` running?")
	defer resp.Body.Close()

	require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300,
		"expected 2xx from demo web UI, got %d", resp.StatusCode)
}
```

- [ ] **Step 2: Verify the test fails cleanly when the demo lab is down**

```bash
cd /home/alex/miniprotector
docker compose -f demo/docker-compose.yml down -v 2>/dev/null || true
cd src && go test -tags=e2e -timeout=30s -run TestE2E_WebUIAvailable -v ./e2e/...
```

Expected: FAIL, with the test output containing `demo web UI unreachable at http://localhost:8091 -- is \`make demo-up\` running?` (not a bare unannotated connection error).

- [ ] **Step 3: Bring the demo lab up and verify the test passes**

```bash
cd /home/alex/miniprotector
make demo-up
```

Expected: script completes with `Demo stack is up.` (this takes a few minutes — it builds 11 images and enrolls each node in turn).

```bash
cd /home/alex/miniprotector/src
go test -tags=e2e -timeout=30s -run TestE2E_WebUIAvailable -v ./e2e/...
```

Expected: `PASS`, `ok github.com/alex-sviridov/miniprotector/e2e`.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector
git add src/e2e/e2e_test.go
git commit -m "$(cat <<'EOF'
test(e2e): add minimal demo web UI smoke test

Replaces the deleted e2e suites with a single test that requires the
demo lab to already be running (make demo-up) and checks its web UI
responds with 2xx at http://localhost:8091. No container orchestration
or polling -- the demo being up is a precondition, not something this
test manages.

See docs/superpowers/specs/2026-07-27-e2e-tests-rewrite-design.md.
EOF
)"
```

---

### Task 3: Update Makefile and README to describe the new precondition

**Files:**
- Modify: `Makefile:171-172`
- Modify: `README.md:98-99`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing consumed by other tasks — purely user-facing doc/help text.

- [ ] **Step 1: Update the `test-e2e` target's help comment and timeout in `Makefile`**

Current (`Makefile:171-172`):

```makefile
test-e2e: ## Run Docker-based e2e tests (requires Docker daemon, ~3 min)
	cd src && go test -tags=e2e -timeout=300s ./e2e/...
```

Replace with:

```makefile
test-e2e: ## Run e2e smoke test against the running demo lab (run `make demo-up` first)
	cd src && go test -tags=e2e -timeout=30s ./e2e/...
```

- [ ] **Step 2: Update the matching comment in `README.md`**

Current (`README.md:98-99`):

```
# Run Docker-based e2e tests (requires Docker daemon, ~3 min)
make test-e2e
```

Replace with:

```
# Run e2e smoke test against the running demo lab (run `make demo-up` first)
make test-e2e
```

- [ ] **Step 3: Verify the target still runs correctly with the new timeout**

```bash
cd /home/alex/miniprotector
make test-e2e
```

Expected: `PASS` (the demo lab from Task 2 Step 3 should still be up; if it was torn down, run `make demo-up` again first).

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector
git add Makefile README.md
git commit -m "$(cat <<'EOF'
docs: update test-e2e help text for demo-lab precondition

The e2e suite no longer builds its own Docker images and containers --
it's a single HTTP smoke test that requires `make demo-up` to already
be running. Updates the Makefile help text and README quick-start
comment to match, and drops the timeout from 300s to 30s.
EOF
)"
```

---

### Task 4: Add CHANGELOG entry

**Files:**
- Modify: `CHANGELOG.md` (add new entry at the top, most-recent-first)

**Interfaces:**
- Consumes: nothing.
- Produces: nothing.

- [ ] **Step 1: Read the current top of `CHANGELOG.md` to match its existing entry format**

```bash
head -30 /home/alex/miniprotector/CHANGELOG.md
```

- [ ] **Step 2: Add a new dated entry at the top, above the most recent existing entry, following that same heading/paragraph style**

Content to add (adjust heading level/date format to match whatever Step 1 showed, but keep this text):

```markdown
## 2026-07-27 — E2E test suite rewrite

Removed all three existing e2e-tagged test suites (`src/e2e`'s Docker-built brfs/bwfs backup
flow, `cmd/issuer`'s real step-ca test, and `cmd/log-gateway`'s real Loki test) and replaced them
with a single minimal smoke test that requires the demo lab (`make demo-up`) to already be
running and checks its web UI responds. The old suites were slow (~3 min) and duplicated
infrastructure the repo already has in `demo/`; the new test trades that coverage for a fast,
simple check that the demo stack is genuinely reachable end-to-end.
```

- [ ] **Step 3: Commit**

```bash
cd /home/alex/miniprotector
git add CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: add changelog entry for e2e test suite rewrite
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** deletion of all three suites (Task 1), new minimal test with exact hardcoded URL, no `TestMain`/polling, 5s client timeout, plain 2xx check (Task 2), Makefile/README precondition updates (Task 3), CHANGELOG entry (Task 4) — every section of the spec maps to a task.
- **Placeholder scan:** no TBD/TODO; every step has literal file paths, literal code, or literal shell commands.
- **Type/name consistency:** `TestE2E_WebUIAvailable` is the only symbol introduced and it's not referenced by any other task. `http://localhost:8091` is used identically in Task 2 (test code) and Task 3 (docs) — matches `demo/docker-compose.yml`'s published port for `web`.
