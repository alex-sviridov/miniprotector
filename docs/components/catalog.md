# catalog

Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Also serves four read-only query RPCs: `ListEntries`
(filter by store host, real source host, a date range, an exact parent directory, and a substring
match against the underlying object ID, keyset-paginated) and the aggregate
`ListClientFacets`/`ListJobFacets`/`ListDirectoryFacets` (grouped counts by client host, policy
name, or parent directory, backing the web catalog view's filter panels) — see
[api-server](./api-server.md), the only intended caller today.

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
