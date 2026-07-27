# Docker Consolidation Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix four small, independent issues surfaced by the docker-build-consolidation branch's final review: a demo config gap that crash-loops `api-server`/`web`, a misleadingly-generic Dockerfile stage name, a missing cache-mount on an unrelated Dockerfile, and a stale doc count.

**Architecture:** Four unrelated one-file (or two-file) fixes, bundled into one plan for PR convenience. No shared code path or ordering dependency between them.

**Tech Stack:** Docker/BuildKit, Docker Compose, Go, Markdown.

## Global Constraints

- Each fix is independently testable and touches only the files named in its own task — no fix should require touching a file owned by another task in this plan.
- No behavior change beyond what each fix explicitly describes (e.g. Task 2's rename must not change what the `ca-demo` stage builds, only its name).

---

## Task 1: Fix `demo/local.conf` missing `clientmanager_admin_api_host`

**Files:**
- Modify: `demo/local.conf`

**Interfaces:** None — this is a config value, not code.

- [ ] **Step 1: Add the missing config line**

In `demo/local.conf`, immediately after the existing line 32 (`clientmanager_api_host=clientmanager-api`), add:

```
clientmanager_admin_api_host=clientmanager-api
```

So that section of the file reads:

```
# Where api-server dials clientmanager-api.
clientmanager_api_host=clientmanager-api
clientmanager_admin_api_host=clientmanager-api
```

- [ ] **Step 2: Bring the demo stack up from a clean slate and verify**

```bash
cd demo && docker compose down -v; cd ..
make demo-up
```

Expected: exits 0. Then:

```bash
docker compose -f demo/docker-compose.yml ps
```

Expected: every service, including `api-server` and `web`, shows `Up` — neither should show
`Restarting`.

- [ ] **Step 3: Re-run the existing smoke test to confirm nothing else regressed**

```bash
docker compose -f demo/docker-compose.yml exec database ./brfs /var/lib/dbdata --destination store:8080
docker compose -f demo/docker-compose.yml exec database ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec database ./rwfs verify store:8080
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
```

Expected: `brfs` reports success with 0 failures; `rwfs list` shows the backed-up files; `rwfs
verify` reports `warnings=0`; the `sqlite3` query returns rows for the backed-up files.

- [ ] **Step 4: Tear down and commit**

```bash
cd demo && docker compose down -v; cd ..
git add demo/local.conf
git commit -m "fix(demo): add missing clientmanager_admin_api_host to demo/local.conf"
```

---

## Task 2: Rename `deploy/build/Dockerfile`'s `ca` stage to `ca-demo`

**Files:**
- Modify: `deploy/build/Dockerfile`
- Modify: `demo/docker-compose.yml`

**Interfaces:**
- Produces: a stage named `ca-demo` (was `ca`) — no other task in this plan or the prior
  consolidation plan depends on the old name.

- [ ] **Step 1: Rename the stage in `deploy/build/Dockerfile`**

Find this line (currently the only `FROM smallstep/step-ca` line in the file):

```dockerfile
FROM smallstep/step-ca AS ca
```

Change it to:

```dockerfile
FROM smallstep/step-ca AS ca-demo
```

Nothing else in that stage's body changes.

- [ ] **Step 2: Update the one consumer, `demo/docker-compose.yml`**

Find the `ca` service's `build:` block:

```yaml
  ca:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: ca
```

Change `target: ca` to `target: ca-demo`:

```yaml
  ca:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: ca-demo
```

(The service is still named `ca` in the compose file — only the Dockerfile `target:` value
changes.)

- [ ] **Step 3: Confirm no other reference to the old stage name remains**

```bash
grep -rn "target: ca$\|AS ca$" --include="*.yml" --include="Dockerfile" .
```

Expected: no matches (the word-boundary-safe patterns `target: ca$` and `AS ca$` won't match
`target: ca-demo` or `AS ca-demo`, since those don't end the line at `ca`).

- [ ] **Step 4: Verify the compose file still resolves and the renamed stage still builds**

```bash
docker compose -f demo/docker-compose.yml config -q
docker build -f deploy/build/Dockerfile --target ca-demo -t ca-demo-check .
docker run --rm --user root ca-demo-check ldd /usr/local/bin/clientmanager
docker rmi ca-demo-check
```

Expected: `config -q` exits 0 with no output; the build succeeds; `ldd` reports "not a dynamic
executable" (confirming the `clientmanager` binary inside is still the statically-linked one — this
rename touched only the stage name, not its content).

- [ ] **Step 5: Commit**

```bash
git add deploy/build/Dockerfile demo/docker-compose.yml
git commit -m "refactor(build): rename ca stage to ca-demo for naming symmetry with issuer-demo"
```

---

## Task 3: Add BuildKit cache mounts to `src/e2e/Dockerfile`

**Files:**
- Modify: `src/e2e/Dockerfile`

**Interfaces:** None — this stage is not consumed by `deploy/build/Dockerfile` or any other file in
this plan; it's built standalone by `src/e2e/docker.go` before the e2e test suite runs.

- [ ] **Step 1: Add cache mounts to the one `RUN` line that compiles binaries**

`src/e2e/Dockerfile` currently has:

```dockerfile
WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs catalog catalogsync
```

Change the `RUN` line to:

```dockerfile
WORKDIR /build
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs catalog catalogsync
```

- [ ] **Step 2: Run the e2e suite to confirm this stage still builds and works**

```bash
make test-e2e
```

Expected: passes (same result as before this change — this is a build-cache addition only, no
change to which binaries are built or how the runtime stage below it is assembled).

- [ ] **Step 3: Commit**

```bash
git add src/e2e/Dockerfile
git commit -m "build(e2e): add go build/mod cache mounts to src/e2e/Dockerfile"
```

---

## Task 4: Fix `demo/README.md`'s stale image count, then add the CHANGELOG entry

**Files:**
- Modify: `demo/README.md`
- Modify: `CHANGELOG.md`

**Interfaces:** None — documentation only. This task runs last because its CHANGELOG entry
describes the whole PR (all of Tasks 1-3).

- [ ] **Step 1: Fix the stale count in `demo/README.md`**

Find this line (currently line 18):

```markdown
Equivalent to `./demo/up.sh` directly. Builds all eight images, brings up `ca` and `issuer` first,
```

Change "eight" to "11":

```markdown
Equivalent to `./demo/up.sh` directly. Builds all 11 images, brings up `ca` and `issuer` first,
```

- [ ] **Step 2: Confirm the new count is accurate**

```bash
grep -c "^\s*build:" demo/docker-compose.yml
```

Expected: `11`.

- [ ] **Step 3: Add the CHANGELOG entry**

In `CHANGELOG.md`, insert a new entry immediately after the intro line and before the existing
topmost entry (`## 2026-07-27 — build: consolidate demo/control-plane Dockerfiles`):

```markdown
## 2026-07-27 — build: docker-consolidation follow-up fixes

Four small fixes to the demo/control-plane Docker consolidation: `demo/local.conf` was missing
`clientmanager_admin_api_host`, which crash-looped `api-server` (and `web`, downstream of it) on
every fresh demo stack — added, mirroring the working control-plane config. `deploy/build/Dockerfile`'s
`ca` stage is renamed to `ca-demo`, matching the `issuer-demo`/`issuer-controlplane` naming
convention, since it's demo-only but was previously named ambiguously enough that wiring the
control-plane's `step-ca` service to it would have silently installed the wrong entrypoint.
`src/e2e/Dockerfile` — an independent Go builder stage the consolidation didn't touch, since it
can't share `deploy/build/Dockerfile`'s `builder` stage — gains the same `--mount=type=cache`
mounts every other Dockerfile got, for the same incremental-rebuild benefit. `demo/README.md`'s
stale "eight images" is corrected to 11, the current count of build-based demo services.
```

- [ ] **Step 4: Commit**

```bash
git add demo/README.md CHANGELOG.md
git commit -m "docs: fix stale demo image count and add follow-ups changelog entry"
```
