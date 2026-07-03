# Backup Catalog

Receives replicated `bwfs` file-version batches over mTLS gRPC and persists them to its own
SQLite database (`catalog.db`). Control-plane component — see
[Architecture](../docs/ARCHITECTURE.md).

## First-time setup

Enroll this node with the CA before the first `docker compose up` (same flow any other node
uses — see [`ca/README.md`](../ca/README.md#enrolling-a-node)):

```bash
certrequest catalog-01 --san catalog.backup.internal --ca-url https://<ca-host>:9000
```

Relay the printed token out-of-band, then set it for the first run:

```bash
MP_CERT_TOKEN=<token> docker compose up -d
```

Edit `catalog_port`/`ca_host` in `local.conf` first if the defaults don't match your deployment.

## Running

```bash
docker compose up -d
```

Restarting after the first run renews the node's certificate automatically (`certclient` always
renews when an identity already exists — no token needed). This does not itself keep a
long-running container's certificate fresh on its own schedule; re-run `certclient` inside the
container (`docker compose exec catalog ./certclient`) or restart it periodically to trigger a
renewal. `catalog` picks up a renewed certificate on its next new incoming connection without
needing a restart.

## Configuring catalogsync to send here

On each `bwfs` node running `catalogsync`, set in `local.conf`:

```
catalog_host=catalog.backup.internal
catalog_port=15723
```

## See Also

- [catalog component](../docs/components/catalog.md)
- [catalogsync component](../docs/components/catalogsync.md)
- [certclient](../docs/components/certclient.md)
- [Architecture](../docs/ARCHITECTURE.md)
