# Control Plane (ca + catalog)

Combined `docker compose` stack for miniprotector's control-plane components: the `step-ca`
certificate authority and the backup `catalog`. See [Architecture](../../docs/ARCHITECTURE.md)
for how these fit into the rest of the system.

One `docker-compose.yml` runs both as separate containers. Each service can also be started
alone (`docker compose up -d step-ca` or `up -d catalog`) — useful once a deployment splits them
across two hosts; just point `catalog/local.conf`'s `ca_host` at wherever `step-ca` ends up.

## First-time setup

The quickest path is from the repo root:

```bash
make control-plane-up
```

This generates the CA's provisioner password (`ca/data/secrets/password`) if it doesn't already
exist, then runs `docker compose up -d` for both services.

`catalog` itself needs an mTLS identity before it can start successfully — the same enrollment
flow any other node uses, with one twist: unlike a bare-metal agent node, `catalog` redeems its
token from *inside its own container*, so the token must be minted with `--ca-url` set to an
address reachable from there — the Compose service name `step-ca`, not `localhost` (which inside
`catalog`'s container means its own loopback, not the CA). Mint it from a throwaway container on
the same Compose network instead of the host-installed `client-manager` binary:

```bash
cd deploy/control-plane
docker run --rm --network control-plane_default \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  golang:1.26 \
  go run ./cmd/clientmanager add catalog --ca-url https://step-ca:9000 \
    --defaults-file /repo/deploy/control-plane/ca/data/config/defaults.json \
    --root /repo/deploy/control-plane/ca/data/certs/root_ca.crt \
    --password-file /repo/deploy/control-plane/ca/data/secrets/password
```

(No `--san` needed here: this token's SAN list only needs to satisfy `catalog`'s *own* enrollment
against the CA. The SAN that matters for other nodes verifying `catalog`'s identity later is a
separate concern — see "Enrolling and connecting an agent" below.)

Then bring `catalog` up with the token:

```bash
MP_CERT_TOKEN=<token> make control-plane-up
```

Until a token is supplied, `catalog` will exit and restart repeatedly (`restart: unless-stopped`)
— that's expected, not a bug; `step-ca` is unaffected and keeps running.

## Running

```bash
make control-plane-up
```

is the primary entry point (idempotent — safe to re-run). Underneath, it's just:

```bash
cd deploy/control-plane
docker compose up -d          # both services
docker compose up -d step-ca  # just the CA
docker compose up -d catalog  # just the catalog
```

Restarting `catalog` after the first successful run renews its certificate automatically
(`certclient` always renews when an identity already exists — no token needed). This doesn't by
itself keep a long-running container's certificate fresh on its own schedule; re-run `certclient`
inside the container (`docker compose exec catalog ./certclient`) or restart it periodically to
trigger a renewal. `catalog` picks up a renewed certificate on its next new incoming connection
without needing a restart.

## Enrolling and connecting an agent (bwfs/brfs) node

On (or near) this control-plane host, mint a token for the agent's real hostname:

```bash
client-manager add node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
```

Relay the printed token to the target node out-of-band (SSH, etc.), then on that node:

```bash
MP_CERT_TOKEN=<token> certclient
```

Re-running `certclient` on a node that already has an identity renews it instead (no token
needed — renewal is authenticated with the existing certificate).

Then, on the agent node, set in `local.conf`:

```
ca_host=<this-host>:9000
catalog_host=<this-host>:15723
```

(`catalog_host` only matters for nodes running `catalogsync`.)

**Important:** a `catalogsync` process on this same host can simply use `catalog_host=localhost`
— hostname/SAN verification is skipped for loopback connections regardless of what SAN `catalog`
enrolled with, which is why the enrollment step above didn't need a `--san`. Any *other* value of
`catalog_host` is subject to standard TLS hostname verification, though — the SAN minted for
`catalog`'s own token must **exactly match** the `catalog_host` string every such connecting node
uses. For a real (non-local) deployment, mint catalog's token with that hostname instead:

```bash
client-manager add catalog-01 --san catalog.backup.internal --ca-url https://localhost:9000
```

and use `catalog_host=catalog.backup.internal` (matching the `--san` value exactly) in every
`bwfs` node's `local.conf`.

## Viewing an issued certificate

```bash
openssl x509 -in <certs-dir>/client.crt -text -noout
```

## See Also

- [client-manager](../../docs/components/client-manager.md)
- [certclient](../../docs/components/certclient.md)
- [catalog component](../../docs/components/catalog.md)
- [catalogsync component](../../docs/components/catalogsync.md)
- [Architecture](../../docs/ARCHITECTURE.md)
