# Control Plane (ca + issuer + catalog + policy-server)

Combined `docker compose` stack for miniprotector's control-plane components: the `step-ca`
certificate authority, the `issuer` operating-certificate service, the backup `catalog`, and
`policy-server`. See [Architecture](../../docs/ARCHITECTURE.md) for how these fit into the rest of
the system.

One `docker-compose.yml` runs all four as separate containers. Each service can also be started
alone (`docker compose up -d step-ca`, `up -d issuer`, `up -d catalog`, or `up -d policy-server`)
— useful once a deployment splits them across hosts; just point each service's own `local.conf`'s
`ca_host`/`issuer_host` at wherever `step-ca`/`issuer` end up.

## First-time setup

The quickest path is from the repo root:

```bash
make control-plane-up
```

This generates the CA's provisioner password (`ca/data/secrets/password`) if it doesn't already
exist, then runs `docker compose up -d` for all three services.

`catalog` itself needs an mTLS identity before it can start successfully — the same enrollment
flow any other node uses, with one twist: unlike a bare-metal agent node, `catalog` redeems its
token from *inside its own container*, so the token must be minted with `--ca-url` set to an
address reachable from there — the Compose service name `step-ca`, not `localhost` (which inside
`catalog`'s container means its own loopback, not the CA).

`issuer` must be up first. Minting the token itself only talks to `step-ca`, but `issuer` and
`client-manager` share the same on-disk database (the `client-manager/data` volume), and
`catalog`'s bundled `agent` starts pulling its operating certificate from `issuer` as soon as
`catalog` boots — so bring `issuer` up before enrolling:

```bash
cd deploy/control-plane
docker compose up -d issuer
```

Mint the token from a throwaway container on the same Compose network instead of the
host-installed `client-manager` binary, mounting the same persistent, config-pointed volume the
`client-manager` service itself uses (see Task 5's `client-manager/local.conf`, which sets
`var_path=/data` — the same directory `issuer` reads):

```bash
docker run --rm --network control-plane_default \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  -v "$(pwd)/client-manager/data:/data" \
  -v "$(pwd)/client-manager/local.conf:/data/local.conf:ro" \
  -e MP_CONFIG_PATH=/data \
  golang:1.26 \
  go run ./cmd/clientmanager add catalog --ca-url https://step-ca:9000 \
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
— that's expected, not a bug; `step-ca` and `issuer` are unaffected and keep running.

`catalog`'s identity is a two-tier pair, not a single certificate that's renewed on restart: the
token above bootstraps the long-lived bootstrap credential once, and from then on `catalog`'s
bundled `agent` keeps *both* tiers fresh continuously in the background for as long as the
container runs — the bootstrap credential daily (`BootstrapCertRefreshIntervalSec`) and the
short-lived operating certificate every `OperatingCertFetchIntervalSec` (15 minutes by default,
fetched from `issuer`) — with no container restart needed to pick up either refresh.

### Enrolling policy-server

The same enrollment flow, condensed (see the `catalog` walkthrough above for the fully-explained
version — the mechanics are identical, just a different service name and no `--san` consideration
since nothing yet resolves `policy-server` by a SAN-verified hostname):

```bash
cd deploy/control-plane
docker compose up -d issuer   # if not already up

docker run --rm --network control-plane_default \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  -v "$(pwd)/client-manager/data:/data" \
  -v "$(pwd)/client-manager/local.conf:/data/local.conf:ro" \
  -e MP_CONFIG_PATH=/data \
  golang:1.26 \
  go run ./cmd/clientmanager add policy-server --ca-url https://step-ca:9000 \
    --root /repo/deploy/control-plane/ca/data/certs/root_ca.crt \
    --password-file /repo/deploy/control-plane/ca/data/secrets/password

MP_CERT_TOKEN=<token> docker compose up -d policy-server
```

Until a token is supplied, `policy-server` will exit and restart repeatedly
(`restart: unless-stopped`) — expected, not a bug. Once enrolled, its bundled `agent` keeps both
credential tiers fresh continuously, the same as `catalog`'s.

## Running

```bash
make control-plane-up
```

is the primary entry point (idempotent — safe to re-run). Underneath, it's just:

```bash
cd deploy/control-plane
docker compose up -d                 # all four services
docker compose up -d step-ca         # just the CA
docker compose up -d issuer          # just the operating-cert issuer
docker compose up -d catalog         # just the catalog
docker compose up -d policy-server   # just policy-server
```

Once enrolled, `catalog` no longer depends on being restarted to keep its certificates fresh:
its bundled `agent` (see [agent](../../docs/components/agent.md)) reconciles both credential tiers
on its own schedule for as long as the container runs. If you want to check on that without
waiting for the next scheduled refresh, `docker compose exec catalog ./agent list-policies` prints
each policy's last success/failure and next run time without forcing anything early.

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

## Smoke test: enroll, connect, revoke

A full walkthrough proving the two-tier credential model actually works end-to-end in this
deployment — this is the exact sequence that was used to discover that the old, single-shot
`certclient` invocation this README used to document was broken, written up here so it stays
repeatable and doesn't bit-rot silently again.

```bash
cd deploy/control-plane

# Brings up step-ca, issuer, and catalog together. catalog isn't enrolled
# yet, so it crash-loops (restart: unless-stopped) until the next step --
# expected, not a bug; step-ca and issuer are unaffected.
make control-plane-up

# Enroll: mint a token for catalog-01, writing to the shared, persistent
# client-manager volume issuer also reads.
docker run --rm --network control-plane_default \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  -v "$(pwd)/client-manager/data:/data" \
  -v "$(pwd)/client-manager/local.conf:/data/local.conf:ro" \
  -e MP_CONFIG_PATH=/data \
  golang:1.26 \
  go run ./cmd/clientmanager add catalog-01 --ca-url https://step-ca:9000 \
    --root /repo/deploy/control-plane/ca/data/certs/root_ca.crt \
    --password-file /repo/deploy/control-plane/ca/data/secrets/password

# Connect: hand the printed token to catalog and confirm it comes up clean.
MP_CERT_TOKEN=<printed-token> docker compose up -d catalog
docker compose logs -f catalog
# Confirm: catalog stays up (no more crash-loop, and no "agent did not
# produce an operating certificate within 60s" error), then Ctrl-C.

# Revoke: mark catalog-01 revoked. This only needs the shared store, not
# the CA, so no --ca-url/--root/--password-file (revoke doesn't accept them).
docker run --rm \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  -v "$(pwd)/client-manager/data:/data" \
  -v "$(pwd)/client-manager/local.conf:/data/local.conf:ro" \
  -e MP_CONFIG_PATH=/data \
  golang:1.26 \
  go run ./cmd/clientmanager revoke catalog-01

# Confirm the refusal: catalog's already-issued operating certificate keeps
# working until it expires (OperatingCertTTLSec, 1 hour by default) --
# revoking doesn't kill an existing connection -- but its bundled agent's
# next scheduled operating-refresh attempt (OperatingCertFetchIntervalSec,
# 15 minutes by default -- see catalog/local.conf) will fail against issuer.
# Watch for that failure in catalog's logs, or check sooner without waiting:
docker compose exec catalog ./agent list-policies
docker compose logs catalog | grep -i operating-refresh
```

Teardown: `docker compose down` (stop/remove containers; named data volumes persist) or
`docker compose down --volumes` (full wipe, including `issuer`'s and `client-manager`'s own data)
— both already work generically, no new tooling needed.

## See Also

- [client-manager](../../docs/components/client-manager.md)
- [issuer](../../docs/components/issuer.md)
- [certclient](../../docs/components/certclient.md)
- [agent](../../docs/components/agent.md)
- [catalog component](../../docs/components/catalog.md)
- [catalogsync component](../../docs/components/catalogsync.md)
- [policy-server component](../../docs/components/policy-server.md)
- [Architecture](../../docs/ARCHITECTURE.md)
