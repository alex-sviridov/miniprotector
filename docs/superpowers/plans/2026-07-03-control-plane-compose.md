# Control-Plane Compose Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge the `ca/` and `catalog/` deploy directories into a single `deploy/control-plane/` directory with one `docker-compose.yml` that runs both as independently-startable services, plus a `make control-plane-up` target that initializes and starts the stack.

**Architecture:** `ca/` and `catalog/`'s deployment files (Dockerfile/entrypoint/compose fragments/config/README) move into per-service subfolders under `deploy/control-plane/`. A single `docker-compose.yml` at that level defines two services, `step-ca` and `catalog`, each startable alone or together. `certrequest`'s hardcoded default paths and one e2e test that reuses the real `ca/` compose fixture are updated to match; docs are updated to the new paths.

**Tech Stack:** Docker Compose, Go 1.26, `smallstep/step-ca`, `make`.

## Global Constraints

- The compose service names are `step-ca` and `catalog` — `step-ca` (not `ca`) because `src/cmd/certrequest/e2e_test.go` already hardcodes `docker compose port step-ca 9000`, and this plan avoids touching that assumption.
- Each service must remain independently startable from the one compose file (`docker compose up -d step-ca` / `up -d catalog`), per the approved design's distributed-future requirement.
- No automation of the `certrequest` → `MP_CERT_TOKEN` → `certclient` enrollment step — it stays manual/out-of-band.
- Do not edit anything under `docs/superpowers/specs/` or `docs/superpowers/plans/` other than this plan and its own spec — those are historical records.
- Full design reference: `docs/superpowers/specs/2026-07-03-control-plane-compose-design.md`.

---

### Task 1: Relocate ca/catalog deploy files into deploy/control-plane/ and add the combined compose file

**Files:**
- Move: `ca/entrypoint.sh` → `deploy/control-plane/ca/entrypoint.sh`
- Move: `catalog/Dockerfile` → `deploy/control-plane/catalog/Dockerfile`
- Move: `catalog/entrypoint.sh` → `deploy/control-plane/catalog/entrypoint.sh`
- Move: `catalog/local.conf` → `deploy/control-plane/catalog/local.conf`
- Delete: `ca/docker-compose.yml`, `ca/.gitignore`, `catalog/docker-compose.yml`
- Create: `deploy/control-plane/docker-compose.yml`
- Create: `deploy/control-plane/.gitignore`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `deploy/control-plane/docker-compose.yml` defining services `step-ca` (port 9000) and `catalog` (port 15723), consumed by Task 2 (test paths), Task 4 (README), and Task 5 (`make control-plane-up`). `deploy/control-plane/ca/data/secrets/password`, `.../ca/data/certs/root_ca.crt`, `.../ca/data/config/defaults.json` are the runtime paths `certrequest`'s new defaults (Task 2) point at.

- [ ] **Step 1: Create the destination directories**

```bash
mkdir -p deploy/control-plane/ca deploy/control-plane/catalog
```

- [ ] **Step 2: Move the files with git mv**

```bash
git mv ca/entrypoint.sh deploy/control-plane/ca/entrypoint.sh
git mv catalog/Dockerfile deploy/control-plane/catalog/Dockerfile
git mv catalog/entrypoint.sh deploy/control-plane/catalog/entrypoint.sh
git mv catalog/local.conf deploy/control-plane/catalog/local.conf
git rm ca/docker-compose.yml ca/.gitignore catalog/docker-compose.yml
```

`ca/README.md` and `catalog/README.md` are handled in Task 4 (they get replaced, not moved) — leave them in place for now.

- [ ] **Step 3: Fix the COPY path inside the moved catalog Dockerfile**

The Dockerfile's build context will now be the repo root reached via `context: ../..` from `deploy/control-plane/docker-compose.yml`, so the `COPY` source path needs the new prefix.

In `deploy/control-plane/catalog/Dockerfile`, change:

```dockerfile
COPY catalog/entrypoint.sh ./entrypoint.sh
```

to:

```dockerfile
COPY deploy/control-plane/catalog/entrypoint.sh ./entrypoint.sh
```

The full file should now read:

```dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make catalog certclient

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/catalog /build/bin/certclient ./
COPY deploy/control-plane/catalog/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 4: Write the combined docker-compose.yml**

Create `deploy/control-plane/docker-compose.yml`:

```yaml
services:
  step-ca:
    image: smallstep/step-ca
    volumes:
      - ./ca/data:/home/step
      - ./ca/entrypoint.sh:/home/step/entrypoint.sh:ro
    ports:
      - "9000:9000"
    entrypoint: ["/home/step/entrypoint.sh"]
    restart: unless-stopped

  catalog:
    build:
      context: ../..
      dockerfile: deploy/control-plane/catalog/Dockerfile
    depends_on:
      - step-ca
    volumes:
      - ./catalog/data:/data
      - ./catalog/local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - STORAGE_PATH=/data/storage
    ports:
      - "15723:15723"
    restart: unless-stopped
```

- [ ] **Step 5: Write deploy/control-plane/.gitignore**

Create `deploy/control-plane/.gitignore`:

```
ca/data/
catalog/data/
```

- [ ] **Step 6: Verify step-ca starts standalone and the catalog image builds**

```bash
cd deploy/control-plane
mkdir -p ca/data/secrets
openssl rand -base64 32 > ca/data/secrets/password
docker compose up -d step-ca
sleep 3
docker compose ps
```

Expected: a `step-ca` row with state `running` (or `Up`), not `restarting`.

```bash
docker compose logs step-ca | tail -20
```

Expected: no fatal errors; a line indicating the CA initialized/started (e.g. mentions `step-ca` serving).

```bash
docker compose build catalog
```

Expected: build succeeds (exit code 0) — this confirms the `COPY deploy/control-plane/catalog/entrypoint.sh` path from Step 3 is correct and the `context: ../..` / `dockerfile:` paths in Step 4 resolve.

- [ ] **Step 7: Clean up the verification containers**

```bash
docker compose down -v
cd ../..
```

- [ ] **Step 8: Commit**

```bash
git add deploy/control-plane ca catalog
git status
git commit -m "feat(deploy): relocate ca/catalog into deploy/control-plane/, add combined compose"
```

(`git status` first to confirm `ca/` and `catalog/` now only show the README.md files as remaining tracked content — expected, handled in Task 4.)

---

### Task 2: Update certrequest's default paths and fix the e2e test's fixture paths

**Files:**
- Modify: `src/cmd/certrequest/arguments.go:35-38`
- Modify: `src/cmd/certrequest/e2e_test.go` (comment at lines 37-38/66, fixture paths at lines 55-63, 82, 88)

**Interfaces:**
- Consumes: `deploy/control-plane/docker-compose.yml`, `deploy/control-plane/ca/entrypoint.sh` from Task 1; the `step-ca` service name from the Global Constraints.
- Produces: `certrequest`'s three flags (`--defaults-file`, `--root`, `--password-file`) now default to `deploy/control-plane/ca/data/...` paths, which Task 4's README examples rely on being correct.

- [ ] **Step 1: Update the default flag paths in arguments.go**

In `src/cmd/certrequest/arguments.go`, change lines 35-38 from:

```go
	cmd.Flags().StringVar(&defaultsFile, "defaults-file", "ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	cmd.Flags().StringVar(&args.RootFile, "root", "ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "ca/data/secrets/password", "Path to the provisioner password file")
```

to:

```go
	cmd.Flags().StringVar(&defaultsFile, "defaults-file", "deploy/control-plane/ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	cmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")
```

- [ ] **Step 2: Build and vet to catch typos**

```bash
cd src && go build ./... && go vet ./...
```

Expected: no output, exit code 0.

- [ ] **Step 3: Update the e2e test's fixture-copy paths**

`src/cmd/certrequest/e2e_test.go` copies the real `ca/docker-compose.yml` + `ca/entrypoint.sh` into a temp dir so it exercises the actual deployment fixture. Those files moved in Task 1, and the compose file now has two services instead of one, so the test must target `step-ca` explicitly rather than `up -d` the whole file.

In `src/cmd/certrequest/e2e_test.go`, change the doc comment (currently lines 37-38) from:

```go
// It reuses the exact ca/docker-compose.yml + ca/entrypoint.sh from the repo,
// copied into a t.TempDir() so it never touches a developer's real ca/data/
```

to:

```go
// It reuses the exact deploy/control-plane/docker-compose.yml (step-ca service
// only) + deploy/control-plane/ca/entrypoint.sh from the repo, copied into a
// t.TempDir() so it never touches a developer's real deploy/control-plane/ca/data/
```

Change the comment currently at line 66 from:

```go
	// A unique project name isolates the container/network names from any
	// other compose project (including a real ca/ stack) that might be
```

to:

```go
	// A unique project name isolates the container/network names from any
	// other compose project (including a real deploy/control-plane stack) that might be
```

Change the fixture-copy block (currently lines 55-57) from:

```go
	copyComposeFileWithEphemeralPort(t, filepath.Join(repoRoot, "ca", "docker-compose.yml"), filepath.Join(tempDir, "docker-compose.yml"))
	copyFile(t, filepath.Join(repoRoot, "ca", "entrypoint.sh"), filepath.Join(tempDir, "entrypoint.sh"))
	require.NoError(t, os.Chmod(filepath.Join(tempDir, "entrypoint.sh"), 0o755))
```

to:

```go
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "ca"), 0o755))
	copyComposeFileWithEphemeralPort(t, filepath.Join(repoRoot, "deploy", "control-plane", "docker-compose.yml"), filepath.Join(tempDir, "docker-compose.yml"))
	copyFile(t, filepath.Join(repoRoot, "deploy", "control-plane", "ca", "entrypoint.sh"), filepath.Join(tempDir, "ca", "entrypoint.sh"))
	require.NoError(t, os.Chmod(filepath.Join(tempDir, "ca", "entrypoint.sh"), 0o755))
```

(The compose file's `step-ca` service mounts `./ca/entrypoint.sh` and `./ca/data`, so the copied fixture must reproduce that `ca/` subdirectory layout under `tempDir`, not a flat layout.)

Change the secrets dir (currently line 60) from:

```go
	secretsDir := filepath.Join(tempDir, "data", "secrets")
```

to:

```go
	secretsDir := filepath.Join(tempDir, "ca", "data", "secrets")
```

Change the `docker compose up` call (currently line 82) from:

```go
	upCmd := compose("up", "-d")
```

to:

```go
	upCmd := compose("up", "-d", "step-ca")
```

(Targeting `step-ca` explicitly means Compose only starts/builds that service — the `catalog` service in the same file, whose `build.context` wouldn't resolve inside the isolated temp dir, is never touched.)

Change the root cert path (currently line 88) from:

```go
	rootPath := filepath.Join(tempDir, "data", "certs", "root_ca.crt")
```

to:

```go
	rootPath := filepath.Join(tempDir, "ca", "data", "certs", "root_ca.crt")
```

- [ ] **Step 4: Run the e2e test**

```bash
cd src && go test -tags=e2e -timeout=300s ./cmd/certrequest/... -run TestE2E_TokenMintAndRedeem -v
```

Expected: `PASS`. If it fails with a Docker build error mentioning `catalog`, the `step-ca` targeting in Step 3 didn't prevent Compose from evaluating the `catalog` service's build — re-check the `up -d step-ca` argument was applied.

- [ ] **Step 5: Run the full unit test suite**

```bash
cd src && go test ./...
```

Expected: all packages pass (no regressions from the path changes).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/certrequest/arguments.go src/cmd/certrequest/e2e_test.go
git commit -m "fix(certrequest): update default cert paths and e2e fixture for deploy/control-plane/ move"
```

---

### Task 3: Update living docs that reference the old ca/catalog paths

**Files:**
- Modify: `docs/ARCHITECTURE.md:18`
- Modify: `docs/components/certrequest.md:24-27,53`
- Modify: `docs/components/catalog.md:47`
- Modify: `docs/components/bwfs.md:106`

**Interfaces:**
- Consumes: the new default paths from Task 2, the new README path from Task 4 (referenced here even though Task 4 creates the file after this task — the link target is correct once Task 4 lands).
- Produces: nothing consumed by later tasks; this is documentation-only.

- [ ] **Step 1: Update docs/ARCHITECTURE.md**

In `docs/ARCHITECTURE.md`, change line 18 from:

```
| Components | `ca/` (step-ca container), `certrequest`, `catalog` | `bwfs`, `brfs`, `rwfs`, `certclient` |
```

to:

```
| Components | `deploy/control-plane/ca/` (step-ca container), `certrequest`, `catalog` | `bwfs`, `brfs`, `rwfs`, `certclient` |
```

- [ ] **Step 2: Update docs/components/certrequest.md**

In `docs/components/certrequest.md`, change the flag table rows (currently lines 24-27) from:

```
| `--defaults-file` | `ca/data/config/defaults.json` | Path to step-ca's `defaults.json`, used to default `--ca-url` when it isn't given explicitly |
| `--root` | `ca/data/certs/root_ca.crt` | Path to the CA's root certificate, used to trust the connection to the CA |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `ca/data/secrets/password` | Path to the provisioner password file |
```

to:

```
| `--defaults-file` | `deploy/control-plane/ca/data/config/defaults.json` | Path to step-ca's `defaults.json`, used to default `--ca-url` when it isn't given explicitly |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate, used to trust the connection to the CA |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |
```

Change the "See Also" link (currently line 53) from:

```
- [ca/ step-ca setup](../../ca/README.md)
```

to:

```
- [control plane setup](../../deploy/control-plane/README.md)
```

- [ ] **Step 3: Update docs/components/catalog.md**

In `docs/components/catalog.md`, change line 47 from:

```
Ships as its own `docker compose` stack — see [`catalog/README.md`](../../catalog/README.md).
```

to:

```
Ships as part of the combined control-plane `docker compose` stack — see
[`deploy/control-plane/README.md`](../../deploy/control-plane/README.md).
```

- [ ] **Step 4: Update docs/components/bwfs.md**

In `docs/components/bwfs.md`, change line 106 from:

```
for `bwfs` — see the `ca/` step-ca setup for how certs are provisioned today.
```

to:

```
for `bwfs` — see the [control plane setup](../../deploy/control-plane/README.md) for how certs are provisioned today.
```

- [ ] **Step 5: Verify no stale living-doc references remain**

```bash
grep -rn 'ca/README\|catalog/README\|](ca/\|](catalog/\|`ca/data\|`ca/`' docs/ARCHITECTURE.md docs/components/ README.md
```

Expected: no output (empty). This intentionally excludes `docs/superpowers/`, which is out of scope per Global Constraints.

- [ ] **Step 6: Commit**

```bash
git add docs/ARCHITECTURE.md docs/components/certrequest.md docs/components/catalog.md docs/components/bwfs.md
git commit -m "docs: update ca/catalog references for deploy/control-plane/ move"
```

---

### Task 4: Write the combined README and remove the old ones

**Files:**
- Create: `deploy/control-plane/README.md`
- Delete: `ca/README.md`, `catalog/README.md`

**Interfaces:**
- Consumes: the compose service names (`step-ca`, `catalog`) and ports (9000, 15723) from Task 1; the SAN/hostname-matching behavior documented in the design (`docs/superpowers/specs/2026-07-03-control-plane-compose-design.md`, "Enrolling and connecting an agent" section).
- Produces: `deploy/control-plane/README.md`, the link target Task 3's doc updates point to.

- [ ] **Step 1: Remove the two old READMEs**

```bash
git rm ca/README.md catalog/README.md
```

(After this, `ca/` and `catalog/` no longer exist at the repo root — everything moved to `deploy/control-plane/` in Task 1, and this was the last remaining content.)

- [ ] **Step 2: Write deploy/control-plane/README.md**

Create `deploy/control-plane/README.md`:

```markdown
# Control Plane (ca + catalog)

Combined `docker compose` stack for miniprotector's control-plane components: the `step-ca`
certificate authority and the backup `catalog`. See [Architecture](../../docs/ARCHITECTURE.md)
for how these fit into the rest of the system.

One `docker-compose.yml` runs both as separate containers. Each service can also be started
alone (`docker compose up -d step-ca` or `up -d catalog`) — useful once a deployment splits them
across two hosts; just point `catalog/local.conf`'s `ca_host` at wherever `step-ca` ends up.

## First-time setup

The quickest path is from the repo root:

```bash
make control-plane-up
```

This generates the CA's provisioner password (`ca/data/secrets/password`) if it doesn't already
exist, then runs `docker compose up -d` for both services.

`catalog` itself needs an mTLS identity before it can start successfully — the same enrollment
flow any other node uses. Mint a token for it once `step-ca` is up:

```bash
certrequest catalog --ca-url https://localhost:9000
```

(No `--san` needed here: this quickstart runs `catalog` on the same host, so `catalog_host` will
be `localhost`, and hostname/SAN verification is skipped for loopback connections — see
"Enrolling and connecting an agent" below for the non-`localhost` case.)

Then bring `catalog` up with the token:

```bash
MP_CERT_TOKEN=<token> make control-plane-up
```

Until a token is supplied, `catalog` will exit and restart repeatedly (`restart: unless-stopped`)
— that's expected, not a bug; `step-ca` is unaffected and keeps running.

## Running

```bash
make control-plane-up
```

is the primary entry point (idempotent — safe to re-run). Underneath, it's just:

```bash
cd deploy/control-plane
docker compose up -d          # both services
docker compose up -d step-ca  # just the CA
docker compose up -d catalog  # just the catalog
```

Restarting `catalog` after the first successful run renews its certificate automatically
(`certclient` always renews when an identity already exists — no token needed). This doesn't by
itself keep a long-running container's certificate fresh on its own schedule; re-run `certclient`
inside the container (`docker compose exec catalog ./certclient`) or restart it periodically to
trigger a renewal. `catalog` picks up a renewed certificate on its next new incoming connection
without needing a restart.

## Enrolling and connecting an agent (bwfs/brfs) node

On (or near) this control-plane host, mint a token for the agent's real hostname:

```bash
certrequest node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
```

Relay the printed token to the target node out-of-band (SSH, etc.), then on that node:

```bash
MP_CERT_TOKEN=<token> certclient
```

Re-running `certclient` on a node that already has an identity renews it instead (no token
needed — renewal is authenticated with the existing certificate).

Then, on the agent node, set in `local.conf`:

```
ca_host=<this-host>:9000
catalog_host=<this-host>:15723
```

(`catalog_host` only matters for nodes running `catalogsync`.)

**Important:** unlike the `localhost` quickstart above, a non-`localhost` `catalog_host` is
subject to standard TLS hostname verification — the SAN minted for `catalog`'s own token must
**exactly match** the `catalog_host` string every connecting node uses. For a real (non-local)
deployment, mint catalog's token with that hostname instead of `localhost`:

```bash
certrequest catalog-01 --san catalog.backup.internal --ca-url https://localhost:9000
```

and use `catalog_host=catalog.backup.internal` (matching the `--san` value exactly) in every
`bwfs` node's `local.conf`.

## Viewing an issued certificate

```bash
openssl x509 -in <certs-dir>/client.crt -text -noout
```

## See Also

- [certrequest](../../docs/components/certrequest.md)
- [certclient](../../docs/components/certclient.md)
- [catalog component](../../docs/components/catalog.md)
- [catalogsync component](../../docs/components/catalogsync.md)
- [Architecture](../../docs/ARCHITECTURE.md)
```

- [ ] **Step 3: Commit**

```bash
git add deploy/control-plane/README.md ca catalog
git commit -m "docs(deploy): write combined control-plane README, remove old ca/catalog READMEs"
```

---

### Task 5: Add the make control-plane-up target

**Files:**
- Modify: `Makefile`

**Interfaces:**
- Consumes: `deploy/control-plane/docker-compose.yml`, `deploy/control-plane/ca/data/secrets/password` path from Task 1.
- Produces: `make control-plane-up`, referenced by Task 4's README (already written — this task makes that reference real).

- [ ] **Step 1: Add the CONTROL_PLANE_DIR variable**

In `Makefile`, change the binary definitions block (currently lines 16-24) from:

```makefile
# Binary definitions
BINARIES := $(notdir $(wildcard src/cmd/*))
BRFS_CMD := cmd/brfs
BWFS_CMD := cmd/bwfs
RWFS_CMD := cmd/rwfs
CERTREQUEST_CMD := cmd/certrequest
CERTCLIENT_CMD := cmd/certclient
CATALOGSYNC_CMD := cmd/catalogsync
CATALOG_CMD := cmd/catalog
```

to:

```makefile
# Binary definitions
BINARIES := $(notdir $(wildcard src/cmd/*))
BRFS_CMD := cmd/brfs
BWFS_CMD := cmd/bwfs
RWFS_CMD := cmd/rwfs
CERTREQUEST_CMD := cmd/certrequest
CERTCLIENT_CMD := cmd/certclient
CATALOGSYNC_CMD := cmd/catalogsync
CATALOG_CMD := cmd/catalog

# Deployment
CONTROL_PLANE_DIR := deploy/control-plane
```

- [ ] **Step 2: Add control-plane-up to .PHONY**

Change line 33 from:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certrequest certclient catalogsync catalog test test-e2e lint
```

to:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certrequest certclient catalogsync catalog test test-e2e lint control-plane-up
```

- [ ] **Step 3: Add the control-plane-up target**

Append after the `clean` target (end of file, currently lines 118-119):

```makefile
control-plane-up: ## Initialize (if needed) and start the control-plane stack (ca + catalog)
	@if [ ! -f $(CONTROL_PLANE_DIR)/ca/data/secrets/password ]; then \
		echo -e "$(BLUE)Generating CA provisioner password...$(NC)"; \
		mkdir -p $(CONTROL_PLANE_DIR)/ca/data/secrets; \
		openssl rand -base64 32 > $(CONTROL_PLANE_DIR)/ca/data/secrets/password; \
	fi
	@cd $(CONTROL_PLANE_DIR) && docker compose up -d
	@echo -e "$(GREEN)Control plane up.$(NC) ca: https://localhost:9000  catalog: localhost:15723"
```

- [ ] **Step 4: Verify first-run behavior (password generation + step-ca up, catalog crash-loops without a token)**

```bash
rm -rf deploy/control-plane/ca/data deploy/control-plane/catalog/data
make control-plane-up
```

Expected output includes `Generating CA provisioner password...` and `Control plane up.`; `ls deploy/control-plane/ca/data/secrets/password` now exists.

```bash
sleep 3
cd deploy/control-plane && docker compose ps
```

Expected: `step-ca` is `running`; `catalog` is `restarting` (no token supplied yet — expected per Global Constraints).

- [ ] **Step 5: Verify idempotent re-run doesn't regenerate the password**

```bash
md5sum ca/data/secrets/password > /tmp/pw-before.md5
cd /home/alex/miniprotector && make control-plane-up
cd deploy/control-plane && md5sum ca/data/secrets/password > /tmp/pw-after.md5
diff /tmp/pw-before.md5 /tmp/pw-after.md5
```

Expected: no diff output, and the second `make control-plane-up` run's output does **not** include `Generating CA provisioner password...`.

- [ ] **Step 6: Verify catalog recovers once a token is supplied**

```bash
certrequest catalog --ca-url https://localhost:9000
```

Expected: prints a token to stdout.

```bash
cd /home/alex/miniprotector
MP_CERT_TOKEN=<token-from-previous-command> make control-plane-up
sleep 3
cd deploy/control-plane && docker compose ps
```

Expected: `catalog` is now `running`, not `restarting`.

- [ ] **Step 7: Tear down the verification stack**

```bash
docker compose down -v
cd /home/alex/miniprotector
rm -rf deploy/control-plane/ca/data deploy/control-plane/catalog/data
```

- [ ] **Step 8: Commit**

```bash
git add Makefile
git commit -m "feat(deploy): add make control-plane-up target"
```

---

## Post-plan manual QA (optional, not a task)

The plan above verifies packaging correctness (paths, build contexts, idempotency). It doesn't
re-verify the `brfs → bwfs → catalogsync → catalog` protocol itself — that's already covered by
`make test-e2e` (`src/e2e`), which builds its own images directly and is untouched by this
relocation. If you want an end-to-end sanity check against the *relocated* control plane
specifically: bring the stack up via `make control-plane-up`, enroll a throwaway `bwfs` node
against it (per the new README's "Enrolling and connecting an agent" section), run `brfs` against
that `bwfs`, and confirm `catalogsync` replicates into `catalog`. This is exploratory QA, not a
required plan step.
