# ClientManagerAdmin Protocol

`api-server` → `clientmanager-admin-api`'s sole RPC surface: CA-admin-equivalent writes onto the
same `clientmanager.sqlite` file `client-manager`'s CLI, `issuer`, and `clientmanager-api` already
share. mTLS (`common/mtls`, same transport every other gRPC call in this project uses). `api-server`
is the sole intended caller — see [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
for why this isn't enforced at the transport layer (the existing mesh-wide "any operating-tier cert
may call any RPC it can reach" convention applies here too, deliberately).

## RPC

```proto
service ClientManagerAdminService {
  rpc AddClient(AddClientRequest) returns (AddClientResponse);
  rpc ReEnrollClient(ReEnrollClientRequest) returns (ReEnrollClientResponse);
  rpc RevokeClient(RevokeClientRequest) returns (clientmanagerapiservice.Client);
  rpc UnrevokeClient(UnrevokeClientRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateDescription(UpdateClientKVRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateAttributes(UpdateClientKVRequest) returns (clientmanagerapiservice.Client);
  rpc UpdateSANs(UpdateClientSANsRequest) returns (clientmanagerapiservice.Client);
}
```

`RevokeClient`/`UnrevokeClient`/`UpdateDescription`/`UpdateAttributes`/`UpdateSANs` all return the
same `Client` message [`clientmanager-api`](../components/clientmanager-api.md)'s
`ListClients`/`GetClient` already return (imported from `clientmanager.proto` rather than
duplicated) — the caller sees the record's new state immediately, without a follow-up read.

## Behavior

- **`AddClient`**: mints a one-time enrollment token via `common/certmint.Mint` (the same mechanism
  `client-manager add` uses) and records the client, in that order — a mint failure never records a
  client, and an already-enrolled hostname (`codes.AlreadyExists`) never re-mints. `hostname` is
  required (`codes.InvalidArgument` if empty).
- **`ReEnrollClient`**: mints a fresh token for an already-tracked hostname (`codes.NotFound`
  otherwise). `sans`, if given, overrides the stored SAN list for this token only and is **not**
  persisted back to the record — matches `client-manager re-enroll`'s existing behavior exactly. Use
  `UpdateSANs` for a persistent SAN change.
- **`RevokeClient`/`UnrevokeClient`**: flip the stored `revoked` flag/timestamp. `codes.NotFound` for
  an untracked hostname. Enforcement (refusing a revoked hostname's next operating-certificate
  request) remains [`issuer`](../components/issuer.md)'s job, unchanged by this service.
- **`UpdateDescription`/`UpdateAttributes`**: apply `set` (upsert) then `unset` (delete) against the
  named key/value kind. `codes.NotFound` for an untracked hostname, mid-way through a batch included
  — a request against an unknown hostname leaves no partial writes only in the trivial sense that the
  very first `SetKV`/`UnsetKV` call already fails for an untracked hostname (the store checks
  existence before every write).
- **`UpdateSANs`**: applies `add` then `remove` against the hostname's SAN list — both are no-ops
  (not errors) for an alias already present/absent, matching `Store.AddSAN`/`RemoveSAN`. Both
  description/attribute and SAN changes reach an already-bootstrapped node on its next
  operating-certificate refresh, the same "genuinely live" mechanism
  [Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
  established — nothing here is retroactive to a certificate already issued.

## See Also

- [clientmanager-admin-api](../components/clientmanager-admin-api.md)
- [clientmanager-api](../components/clientmanager-api.md) — the read-only sibling service
- [client-manager](../components/client-manager.md) — the CLI this service's write logic mirrors
- [REST API v1](../api/rest-v1.md) — `api-server`'s REST surface onto this protocol
- [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
- [Security Model](../SECURITY.md)
