# client-manager

Owns the persistent list of enrolled clients: when they were added, free-form annotations
(`description`), attributes intended for baking into a client's certificate (`attribute`), SAN
aliases (`san`), and a revoked flag. Holds the CA's provisioner password directly and mints
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
- `revoke`/`unrevoke` only set a flag in `client-manager`'s own database in this plan — nothing yet
  blocks a renewal. See the phase-2 design's architecture for the listening service that will
  enforce this, not yet built.
- `attribute`/`san` values are stored only; a client's next `re-enroll` is currently the only way
  to mint a token reflecting a client's current attributes/SANs. Automatic refresh on an ordinary
  credential renewal is what the phase-2 listening service (not yet built) provides.
- `list`'s `LAST_SEEN` column always reads `unknown` — `client-manager` has no visibility into
  renewals, which happen directly between `certclient` and the CA.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `var_path` | binary's own directory | Where `clientmanager.sqlite` lives |

## Building

```bash
make clientmanager
```

## See Also

- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
- [Design: Client Manager (phase 1)](../superpowers/specs/2026-07-04-client-manager-design.md)
- [Architecture](../ARCHITECTURE.md)
