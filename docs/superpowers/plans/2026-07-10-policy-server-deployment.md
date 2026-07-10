# Policy Server Deployment Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the already-built `policy-server` binary into both Docker Compose deployment environments (`deploy/control-plane/` and `demo/`), following `catalog`'s existing deployment pattern exactly, so it can actually run as a real, enrolled node in either environment.

**Architecture:** `policy-server` is control-plane-by-role but obtains its mTLS identity as an ordinary `agent`-managed enrolled node — bundling `certclient`/`agent` into its own image and going through the same bootstrap-token enrollment flow every node uses, exactly like `catalog` already does. It differs from `catalog` in one structural way: no local SQLite database (`STORAGE_PATH`) — its persistent state is the operator-authored `policies/` directory, which `policy-server`'s own `main.go` already creates on startup, so no entrypoint-side directory setup is needed.

**Tech Stack:** Docker, Docker Compose, the same `golang:1.26`/`debian:bookworm-slim` multi-stage Dockerfile pattern already used by every other control-plane image in this repo.

## Global Constraints

- No changes to `policy-server`'s own application code (`src/cmd/policy-server/`) — this plan is deployment-only.
- No client-side consumer of `GetPolicies` is wired in either environment — this plan only makes `policy-server` reachable and enrollable, matching every other control-plane service's own deployment-before-consumer history in this repo.
- Every new/modified deployment file must mirror `catalog`'s existing equivalent file as closely as the structural difference (no `STORAGE_PATH`, no positional CLI argument) allows — no invented conventions.

---

## File Structure

| File | Responsibility |
|---|---|
| `deploy/control-plane/policy-server/Dockerfile` (new) | Multi-stage build: `make policy-server certclient agent`, then a slim runtime image |
| `deploy/control-plane/policy-server/entrypoint.sh` (new) | Bootstrap/renew + `agent serve` + wait-for-operating-cert + `exec ./policy-server` |
| `deploy/control-plane/policy-server/local.conf` (new) | `policy-server`'s own config in the production-reference deployment |
| `deploy/control-plane/docker-compose.yml` (modify) | New `policy-server` service block |
| `deploy/control-plane/README.md` (modify) | Enrollment instructions for `policy-server` |
| `demo/docker-compose.yml` (modify) | New `policy-server` service block, reusing the same Dockerfile |
| `demo/up.sh` (modify) | `enroll policy-server` line |
| `demo/README.md` (modify) | Mention `policy-server` in the image count, enrollment list, and try-it commands |
| `docs/ARCHITECTURE.md` (modify) | Fill in `policy-server`'s row in the "Control Plane vs. Agents" table |
| `CHANGELOG.md` (modify) | New entry for this deployment-wiring change |

---

### Task 1: `deploy/control-plane/` wiring

**Files:**
- Create: `deploy/control-plane/policy-server/Dockerfile`
- Create: `deploy/control-plane/policy-server/entrypoint.sh`
- Create: `deploy/control-plane/policy-server/local.conf`
- Modify: `deploy/control-plane/docker-compose.yml`
- Modify: `deploy/control-plane/README.md`

No TDD cycle applies to this task (deployment configuration, not Go code) — each step is create-file-with-exact-content, followed by a syntax verification step.

- [ ] **Step 1: Create the Dockerfile**

`deploy/control-plane/policy-server/Dockerfile`:

```dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make policy-server certclient agent

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/policy-server /build/bin/certclient /build/bin/agent ./
COPY deploy/control-plane/policy-server/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

(No `sqlite3` package — unlike `catalog`'s Dockerfile, `policy-server` has no local database.)

- [ ] **Step 2: Create the entrypoint script**

`deploy/control-plane/policy-server/entrypoint.sh`:

```sh
#!/bin/sh
set -e

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart -- no expiry check, certclient always renews when an
# identity already exists) of the long-lived bootstrap credential.
if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent keeps both the bootstrap credential (daily) and the operating
# credential (every 15 min, talking to issuer) fresh continuously, so
# policy-server never needs a container restart to pick up a renewal.
./agent serve &

# Wait for agent's first operating-refresh to produce client.crt/client.key
# before starting policy-server -- a fresh volume only has the bootstrap
# credential until agent's reconcile loop completes its first cycle (due
# immediately for a never-run policy); without this wait, policy-server
# would race agent and could crash-loop on a genuinely fresh deployment's
# first boot.
timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
	sleep 1
	timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
	echo "agent did not produce an operating certificate within 60s" >&2
	exit 1
fi

# No mkdir/STORAGE_PATH step here, unlike catalog's entrypoint -- policy-server
# has no local database; its own main.go already os.MkdirAll's
# $MP_CONFIG_PATH/policies on startup.
exec ./policy-server --debug="${DEBUG:-false}"
```

Make it executable in git's index (the Dockerfile's own `RUN chmod +x` also covers this inside the image, but keeping the working-tree file executable matches `catalog/entrypoint.sh`'s convention):

```bash
chmod +x deploy/control-plane/policy-server/entrypoint.sh
```

- [ ] **Step 3: Create the local config**

`deploy/control-plane/policy-server/local.conf`:

```
# default_port/default_streams/logfolder are required by every miniprotector
# binary's shared config parser, even though policy-server itself only uses
# policy_server_port and ca_host below. Harmless placeholders.
default_port=15722
default_streams=4
logfolder=/data/log

# The port policy-server listens on. No consumer dials this yet -- wiring an
# actual client (agent/brfs calling GetPolicies) is separate, later work.
policy_server_port=9300

# Set to this deployment's CA host:port before first boot.
ca_host=ca.backup.internal:9000

# Where policy-server's agent-managed operating-refresh policy dials issuer.
issuer_host=issuer
issuer_port=9200

# agent's own reconcile-loop tick cadence.
ReconcileIntervalSec=30

# How often agent refreshes each credential tier -- see docs/SECURITY.md
# for why these two are on such different cadences.
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
```

- [ ] **Step 4: Add the service block to docker-compose.yml**

In `deploy/control-plane/docker-compose.yml`, insert a new `policy-server` service after the existing `catalog` block (after its `restart: unless-stopped` line, before the file's end):

```yaml

  policy-server:
    build:
      context: ../..
      dockerfile: deploy/control-plane/policy-server/Dockerfile
    depends_on:
      - step-ca
      - issuer
    volumes:
      - ./policy-server/data:/data
      - ./policy-server/local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    ports:
      - "9300:9300"
    restart: unless-stopped
```

- [ ] **Step 5: Verify the compose file is well-formed**

Run: `cd deploy/control-plane && docker compose config >/dev/null && echo OK`
Expected: `OK` (pure syntax/interpolation check, no build). If `docker` isn't available in this environment, note that in your report instead of treating it as a blocker — this is the same pattern this repo's own e2e tests already use for optional Docker availability.

- [ ] **Step 6: Update `deploy/control-plane/README.md`**

Change the title (line 1):

```markdown
# Control Plane (ca + issuer + catalog + policy-server)
```

Change the intro paragraph (originally lines 3-10) to:

```markdown
Combined `docker compose` stack for miniprotector's control-plane components: the `step-ca`
certificate authority, the `issuer` operating-certificate service, the backup `catalog`, and
`policy-server`. See [Architecture](../../docs/ARCHITECTURE.md) for how these fit into the rest of
the system.

One `docker-compose.yml` runs all four as separate containers. Each service can also be started
alone (`docker compose up -d step-ca`, `up -d issuer`, `up -d catalog`, or `up -d policy-server`)
— useful once a deployment splits them across hosts; just point each service's own `local.conf`'s
`ca_host`/`issuer_host` at wherever `step-ca`/`issuer` end up.
```

After the existing catalog enrollment section (originally ending at "...with no container restart
needed to pick up either refresh." — the paragraph just before `## Running`), insert a new
subsection:

```markdown
### Enrolling policy-server

The same enrollment flow, condensed (see the `catalog` walkthrough above for the fully-explained
version — the mechanics are identical, just a different service name and no `--san` consideration
since nothing yet resolves `policy-server` by a SAN-verified hostname):

```bash
cd deploy/control-plane
docker compose up -d issuer   # if not already up

docker run --rm --network control-plane_default \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  -v "$(pwd)/client-manager/data:/data" \
  -v "$(pwd)/client-manager/local.conf:/data/local.conf:ro" \
  -e MP_CONFIG_PATH=/data \
  golang:1.26 \
  go run ./cmd/clientmanager add policy-server --ca-url https://step-ca:9000 \
    --root /repo/deploy/control-plane/ca/data/certs/root_ca.crt \
    --password-file /repo/deploy/control-plane/ca/data/secrets/password

MP_CERT_TOKEN=<token> docker compose up -d policy-server
```

Until a token is supplied, `policy-server` will exit and restart repeatedly
(`restart: unless-stopped`) — expected, not a bug. Once enrolled, its bundled `agent` keeps both
credential tiers fresh continuously, the same as `catalog`'s.
```

In the `## Running` section, change the command list to add policy-server:

```markdown
```bash
cd deploy/control-plane
docker compose up -d                 # all four services
docker compose up -d step-ca         # just the CA
docker compose up -d issuer          # just the operating-cert issuer
docker compose up -d catalog         # just the catalog
docker compose up -d policy-server   # just policy-server
```
```

In `## See Also`, add one line after the `catalog component` line:

```markdown
- [policy-server component](../../docs/components/policy-server.md)
```

- [ ] **Step 7: Commit**

```bash
git add deploy/control-plane/policy-server/ deploy/control-plane/docker-compose.yml deploy/control-plane/README.md
git commit -m "feat(deploy): add policy-server to the control-plane compose stack"
```

---

### Task 2: `demo/` wiring

**Files:**
- Modify: `demo/docker-compose.yml`
- Modify: `demo/up.sh`
- Modify: `demo/README.md`

**Interfaces:**
- Consumes: `deploy/control-plane/policy-server/Dockerfile` (Task 1) — demo reuses it directly, same as it already reuses `deploy/control-plane/catalog/Dockerfile`.

- [ ] **Step 1: Add the service block and volume to demo/docker-compose.yml**

Insert a new `policy-server` service after the existing `catalog` block (after its `restart: unless-stopped` line, before the `client:` block):

```yaml

  policy-server:
    build:
      context: ..
      dockerfile: deploy/control-plane/policy-server/Dockerfile
    depends_on:
      - ca
      - issuer
    volumes:
      - policy-server-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    restart: unless-stopped
```

Add `policy-server-data:` to the top-level `volumes:` list (alongside the existing `catalog-data:` line):

```yaml
volumes:
  ca-data:
  client-manager-data:
  issuer-data:
  catalog-data:
  policy-server-data:
  client-data:
  store-data:
```

- [ ] **Step 2: Verify the compose file is well-formed**

Run: `cd demo && docker compose config >/dev/null && echo OK`
Expected: `OK`. If `docker` isn't available, note it in your report rather than blocking.

- [ ] **Step 3: Add enrollment to up.sh**

In `demo/up.sh`, change:

```sh
enroll catalog
enroll client
enroll store
```

to:

```sh
enroll catalog
enroll policy-server
enroll client
enroll store
```

Add one line to the printed `MSG` block's list of things to try (after the existing
`docker compose ... exec catalog ./agent list-policies` line):

```
  docker compose -f demo/docker-compose.yml exec policy-server ./agent list-policies
```

- [ ] **Step 4: Update demo/README.md**

Change "Builds all five images" to "Builds all six images" and mention policy-server in the
enrollment sentence (in the `## Bring it up` section):

```markdown
Equivalent to `./demo/up.sh` directly. Builds all six images, brings up `ca` and `issuer` first,
then mints and redeems an enrollment token for `catalog`, `policy-server`, `client`, and `store` in
turn (skipping re-minting on a re-run against an already-enrolled node).
```

Add one command to the `## Try it` block (after the existing
`docker compose ... exec catalog ./agent list-policies` line):

```
docker compose -f demo/docker-compose.yml exec policy-server ./agent list-policies
```

- [ ] **Step 5: Commit**

```bash
git add demo/docker-compose.yml demo/up.sh demo/README.md
git commit -m "feat(demo): add policy-server to the demo lab stack"
```

---

### Task 3: Documentation completeness and CHANGELOG

**Files:**
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Fill in policy-server's row in the "Control Plane vs. Agents" table**

In `docs/ARCHITECTURE.md`, the table currently (as of this branch) reads:

```markdown
|  | Control plane | Agents |
|---|---|---|
| Components | `deploy/control-plane/ca/` (step-ca container), `catalog`, `client-manager`, `issuer` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
| Runs where | On the CA host (`client-manager`, `issuer`); `catalog` runs centrally, wherever the catalog deployment lives — see below | Dial `ca_host:9000` outbound for enrollment/renewal and `issuer_host:9200` outbound for operating-certificate refresh; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Network role | Serves enrollment/renewal/admin (`/sign`, `/renew`, `/roots`, `/provisioners`) on `:9000`; `issuer` serves `RequestOperatingCert`/`DescribeSANs` on `:9200` (mTLS); neither has a role in backup traffic | Dial `ca_host:9000` (bootstrap/renew) and `issuer_host:9200` (operating-refresh) outbound only; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Docker/e2e images | Control-plane-only binaries (`client-manager`, `issuer`) never ship onto an agent host or into an agent image | Agent images bundle `certclient` and `agent` — `catalog`'s image is one of them, since it's deployed as an ordinary `agent`-managed enrolled node (see [Control Plane README](../deploy/control-plane/README.md)) |
```

Replace it with:

```markdown
|  | Control plane | Agents |
|---|---|---|
| Components | `deploy/control-plane/ca/` (step-ca container), `catalog`, `policy-server`, `client-manager`, `issuer` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
| Runs where | On the CA host (`client-manager`, `issuer`); `catalog`/`policy-server` run centrally, wherever each deployment lives — see below | Dial `ca_host:9000` outbound for enrollment/renewal and `issuer_host:9200` outbound for operating-certificate refresh; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Network role | Serves enrollment/renewal/admin (`/sign`, `/renew`, `/roots`, `/provisioners`) on `:9000`; `issuer` serves `RequestOperatingCert`/`DescribeSANs` on `:9200` (mTLS); `policy-server` serves `GetPolicies` on `:9300` (mTLS, no client-side consumer wired yet); none of these has a role in backup traffic | Dial `ca_host:9000` (bootstrap/renew) and `issuer_host:9200` (operating-refresh) outbound only; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Docker/e2e images | Control-plane-only binaries (`client-manager`, `issuer`) never ship onto an agent host or into an agent image | Agent images bundle `certclient` and `agent` — `catalog`'s and `policy-server`'s images are both among them, since each is deployed as an ordinary `agent`-managed enrolled node (see [Control Plane README](../deploy/control-plane/README.md)) |
```

- [ ] **Step 2: Add an explanatory paragraph for policy-server**

After the existing `catalog` explanatory paragraph (the one ending "...for `catalogsync`
connections from every `bwfs` node's agent host."), insert:

```markdown
`policy-server` is control plane by role (a fleet-wide policy distribution service) but, like
`catalog`, obtains its own mTLS identity as an ordinary `agent`-managed enrolled node rather than a
one-shot bootstrap — its image bundles `agent` the same way `catalog`'s does. It listens on its own
port (`policy_server_port`, default 9300); nothing dials it yet, since no client-side consumer of
`GetPolicies` exists in this codebase — wiring one (`agent` or `brfs` fetching and acting on
policies) is separate, later work, the same way `issuer`'s own phase 2b deliberately left `agent`
integration for a follow-up phase.
```

- [ ] **Step 3: Add the CHANGELOG entry**

Add to the top of `CHANGELOG.md` (most recent first):

```markdown
## 2026-07-10 — Deploy policy-server as an enrolled control-plane node

`policy-server` (added earlier this same day) now has a real deployment story in both
`deploy/control-plane/` and `demo/`, following `catalog`'s existing pattern exactly: its own
Docker image bundling `agent`/`certclient`, enrolled through the same bootstrap-token flow every
node uses, with continuously-refreshed bootstrap and operating credentials rather than a one-shot
identity. It differs from `catalog` in one structural way — no local database, so no
`STORAGE_PATH`/positional CLI argument, and its entrypoint needs no directory-creation step since
`policy-server` already creates its own `policies/` directory on startup. Still no client-side
consumer of `GetPolicies` in either environment — that remains separate, later work.
```

- [ ] **Step 4: Final verification**

Run: `cd deploy/control-plane && docker compose config >/dev/null && echo "control-plane OK"`
Run: `cd demo && docker compose config >/dev/null && echo "demo OK"`
Expected: both print their `OK` line (or, if Docker isn't available in this environment, note that
in your report — the YAML/interpolation checks are the primary verification for this doc-and-config-only
task; there is no Go code in this plan to `go build`/`go test`).

- [ ] **Step 5: Commit**

```bash
git add docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document policy-server's deployment wiring"
```

---

## Self-Review

**Spec coverage:**
- `deploy/control-plane/` Dockerfile, entrypoint, local.conf, compose service, README → Task 1.
- `demo/` compose service (reusing Task 1's Dockerfile), `up.sh` enrollment, README → Task 2.
- Documentation completeness gap (ARCHITECTURE.md's table) flagged by the parent feature's own
  final review, plus a CHANGELOG entry → Task 3.
- Explicitly *not* covered here (correctly, per this plan's own Non-Goals and the parent design's
  own Non-Goals): no client-side consumer of `GetPolicies` in either environment, no changes to
  `policy-server`'s application code.

**Placeholder scan:** no "TBD"/"TODO"; every file-content block is the complete, exact content to
write — no "similar to catalog" shorthand anywhere a real engineer would need to fill in.

**Type/naming consistency:** service name `policy-server` (matching the binary name and existing
docs/spec usage) used identically across `deploy/control-plane/docker-compose.yml`,
`demo/docker-compose.yml`, `demo/up.sh`'s `enroll` call, and both READMEs. Port `9300` and config
key `policy_server_port` match the values already shipped in `src/common/config/config.go` on this
branch (Task 8 of the parent plan) — not re-derived or guessed here.

No gaps found.
