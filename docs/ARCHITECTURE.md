# System Architecture
A backup system with intelligent deduplication and integrity verification.

## Components

| Component | Full name | Status |
|-----------|-----------|--------|
| brfs | Backup Reader for File System — reads files from source, sends via gRPC | Implemented |
| bwfs | Backup Writer for File System — receives via gRPC, stores chunks + metadata | Implemented |
| rwfs | Restore Writer for File System — queries bwfs (list, verify; restore TBD) | list + verify implemented; full restore not yet implemented |
| catalogsync | Replicates a bwfs node's file_versions to a backup catalog | Implemented |
| catalog | Backup Catalog — receives catalogsync's replicated file_versions over gRPC | Implemented |
| agent | Node Agent — reconciles local state against embedded policies | Implemented (two policies: bootstrap credential renewal and operating-certificate refresh via `issuer`) |
| client-manager | Owns the enrolled-client list: descriptions, RBAC-bound attributes, SAN aliases, revoked status; mints enrollment tokens directly | Implemented (enforcement lives in `issuer`, which agent now drives — see below) |
| issuer | Mints short-lived operating certificates, enforcing revoke and embedding current attributes; shares client-manager's database | Implemented (agent integration done; a CA-side custom template for attribute embedding remains separate, later work) |

## Control Plane vs. Agents

|  | Control plane | Agents |
|---|---|---|
| Components | `deploy/control-plane/ca/` (step-ca container), `catalog`, `client-manager`, `issuer` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
| Runs where | On the CA host (`client-manager`, `issuer`); `catalog` runs centrally, wherever the catalog deployment lives — see below | Dial `ca_host:9000` outbound for enrollment/renewal and `issuer_host:9200` outbound for operating-certificate refresh; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Network role | Serves enrollment/renewal/admin (`/sign`, `/renew`, `/roots`, `/provisioners`) on `:9000`; `issuer` serves `RequestOperatingCert`/`DescribeSANs` on `:9200` (mTLS); neither has a role in backup traffic | Dial `ca_host:9000` (bootstrap/renew) and `issuer_host:9200` (operating-refresh) outbound only; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Docker/e2e images | Control-plane-only binaries (`client-manager`, `issuer`) never ship onto an agent host or into an agent image | Agent images bundle `certclient` only |

`catalog` is control plane by role (a fleet-wide central service, not colocated with any single
backup node) but bootstraps its own mTLS identity the same way agents do, via `certclient` — it
doesn't fit either row cleanly. It listens on its own port (`catalog_port`, default 15723) for
`catalogsync` connections from every `bwfs` node's agent host.

A node's mTLS identity is obtained in two tiers, both via `certclient`: `bootstrap` redeems a
one-time token minted by `client-manager` for `ca.crt` plus a long-lived `bootstrap.crt`/
`bootstrap.key` pair; `operating-refresh` then uses that bootstrap credential to authenticate to
`issuer` and obtain the short-lived `client.crt`/`client.key` that `common/mtls` actually reads for
every other component's transport. See [client-manager](components/client-manager.md),
[issuer](components/issuer.md), [certclient](components/certclient.md), and, for the full
rationale behind this split and its trust-model trade-offs, [Security Model](SECURITY.md).

`agent` is a node-level process that wraps `certclient` — intended to replace the bare cron entries
that would otherwise invoke `certclient` directly: `agent serve` runs a reconcile loop with two
config-driven policies, `bootstrap-refresh` (`certclient renew`, daily) and `operating-refresh`
(`certclient operating-refresh`, every 15 minutes by default), tracking each policy's outcome in a
local cache (`agent list-policies` inspects it). It has no network role of its own; all network
behavior is `certclient`'s, unchanged. See [agent](components/agent.md).

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
- **rwfs** (future restore) reconstructs files on the destination filesystem

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

    %% Restore Flow (list/verify implemented)
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
    class brfs,bwfs,catalogsync,Catalog component
    class rwfs component
    class DB database
```
