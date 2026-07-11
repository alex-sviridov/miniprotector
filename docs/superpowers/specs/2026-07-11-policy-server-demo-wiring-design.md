# Policy Server Consumer Wiring & Demo Content — Design

> Follow-up to `docs/superpowers/specs/2026-07-10-policy-server-deployment-design.md`, which made
> `policy-server` a running, enrollable service in both `deploy/control-plane/docker-compose.yml`
> and `demo/docker-compose.yml`. That doc's own Non-Goals explicitly deferred wiring any consumer of
> `GetPolicies` ("nothing in either compose environment calls the RPC"). Since then,
> `cmd/policyclient` and `agent`'s `policy-update` job (`docs/superpowers/specs/
> 2026-07-10-agent-policy-update-job-design.md`) and `agent`'s policy-driven backup-task execution
> (`docs/superpowers/specs/2026-07-10-agent-backup-execution-design.md`) have both shipped — `agent`
> now unconditionally runs `policy-update` every reconcile cycle on every node, and derives real
> `brfs` backup jobs from whatever lands in `policies-cache.json`. This spec closes the resulting gap
> and gives the demo lab real, working policy content to show it off.

## Problem

Two separate gaps, discovered while planning this work:

1. **No node can actually reach `policy-server`.** `agent`'s `policy-update` policy is one of three
   unconditional standard policies (`cmd/agent/policy.go`) — it runs every reconcile cycle on every
   node that runs `agent serve`, including `catalog` and `policy-server` itself. `policyclient fetch`
   refuses to run without `policy_server_host` set (`cmd/policyclient/main.go`), and no `local.conf`
   in either `deploy/control-plane/` or `demo/` sets it. The job has been failing every cycle on
   every `agent`-running node in both environments since it was merged — silently, except for the
   per-policy last-error tracking added in `docs/superpowers/specs/
   2026-07-11-agent-backup-state-hygiene-design.md`, which would now be surfacing exactly this
   failure in `agent list-policies` output.
2. **The demo has no policy content and only one backup-source node.** `demo/docker-compose.yml`'s
   `policy-server` service has a named volume, not host-editable content, and nothing seeds it.
   There's one generic client (`client`) pushing generic sample files — no way to demonstrate
   hostname-based, label-based, or multi-hostname policy selection side by side.

## Approach

### 1. Consumer wiring

Add `policy_server_host=policy-server` to every `local.conf` that config an `agent`-running node in
both environments:

- `demo/local.conf` — one shared file already mounted into every demo service (`catalog`,
  `policy-server`, and both backup-client nodes below), so this single line fixes all of them.
- `deploy/control-plane/catalog/local.conf`.
- `deploy/control-plane/policy-server/local.conf` — points at itself; also replace the comment
  currently reading "No consumer dials this yet — wiring an actual client... is separate, later
  work," which is no longer accurate.

`policy_server_port` is not set explicitly anywhere — `config.Config`'s default (`9300`) already
matches `policy-server`'s actual listening port in both environments.

`deploy/control-plane/issuer/local.conf` is untouched: `issuer` mints its own identity directly and
never runs generic `agent serve` (`deploy/control-plane/issuer/Dockerfile`), so it never runs
`policy-update`.

### 2. Demo topology: rename `client` → `database`, add `webserver`

- Rename the existing `client` service to **`database`** throughout `demo/docker-compose.yml`,
  `demo/up.sh`, and `demo/README.md` — the only three files that reference it (confirmed by search;
  `store` and every other service are unaffected). Volume `client-data` → `database-data`.
- Add a new service, **`webserver`**, built from the same `demo/backup-host/Dockerfile` as
  `database` (no image changes needed — it already only differs from `store` by leaving
  `STORAGE_PATH` unset). New volume `webserver-data`.
- `demo/up.sh`'s `enroll()` helper gains an optional third argument, a space-separated
  `key=value` attribute string, applied via `clientmanager attribute set` immediately after
  `clientmanager add` and before the node's own container starts — so the attribute is already
  present in `client-manager`'s database before that node's first `operating-refresh` mints a cert,
  guaranteeing the label is embedded from the very first certificate rather than lagging a refresh
  cycle behind:

  ```sh
  enroll catalog
  enroll policy-server
  enroll database
  enroll webserver "role=web"
  enroll store
  ```

  The attribute-set call only runs on the not-yet-enrolled branch (a fresh `clientmanager add`), not
  on the idempotent re-run branch — consistent with `up.sh`'s existing documented idempotency
  contract (a clean `down -v` is the supported way to reset state).

### 3. Sample content: themed, real, per-role

`demo/sample-data/` is restructured from its current flat `notes.txt`/`hello.txt` into three
subfolders, each holding small fake files with plausible (not functional) content:

| Host directory | Fake files | Mounted at | Mounted on |
|---|---|---|---|
| `demo/sample-data/audit/` | `audit.log` | `/var/log/audit` | `database`, `webserver` |
| `demo/sample-data/db/` | `dump.sql`, `schema.sql` | `/var/lib/dbdata` | `database` only |
| `demo/sample-data/web/` | `index.html`, `style.css` | `/var/www/html` | `webserver` only |

All three mounts are read-only (`:ro`) — this is fixture content for `brfs` to read, not something
either container writes to. `demo/README.md`'s existing "Try it" `brfs`/`rwfs` walkthrough commands,
which currently reference `client` and `/data/sample-data`, are updated to `database` and
`/var/lib/dbdata`.

### 4. Three demo policies, seeded into `policy-server`

`demo/docker-compose.yml`'s `policy-server` service gains a second volume entry,
`./policy-server/policies:/data/policies` (read-write, matching `deploy/control-plane`'s own
bind-mount convention for policy-server's data — not `:ro`, so the `.changed`-triggered reload can
still be demonstrated by editing a host file and touching `.changed` directly), alongside the
existing named `policy-server-data:/data` volume — Docker mounts the bind mount over that
subdirectory of the named volume, which is a well-supported pattern.

Three files under `demo/policy-server/policies/`, each demonstrating a different `client_filters`
mechanism:

**`audit-logs.json`** — explicit multi-hostname list, no label restriction:
```json
{
  "metadata": {"name": "audit-logs", "created_at": "2026-07-11T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
  "client_filters": {"hostnames": ["database", "webserver"]},
  "object_filters": [{"path": "/var/log/audit"}],
  "rpo": "1h",
  "backup_window": ["0 * * * *"],
  "destination": "store:8080"
}
```

**`database-backup.json`** — single-hostname selection:
```json
{
  "metadata": {"name": "database-backup", "created_at": "2026-07-11T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
  "client_filters": {"hostnames": ["database"]},
  "object_filters": [{"path": "/var/lib/dbdata"}],
  "rpo": "1h",
  "backup_window": ["0 * * * *"],
  "destination": "store:8080"
}
```

**`webserver-backup.json`** — label-only selection (matches only the node carrying `role=web`):
```json
{
  "metadata": {"name": "webserver-backup", "created_at": "2026-07-11T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
  "client_filters": {"labels": {"role": "web"}},
  "object_filters": [{"path": "/var/www/html"}],
  "rpo": "1h",
  "backup_window": ["0 * * * *"],
  "destination": "store:8080"
}
```

`backup_window: ["0 * * * *"]` (hourly) paired with `agent`'s default `BackupWindowGraceSec` (3600s)
guarantees `windowOpen` is always true regardless of when the demo happens to be started — combined
with `rpo` always being "elapsed" on a node's first-ever run (`PolicyState.LastSuccessAt == nil`),
every policy's backup task is due immediately after enrollment, so the walkthrough doesn't need to
wait for a specific time of day or a real elapsed RPO window.

### 5. Documentation

- `demo/README.md`: node list/count updated (three backup-capable nodes: `database`, `webserver`,
  `store`), "Try it" commands updated to the new hostnames/paths, and a new short walkthrough showing
  `agent list-policies` on `database` vs. `webserver` resolving different (and, for `audit-logs`,
  shared) policy sets, plus a `.changed`-triggered reload demonstration (edit a policy file under
  `demo/policy-server/policies/`, touch `.changed`, show the reload log line in `docker compose logs
  policy-server`).
- `CHANGELOG.md`: one entry covering the consumer wiring and the demo content.

`docs/ARCHITECTURE.md` is not touched — it references `demo/README.md` for lab-environment details
rather than enumerating demo node names itself, so nothing there goes stale.

## Non-Goals

- No changes to `policy-server`'s, `agent`'s, or `policyclient`'s own application code — this is
  deployment configuration and demo fixture content only.
- No new `deploy/control-plane` demo content or example policies — that environment stays a bare
  production-reference skeleton with no seeded clients or policies, matching its existing posture
  (unlike `demo/`, `deploy/control-plane` never ships example data).
- No change to `MaxConcurrentBackupJobs` or any other `agent`/backup-execution tuning — the demo's
  three policies (max two backup tasks due at once per node) stay comfortably under the existing
  default cap of 2.
- No renaming or restructuring of `store` — it keeps its existing role, name, and configuration
  untouched.

## Testing

- Cold `make demo-down && make demo-up`: confirms `database` and `webserver` both enroll, reach
  steady `Up`, and their `agent list-policies` output shows `policy-update` succeeding (not
  failing on missing `policy_server_host`).
- `docker compose exec database ./agent list-policies` shows `audit-logs` and `database-backup`
  resolved; `docker compose exec webserver ./agent list-policies` shows `audit-logs` and
  `webserver-backup` resolved — confirming all three filter variants match correctly.
- After the first reconcile cycle, `docker compose exec store ./rwfs list store:8080` (or the
  catalog's `entry_records` table) shows real backup entries landing from both `database` and
  `webserver`'s `brfs` runs, proving the policy-to-execution pipeline works end-to-end, not just
  that `GetPolicies` resolves correctly.
- `deploy/control-plane`'s `docker compose config` (or equivalent) still validates after the
  `local.conf` edits.
