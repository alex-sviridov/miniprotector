# client-manager

Owns the persistent list of enrolled clients: when they were added, free-form annotations
(`description`), attributes intended for future baking into a client's certificate (`attribute`,
see [Design: Client Manager](../superpowers/specs/2026-07-04-client-manager-design.md)), and a
revoked flag. **Control-plane tool** — runs on its own host, as an ordinarily-enrolled node (its
own mTLS identity via `certclient`), separate from the CA.

## Usage

```
client-manager add <hostname> [--san alias]...
client-manager re-enroll <hostname>
client-manager list
client-manager show <hostname>
client-manager revoke <hostname>
client-manager unrevoke <hostname>

client-manager description set <hostname> k=v [k=v...]
client-manager description unset <hostname> k
client-manager attribute set <hostname> k=v [k=v...]
client-manager attribute unset <hostname> k
```

`add`/`re-enroll` are the only two commands that touch the network: they call
[`certrequest serve`](./certrequest.md)'s `MintEnrollmentToken` RPC over mTLS, using
`client-manager`'s own bootstrapped identity — `client-manager` never holds the CA's provisioner
password. The returned token is printed to stdout for the operator to relay out-of-band, same as
`certrequest`'s one-shot CLI today. Everything else is local SQLite CRUD.

## Behavior

- `add` errors if `hostname` is already tracked (use `re-enroll` or `description|attribute set`
  instead) and records nothing locally unless minting actually succeeded.
- `revoke`/`unrevoke` only set a flag in `client-manager`'s own database in this phase — nothing
  yet blocks a renewal or invalidates a live certificate. See the design spec's "Non-Goals" for
  why, and its "Relationship to Phase 2" for what closes that gap.
- `attribute` values are stored only; baking them into an issued certificate requires the phase-2
  CA-side webhook responder, not yet built.
- `list`'s `LAST_SEEN` column always reads `unknown` in this phase — `client-manager` has no
  visibility into renewals, which happen directly between `certclient` and the CA.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `certrequest_host` | | Host where `certrequest serve` runs (typically the CA host) |
| `certrequest_port` | 9100 | Port `certrequest serve` listens on |
| `var_path` | binary's own directory | Where `clientmanager.sqlite` lives |

## Building

```bash
make clientmanager
```

## See Also

- [certrequest](./certrequest.md) — `serve` mode is the only thing `client-manager` calls over the network
- [Enrollment Broker Protocol](../protocols/enrollment-broker.md)
- [Design: Client Manager](../superpowers/specs/2026-07-04-client-manager-design.md)
- [Architecture](../ARCHITECTURE.md)
