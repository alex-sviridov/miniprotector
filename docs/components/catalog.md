# catalog

Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Also serves five read-only query RPCs: `ListEntries`
(filter by store host, real source host, a date range, an exact parent directory, and a substring
match against the underlying object ID, keyset-paginated), the aggregate
`ListClientFacets`/`ListJobFacets`/`ListDirectoryFacets` (grouped counts by client host, policy
name, or parent directory, backing the web catalog view's filter panels), and
`ListDirectoryChildren` (the web catalog view's directory browsing: what's directly under a given
path) — see [api-server](./api-server.md), the only intended caller today.

## Authorization

`SyncFileVersions` requires the `store` role; the six read-only query RPCs require
`control-plane`. See [Security Model](../SECURITY.md#role-based-rpc-authorization).

## Usage

```
catalog <storage_path> [--port N] [--debug]
```

`storage_path` is where `catalog.db` lives. `--port` defaults to `catalog_port` from
`local.conf` (15723 if unset).

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `catalog_port` config value | Port to listen on |
| `--debug` | false | Enable debug logging |

## How It Works

`SyncFileVersions` is the write path: one call per batch `catalogsync` sends. Each entry is
persisted keyed by `(store_node, job_id, object_id)`:

- `store_node` is the CA-verified hostname from the caller's mTLS client certificate
  (`mtls.PeerHostname`), never taken from the RPC payload. `job_id`/`object_id` alone are only
  unique within a single `bwfs` node; `store_node` disambiguates across a fleet of nodes
  replicating to the same catalog.
- `source_host` — the real originating (backed-up) host — is derived at the same time, by decoding
  each entry's `metadata` blob and reading its embedded host. It's distinct from `store_node`: a
  `bwfs` node forwards entries for whatever host was actually backed up, which is not necessarily
  itself.
- `parent_directory`'s full ancestor chain — every directory between it and its root — is also
  recorded, in a second table (`catalog_directories`, one row per distinct directory ever seen)
  populated at the same sync time via `decodeDirectoryAncestors`. This is what backs
  `ListDirectoryChildren` (see [Catalog Sync Protocol](../protocols/catalog-sync.md)): answering
  "what's directly under this path" from `EntryRecord`'s `parent_directory` column alone isn't
  possible, since it only names a file's *immediate* directory, not every ancestor of it.
- `expire_at` — the per-file retention expiry `brfs` stamped at backup time — is stored as received
  (`0` on the wire becomes NULL). The catalog never expires anything on its own; entries leave it
  only through `DeleteFileVersions` below.
- `DeleteFileVersions` is the delete path: `catalogsync` forwards each version `bwfs` deleted
  (retention cleanup, or a failed job's purge), and `catalog` removes the matching entries under
  the caller's own verified `store_node` — idempotent, an unknown entry is a no-op. The
  `catalog_directories` rows are left alone, so a directory that lost all its files can still appear
  in the directory list (a known limitation); entry listings never show a deleted version.
- A batch containing an entry already stored for its `(store_node, job_id, object_id)` is a
  no-op for that entry (`ON CONFLICT DO NOTHING`) — safe for `catalogsync` to resend a batch it
  isn't sure was received.

## Configuration Keys

- `catalog_port` — port `catalog` listens on *(default: 15723)*

## Certificates

Same mTLS pattern as `bwfs`/`brfs`/`rwfs`: identity bootstrapped/renewed via the **`certclient`**
binary against `MP_CONFIG_PATH/certs`. `catalog` itself never talks to the CA directly. A
certificate renewed on disk while `catalog` is running is picked up automatically on the next new
incoming connection — no restart required.

## Deployment

Ships as part of the combined control-plane `docker compose` stack — see
[`deploy/control-plane/README.md`](../../deploy/control-plane/README.md).

## Building

```bash
make catalog
```

## See Also

- [catalogsync](./catalogsync.md) — the component that sends batches here
- [api-server](./api-server.md) — exposes `ListEntries` over REST
- [Catalog Sync Protocol](../protocols/catalog-sync.md)
- [certclient](./certclient.md)
- [Architecture](../ARCHITECTURE.md)
