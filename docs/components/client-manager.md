# client-manager

Owns the persistent list of enrolled clients: when they were added, free-form annotations
(`description`), attributes baked into a client's certificate as a real X.509 extension
(`attribute`), SAN aliases (`san`), and a revoked flag. Holds the CA's provisioner password directly and mints
enrollment tokens in-process — see
[Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
for why this is safe: `client-manager` runs directly on the CA host, as a single-operator CLI tool
with no network interface of its own, rather than a separate, less-trusted host (phase 1's
original placement — see [Design: Client Manager](../superpowers/specs/2026-07-04-client-manager-design.md)
for that earlier reasoning and why it changed).

## Usage

```
client-manager add <hostname> [--san alias]... [--ca-url url] [--defaults-file path] [--root path] [--provisioner name] [--password-file path]
client-manager re-enroll <hostname> [--san alias]... [--ca-url url] [--defaults-file path] [--root path] [--provisioner name] [--password-file path]
client-manager list
client-manager show <hostname>
client-manager revoke <hostname>
client-manager unrevoke <hostname>

client-manager description set <hostname> k=v [k=v...]
client-manager description unset <hostname> k
client-manager attribute set <hostname> k=v [k=v...]
client-manager attribute unset <hostname> k
client-manager san add <hostname> <alias>
client-manager san remove <hostname> <alias>
```

`add`/`re-enroll` mint a one-time enrollment token directly (the same mechanism `certrequest` used
to provide, now built in) and print it to stdout for the operator to relay out-of-band to the
target node, same as before. Everything else is local SQLite CRUD — `client-manager` has no
network interface at all.

| Flag | Default | Description |
|------|---------|-------------|
| `--san` | | Additional SAN alias for the token (repeatable) |
| `--ca-url` | read from `--defaults-file` | CA URL, e.g. `https://localhost:9000` |
| `--defaults-file` | `deploy/control-plane/ca/data/config/defaults.json` | Path to step-ca's `defaults.json`, used to default `--ca-url` when it isn't given explicitly |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |

## Behavior

- `add` errors if `hostname` is already tracked (use `re-enroll` or `description|attribute|san`
  instead) and records nothing locally unless minting actually succeeded.
- `revoke`/`unrevoke` set a flag in `client-manager`'s own database — `client-manager` itself has
  no network interface, so it never enforces this directly. Enforcement is
  [`issuer`](./issuer.md)'s job: sharing this binary's database, `issuer` refuses to issue a fresh
  operating certificate to a revoked hostname on that hostname's next `RequestOperatingCert` call.
  `attribute`/`san` changes are likewise read by `issuer` on the client's next operating-certificate
  request, not applied retroactively to a certificate already issued.
- On an already-enrolled, not-revoked node, `agent`'s `operating-refresh` policy execs `certclient
  operating-refresh` on a schedule (`OperatingCertFetchIntervalSec`, `local.conf`), so `revoke`,
  `attribute`, and `san` changes made here typically reach the node within that interval, without
  an operator needing to run `re-enroll`. `re-enroll` remains the only way to rotate the node's
  long-lived bootstrap credential itself (a new enrollment token) rather than just its short-lived
  operating certificate. See [agent](./agent.md) and [certclient](./certclient.md).
- `list`'s `LAST_SEEN` column now reflects real data once `issuer` has served at least one request
  for that hostname; `never` until then.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `var_path` | binary's own directory | Where `clientmanager.sqlite` lives |

## Building

```bash
make clientmanager
```

## See Also

- [issuer](./issuer.md) — enforces revoke/attribute, shares this binary's database
- [certclient](./certclient.md) — `operating-refresh` is how a node picks up revoke/attribute/san changes
- [clientmanager-api](./clientmanager-api.md) — a separate daemon sharing this component's database
  for read-only access, the same way `issuer` already does; `client-manager` itself is unaffected
- [agent](./agent.md) — schedules that refresh via its `operating-refresh` policy
- [Security Model](../SECURITY.md)
- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
- [Design: Credential Tier Enforcement](../superpowers/specs/2026-07-05-credential-tier-enforcement-design.md)
- [Design: Client Manager (phase 1)](../superpowers/specs/2026-07-04-client-manager-design.md)
- [Architecture](../ARCHITECTURE.md)
