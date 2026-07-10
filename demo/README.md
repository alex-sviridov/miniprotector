# Demo Lab Environment

A self-contained `docker compose` stack — CA, `issuer`, `catalog`, and two backup-capable nodes
(`client`, `store`) — mutually enrolled via mTLS, brought up with one script. Unlike
[`deploy/control-plane`](../deploy/control-plane/README.md), this never touches your host
filesystem beyond this directory: every secret and every byte of state lives in Docker-managed
named volumes, and no port is published to the host. Everything is reached via `docker compose
exec`.

## Bring it up

```bash
make demo-up
```

Equivalent to `./demo/up.sh` directly. Builds all six images, brings up `ca` and `issuer` first,
then mints and redeems an enrollment token for `catalog`, `policy-server`, `client`, and `store` in
turn (skipping re-minting on a re-run against an already-enrolled node).

## Try it

```bash
docker compose -f demo/docker-compose.yml exec client ./brfs /data/sample-data --destination store:8080
docker compose -f demo/docker-compose.yml exec client ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec client ./rwfs verify store:8080
docker compose -f demo/docker-compose.yml logs -f store          # watch bwfs receive + catalogsync replicate
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
docker compose -f demo/docker-compose.yml exec catalog ./agent list-policies
docker compose -f demo/docker-compose.yml exec policy-server ./agent list-policies
docker compose -f demo/docker-compose.yml exec client ./agent list-policies
docker compose -f demo/docker-compose.yml exec store ./agent list-policies
```

## Revoke, and watch mesh access lapse without losing identity

```bash
docker compose -f demo/docker-compose.yml exec ca clientmanager revoke store
docker compose -f demo/docker-compose.yml exec store ./certclient operating-refresh   # fails
docker compose -f demo/docker-compose.yml exec store ./certclient renew               # still succeeds
docker compose -f demo/docker-compose.yml exec ca clientmanager unrevoke store
```

## Reset

```bash
make demo-down
```

Removes every container and volume — the next `make demo-up` starts from a byte-for-byte clean
slate, including a freshly generated CA and provisioner password.

## See Also

- [Design: Demo Lab Environment v2](../docs/superpowers/specs/2026-07-06-demo-lab-environment-v2-design.md)
- [Control Plane](../deploy/control-plane/README.md) — the production-shaped deployment reference this demo deliberately never reuses (separate compose file, volumes, and ports)
- [Architecture](../docs/ARCHITECTURE.md)
- [Security Model](../docs/SECURITY.md)
