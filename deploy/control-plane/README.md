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
flow any other node uses. Mint a token for it once `step-ca` is up:

```bash
certrequest catalog --ca-url https://localhost:9000
```

(No `--san` needed here: this quickstart runs `catalog` on the same host, so `catalog_host` will
be `localhost`, and hostname/SAN verification is skipped for loopback connections — see
"Enrolling and connecting an agent" below for the non-`localhost` case.)

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
certrequest node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
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

**Important:** unlike the `localhost` quickstart above, a non-`localhost` `catalog_host` is
subject to standard TLS hostname verification — the SAN minted for `catalog`'s own token must
**exactly match** the `catalog_host` string every connecting node uses. For a real (non-local)
deployment, mint catalog's token with that hostname instead of `localhost`:

```bash
certrequest catalog-01 --san catalog.backup.internal --ca-url https://localhost:9000
```

and use `catalog_host=catalog.backup.internal` (matching the `--san` value exactly) in every
`bwfs` node's `local.conf`.

## Viewing an issued certificate

```bash
openssl x509 -in <certs-dir>/client.crt -text -noout
```

## See Also

- [certrequest](../../docs/components/certrequest.md)
- [certclient](../../docs/components/certclient.md)
- [catalog component](../../docs/components/catalog.md)
- [catalogsync component](../../docs/components/catalogsync.md)
- [Architecture](../../docs/ARCHITECTURE.md)
