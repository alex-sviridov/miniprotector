# Client Manager (Phase 1: Core) — Design

> Phase 1 of 2. This spec covers `client-manager`'s own DB, CLI, and enrollment path. A follow-up
> spec (phase 2, once step-ca's provisioner webhook API is verified against the pinned version)
> covers the CA-host-local webhook responder that actually makes `revoke` block a renewal and
> `attribute` values land in an issued certificate. Nothing in this phase touches `common/mtls` or
> any peer's handshake path.

## Problem

Enrolling a node today is a manual, stateless, one-shot act: an operator runs `certrequest
<hostname>` near the CA, relays the printed token out-of-band, and nothing about that client is
recorded anywhere. There's no list of what's been enrolled, no place to annotate a node (owner,
location, purpose), and no way to mark a node as no longer trusted. There's also no way to attach
data to a node that should end up embedded in its certificate for future role-based access control
— `docs/superpowers/specs/2026-07-03-policy-reconciliation-design.md` already anticipated this
need ("likely via a verified role/tag claim embedded in the cert ... not a client-declared value")
without building it.

## Goals

- A persistent, queryable list of enrolled clients: hostname, when added, revoked status.
- Per-client **descriptions** — free-form key/value annotations, for humans only, never touch a
  certificate.
- Per-client **attributes** — key/value pairs intended to be baked into that client's certificate
  by a future CA-side mechanism (phase 2), for RBAC.
- Enrolling a new node and recording it happen as one operator action, not two disconnected steps.
- The CA's own enrollment/renewal path gains no new hard dependency and no new holder of
  CA-admin-equivalent privilege other than what already exists today.

## Non-Goals (deferred to phase 2)

- **No enforcement.** `revoke` only sets a flag in `client-manager`'s own DB in this phase. It does
  not block a renewal — nothing today checks revocation status anywhere in the mesh (confirmed:
  `src/common/mtls/mtls.go` only verifies the cert chain and expiry). Enforcing it requires a
  CA-host-local component consulted during renewal — phase 2.
- **No attribute baking.** `attribute` values are stored, not embedded in any issued certificate.
  That requires step-ca's provisioner webhook mechanism (`ENRICHING` kind), whose exact
  payload/config shape needs verification against this repo's pinned step-ca version — phase 2.
- **No real `last_seen`/`last_renewed` data.** Renewal happens directly between `certclient` and
  the CA; `client-manager` has no visibility into it until phase 2's webhook responder exists.
  `client-manager list` shows `added_at` only.
- **No hard revocation** (CRL/OCSP-style immediate invalidation of a live cert already in use by
  other peers) — a separate, larger piece of work, later than even phase 2.
- **No web UI.** CLI only.
- **No group/fleet-based selectors** — matches the existing non-goal in the policy-reconciliation
  design; out of scope here too.
- **No automation of the token-relay step** — `add`/`re-enroll` print a token to stdout; relaying
  it to the target node stays a manual, out-of-band step, same as `certrequest` today.

## Architecture

### Components

1. **`client-manager`** (new binary) — an ordinarily-enrolled control-plane node (its own mTLS
   identity via `certclient`, same as any other node), running on its own host, separate from the
   CA. Owns a local SQLite DB of clients/descriptions/attributes/revoked-flags. Its only network
   role is calling `certrequest serve` (below) to mint enrollment tokens.
2. **`certrequest serve`** (new mode on the existing `certrequest` binary) — mirrors the
   `agent serve` / `agent list-policies` split already in this codebase. `certrequest <hostname>
   ...` (today's one-shot CLI) is unchanged. `certrequest serve` is new: a persistent process,
   still run on/near the CA host, holding the provisioner password and exposing exactly one
   mTLS-authenticated RPC, `MintEnrollmentToken(hostname, sans)`. Nothing else — this is now the
   highest-value target in the system (network-reachable, CA-admin-equivalent privilege), so its
   surface stays deliberately minimal. The actual minting call
   (`ca.NewProvisioner`/`Provisioner.Token`, ~15 lines today inlined in `certrequest`'s `main.go`)
   is factored into a small shared internal package so both modes call identical code.

### Why this shape (privilege boundary)

An earlier draft of this design had `client-manager` call `ca.NewProvisioner` directly, the same
way `certrequest`'s CLI does — meaning `client-manager`'s host would need the CA's provisioner
password, i.e. full CA-admin-equivalent privilege, just to mint tokens on operators' behalf. That's
a real privilege leak: it turns a second, less-locked-down host into an equally attractive target
as the CA itself. Routing token-minting through `certrequest serve` fixes this — `client-manager`
only ever holds an ordinary node identity (the same kind `bwfs`/`catalog` already have), and the
provisioner password lives in exactly one place, unchanged from today.

### Trust config

New `local.conf` key, `client_manager_host`, following the existing `ca_host`/`catalog_host`
convention — set identically on every host:

- `certrequest serve` reads its own `client_manager_host` and trusts exactly one peer: the caller
  whose mTLS-verified hostname (`mtls.PeerHostname`, the same derivation `catalog` already uses
  for `source_node`) matches that string exactly. One comparison, no allowlist data structure, no
  new storage — deliberately as simple as the "super simple and secure" bar this component needs
  to clear.
- Every other node carries the same key, unused for now (same posture as `catalog_host` being
  present everywhere today even though only `catalogsync`-running nodes read it) — available once
  `client-manager` grows a query surface other nodes might call.

`client-manager` reaches `certrequest serve` at the CA's own host (`ca_host`, colocated per the
existing "control-plane tool, run on/near the CA host" convention) on a new dedicated port,
`certrequest_port` (default `9100`) — a new gRPC service distinct from step-ca's own `:9000`.

### Cluster bootstrap (operational sequence)

1. Start `ca` (`step-ca` + `certrequest serve` alongside it).
2. Operator manually mints and relays **one** token, for `client-manager`'s own hostname, using
   today's one-shot `certrequest <hostname>` CLI directly on/near the CA host — the only manual
   step in the cluster's life, same shape as bootstrapping `catalog` today.
3. `client-manager` redeems that token via `certclient`, getting an ordinary mTLS identity. No
   provisioner password ever touches its host.
4. From then on, every other node is enrolled via `client-manager add <hostname>`, which calls
   `certrequest serve`'s `MintEnrollmentToken` over mTLS. Raw `certrequest` CLI usage drops to zero
   after step 2, except scripted/ephemeral contexts (e.g. the demo lab) that may keep calling it
   directly.

## Data Model

SQLite, one file at `<var_dir>/clientmanager.sqlite`, where `<var_dir>` is `config.ResolveVarDir`
— the same `var_path`-or-binary-directory resolution `agent` already uses for its state file. No
positional DB-path argument.

```sql
CREATE TABLE clients (
    hostname   TEXT PRIMARY KEY,
    added_at   TIMESTAMP NOT NULL,
    revoked    BOOLEAN NOT NULL DEFAULT 0,
    revoked_at TIMESTAMP
);

CREATE TABLE client_kv (
    hostname TEXT NOT NULL REFERENCES clients(hostname),
    kind     TEXT NOT NULL CHECK (kind IN ('description', 'attribute')),
    key      TEXT NOT NULL,
    value    TEXT NOT NULL,
    PRIMARY KEY (hostname, kind, key)
);
```

One `kind`-tagged kv table, not two physically separate tables — the `CHECK` constraint is what
actually keeps a description from being read as an attribute; the two share identical CRUD
otherwise. If per-field access control (e.g. only some operators may edit `attribute`s) becomes a
real requirement later, splitting the table is a small, mechanical follow-up, not a redesign.

## CLI

`client-manager <subcommand> ...` — noun-then-verb for the two kv resource kinds; everything else
stays flat since "client" is the tool's implicit subject:

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

`add`/`re-enroll` are the only two that touch the network — dialing `certrequest serve` over mTLS
using `client-manager`'s own bootstrapped identity via the existing `common/mtls` client
credentials helper (no new TLS code). Everything else is local DB CRUD.

`client-manager list` output:

```
HOSTNAME       ADDED_AT              REVOKED  LAST_SEEN
node-east-01   2026-07-04 10:15:02   no       unknown
```

`LAST_SEEN` is always `unknown` in phase 1 — stated plainly rather than showing a misleading value.

## Data Flow: `add`

```
operator: client-manager add node-east-01 --san node-east-01.internal
  -> client-manager dials <ca_host>:<certrequest_port> over mTLS
  -> calls MintEnrollmentToken("node-east-01", ["node-east-01", "node-east-01.internal"])
  -> certrequest serve checks caller's verified mTLS hostname == its own client_manager_host
       mismatch -> reject (PermissionDenied), nothing minted
       match     -> mints token via the shared internal minting package (same call
                     certrequest's CLI mode uses)
  -> token returned over the mTLS response, never persisted by client-manager (it's one-time-use
     and jti-tracked by the CA already; there's no reason to store it)
  -> client-manager writes {hostname, added_at: now, revoked: false} (+ any --description/--attr
     given inline) to its local DB
  -> client-manager prints the token to stdout for the operator to relay out-of-band
```

`re-enroll` is the same flow minus the DB insert (the row must already exist) — for a node that
needs a fresh token (lost its identity, disk wiped) without losing its recorded descriptions,
attributes, or `added_at`.

## Error Handling

- **Broker unreachable** or **rejected caller** (`client_manager_host` mismatch): `add`/`re-enroll`
  fail outright with a message that distinguishes the two cases (availability problem vs.
  misconfiguration). Nothing is written to the DB on failure — a client only exists locally once a
  token was actually minted.
- **Duplicate `add`** on an existing hostname errors, pointing at `re-enroll` or
  `description|attribute set` instead — no silent re-minting on a repeated/fat-fingered call.
- **Any subcommand targeting an unknown hostname** errors.
- **`revoke`/`unrevoke`/`description|attribute set|unset` on a known hostname**: pure local DB
  writes; no network involved, no failure mode beyond ordinary DB I/O errors.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `client_manager_host` | | Hostname `certrequest serve` trusts as the sole caller of `MintEnrollmentToken`; same value every other host carries for future use |
| `certrequest_port` | 9100 | Port `certrequest serve` listens on |
| `var_path` | binary's own directory | Already exists (via `agent`) — reused for `clientmanager.sqlite`'s location |

## Testing

- Unit: CLI arg parsing (mirrors `certrequest`'s existing `arguments_test.go`).
- Unit: SQLite CRUD round-trip for `clients`/`client_kv` (add, list, show, revoke/unrevoke,
  description/attribute set/unset) against a temp DB file.
- Unit: `certrequest serve`'s hostname-match authorization check, mocked peer identity, both
  match and mismatch cases.
- Integration: a real ephemeral `step-ca` + `certrequest serve`, confirming `client-manager add`
  produces a token `certclient` can actually redeem end-to-end — mirrors `certrequest`'s own
  existing e2e test style.
- Integration: `certrequest serve` rejects a caller whose mTLS hostname doesn't match
  `client_manager_host`.

## Documentation Impact

Per `.claude/CLAUDE.md`:

- `docs/components/client-manager.md` (new).
- `docs/components/certrequest.md` — new section documenting `serve` mode, its RPC, and the
  privilege-boundary rationale.
- `docs/protocols/enrollment-broker.md` (new) — `MintEnrollmentToken` request/response shape.
- `docs/ARCHITECTURE.md` — control-plane table gains `client-manager` and `certrequest serve`.
- `README.md` — cross-link the new component doc.

## Files Changed

| Path | Change |
|------|--------|
| `src/cmd/certrequest/serve.go` (new) | `certrequest serve`: gRPC server exposing `MintEnrollmentToken`, mTLS-authenticated, single-hostname trust check against `client_manager_host` |
| `src/cmd/certrequest/main.go` | Dispatch between one-shot CLI (unchanged) and `serve` |
| `src/common/certmint/` (new internal package) | Factored-out `ca.NewProvisioner`/`Provisioner.Token` call, shared by both `certrequest` modes |
| `src/cmd/clientmanager/main.go`, `arguments.go` (new) | CLI entrypoint and subcommand parsing |
| `src/cmd/clientmanager/db.go` (new) | SQLite schema + CRUD |
| `src/cmd/clientmanager/broker_client.go` (new) | mTLS gRPC client calling `certrequest serve` |
| `src/proto/enrollment_broker.proto` (new) + generated | `MintEnrollmentToken` RPC schema |
| `src/common/config/config.go` | Add `ClientManagerHost`/`CertrequestPort` fields and key parsing |
| `Makefile` | Add `clientmanager` build target |
| `docs/components/client-manager.md` (new), `docs/components/certrequest.md`, `docs/protocols/enrollment-broker.md` (new), `docs/ARCHITECTURE.md`, `README.md` | Per Documentation Impact above |

## Relationship to Phase 2

Phase 2 (separate spec, once step-ca's provisioner webhook API is verified against the pinned
version) adds a CA-host-local webhook responder: a persistent cache asynchronously replicated
*down* from `client-manager`'s DB (attributes, revoked flags) and asynchronously flushed *up* with
best-effort `last_seen` data — never a live call from the CA into `client-manager` on the hot
path, so `client-manager`'s own availability never gates a renewal. step-ca's provisioner webhook
config (`ENRICHING` for attributes, `AUTHORIZING` for revocation) points at this local responder.
Nothing in this phase's schema or CLI needs to change to support that — `client_kv`'s `attribute`
rows and `clients.revoked` are already exactly the data phase 2 will read.
