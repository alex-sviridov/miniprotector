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
| agent | Node Agent — reconciles local state against embedded policies | Implemented (v1: cert renewal only) |
| client-manager | Owns the enrolled-client list: descriptions, RBAC-bound attributes, revoked status | Implemented (phase 1: no enforcement yet) |

## Control Plane vs. Agents

|  | Control plane | Agents |
|---|---|---|
| Components | `deploy/control-plane/ca/` (step-ca container), `certrequest` (one-shot CLI and `serve` mode), `catalog`, `client-manager` | `bwfs`, `brfs`, `rwfs`, `certclient`, `agent` |
| Runs where | On/near the CA host (`certrequest`); `catalog` runs centrally, wherever the catalog deployment lives — see below | Dial `ca_host:9000` outbound only, for enrollment/renewal; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Network role | Serves enrollment/renewal/admin (`/sign`, `/renew`, `/roots`, `/provisioners`) on `:9000`; has no role in backup traffic | Dial `ca_host:9000` outbound only, for enrollment/renewal; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Docker/e2e images | `certrequest` never ships onto an agent host or into an agent image | Agent images bundle `certclient` only |

`catalog` is control plane by role (a fleet-wide central service, not colocated with any single
backup node) but bootstraps its own mTLS identity the same way agents do, via `certclient` — it
doesn't fit either row cleanly. It listens on its own port (`catalog_port`, default 15723) for
`catalogsync` connections from every `bwfs` node's agent host.

A node's mTLS identity (`ca.crt`, `client.crt`, `client.key`, consumed by `common/mtls`) is
obtained via `certclient`, using a token minted by `certrequest`. See
[certrequest](components/certrequest.md) and [certclient](components/certclient.md).

`agent` is a node-level process that wraps `certclient` — intended to replace the bare cron entry
that invokes `certclient` directly: `agent serve` runs a reconcile loop that periodically execs `certclient`
and tracks the outcome in a local cache (`agent list-policies` inspects it). It has no network
role of its own in v1; all network behavior is `certclient`'s, unchanged.

`client-manager` is control plane by role (an admin-facing service tracking the enrolled-client
fleet) but, like `catalog`, bootstraps its own mTLS identity the same way agents do, via
`certclient`. Its only network role is calling `certrequest serve`'s `MintEnrollmentToken` RPC —
see [Design: Client Manager](superpowers/specs/2026-07-04-client-manager-design.md) for why
token-minting is routed through a narrow broker rather than giving `client-manager` the CA's
provisioner password directly.

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
