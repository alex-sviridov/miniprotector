# System Architecture
A backup system with intelligent deduplication and integrity verification.

## Components

| Component | Full name | Status |
|-----------|-----------|--------|
| brfs | Backup Reader for File System — reads files from source, sends via gRPC | Implemented |
| bwfs | Backup Writer for File System — receives via gRPC, stores chunks + metadata | Implemented |
| rwfs | Restore Writer for File System — queries bwfs (list, verify, restore) | list, verify, and restore fully implemented -- `restore` creates the resolved directory structure and writes real file content to the destination filesystem |
| catalogsync | Replicates a bwfs node's file_versions to a backup catalog | Implemented |
| catalog | Backup Catalog — receives catalogsync's replicated file_versions over gRPC | Implemented |
| agent | Node Agent — reconciles local state against embedded policies | Implemented (bootstrap credential renewal, operating-certificate refresh via `issuer`, policy fetch via `policyclient`, policy-driven backup execution via `brfs`, one-shot restore-policy verification via `rwfs verify`, and one-shot restore execution via `rwfs restore` for `mode: "restore"` policies -- both directory structure creation and file content restore are real) |
| client-manager | Owns the enrolled-client list: descriptions, RBAC-bound attributes, SAN aliases, revoked status; mints enrollment tokens directly | Implemented (enforcement lives in `issuer`, which agent now drives — see below) |
| issuer | Mints short-lived operating certificates, enforcing revoke and embedding current attributes; shares client-manager's database | Implemented (agent integration done; a CA-side custom template for attribute embedding remains separate, later work) |
| policy-server | Serves backup policies filtered by a requesting client's hostname and attribute labels, reading labels from the peer cert; tracks per-host check-ins in a local SQLite database | Implemented (`agent` fetches, caches, and now acts on its policies — deriving and running scheduled `brfs` backups via `policyclient`) |
| log-gateway | mTLS-terminating HTTP reverse proxy in front of Loki; gates on a valid operating certificate, forwards the push body unmodified | Implemented (agent bundles, configures, and supervises the Vector process that ships to it) |
| clientmanager-api | Read-only gRPC daemon exposing `client-manager`'s enrolled-client data (`ListClients`/`GetClient`), sharing its SQLite file the same way `issuer` already does | Implemented |
| clientmanager-admin-api | CA-admin-equivalent gRPC writes (issue/re-enroll/revoke/unrevoke/description/attribute/SAN) onto the same database, packaged in clientmanager-api's container | Implemented |
| api-server | Read-only REST API in front of `clientmanager-api` and `catalog` — this system's first REST (not gRPC) entry point, for callers without a mesh mTLS client certificate | Implemented |
| web | Static Vue frontend over `api-server`'s REST API — this system's first browser UI; served by nginx, no mTLS identity of its own | Implemented |

## Control Plane vs. Agents

See [demo/README.md](../demo/README.md) for a self-contained, one-command lab environment
exercising this whole topology end to end.

|  | Control plane | Agents |
|---|---|---|
| Components | `deploy/control-plane/ca/` (step-ca container), `catalog`, `policy-server`, `client-manager`, `issuer`, `clientmanager-api`, `clientmanager-admin-api`, `api-server` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
| Runs where | On the CA host (`client-manager`, `issuer`, `clientmanager-api`, `clientmanager-admin-api`); `catalog`/`policy-server`/`api-server` run centrally, wherever each deployment lives — see below | Dial `ca_host:9000` outbound for enrollment/renewal and `issuer_host:9200` outbound for operating-certificate refresh, and `policy_server_host:9300` outbound for policy fetching; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Network role | Serves enrollment/renewal/admin (`/sign`, `/renew`, `/roots`, `/provisioners`) on `:9000`; `issuer` serves `RequestOperatingCert`/`DescribeSANs` on `:9200` (mTLS); `policy-server` serves `GetPolicies` on `:9300` (mTLS, fetched by `agent` via `policyclient`); `clientmanager-api` serves `ListClients`/`GetClient` on `:9500` (mTLS); `clientmanager-admin-api` serves `AddClient`/`ReEnrollClient`/`RevokeClient`/`UnrevokeClient`/`UpdateDescription`/`UpdateAttributes`/`UpdateSANs` on `:9501` (mTLS) — a third holder of CA-admin-equivalent access, alongside `client-manager` and `issuer`, stated explicitly rather than left implicit; `api-server` serves this system's first REST (not gRPC) surface on `:8090` (plain HTTP, bearer-token authenticated), dialing `clientmanager-api`, `clientmanager-admin-api`, and `catalog` outbound over mTLS on their behalf — none of these has a role in backup traffic | Dial `ca_host:9000` (bootstrap/renew) and `issuer_host:9200` (operating-refresh) outbound only; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Docker/e2e images | Control-plane-only binaries (`client-manager`, `issuer`) never ship onto an agent host or into an agent image | Agent images bundle `certclient` and `agent` — `catalog`'s, `policy-server`'s, `clientmanager-api`'s, and `api-server`'s images are all among them, since each is deployed as an ordinary `agent`-managed enrolled node (see [Control Plane README](../deploy/control-plane/README.md)) |

`issuer` is the one exception to the "obtained via `certclient`" rule below: it mints and signs its
own mTLS server identity directly at startup and re-mints it on an internal ticker while running,
reusing the same CA provisioner access it already holds for issuing operating certificates — no
enrollment token, no `certclient`, no second process on the CA host. See
[issuer](components/issuer.md#self-identity-minting-its-own-server-certificate).

`catalog` is control plane by role (a fleet-wide central service, not colocated with any single
backup node) but obtains its own mTLS identity the same way any other enrolled node does — its
image bundles `agent` (which wraps `certclient`), and it runs as an ordinary `agent`-managed node
with continuously-refreshed bootstrap and operating credentials, not a one-shot bootstrap redeemed
only at container start — it doesn't fit either row cleanly. It listens on its own port
(`catalog_port`, default 15723) for `catalogsync` connections from every `bwfs` node's agent host.

`policy-server` is control plane by role (a fleet-wide policy distribution service) but, like
`catalog`, obtains its own mTLS identity as an ordinary `agent`-managed enrolled node rather than a
one-shot bootstrap — its image bundles `agent` the same way `catalog`'s does. It listens on its own
port (`policy_server_port`, default 9300); `agent` now dials it on a schedule (`policy-update`, via
`policyclient fetch`) and caches the result locally, and `agent` now acts on that cache directly, deriving and running scheduled `brfs` backups from
it — see [agent](components/agent.md#policy-driven-backup-execution).
It also now owns local state -- a SQLite database recording which hosts have checked in for which
policy and when, purged on a fixed 1-minute cleanup tick -- the first piece of persistent state this
component has held; it still calls no other service. See
[policy-server](components/policy-server.md#check-in-tracking).

`clientmanager-api` runs on the CA host alongside `client-manager` and `issuer`, needing the same
direct filesystem access to the shared `clientmanager.sqlite` file — but unlike `issuer`, it
obtains its mTLS identity the ordinary way, via `certclient`. It is a thin, read-only (`ListClients`/
`GetClient`) gRPC front end onto that database; `client-manager` (the CLI) and `issuer` remain the
only writers. It listens on its own port (`clientmanager_api_port`, default 9500). See
[clientmanager-api](components/clientmanager-api.md).

`clientmanager-admin-api` runs alongside `clientmanager-api` in the *same container*, sharing one
mesh identity/`agent` process rather than enrolling separately — see
[clientmanager-admin-api](components/clientmanager-admin-api.md) and
[Design: clientmanager-admin-api](superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
for why (avoiding a second one-time enrollment token and a second `agent` process for what would
otherwise be a purely operational cost, at the price of shared container-filesystem isolation between
the two binaries).

`api-server` is control plane by role and, like `catalog`/`policy-server`, obtains its own mTLS
identity as an ordinary `agent`-managed enrolled node — but only for its *outbound* calls to
`clientmanager-api` and `catalog`. Its inbound side is this system's first REST (not gRPC) entry
point: a plain-HTTP listener on its own port (`api_server_port`, default 8090), guarded by a single
shared bearer token rather than mesh mTLS, for callers (browsers, admin tools) that don't hold a
mesh client certificate. Each REST endpoint maps to exactly one backend gRPC call — no
cross-service aggregation. See [api-server](components/api-server.md) and
[REST API v1](api/rest-v1.md).

A node's mTLS identity is obtained in two tiers, both via `certclient`: `bootstrap` redeems a
one-time token minted by `client-manager` for `ca.crt` plus a long-lived `bootstrap.crt`/
`bootstrap.key` pair; `operating-refresh` then uses that bootstrap credential to authenticate to
`issuer` and obtain the short-lived `client.crt`/`client.key` that `common/mtls` actually reads for
every other component's transport. See [client-manager](components/client-manager.md),
[issuer](components/issuer.md), [certclient](components/certclient.md), and, for the full
rationale behind this split and its trust-model trade-offs, [Security Model](SECURITY.md).

`agent` is a node-level process that wraps `certclient` — intended to replace the bare cron entries
that would otherwise invoke `certclient` directly: `agent serve` runs a reconcile loop with three
config-driven policies: `bootstrap-refresh` (`certclient renew`, daily) and `operating-refresh`
(`certclient operating-refresh`, every 15 minutes by default) keep this node's mTLS credentials
fresh; `policy-update` (`policyclient fetch`, every 15 minutes by default) fetches this node's
applicable backup policies from `policy-server` into a local cache. `agent` also derives a dynamic
backup task per cached policy's object filter (a path plus optional include/exclude glob patterns,
passed straight through to `brfs`) and executes `brfs` for each one on a schedule gated by
that policy's `backup_window` and `rpo` — see [agent](components/agent.md#policy-driven-backup-execution).
`agent` additionally supervises a `bwfs server` process and a `catalogsync` process, independently
of each other, for every cached `"storage"`-typed policy targeting this node (ensure-running, not
scheduled — see [agent](components/agent.md#storage-policy-supervision)), the first actual consumer
of the `"storage"` policy type. `agent` additionally derives one one-shot verification task per
cached `"restore"`-typed policy, executing `rwfs verify` against the resolved source `bwfs` (or,
when that policy's `mode` is `"restore"`, `rwfs restore`, which creates the resolved directory
structure on disk and then writes the resolved file content to it) —
see
[agent](components/agent.md#policy-driven-restore-verification). Each policy's (and backup task's,
storage task's, and restore task's) outcome is tracked in
the same local cache (`agent list-policies` inspects it). See [agent](components/agent.md).

`client-manager` is control plane by role (an admin-facing tool tracking the enrolled-client
fleet) but, unlike every other component in this table, has no mTLS identity and no network
interface of its own at all — it runs directly on the CA host as a single-operator CLI, holding
the CA's provisioner password directly. See
[Design: Client Manager Phase 2](superpowers/specs/2026-07-04-client-manager-phase2-design.md)
for why that's safe now, having originally been placed on a separate host in phase 1 specifically
to avoid this.

## Backup Process

- **brfs** reads files from the source filesystem
- Connects to **bwfs** via network or Unix socket, authenticated with mutual TLS
- Sends chunked file data using the backup protocol
- **bwfs** stores needed chunks on the backup filesystem and records metadata in SQLite

## Restore/Verify Process

- **rwfs** connects to **bwfs** via network or Unix socket using the list/restore protocol, authenticated with mutual TLS
- **rwfs list** queries metadata from the remote **bwfs** server
- **rwfs verify** fetches all chunks and re-verifies BLAKE3 and CRC32 integrity without writing to disk
- **rwfs restore** creates the resolved directory structure on the destination filesystem and then
  writes the resolved file content to it, verifying per-chunk BLAKE3 and the whole-file CRC32 as it
  writes

## Data Flow

```mermaid
graph TB
    subgraph "Source Machine"
        SrcFS[Source Filesystem]
        brfs[brfs<br/>Backup Reader]
    end

    subgraph "Backup Machine"
        bwfs[bwfs<br/>Backup Writer]
        BackupFS[Backup Filesystem]
        DB[(SQLite Database)]
        catalogsync[catalogsync<br/>Catalog Replicator]
        bwfsAgent[agent<br/>Node Agent]
    end

    subgraph "Catalog"
        Catalog[(Backup Catalog)]
    end

    subgraph "Destination Machine"
        rwfs[rwfs<br/>Restore Writer]
        DstFS[Destination Filesystem]
    end

    %% Backup Flow
    SrcFS -->|reads files| brfs
    brfs -->|backup protocol<br/>network/unix socket, mTLS| bwfs
    bwfs -->|stores chunks| BackupFS
    bwfs -->|stores metadata| DB

    %% Storage-policy supervision -- agent ensures bwfs and catalogsync are
    %% each independently running (not a scheduled job, unlike agent's
    %% backup tasks above; the two supervised processes have no ordering or
    %% coordination between them)
    bwfsAgent -.->|supervises: start/crash-restart/stop| bwfs
    bwfsAgent -.->|supervises: start/crash-restart/stop| catalogsync

    %% Restore Flow (list/verify/restore implemented)
    bwfs -->|list/restore protocol<br/>network/unix socket, mTLS| rwfs
    rwfs -->|writes files| DstFS

    %% Catalog Replication Flow (bwfs's own operation is unaffected either way)
    DB -->|reads file_versions,<br/>read-only| catalogsync
    catalogsync -->|SyncFileVersions<br/>gRPC, mTLS| Catalog

    classDef filesystem fill:#e1f5fe
    classDef component fill:#f3e5f5
    classDef planned fill:#f5f5f5,stroke-dasharray:5
    classDef database fill:#fff3e0

    class SrcFS,BackupFS,DstFS filesystem
    class brfs,bwfs,catalogsync,Catalog,bwfsAgent component
    class rwfs component
    class DB database
```
