# Design: E2E Test Rewrite — Minimal Demo Smoke Test

## Problem

The repo currently has three separate `//go:build e2e` test suites, each standing up its own
throwaway infrastructure from scratch:

- `src/e2e/` — builds a Docker image and runs `brfs`/`bwfs` containers directly to exercise the
  backup/verify flow (`e2e_test.go`, `docker.go`, `catalog_test.go`, `catalog_validate.go`,
  `testdata.go`, `validate.go`, plus `config.conf`, `Dockerfile`, and a `testdata/` tree).
- `src/cmd/issuer/e2e_test.go` — spins up a real `step-ca` to test certificate minting/signing.
- `src/cmd/log-gateway/e2e_test.go` — spins up a real Loki container to test authenticated log
  push/query.

These are heavyweight, slow (the Makefile documents `src/e2e` alone as "~3 min"), and duplicate
infrastructure the repo already has in `demo/` (a full `docker compose` stack — CA, issuer,
catalog, policy-server, and three backup-capable nodes, mutually enrolled via mTLS). They're being
replaced wholesale with a minimal suite that assumes the demo lab is already running and performs
a single smoke check against it.

## Scope

Remove all three suites entirely and replace them with one minimal test.

## What's removed

- `src/e2e/` — every file: `e2e_test.go`, `docker.go`, `catalog_test.go`, `catalog_validate.go`,
  `testdata.go`, `validate.go`, `config.conf`, `Dockerfile`, `testdata/`.
- `src/cmd/issuer/e2e_test.go`.
- `src/cmd/log-gateway/e2e_test.go`.

All helper functions used by these suites (`requireDocker`, `startTestCA`, etc.) are defined
inside the files being deleted, so removal doesn't orphan anything elsewhere. The `docker/docker`
Go SDK dependency is imported only by `src/e2e`; `go mod tidy` will drop it from `go.mod`/`go.sum`
as a side effect. No other package imports anything from these three files.

## What's added

A single new file, `src/e2e/e2e_test.go` — the only file remaining in the directory — under the
same `//go:build e2e` tag and `package e2e`, containing one test:

```go
func TestE2E_WebUIAvailable(t *testing.T) {
    client := &http.Client{Timeout: 5 * time.Second}
    resp, err := client.Get("http://localhost:8091")
    require.NoError(t, err, "demo web UI unreachable at http://localhost:8091 — is `make demo-up` running?")
    defer resp.Body.Close()
    require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300,
        "expected 2xx from demo web UI, got %d", resp.StatusCode)
}
```

Design choices:

- **No `TestMain`, no container orchestration, no polling/retry.** The demo lab being up
  (`make demo-up`) is a precondition the test does not manage or wait on. If it's down, the test
  fails immediately with a clear message rather than hanging.
- **URL is hardcoded** to `http://localhost:8091` — the fixed host port
  `demo/docker-compose.yml` publishes for the `web` service. Not made configurable; this is a
  single fixed target with no other environment to point at.
- **Plain HTTP 200 check, no body inspection.** Confirms the container is up and serving; doesn't
  assert anything about the Vue app's rendered content.

## Supporting changes

- **`Makefile`**: `test-e2e` target's help comment changes from
  `## Run Docker-based e2e tests (requires Docker daemon, ~3 min)` to reflect the new
  precondition (demo lab must already be running via `make demo-up`). The `-timeout=300s` flag is
  reduced (e.g. to `30s`) since the new suite is a single HTTP request, not a multi-minute Docker
  build-and-run flow.
- **`README.md`**: the comment above `make test-e2e` in the quick-start/documentation section is
  updated to match the new precondition.
- **No changes** to `docs/components/*.md`, `docs/ARCHITECTURE.md`, or `docs/protocols/` — no
  protocol or component behavior changes here, only test infrastructure.
- **`CHANGELOG.md`**: an entry is added when this change merges to `main`, per project convention.

## Testing the change itself

- With the demo lab down: `make test-e2e` fails with the custom "demo web UI unreachable" message
  (not a bare connection-refused stack trace).
- With the demo lab up (`make demo-up`): `make test-e2e` passes.
