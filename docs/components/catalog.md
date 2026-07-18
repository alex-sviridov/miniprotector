# catalog

Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Also serves `ListEntries`, a read-only query RPC (filter by
source host and a substring match against the underlying object ID, keyset-paginated) — see
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
persisted keyed by `(source_node, job_id, object_id)`:

- `source_node` is the CA-verified hostname from the caller's mTLS client certificate
  (`mtls.PeerHostname`), never taken from the RPC payload. `job_id`/`object_id` alone are only
  unique within a single `bwfs` node; `source_node` disambiguates across a fleet of nodes
  replicating to the same catalog.
- A batch containing an entry already stored for its `(source_node, job_id, object_id)` is a
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
