# Design: clientmanager-admin-api — network-reachable client enrollment/revocation/metadata writes

**Date:** 2026-07-19
**Status:** Approved for planning

## Problem

`client-manager`'s CLI is the only thing that can enroll a new client (issue an enrollment token),
re-enroll one, revoke/unrevoke it, or edit its description/attribute/SAN metadata — and it's
deliberately a single-operator tool with no network interface, run directly on the CA host (see
[Design: Client Manager Phase 2](2026-07-04-client-manager-phase2-design.md)). `clientmanager-api`,
the gRPC daemon `api-server` already talks to, is explicitly read-only: `ListClients`/`GetClient`
only, "clientmanager-api never writes" per its own proto comment.

This adds those write operations to `api-server`'s REST surface, reachable from the browser/any REST
client the same way policy CRUD already is — enroll/re-enroll a client (mint a token), revoke/
unrevoke, edit description and attribute ("label") key/value pairs, manage SAN aliases.

## Approach

**The core tension:** minting an enrollment token requires the CA provisioner's password —
documented elsewhere as conferring "full token-minting authority for any hostname, equivalent to
CA-admin privilege." Today exactly two things hold that password: `client-manager` (CLI, no network
surface at all) and `issuer` (network-reachable, but its RPCs are narrowly self-service — a caller
can only ever act on its own verified mTLS peer identity, never an arbitrary hostname). Exposing
enrollment/revocation over REST necessarily creates a third network-reachable holder of that
password. The question this spec resolves is how to minimize what that costs.

Three shapes were considered:

- **Route through `issuer`.** Rejected: `issuer`'s RPCs never take a `hostname` request field — the
  target is always the caller's own peer identity, by design. Admin writes need the opposite
  (`clientmanager-api`/`api-server` acting on an arbitrary target hostname on an operator's behalf).
  Fitting that shape into `issuer` would mean either a second listener or relaxing
  `mtls.LoadIssuerServerCredentials`'s bootstrap/issuer-caller-only restriction — both are more
  invasive than the problem calls for, and both touch a component whose narrow, self-service scope
  is itself a deliberate security property.
- **Bolt write RPCs directly onto `clientmanager-api`.** Simplest, one process. Rejected as the
  primary design: `clientmanager-api` is a general-purpose, read/query-oriented service that will
  keep gaining unrelated features over time; giving that same process the provisioner password means
  every future bug in it — not just in the write path — carries CA-admin-equivalent blast radius.
- **(Chosen) A new, deliberately small sibling: `clientmanager-admin-api`.** A second binary holding
  the provisioner password directly, exposing only the fixed set of write RPCs below, sharing the
  same `clientmanager.sqlite` volume `clientmanager-api`/`client-manager`/`issuer` already share.
  `clientmanager-api` itself is completely untouched — still read-only, still password-free. This
  mirrors the same reasoning that already separates `issuer` from `client-manager`: keep anything
  holding the provisioner password narrow, fixed-surface, and auditable, rather than folded into a
  service that grows for unrelated reasons.

**Packaging:** `clientmanager-admin-api` ships in the *same container* as `clientmanager-api`
(same Dockerfile, same `entrypoint.sh`), sharing one `agent`-managed mesh identity/enrollment —
avoiding a second one-time enrollment token and a second `agent` process for what would otherwise be
a purely operational cost. The two remain separate OS processes/binaries, each `exec`'d
independently, so process-level isolation (separate memory space) is kept; only filesystem-level
isolation is given up, since both processes share the container's filesystem. See "Security
Evaluation" for what that trades away.

**Caller restriction:** every gRPC service in this mesh (`clientmanager-api`, `catalog`,
`policy-server`, ...) uses the default `mtls.LoadServerCredentials`, accepting any valid
operating-tier certificate from any enrolled node — the documented "any operating-tier cert may call
any RPC it can reach" convention. `clientmanager-admin-api` follows that same existing convention
rather than adding a peer-identity allowlist restricting it to `api-server` specifically. This is a
deliberate acceptance of the project's existing mesh-trust model, not an oversight — see "Security
Evaluation" for the trade-off this implies.

## Architecture

`clientmanager-admin-api` gains the CA provisioner credentials (`--ca-url`, `--root`,
`--provisioner`, `--password-file` — the same four flags `issuer`'s Dockerfile already hardcodes)
and a dependency on `common/certmint`. No new business logic is needed: every write operation
already exists as a function called today only by `client-manager`'s CLI, and
`clientmanager-admin-api`'s RPC handlers call them directly:

- `certmint.Mint(hostname, sans, opts)` — mints an enrollment token
- `store.AddClient` — records a newly-enrolled client (paired with `Mint` for `AddClient`/`ReEnrollClient`)
- `store.SetRevoked(hostname, revoked, at)` — revoke / unrevoke
- `store.SetKV` / `UnsetKV(hostname, KindDescription|KindAttribute, key, value)` — description &
  attribute ("label") management
- `store.AddSAN` / `RemoveSAN` — SAN alias management

One small, justified extraction: the "load a client record plus its description/attribute KV pairs"
logic (currently a private `toProtoClient` helper inside `clientmanager-api`) moves down into
`storage/clientmanager` as `Store.LoadClientView`, returning a plain (non-proto) struct. Both
`clientmanager-api` and `clientmanager-admin-api` do their own trivial, one-line proto conversion
from that shared view — avoiding duplicating the actual DB-loading logic across the two binaries
while keeping proto types out of the storage package.

`ReEnrollClient` mirrors the CLI's existing `re-enroll` behavior exactly: SAN overrides passed to it
are used for the minted token but are **not** persisted back to the stored record (matching
`runReEnroll`'s current behavior — SAN changes that should persist go through `UpdateSANs`/`san
add`/`san remove` instead, the mechanism Phase 2 made "genuinely live" on every credential refresh).

## gRPC surface

New proto, `src/api/clientmanageradmin.proto` — a separate file/package from `clientmanager.proto`,
importing its `Client` message rather than duplicating the client-record shape:

```proto
syntax = "proto3";

package clientmanageradminservice;

import "clientmanager.proto";

option go_package = "./proto";

// ClientManagerAdminService holds the CA provisioner password directly
// (CA-admin-equivalent access) -- deliberately isolated from
// clientmanager-api's general-purpose, password-free read surface. See
// docs/superpowers/specs/2026-07-19-clientmanager-admin-api-design.md.
service ClientManagerAdminService {
  rpc AddClient(AddClientRequest) returns (AddClientResponse);
  rpc ReEnrollClient(ReEnrollClientRequest) returns (ReEnrollClientResponse);
  rpc RevokeClient(RevokeClientRequest) returns (clientmanagerapiservice.Client);
  rpc UnrevokeClient(UnrevokeClientRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateDescription(UpdateClientKVRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateAttributes(UpdateClientKVRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateSANs(UpdateClientSANsRequest) returns (clientmanagerapiservice.Client);
}

message AddClientRequest {
  string hostname = 1;
  repeated string sans = 2;
}
message AddClientResponse {
  string token = 1;
}

message ReEnrollClientRequest {
  string hostname = 1;
  repeated string sans = 2; // empty = keep stored SANs
}
message ReEnrollClientResponse {
  string token = 1;
}

message RevokeClientRequest   { string hostname = 1; }
message UnrevokeClientRequest { string hostname = 1; }

message UpdateClientKVRequest {
  string hostname = 1;
  map<string, string> set = 2;
  repeated string unset = 3;
}

message UpdateClientSANsRequest {
  string hostname = 1;
  repeated string add = 2;
  repeated string remove = 3;
}
```

`AddClient` on an already-enrolled hostname returns `codes.AlreadyExists`. `ReEnrollClient`/
`RevokeClient`/`UnrevokeClient`/`UpdateDescription`/`UpdateAttributes`/`UpdateSANs` on an unknown
hostname return `codes.NotFound` — the same errors `store`'s methods already produce today, just
translated to gRPC status codes the way `clientmanager-api`'s existing `GetClient` already does.

## `api-server` REST surface

New endpoints, following every existing convention (`writeJSON`, `writeGRPCError`, the same bearer
token guarding every request):

| Method | Path | Backend RPC | Success |
|---|---|---|---|
| `POST` | `/api/v1/clients` | `AddClient` | `201`, `{"hostname", "token"}` |
| `POST` | `/api/v1/clients/{hostname}/reenroll` | `ReEnrollClient` | `200`, `{"hostname", "token"}` |
| `POST` | `/api/v1/clients/{hostname}/revoke` | `RevokeClient` | `200`, client record |
| `POST` | `/api/v1/clients/{hostname}/unrevoke` | `UnrevokeClient` | `200`, client record |
| `PATCH` | `/api/v1/clients/{hostname}/description` | `UpdateDescription` | `200`, client record |
| `PATCH` | `/api/v1/clients/{hostname}/attributes` | `UpdateAttributes` | `200`, client record |
| `PATCH` | `/api/v1/clients/{hostname}/sans` | `UpdateSANs` | `200`, client record |

- `POST /api/v1/clients` body: `{"hostname": "...", "sans": ["..."]}`.
- `POST .../reenroll` body: `{"sans": ["..."]}`, optional (omitted/empty keeps the stored SANs).
- `POST .../revoke` / `.../unrevoke`: no body.
- `PATCH .../description` / `.../attributes` body: `{"set": {"k": "v"}, "unset": ["k2"]}` — a
  partial update matching the CLI's per-key `set`/`unset` semantics, deliberately not a full-replace
  `PUT` like policies get. JSON field stays `attributes`, matching the existing `GET /clients`
  response shape (`policy-server`'s "labels" is the same underlying concept, just a different name
  in that component's own docs).
- `PATCH .../sans` body: `{"add": ["alias"], "remove": ["alias2"]}`.
- `writeGRPCError` gains one mapping: `codes.AlreadyExists` → `409`.
- `server.go`'s client-interface pattern extends with a new `clientManagerAdminClient` interface
  (the subset of the generated client these handlers need), alongside the existing
  `clientManagerClient`. `api-server` gets a second outbound mTLS connection, to
  `clientmanager_admin_api_host`/`_port` (new config keys, default port `9501`).

## Deployment

No new service in `docker-compose.yml`. The existing `clientmanager-api` service block is extended:

```yaml
clientmanager-api:
  build:
    context: ../..
    dockerfile: deploy/control-plane/clientmanager-api/Dockerfile
  depends_on:
    - step-ca
    - issuer
  volumes:
    - ./clientmanager-api/data:/data
    - ./clientmanager-api/local.conf:/data/local.conf:ro
    - ./client-manager/data:/data/client-manager
    - ./ca/data/certs/root_ca.crt:/data/root_ca.crt:ro       # new
    - ./ca/data/secrets/password:/data/secrets/password:ro    # new
  environment:
    - MP_CONFIG_PATH=/data
    - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
  ports:
    - "9500:9500"
    - "9501:9501"   # new: clientmanager-admin-api
  restart: unless-stopped
```

`deploy/control-plane/clientmanager-api/Dockerfile`'s build stage adds `clientmanager-admin-api` to
its `make` target list (alongside `clientmanager-api certclient agent policyclient`) and copies the
new binary in. `entrypoint.sh` keeps its existing bootstrap/renew + `agent serve` + wait-for-cert
sequence unchanged, then starts both service binaries as background processes rather than the
current single `exec`:

```sh
./clientmanager-api --debug="${DEBUG:-false}" &
./clientmanager-admin-api --debug="${DEBUG:-false}" \
    --ca-url https://step-ca:9000 \
    --root /data/root_ca.crt \
    --provisioner admin@backup.internal \
    --password-file /data/secrets/password &
wait
```

`entrypoint.sh` uses `#!/bin/sh` (dash), so this deliberately uses plain POSIX `wait` (blocks until
*both* background jobs exit) rather than bash's `wait -n` (blocks until the *first* exits) —
portable, but means that if one process crashes, the container stays up with only the other still
serving until something else notices (no automatic container-restart-on-partial-failure). Exact
process-supervision behavior (e.g. a small trap-based wrapper that exits as soon as either process
dies, restoring the "container dies if its service dies" property `restart: unless-stopped` expects)
is an implementation-stage detail, not pinned down further here.

Both processes share the one mesh identity `agent` already obtained (`certs/client.crt` etc.) — they
present the same peer identity when acting as gRPC servers, consistent with the project's existing
"any operating-tier cert may call any RPC it can reach" convention this spec deliberately keeps (see
Approach).

## Security Evaluation

- **A third holder of CA-admin-equivalent access**, alongside `client-manager` (CLI) and `issuer`.
  Stated plainly: this is the inherent cost of exposing enrollment/revocation over the network at
  all, not something this design eliminates — only minimizes. `clientmanager-admin-api`'s RPC
  surface is fixed and small (seven RPCs, all listed above) rather than open-ended, which is what
  keeps it auditable.
- **Filesystem isolation is given up for operational convenience.** Because `clientmanager-api` and
  `clientmanager-admin-api` share one container (see "Packaging" above), an arbitrary-file-read bug
  in the read-path binary could reach the provisioner password file on disk, even though only the
  admin binary reads it during normal operation. Process-level isolation (separate memory space,
  separate binary) is kept; container/filesystem-level isolation is not. This was a deliberate
  trade against doubling the number of one-time mesh enrollments and `agent` processes for this one
  feature — revisitable later (splitting into two containers) if the risk calculus changes.
- **No caller restriction beyond the existing mesh-wide convention.** `clientmanager-admin-api`
  accepts any valid operating-tier certificate, the same as `clientmanager-api`/`catalog`/
  `policy-server` today — it does not restrict callers to `api-server`'s specific peer identity.
  Concretely: any single compromised node holding a valid operating certificate anywhere in the
  fleet could, if it can reach the port, call these RPCs directly, bypassing `api-server`'s bearer
  token entirely. This is an explicit, accepted continuation of the project's existing convention
  (the same one `api-server`'s own docs already call out for `clientmanager-api`/`catalog`), not a
  gap newly introduced by this spec — but the stakes are materially higher here than for the
  read-only service that convention was originally accepted for.
- **Auth stays the existing single `api-server` bearer token**, no RBAC — consistent with the
  already-accepted gap for policy writes, now covering a higher-stakes capability (minting
  enrollment tokens, revoking arbitrary nodes) than editing backup policies.
- **Token exposure in REST responses.** The minted one-time token is returned once in the JSON
  response body — necessary, since the caller needs it to relay out-of-band. Never logged, never
  persisted beyond the in-flight request/response.
- **`client-manager` CLI is unaffected** and remains available for direct, on-host admin access —
  this adds a path, it doesn't replace the existing one.

## Testing

- `storage/clientmanager`: unit test for the new `LoadClientView` extraction.
- `clientmanager-admin-api`: unit tests per RPC against a stub minter (mirroring the CLI's existing
  `add_test.go`/`label_test.go`/`san_test.go` pattern) and a real (test) SQLite store — covering
  `AddClient`'s `AlreadyExists` path, `ReEnrollClient`'s SAN-override-not-persisted behavior, and
  `NotFound` handling for every RPC operating on an unknown hostname. One integration-style test
  minting against a real, throwaway `step-ca` instance, confirming the resulting token is actually
  redeemable (mirroring `certrequest`'s existing integration test).
- `api-server`: one test per new endpoint against a fake `clientManagerAdminClient` — success path,
  `404`/`409`/`400` gRPC-error-mapping paths, and malformed-JSON-body handling for
  `POST`/`PATCH` bodies.

## Documentation Impact

- New `docs/protocols/clientmanager-admin.md` (per this repo's gRPC-protocol documentation rule).
- New `docs/components/clientmanager-admin-api.md`.
- Cross-link updates: `docs/components/clientmanager-api.md` (See Also → new sibling), `client-manager.md`
  (note the new network-reachable path alongside the CLI), `api-server.md` (new config keys,
  endpoints reference).
- `docs/api/rest-v1.md` gains the seven new endpoints.
- `README.md`: component list and documentation index.
- `docs/ARCHITECTURE.md`: topology/data-flow diagram gains the new binary; control-plane-vs-agents
  table gets the third password-holder called out explicitly, consistent with how that table already
  documents this project's trust boundaries.
- `CHANGELOG.md` entry.
