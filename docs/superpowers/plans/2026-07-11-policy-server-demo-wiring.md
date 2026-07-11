# Policy Server Consumer Wiring & Demo Content Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `agent`'s already-shipped `policy-update` job actually reach `policy-server` in both `deploy/control-plane/` and `demo/`, then give the demo lab real, working policy content — a second backup-source client and three example policies that each demonstrate a different `client_filters` selection mechanism (multi-hostname, single-hostname, label).

**Architecture:** `policy_server_host` is missing from every `local.conf` that configures a node running `agent serve`, so `policy-update` has been failing every reconcile cycle everywhere since it shipped — this plan adds that one config key in three places. The demo's single generic `client` node is renamed to `database` and a new `webserver` node is added (both from the existing `demo/backup-host/Dockerfile`, unchanged), each mounting themed read-only fixture content. `policy-server`'s demo volume gains a host-editable `policies/` bind mount seeded with three JSON policy files.

**Tech Stack:** Docker Compose, POSIX shell (`demo/up.sh`), JSON (policy files), Markdown docs. No Go code changes.

## Global Constraints

- No changes to `policy-server`'s, `agent`'s, or `policyclient`'s own application code — deployment configuration and demo fixture content only.
- No new `deploy/control-plane` demo content or example policies — that environment stays a bare production-reference skeleton with no seeded clients or policies.
- `store` is not renamed or reconfigured — only `client` (→ `database`) is renamed, and `webserver` is newly added.
- Every policy's `backup_window` must be `["0 * * * *"]` (hourly) so, combined with `agent`'s default `BackupWindowGraceSec` (3600s), the task is always due regardless of when the demo is started.
- `deploy/control-plane/issuer/local.conf` is untouched — `issuer` never runs `agent serve`, so it never runs `policy-update`.

---

## File Structure

| File | Responsibility |
|---|---|
| `demo/local.conf` (modify) | Add `policy_server_host` — fixes `policy-update` for every demo service at once |
| `deploy/control-plane/catalog/local.conf` (modify) | Add `policy_server_host` |
| `deploy/control-plane/policy-server/local.conf` (modify) | Add `policy_server_host`, fix stale comment |
| `demo/docker-compose.yml` (modify) | Rename `client`→`database`, add `webserver` service, restructure sample-data mounts, add `policy-server` policies bind mount |
| `demo/sample-data/audit/audit.log`, `demo/sample-data/db/dump.sql`, `demo/sample-data/db/schema.sql`, `demo/sample-data/web/index.html`, `demo/sample-data/web/style.css` (new) | Themed fixture content, replacing the old flat `notes.txt`/`hello.txt` |
| `demo/policy-server/policies/audit-logs.json`, `database-backup.json`, `webserver-backup.json` (new) | The three example policies |
| `demo/up.sh` (modify) | `enroll()` gains an attribute-setting third argument; enrolls `database`/`webserver` (with `webserver`'s `role=web` label) in place of `client`; updated printed command list |
| `demo/README.md` (modify) | Updated topology description, "Try it" commands, new "Backup policies" walkthrough section |
| `Makefile` (modify) | `demo-up` target's help text |
| `CHANGELOG.md` (modify) | New entry |

---

### Task 1: Consumer wiring — `policy_server_host`

**Files:**
- Modify: `demo/local.conf`
- Modify: `deploy/control-plane/catalog/local.conf`
- Modify: `deploy/control-plane/policy-server/local.conf`

No TDD cycle applies (configuration values, not Go code) — each step is an exact edit followed by a `grep` verification.

- [ ] **Step 1: Add `policy_server_host` to `demo/local.conf`**

Current content of `demo/local.conf`:

```
default_port=8080
default_streams=4
logfolder=/var/log/miniprotector
ca_host=ca:9000
issuer_host=issuer
issuer_port=9200
catalog_host=catalog
catalog_port=15723
ReconcileIntervalSec=30
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
JobTimeoutSec=30
var_path=/data/client-manager
ConnectionTimeOutSec=30
FileLockTimeoutSec=30
```

Add one line after `catalog_port=15723`:

```
default_port=8080
default_streams=4
logfolder=/var/log/miniprotector
ca_host=ca:9000
issuer_host=issuer
issuer_port=9200
catalog_host=catalog
catalog_port=15723
policy_server_host=policy-server
ReconcileIntervalSec=30
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
JobTimeoutSec=30
var_path=/data/client-manager
ConnectionTimeOutSec=30
FileLockTimeoutSec=30
```

(`policy_server_port` is not set — `config.Config`'s default of `9300` already matches `policy-server`'s actual listening port.)

- [ ] **Step 2: Add `policy_server_host` to `deploy/control-plane/catalog/local.conf`**

Current content:

```
# default_port/default_streams/logfolder are required by every miniprotector
# binary's shared config parser, even though catalog itself only uses
# catalog_port and ca_host below. Harmless placeholders.
default_port=15722
default_streams=4
logfolder=/data/log

# The port catalog listens on, and the port bwfs nodes' catalogsync dials
# (paired with catalog_host, set in each bwfs node's own local.conf).
catalog_port=15723

# Set to this deployment's CA host:port before first boot.
ca_host=ca.backup.internal:9000

# Where catalog's agent-managed operating-refresh policy dials issuer.
issuer_host=issuer
issuer_port=9200

# agent's own reconcile-loop tick cadence.
ReconcileIntervalSec=30

# How often agent refreshes each credential tier -- see docs/SECURITY.md
# for why these two are on such different cadences.
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
```

Insert a new block after the `issuer_host`/`issuer_port` lines:

```
# default_port/default_streams/logfolder are required by every miniprotector
# binary's shared config parser, even though catalog itself only uses
# catalog_port and ca_host below. Harmless placeholders.
default_port=15722
default_streams=4
logfolder=/data/log

# The port catalog listens on, and the port bwfs nodes' catalogsync dials
# (paired with catalog_host, set in each bwfs node's own local.conf).
catalog_port=15723

# Set to this deployment's CA host:port before first boot.
ca_host=ca.backup.internal:9000

# Where catalog's agent-managed operating-refresh policy dials issuer.
issuer_host=issuer
issuer_port=9200

# Where catalog's agent-managed policy-update job dials policy-server.
policy_server_host=policy-server

# agent's own reconcile-loop tick cadence.
ReconcileIntervalSec=30

# How often agent refreshes each credential tier -- see docs/SECURITY.md
# for why these two are on such different cadences.
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
```

- [ ] **Step 3: Add `policy_server_host` to `deploy/control-plane/policy-server/local.conf`, and fix the stale comment**

Current content:

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

Replace it with:

```
# default_port/default_streams/logfolder are required by every miniprotector
# binary's shared config parser, even though policy-server itself only uses
# policy_server_port and ca_host below. Harmless placeholders.
default_port=15722
default_streams=4
logfolder=/data/log

# The port policy-server listens on.
policy_server_port=9300

# Set to this deployment's CA host:port before first boot.
ca_host=ca.backup.internal:9000

# Where policy-server's agent-managed operating-refresh policy dials issuer.
issuer_host=issuer
issuer_port=9200

# Where policy-server's own agent-managed policy-update job dials
# policy-server -- itself. Every agent-managed node runs this job
# unconditionally, policy-server included.
policy_server_host=policy-server

# agent's own reconcile-loop tick cadence.
ReconcileIntervalSec=30

# How often agent refreshes each credential tier -- see docs/SECURITY.md
# for why these two are on such different cadences.
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
```

- [ ] **Step 4: Verify all three files**

Run: `grep -n policy_server_host demo/local.conf deploy/control-plane/catalog/local.conf deploy/control-plane/policy-server/local.conf`
Expected: one `policy_server_host=policy-server` line in each of the three files.

- [ ] **Step 5: Commit**

```bash
git add demo/local.conf deploy/control-plane/catalog/local.conf deploy/control-plane/policy-server/local.conf
git commit -m "fix(deploy): set policy_server_host so agent's policy-update job can actually reach policy-server"
```

---

### Task 2: Demo topology — rename `client`→`database`, add `webserver`, themed sample data

**Files:**
- Modify: `demo/docker-compose.yml`
- Create: `demo/sample-data/audit/audit.log`
- Create: `demo/sample-data/db/dump.sql`
- Create: `demo/sample-data/db/schema.sql`
- Create: `demo/sample-data/web/index.html`
- Create: `demo/sample-data/web/style.css`
- Delete: `demo/sample-data/notes.txt`
- Delete: `demo/sample-data/hello.txt`

**Interfaces:**
- Consumes: Task 1's `demo/local.conf` (already updated; this task doesn't touch it further).
- Produces: `database` and `webserver` as the two demo hostnames Task 3 (`up.sh`) and Task 4 (policy files) reference. `store:8080` as the shared backup destination (unchanged from the existing `store` service).

- [ ] **Step 1: Remove the old flat sample files and create the three themed directories**

```bash
rm demo/sample-data/notes.txt demo/sample-data/hello.txt
mkdir -p demo/sample-data/audit demo/sample-data/db demo/sample-data/web
```

- [ ] **Step 2: Create the fixture files**

`demo/sample-data/audit/audit.log`:

```
2026-07-11T02:00:01Z audit: user=root action=login result=success
2026-07-11T02:00:15Z audit: user=deploy action=sudo command=/usr/bin/systemctl result=success
2026-07-11T03:00:02Z audit: user=root action=login result=failure
```

`demo/sample-data/db/dump.sql`:

```sql
-- Fake demo fixture: not a real database dump, just backup-worthy content.
CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT, email TEXT);
INSERT INTO customers VALUES (1, 'Ada Lovelace', 'ada@example.com');
INSERT INTO customers VALUES (2, 'Grace Hopper', 'grace@example.com');
```

`demo/sample-data/db/schema.sql`:

```sql
-- Fake demo fixture: schema for the customers table above.
CREATE TABLE IF NOT EXISTS customers (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE
);
```

`demo/sample-data/web/index.html`:

```html
<!DOCTYPE html>
<html>
<head><title>miniprotector demo</title></head>
<body>
<h1>It works.</h1>
<p>This is fake content backed up by the webserver-backup policy.</p>
</body>
</html>
```

`demo/sample-data/web/style.css`:

```css
body { font-family: sans-serif; margin: 2rem; }
h1 { color: #2c3e50; }
```

- [ ] **Step 3: Replace `demo/docker-compose.yml` in full**

Replace the entire file with:

```yaml
services:
  ca:
    build:
      context: ..
      dockerfile: demo/ca/Dockerfile
    volumes:
      - ca-data:/home/step
      - client-manager-data:/data/client-manager
      - ./ca/clientmanager-local.conf:/data/client-manager-conf/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data/client-manager-conf
    restart: unless-stopped

  issuer:
    build:
      context: ..
      dockerfile: demo/issuer/Dockerfile
    depends_on:
      - ca
    volumes:
      - issuer-data:/data
      - ./local.conf:/data/local.conf:ro
      - client-manager-data:/data/client-manager
      - ca-data:/ca-data:ro
    environment:
      - MP_CONFIG_PATH=/data
    restart: unless-stopped

  catalog:
    build:
      context: ..
      dockerfile: deploy/control-plane/catalog/Dockerfile
    depends_on:
      - ca
      - issuer
    volumes:
      - catalog-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - STORAGE_PATH=/data/storage
    restart: unless-stopped

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
      - ./policy-server/policies:/data/policies
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    restart: unless-stopped

  database:
    build:
      context: ..
      dockerfile: demo/backup-host/Dockerfile
    depends_on:
      - ca
      - issuer
    volumes:
      - database-data:/data
      - ./local.conf:/data/local.conf:ro
      - ./sample-data/audit:/var/log/audit:ro
      - ./sample-data/db:/var/lib/dbdata:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    restart: unless-stopped

  webserver:
    build:
      context: ..
      dockerfile: demo/backup-host/Dockerfile
    depends_on:
      - ca
      - issuer
    volumes:
      - webserver-data:/data
      - ./local.conf:/data/local.conf:ro
      - ./sample-data/audit:/var/log/audit:ro
      - ./sample-data/web:/var/www/html:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    restart: unless-stopped

  store:
    build:
      context: ..
      dockerfile: demo/backup-host/Dockerfile
    depends_on:
      - ca
      - issuer
      - catalog
    volumes:
      - store-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - STORAGE_PATH=/data/storage
    restart: unless-stopped

volumes:
  ca-data:
  client-manager-data:
  issuer-data:
  catalog-data:
  policy-server-data:
  database-data:
  webserver-data:
  store-data:
```

Note what changed from the previous file: the `client` service is now `database` (renamed, with its volume `client-data`→`database-data`, and `./sample-data:/data/sample-data:ro` replaced by the two themed mounts above); a new `webserver` service was added (same Dockerfile, its own `webserver-data` volume, the shared audit mount plus its own `web` mount); `policy-server` gained the `./policy-server/policies:/data/policies` bind mount.

- [ ] **Step 4: Verify the compose file is well-formed**

Run: `cd demo && docker compose config >/dev/null && echo OK`
Expected: `OK`. If `docker` isn't available in this environment, note that in your report instead of treating it as a blocker.

- [ ] **Step 5: Commit**

```bash
git add demo/docker-compose.yml demo/sample-data
git commit -m "feat(demo): rename client to database, add webserver, and use themed fixture content"
```

---

### Task 3: `demo/up.sh` — enroll `database`/`webserver`, label `webserver`

**Files:**
- Modify: `demo/up.sh`

**Interfaces:**
- Consumes: Task 2's `database`/`webserver` service names.

- [ ] **Step 1: Replace the `enroll()` function and its call list**

Current content (the `enroll` function through its calls):

```sh
enroll() {
    name="$1"
    if docker compose run --rm --no-deps --entrypoint sh "$name" \
        -c 'test -f /data/certs/bootstrap.crt' >/dev/null 2>&1; then
        echo "$name already enrolled, starting..."
        docker compose up -d "$name"
        return
    fi
    echo "Enrolling $name..."
    token=$(docker compose exec -T ca clientmanager add "$name" \
        --ca-url https://ca:9000 \
        --root /home/step/certs/root_ca.crt \
        --password-file /home/step/secrets/password \
        --defaults-file /home/step/config/defaults.json)
    MP_CERT_TOKEN="$token" docker compose up -d "$name"
}

enroll catalog
enroll policy-server
enroll client
enroll store
```

Replace with:

```sh
# $2, if given, is a space-separated "key=value" attribute string applied
# via `clientmanager attribute set` right after `add` and before the
# node's own container starts -- only on first enrollment (the branch
# below that runs `add` at all), so the attribute is already in
# client-manager's database before the node's first operating-refresh
# mints a certificate embedding it.
enroll() {
    name="$1"
    attrs="$2"
    if docker compose run --rm --no-deps --entrypoint sh "$name" \
        -c 'test -f /data/certs/bootstrap.crt' >/dev/null 2>&1; then
        echo "$name already enrolled, starting..."
        docker compose up -d "$name"
        return
    fi
    echo "Enrolling $name..."
    token=$(docker compose exec -T ca clientmanager add "$name" \
        --ca-url https://ca:9000 \
        --root /home/step/certs/root_ca.crt \
        --password-file /home/step/secrets/password \
        --defaults-file /home/step/config/defaults.json)
    if [ -n "$attrs" ]; then
        docker compose exec -T ca clientmanager attribute set "$name" $attrs
    fi
    MP_CERT_TOKEN="$token" docker compose up -d "$name"
}

enroll catalog
enroll policy-server
enroll database
enroll webserver "role=web"
enroll store
```

- [ ] **Step 2: Update the printed command list**

Current content:

```sh
cat <<'MSG'

Demo stack is up. Try:
  docker compose -f demo/docker-compose.yml exec client ./brfs /data/sample-data --destination store:8080
  docker compose -f demo/docker-compose.yml exec client ./rwfs list store:8080
  docker compose -f demo/docker-compose.yml exec client ./rwfs verify store:8080
  docker compose -f demo/docker-compose.yml logs -f store
  docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
  docker compose -f demo/docker-compose.yml exec policy-server ./agent list-policies
  docker compose -f demo/docker-compose.yml exec store ./agent list-policies

Reset with: docker compose -f demo/docker-compose.yml down -v
MSG
```

Replace with:

```sh
cat <<'MSG'

Demo stack is up. Try:
  docker compose -f demo/docker-compose.yml exec database ./brfs /var/lib/dbdata --destination store:8080
  docker compose -f demo/docker-compose.yml exec database ./rwfs list store:8080
  docker compose -f demo/docker-compose.yml exec database ./rwfs verify store:8080
  docker compose -f demo/docker-compose.yml logs -f store          # watch bwfs receive + catalogsync replicate
  docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
  docker compose -f demo/docker-compose.yml exec catalog ./agent list-policies
  docker compose -f demo/docker-compose.yml exec policy-server ./agent list-policies
  docker compose -f demo/docker-compose.yml exec database ./agent list-policies
  docker compose -f demo/docker-compose.yml exec webserver ./agent list-policies
  docker compose -f demo/docker-compose.yml exec store ./agent list-policies

Reset with: docker compose -f demo/docker-compose.yml down -v
MSG
```

- [ ] **Step 3: Verify the script is still syntactically valid shell**

Run: `sh -n demo/up.sh && echo OK`
Expected: `OK`

- [ ] **Step 4: Commit**

```bash
git add demo/up.sh
git commit -m "feat(demo): enroll database and webserver in place of client, labeling webserver role=web"
```

---

### Task 4: Seed `policy-server` with three example policies

**Files:**
- Create: `demo/policy-server/policies/audit-logs.json`
- Create: `demo/policy-server/policies/database-backup.json`
- Create: `demo/policy-server/policies/webserver-backup.json`

**Interfaces:**
- Consumes: Task 2's `./policy-server/policies:/data/policies` bind mount (already added to `demo/docker-compose.yml`); Task 2's `/var/log/audit`, `/var/lib/dbdata`, `/var/www/html` mount paths; Task 3's `role=web` label on `webserver`.

- [ ] **Step 1: Create `demo/policy-server/policies/audit-logs.json`**

Multi-hostname selection — covers both clients by explicit name list:

```json
{
  "metadata": {
    "name": "audit-logs",
    "created_at": "2026-07-11T00:00:00Z",
    "updated_at": "2026-07-11T00:00:00Z"
  },
  "client_filters": {
    "hostnames": ["database", "webserver"]
  },
  "object_filters": [
    {"path": "/var/log/audit"}
  ],
  "rpo": "1h",
  "backup_window": ["0 * * * *"],
  "destination": "store:8080"
}
```

- [ ] **Step 2: Create `demo/policy-server/policies/database-backup.json`**

Single-hostname selection:

```json
{
  "metadata": {
    "name": "database-backup",
    "created_at": "2026-07-11T00:00:00Z",
    "updated_at": "2026-07-11T00:00:00Z"
  },
  "client_filters": {
    "hostnames": ["database"]
  },
  "object_filters": [
    {"path": "/var/lib/dbdata"}
  ],
  "rpo": "1h",
  "backup_window": ["0 * * * *"],
  "destination": "store:8080"
}
```

- [ ] **Step 3: Create `demo/policy-server/policies/webserver-backup.json`**

Label-only selection — matches only the node carrying `role=web`:

```json
{
  "metadata": {
    "name": "webserver-backup",
    "created_at": "2026-07-11T00:00:00Z",
    "updated_at": "2026-07-11T00:00:00Z"
  },
  "client_filters": {
    "labels": {"role": "web"}
  },
  "object_filters": [
    {"path": "/var/www/html"}
  ],
  "rpo": "1h",
  "backup_window": ["0 * * * *"],
  "destination": "store:8080"
}
```

- [ ] **Step 4: Validate all three files are syntactically valid JSON**

Run:
```bash
for f in demo/policy-server/policies/*.json; do python3 -m json.tool "$f" >/dev/null && echo "$f OK"; done
```
Expected: three `... OK` lines.

- [ ] **Step 5: Commit**

```bash
git add demo/policy-server/policies
git commit -m "feat(demo): seed policy-server with three example policies (multi-hostname, hostname, label)"
```

---

### Task 5: Documentation — `demo/README.md`, `Makefile`, `CHANGELOG.md`

**Files:**
- Modify: `demo/README.md`
- Modify: `Makefile`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update `demo/README.md`'s intro paragraph**

Current (lines 1-8):

```markdown
# Demo Lab Environment

A self-contained `docker compose` stack — CA, `issuer`, `catalog`, and two backup-capable nodes
(`client`, `store`) — mutually enrolled via mTLS, brought up with one script. Unlike
[`deploy/control-plane`](../deploy/control-plane/README.md), this never touches your host
filesystem beyond this directory: every secret and every byte of state lives in Docker-managed
named volumes, and no port is published to the host. Everything is reached via `docker compose
exec`.
```

Replace with:

```markdown
# Demo Lab Environment

A self-contained `docker compose` stack — CA, `issuer`, `catalog`, `policy-server`, and three
backup-capable nodes (`database`, `webserver`, `store`) — mutually enrolled via mTLS, brought up
with one script. Unlike [`deploy/control-plane`](../deploy/control-plane/README.md), this never
touches your host filesystem beyond this directory (except `demo/policy-server/policies/`, which
you're meant to edit — see "Backup policies" below): every secret and every byte of state lives in
Docker-managed named volumes, and no port is published to the host. Everything is reached via
`docker compose exec`.
```

- [ ] **Step 2: Update the "Bring it up" section**

Current:

```markdown
## Bring it up

```bash
make demo-up
```

Equivalent to `./demo/up.sh` directly. Builds all six images, brings up `ca` and `issuer` first,
then mints and redeems an enrollment token for `catalog`, `policy-server`, `client`, and `store` in
turn (skipping re-minting on a re-run against an already-enrolled node).
```

Replace with:

```markdown
## Bring it up

```bash
make demo-up
```

Equivalent to `./demo/up.sh` directly. Builds all seven images, brings up `ca` and `issuer` first,
then mints and redeems an enrollment token for `catalog`, `policy-server`, `database`, `webserver`,
and `store` in turn (skipping re-minting on a re-run against an already-enrolled node). `webserver`
is additionally tagged with the attribute `role=web`, used by one of the example backup policies
below.
```

- [ ] **Step 3: Update the "Try it" section**

Current:

```markdown
## Try it

```bash
docker compose -f demo/docker-compose.yml exec client ./brfs /data/sample-data --destination store:8080
docker compose -f demo/docker-compose.yml exec client ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec client ./rwfs verify store:8080
docker compose -f demo/docker-compose.yml logs -f store          # watch bwfs receive + catalogsync replicate
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
docker compose -f demo/docker-compose.yml exec catalog ./agent list-policies
docker compose -f demo/docker-compose.yml exec policy-server ./agent list-policies
docker compose -f demo/docker-compose.yml exec client ./agent list-policies
docker compose -f demo/docker-compose.yml exec store ./agent list-policies
```
```

Replace with:

```markdown
## Try it

```bash
docker compose -f demo/docker-compose.yml exec database ./brfs /var/lib/dbdata --destination store:8080
docker compose -f demo/docker-compose.yml exec database ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec database ./rwfs verify store:8080
docker compose -f demo/docker-compose.yml logs -f store          # watch bwfs receive + catalogsync replicate
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
```
```

- [ ] **Step 4: Insert a new "Backup policies" section, after "Try it" and before "Revoke, and watch mesh access lapse..."**

```markdown
## Backup policies

`policy-server` ships with three example policies (`demo/policy-server/policies/`), each
demonstrating a different way `client_filters` can select clients:

| Policy | Selects | Backs up |
|---|---|---|
| `audit-logs` | `database` and `webserver`, by explicit hostname list | `/var/log/audit` |
| `database-backup` | `database`, by hostname | `/var/lib/dbdata` |
| `webserver-backup` | any client labeled `role=web` (only `webserver`, here) | `/var/www/html` |

Confirm each node resolves the policies meant for it (`catalog`, `policy-server`, and `store` run
`policy-update` too, like every `agent`-managed node, but match none of these three policies — their
own `list-policies` output is still worth checking, just to confirm the job itself is succeeding
rather than failing on a missing `policy_server_host`):

```bash
docker compose -f demo/docker-compose.yml exec catalog ./agent list-policies
docker compose -f demo/docker-compose.yml exec policy-server ./agent list-policies
docker compose -f demo/docker-compose.yml exec database ./agent list-policies
docker compose -f demo/docker-compose.yml exec webserver ./agent list-policies
docker compose -f demo/docker-compose.yml exec store ./agent list-policies
```

`database` should show `audit-logs` and `database-backup`; `webserver` should show `audit-logs` and
`webserver-backup`. Every policy's `backup_window` is hourly, so within `agent`'s own reconcile
loop a due backup runs for real shortly after enrollment — watch one land:

```bash
docker compose -f demo/docker-compose.yml logs -f database
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
```

Edit a policy and watch it reload live. `policy-server` watches one sentinel file,
`policies/.changed` — any write to it triggers a full reload of every `*.json` file under
`policies/`, so a multi-file edit reloads atomically instead of file-by-file:

```bash
docker compose -f demo/docker-compose.yml exec policy-server sh -c \
  "sed -i 's/1h/30m/' /data/policies/database-backup.json && touch /data/policies/.changed"
docker compose -f demo/docker-compose.yml logs policy-server | tail -5   # confirm the reload log line
```
```

- [ ] **Step 5: Update the `Makefile`'s `demo-up` help text**

Current line:

```makefile
demo-up: ## Bring up the self-contained demo lab (ca + issuer + catalog + client + store)
```

Replace with:

```makefile
demo-up: ## Bring up the self-contained demo lab (ca + issuer + catalog + policy-server + database + webserver + store)
```

- [ ] **Step 6: Add a `CHANGELOG.md` entry**

Insert a new entry at the top, right after the `# Changelog` header and its intro line (before the existing `## 2026-07-11 — Agent backup state hygiene...` entry):

```markdown
## 2026-07-11 — Policy server consumer wiring and demo content

`policy_server_host` — missing from every `local.conf` since `policy-update` shipped, silently
failing that job every reconcile cycle everywhere — is now set in `demo/local.conf` and both
`deploy/control-plane/catalog/local.conf` and `deploy/control-plane/policy-server/local.conf`. The
demo lab's single generic `client` node is renamed to `database` and a new `webserver` node
(labeled `role=web`) joins it; `policy-server` ships with three example policies covering every
`client_filters` selection mechanism (explicit multi-hostname, single hostname, label) against
themed fixture content (`/var/log/audit`, `/var/lib/dbdata`, `/var/www/html`).
```

- [ ] **Step 7: Commit**

```bash
git add demo/README.md Makefile CHANGELOG.md
git commit -m "docs: document policy-server consumer wiring and the demo's new backup policies"
```

---

### Task 6: End-to-end verification

**Files:** none (verification only).

- [ ] **Step 1: Cold rebuild and bring up the demo**

Run: `make demo-down && make demo-up`
Expected: all seven services reach steady `Up`; `database` and `webserver` both enroll
successfully (no `already-tracked-hostname` or timeout errors in the script's output). If `docker`
isn't available in this environment, note that in your report and skip to reviewing the plan's
static content (Tasks 1-5) instead of treating this as a blocker.

- [ ] **Step 2: Confirm `policy-update` is succeeding fleet-wide**

Run: `docker compose -f demo/docker-compose.yml exec catalog ./agent list-policies`
Expected: the `policy-update` row shows a recent successful run, no `LastError` — proving Task 1's
fix actually took effect (previously this would show a `policy_server_host not set` failure every
cycle).

- [ ] **Step 3: Confirm each client resolves exactly the policies meant for it**

Run:
```bash
docker compose -f demo/docker-compose.yml exec database ./agent list-policies
docker compose -f demo/docker-compose.yml exec webserver ./agent list-policies
```
Expected: `database`'s output lists backup tasks derived from `audit-logs` and `database-backup`
only; `webserver`'s output lists tasks derived from `audit-logs` and `webserver-backup` only —
confirming all three `client_filters` variants (multi-hostname, single hostname, label) resolve
correctly against the real running `policy-server`.

- [ ] **Step 4: Confirm a real backup actually lands**

Run: `docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"`
Expected: rows present whose source corresponds to `database`'s and `webserver`'s backup runs —
proving the full pipeline (policy-server → policyclient cache → agent-derived backup task → brfs →
store → catalogsync → catalog) works end-to-end, not just that `GetPolicies` resolves correctly.

- [ ] **Step 5: Confirm the `.changed` reload demo from the README works as documented**

Run the exact commands from `demo/README.md`'s new "Backup policies" section (the `sed`+`touch`
pair, then `docker compose logs policy-server | tail -5`).
Expected: a log line confirming policy-server reloaded its cache after the `.changed` write.

- [ ] **Step 6: Tear down**

Run: `make demo-down`
Expected: clean teardown, no errors — confirms nothing left the demo in a state that would break
the next `make demo-up`.

No commit for this task — it's verification only, not a code change.
