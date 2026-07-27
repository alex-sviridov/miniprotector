# Design: docker-build-consolidation follow-up fixes

**Date:** 2026-07-27
**Status:** Approved for planning

## Problem

The [docker-build-consolidation](2026-07-27-docker-build-consolidation-design.md) work's final review
surfaced four small issues, none of which blocked that merge but all of which are worth fixing:

1. `demo/local.conf` is missing a config key, causing `api-server`/`web` to crash-loop on a fresh
   demo stack.
2. `deploy/build/Dockerfile`'s `ca` stage is demo-only but generically named, unlike its sibling
   `issuer-demo`/`issuer-controlplane` pair.
3. `src/e2e/Dockerfile` is an 8th independent Go builder stage that never got the BuildKit cache
   mounts the consolidation added everywhere else.
4. `demo/README.md` states a stale image count.

These four are unrelated — different files, no shared code path, no ordering dependency — bundled
into one plan purely so they land as a single small maintenance PR rather than four.

## Fixes

### 1. `demo/local.conf`: add `clientmanager_admin_api_host`

`api-server` (`src/cmd/api-server`) dials `ClientManagerAdminAPIHost:ClientManagerAdminAPIPort` to
reach `clientmanager-admin-api`. `demo/local.conf` sets `clientmanager_api_host` but never
`clientmanager_admin_api_host`, so `config.ParseConfig` leaves that field empty and `api-server`
tries to dial `":9501"`, fails, and `os.Exit(1)`s — Compose restarts it forever, and `web`
(depends on `api-server`) crash-loops downstream of that.

Fix: add one line to `demo/local.conf`, mirroring the working control-plane config
(`deploy/control-plane/api-server/local.conf:25`):

```
clientmanager_admin_api_host=clientmanager-api
```

Placed next to the existing `clientmanager_api_host=clientmanager-api` line for locality.

### 2. Rename `deploy/build/Dockerfile`'s `ca` stage to `ca-demo`

Every other demo-only stage in this file that has a real control-plane counterpart is named with an
explicit `-demo` suffix (`issuer-demo` vs. `issuer-controlplane`). `ca` breaks that pattern — it's
demo-only (the control-plane's `step-ca` service uses the plain prebuilt image, no `build:` block,
no `clientmanager` CLI bundled in), but its name gives no signal of that. A future edit wiring
`step-ca` to `target: ca` would silently swap in the demo's `clientmanager`-bundling entrypoint.

Fix: `deploy/build/Dockerfile:48`, `FROM smallstep/step-ca AS ca` → `FROM smallstep/step-ca AS
ca-demo`. One consumer to update: `demo/docker-compose.yml:6`, `target: ca` → `target: ca-demo`.
No other file references this stage name.

### 3. `src/e2e/Dockerfile`: add cache mounts

This file builds `brfs bwfs rwfs catalog catalogsync` in its own `golang:1.26` stage, independent of
`deploy/build/Dockerfile` — it can't consume the shared `builder` stage because
`src/e2e/docker.go`'s `addDirToTar` hand-walks `src/` to build the image's context (see that file's
comment and `.dockerignore`'s own note about it), rather than going through `docker build`'s normal
CLI/SDK context builder that `deploy/build/Dockerfile` relies on. Consolidating it into the shared
file is out of scope for this fix — it would mean either changing how `src/e2e/docker.go` builds
its context, or giving up the repo-root `COPY . .` this file doesn't currently do (it only ever
walks `src/`). This fix is narrower: give this standalone stage the same incremental-rebuild benefit
the consolidation added everywhere else, independent of that larger question.

Fix: add the same two mounts already used in `deploy/build/Dockerfile` to this file's one `RUN` line:

```dockerfile
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs catalog catalogsync
```

### 4. `demo/README.md`: fix the stale image count

Line 18 reads "Builds all eight images" — written before this consolidation and never updated
despite already having drifted (it was wrong even on `main` before the consolidation branch, per the
final review). `demo/docker-compose.yml` has 11 services with a `build:` block (`ca`, `issuer`,
`clientmanager-api`, `catalog`, `api-server`, `policy-server`, `log-gateway`, `database`,
`webserver`, `store`, `web`). Change "eight" to "11".

## Testing plan

- **Fix 1:** `cd demo && docker compose down -v && cd .. && make demo-up`; confirm
  `docker compose -f demo/docker-compose.yml ps` shows `api-server` and `web` both `Up`, not
  `Restarting`. Re-run the existing smoke test from `demo/README.md` (backup/list/verify/catalog
  query) to confirm nothing else regressed.
- **Fix 2:** `docker compose -f demo/docker-compose.yml config` resolves with no error;
  `docker build -f deploy/build/Dockerfile --target ca-demo .` builds successfully;
  `grep -rn 'AS ca\b\|target: ca\b'` (word-boundary, so it doesn't match `ca-demo`) finds no
  remaining reference to the old stage name anywhere in the repo.
- **Fix 3:** `make test-e2e` passes (this target is the only consumer of `src/e2e/Dockerfile` —
  `src/e2e/docker.go` builds this image programmatically before running the e2e suite).
- **Fix 4:** visual confirmation the line now reads "11 images" and that count matches
  `grep -c '^\s*build:' demo/docker-compose.yml`.

## Documentation

Per `.claude/CLAUDE.md`'s feature-change rules: no `docs/components/*.md` or `docs/ARCHITECTURE.md`
changes are needed — none of these four fixes change a command, flag, or system topology, they're a
config correction, an internal stage rename, a build-cache addition, and a doc typo fix. Add one
`CHANGELOG.md` entry covering all four, since they land as one PR.

## Out of scope

- Consolidating `src/e2e/Dockerfile` into `deploy/build/Dockerfile` — real option, bigger change
  (would need `src/e2e/docker.go`'s context-building approach revisited), not part of this
  narrowly-scoped fix.
- Any other stale-documentation sweep beyond the one line identified in Fix 4.
