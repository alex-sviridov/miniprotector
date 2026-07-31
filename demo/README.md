# Demo Lab Environment

A self-contained `docker compose` stack — CA, `issuer`, `catalog`, `policy-server`, and three
backup-capable nodes (`database`, `webserver`, `store`) — mutually enrolled via mTLS, brought up
with one script. Unlike [`deploy/control-plane`](../deploy/control-plane/README.md), this never
touches your host filesystem beyond this directory (except `demo/policy-server/policies/`, which
you're meant to edit — see "Backup policies" and "Storage policy" below): every secret and every
byte of state lives in Docker-managed named volumes, and no port is published to the host.
Everything is reached via `docker compose exec`.

## Bring it up

```bash
make demo-up
```

Equivalent to `./demo/up.sh` directly. Builds all 11 service images, brings up `ca` and `issuer` first,
then mints and redeems an enrollment token for `catalog`, `policy-server`, `database`, `webserver`,
and `store` in turn (skipping re-minting on a re-run against an already-enrolled node). `webserver`
is additionally tagged with the attribute `role=web`, used by one of the example backup policies
below.

## Try it

`store`'s `bwfs`/`catalogsync` take up to `ReconcileIntervalSec` (30s in this demo) to come up after
`make demo-up` returns — see "Storage policy" below. If the first command fails immediately after
bringing the stack up, wait a bit and retry.

```bash
docker compose -f demo/docker-compose.yml exec database ./brfs /var/lib/dbdata --destination store:8080
docker compose -f demo/docker-compose.yml exec database ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec database ./rwfs verify store:8080
docker compose -f demo/docker-compose.yml logs -f store          # watch bwfs receive + catalogsync replicate
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
```

## Browser UI

`web` serves a small Vue frontend over `api-server`'s read-only REST API, published at
`http://localhost:8091`. On first load it prompts for a bearer token — use the demo lab's
placeholder token, `dev-placeholder-token-change-me` (see `demo/local.conf`). From there, browse
`/clients` and `/catalog`.

## Backup policies

`policy-server` ships with three example policies (`demo/policy-server/policies/backup/`), each
demonstrating a different way `client_filters` can select clients:

| Policy | Selects | Backs up |
|---|---|---|
| `audit-logs` | `database` and `webserver`, by explicit hostname list | `/var/log/audit` |
| `database-backup` | `database`, by hostname | `/var/lib/dbdata` |
| `webserver-backup` | any client labeled `role=web` (only `webserver`, here) | `/var/www/html` |

## Storage policy

`store` doesn't run `bwfs`/`catalogsync` unconditionally — like every other node, it just runs
`agent`, which starts and supervises both processes once it picks up the one storage policy shipped
in `demo/policy-server/policies/storage/store.json` (targets `store` by hostname, port `8080`, root
`/data/storage` — matching what every example backup policy's `destination: "store:8080"` expects).
That pickup happens on `agent`'s next reconcile tick after enrollment, so expect a roughly
`ReconcileIntervalSec`-long (30s in this demo) delay after `make demo-up` before
`docker compose -f demo/docker-compose.yml logs -f store` shows either process starting.

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
`policies/.changed` — any write to it triggers a full reload of every `*.json` file found one level
under `policies/` (i.e. under a type subfolder such as `policies/backup/`), so a multi-file edit
reloads atomically instead of file-by-file:

```bash
docker compose -f demo/docker-compose.yml exec policy-server sh -c \
  "sed -i 's/1h/30m/' /data/policies/backup/database-backup.json && touch /data/policies/.changed"
docker compose -f demo/docker-compose.yml logs policy-server | tail -5   # confirm the reload log line
```

## Revoke, and watch mesh access lapse without losing identity

```bash
docker compose -f demo/docker-compose.yml exec ca clientmanager revoke store
docker compose -f demo/docker-compose.yml exec store ./certclient operating-refresh   # fails
docker compose -f demo/docker-compose.yml exec store ./certclient renew               # still succeeds
docker compose -f demo/docker-compose.yml exec ca clientmanager unrevoke store
```

## Reset

```bash
make demo-down
```

Removes every container and volume — the next `make demo-up` starts from a byte-for-byte clean
slate, including a freshly generated CA and provisioner password.

## See Also

- [Design: Demo Lab Environment v2](../docs/superpowers/specs/2026-07-06-demo-lab-environment-v2-design.md)
- [Control Plane](../deploy/control-plane/README.md) — the production-shaped deployment reference this demo deliberately never reuses (separate compose file, volumes, and ports)
- [Architecture](../docs/ARCHITECTURE.md)
- [Security Model](../docs/SECURITY.md)
