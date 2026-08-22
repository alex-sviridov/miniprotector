# Role-Based gRPC Authorization — Design

> Started from a wrong premise (certificates being issued for a self-reported server name) that
> didn't hold up against the code: hostnames are already operator-assigned at enrollment and
> embedded in the enrollment token's JWT claims, and every RPC already derives identity from the
> verified mTLS peer certificate (`common/mtls.PeerHostname`), never a request field. Rescoped
> during brainstorming to the real, already-documented gap: **every operating-tier certificate is
> interchangeable** — any enrolled node can call any RPC on any control-plane service it can reach.

## Problem

Every gRPC server in this project (`clientmanager-api`, `clientmanager-admin-api`, `catalog`,
`policy-server`, `bwfs`) is started via `common/connection.StartServer`, which loads credentials
via `mtls.LoadServerCredentials` — this accepts **any** valid operating-tier certificate, full
stop. There is no concept of *which kind* of node is calling beyond "not revoked, not
bootstrap-tier." `docs/components/api-server.md` already states this as an accepted trade-off:

> "Any node holding a valid mesh operating credential can still call
> `clientmanager-api`/`clientmanager-admin-api`/`catalog`/`policy-server`'s RPCs directly,
> bypassing this token — an accepted continuation of this project's existing 'any operating-tier
> cert may call any RPC it can reach' convention, not a new gap."

Concretely: a single compromised `bwfs`/`brfs`/`rwfs` node — the least-privileged thing in the
fleet — holds an operating certificate that is equally valid for dialing
`clientmanager-admin-api` and calling `AddClient`/`ReEnrollClient`/`RevokeClient`/
`UpdateAttributes`/`UpdateSANs`. That is CA-admin-equivalent power (mint enrollment tokens, revoke
arbitrary nodes, rewrite any node's SAN list) reachable from the single lowest-value machine in
the mesh.

## Scope

**In scope:** a per-RPC role check, enforced by a gRPC server interceptor, for every gRPC service
in this project (`clientmanager-api`, `clientmanager-admin-api`, `catalog`, `policy-server`,
`bwfs`'s backup/list/restore RPCs). A closed, three-value role enum (`control-plane`, `store`,
`client`) assigned per node at enrollment time and carried in the operating certificate via the
*existing* `attribute` extension mechanism.

**Out of scope:**
- `issuer` — already gated by its own, orthogonal EKU bootstrap/operating tier check
  (`mtls.LoadIssuerServerCredentials`); untouched by this design.
- `log-gateway`'s HTTP push endpoint — deliberately "must authenticate to push at all," not
  "must be a specific kind of node," per its existing documented rationale in `SECURITY.md`.
- `api-server`'s REST bearer-token layer — separate, pre-existing auth mechanism; unaffected.
- Any live-migration or grace-period handling for already-enrolled nodes lacking a role. Per
  explicit direction: no backward compatibility is needed. Existing demo/lab deployments are
  expected to re-enroll (or receive an `attribute set <hostname> authz-role=...` backfill) as part of
  rolling this out, same precedent as `docs/superpowers/specs/2026-07-05-credential-tier-enforcement-design.md`.

## Design

### Role model & storage: reuse the existing `attribute` extension

A node's role is stored as an ordinary `attribute` key/value pair — `authz-role=<value>` — in
`client-manager`'s existing generic `ClientKVRecord` table (`KindAttribute`). **Key name:**
`authz-role`, not the bare `role` — `demo/policy-server/policies/backup/webserver-backup.json`
already uses a free-form `role` attribute (`"labels": {"role": "web"}`) as a policy-targeting
label (`demo/up.sh`'s `enroll webserver "role=web"`), unrelated to authorization. Reusing that key
would either break that demo policy (this design's enum validation would reject `web` as not one
of `control-plane`/`store`/`client`) or, worse, silently conflate two unrelated concepts under one
key. `authz-role` avoids the collision; the CLI flag introduced below is still named `--role` for
operator ergonomics — only the underlying stored attribute key differs. This is already:

- Settable via `client-manager attribute set <hostname> authz-role=...` and the network-reachable
  `UpdateAttributes` RPC — no new storage, no new RPC needed for changing it later.
- Embedded into every issued operating certificate today, unread: `issuer`'s
  `RequestOperatingCert` (`src/cmd/issuer/server.go`) already loads every `KindAttribute` row for
  the hostname into a plain map and passes it through `mintAndSign` → `TemplateData.Attributes` →
  `deploy/control-plane/ca/templates/leaf.tpl`'s existing `extensions` block (OID
  `1.3.6.1.4.1.61183.1.1`). An `authz-role` key needs zero changes to `issuer`, `mintsign.go`, or
  `leaf.tpl` to start flowing through.
- Already parseable off the verified peer certificate: `common/mtls.PeerAttributes(ctx)` already
  extracts and JSON-decodes this exact extension. Role enforcement just reads
  `PeerAttributes(ctx)["authz-role"]`.

**Value shape:** a comma-separated list of tokens from the closed enum `{control-plane, store,
client}` (e.g. `authz-role=store`, or `authz-role=store,client` if a node ever legitimately needs
two — not required by today's matrix, but free to express). Split on `,`, each token checked for
membership.

**Validation:** `Store.SetKV` gains a special case — when `kind == KindAttribute && key ==
"authz-role"`, validate every comma-separated token against the enum before writing, returning an
error on an unknown token (e.g. a typo) instead of silently accepting it. This is the single choke
point every write path (CLI `attribute set`, `clientmanager-admin-api`'s `UpdateAttributes`, and
the new atomic-at-creation path below) goes through. `attribute unset <hostname> authz-role` (and
the equivalent `UpdateAttributes` unset path) is intentionally left unrestricted — removing a
node's role simply leaves it with zero roles, which the fail-closed default below already handles
correctly (denied everywhere except `GetPolicies`), so no special-case guard against unsetting it
is needed.

### Setting role at enrollment

Unlike `description`/generic `attribute`, which are always separate follow-up commands run after
`add`, role is set as part of the same `add`/`AddClient` call — an operator enrolling a node states
its role up front rather than remembering a second command, and a freshly-enrolled node reads as
"role: client" (the default) rather than "no role yet" from the very first `LoadClientView`. A
failure of the second call (`SetKV`) after `AddClient` already succeeded leaves the node enrolled
with no role attribute, which the fail-closed default (see Error Handling below) already handles
safely — denied every role-gated RPC, not an inconsistent partial grant — so this narrow window is
an accepted, self-correcting edge case (an operator notices immediately, since the node can't do
anything gated by role until re-run), not a new failure class this design needs to prevent.

- The CLI's `runAdd` and `clientmanager-admin-api`'s `AddClient` handler each validate the
  resolved role (shared `clientmanagerstore.ValidateRole` helper) *before* minting a token, then
  call the existing `Store.AddClient` unchanged followed immediately by
  `Store.SetKV(ctx, hostname, KindAttribute, "authz-role", role)`. This is a same-request
  sequential pair, not a single GORM transaction — deliberately matching this codebase's existing
  convention that `description`/`attribute` writes are always separate `SetKV` calls from `add`
  (only SANs are columns on `ClientRecord` itself); the earlier alternative (extending
  `Store.AddClient`'s own signature to take a role and write it in the same transaction) was
  rejected during planning because it would have forced an unrelated 5th parameter onto roughly 50
  existing `AddClient` call sites across `issuer`, `clientmanager-api`, and `client-manager`'s own
  tests that have nothing to do with roles. Validating before minting means an invalid `--role`
  never wastes a one-time enrollment token.
- **`client-manager add <hostname> --role <role>`** — new flag, mirrors `--san`. **Defaults to
  `client` when omitted** (the common case — most enrolled nodes are ordinary `brfs`/`rwfs`
  backup-agent hosts). CLI resolves the default before calling `Store.AddClient` (empty flag →
  literal `"client"`), so the store method itself always requires a valid, non-empty value.
- **`clientmanageradmin.proto`**: `AddClientRequest` and `ReEnrollClientRequest` each gain
  `string role = 3`. Empty means "use the default" for `AddClient` (server resolves to
  `"client"`, same default as the CLI) and "keep the hostname's currently stored role" for
  `ReEnrollClient` (same empty-means-keep convention `sans` already uses on that message).
  `clientmanager-admin-api`'s `AddClient`/`ReEnrollClient` handlers (`src/cmd/clientmanager-admin-api/server.go`)
  resolve the default/keep-existing the same way the CLI does before calling the store.

This is the one `.proto` change in this design — per this repo's documentation rules, it requires
updating `docs/protocols/clientmanager-admin.md` to describe the new field before it's committed.

### Enforcement mechanism: a generic gRPC interceptor

A single mechanism for every service — including `catalog` and `policy-server`, which each serve
RPCs that need *different* role sets from the same listener, which rules out a pure listener-level
(EKU-style) check as a complete solution on its own.

- **New in `common/mtls`** (alongside `PeerAttributes`): a small interceptor factory taking a
  `map[string][]string` of full gRPC method name (`info.FullMethod` / `StreamServerInfo.FullMethod`,
  e.g. `"/catalogservice.CatalogService/SyncFileVersions"`) → allowed roles, returning both a
  `grpc.UnaryServerInterceptor` and a `grpc.StreamServerInterceptor` (`bwfs`'s
  `ProcessBackupStream`, `ListFiles`, `ResolveRestoreFiles`, and `RestoreFile` are all streaming
  RPCs, so both interceptor kinds are needed). Per call: read
  `PeerAttributes(ctx)["authz-role"]` (already-parsed, in-memory, off the already-terminated TLS
  connection — no extra I/O, no extra round trip), split on `,`, and check intersection with the
  method's allow-list. No entry for a method in the map means "no role required" (used for
  `GetPolicies`, see matrix below).
- **`common/connection.StartServer`/`StartServerWithCredentials`** each gain a
  `roleRequirements map[string][]string` parameter, wiring the two interceptors via
  `grpc.ChainUnaryInterceptor`/`grpc.ChainStreamInterceptor` when building the `grpc.Server`. Every
  server's `main.go` passes its own matrix literal at the call site — the role matrix lives next to
  each service's own RPC definitions, not centralized in the transport package.

### Authorization matrix

| Service | RPCs | Allowed roles |
|---|---|---|
| `clientmanager-api` | `ListClients`, `GetClient` | `control-plane` |
| `clientmanager-admin-api` | `AddClient`, `ReEnrollClient`, `RevokeClient`, `UnrevokeClient`, `UpdateDescription`, `UpdateAttributes`, `UpdateSANs` | `control-plane` |
| `catalog` | `SyncFileVersions` | `store` |
| `catalog` | `ListEntries`, `ListClientFacets`, `ListJobFacets`, `ListDirectoryFacets`, `ListStoreFacets`, `ListDirectoryChildren` | `control-plane` |
| `policy-server` | `GetPolicies` | *(no restriction — every agent-managed node, of every role, needs this to function)* |
| `policy-server` | `ListPolicies`, `CreatePolicy`, `UpdatePolicy`, `DeletePolicy`, `GetNodeCertStatus` | `control-plane` |
| `bwfs` (`backup.proto`) | `ProcessBackupStream`, `BackupCommit` | `client` |
| `bwfs` (`list.proto`) | `ListFiles`, `ResolveRestoreFiles` | `client` |
| `bwfs` (`restore.proto`) | `RestoreFile` | `client` |

This closes the motivating gap: a compromised `bwfs`/`brfs`/`rwfs` node holds a `client`-role
certificate, and `clientmanager-admin-api`/`clientmanager-api`/`catalog`'s admin RPCs now reject it
at the interceptor before any handler logic runs — it can no longer mint enrollment tokens,
revoke arbitrary nodes, or rewrite SAN/attribute data for the fleet.

`catalog`/`policy-server`/`clientmanager-api`/`clientmanager-admin-api`/`api-server` all run as
ordinary `agent`-managed enrolled nodes themselves (per `ARCHITECTURE.md`) and are enrolled with
`authz-role=control-plane` — which is why `GetPolicies` stays open to every role: these nodes need
it for their own certificate/policy lifecycle exactly like any `client`/`store` node does.

### Error handling

A role mismatch (or a missing/empty `authz-role` attribute — a fail-closed default, not fail-open) returns
`codes.PermissionDenied` with a message naming the required role(s), consistent with this
codebase's existing use of gRPC status codes for authorization failures (e.g. `issuer`'s revoked-hostname
refusal). This is a request-time error, not a connection-time one (unlike the EKU bootstrap/operating
check, which fails at the TLS handshake) — the peer's mTLS handshake still succeeds, since role is
read from the certificate's contents by the interceptor, not verified by the TLS stack itself.
Callers see an ordinary gRPC error and rely on existing retry/logging behavior; no new recovery
path is introduced.

### Rollout

No backward-compatibility path is being built. Once the interceptor-enforcing binaries are
deployed, any node without a matching `authz-role` attribute on its current operating certificate
is denied every role-gated RPC until it re-enrolls (or is backfilled via `attribute set ...
authz-role=...` and picks up a fresh operating certificate on its next `operating-refresh`,
≤15 minutes). This is
an explicit, accepted trade-off for this rollout, not an oversight — `demo/`'s enrollment scripts
need `--role` added to their `client-manager add` calls as part of this change so the demo
environment keeps working.

### Testing

- **Unit — `common/mtls` interceptors**: fake `peer.Peer`/`credentials.TLSInfo` in context (no
  real server needed, matching how `PeerAttributes` is presumably already tested), table-driven
  over role × method → allow/deny.
- **Unit — `Store.AddClient`/`SetKV` role validation**: valid tokens accepted, unknown tokens
  rejected, multi-role comma-separated values round-trip correctly.
- **Unit — per service**: one test per RPC × representative caller role (allowed/denied),
  asserting `codes.PermissionDenied` on mismatch and normal pass-through on match, for
  `clientmanager-api`, `clientmanager-admin-api`, `catalog`, `policy-server`, `bwfs`.
- **E2E**: extend whatever harness already proves the attribute round-trip
  (`src/cmd/issuer/e2e_test.go`) with a case enrolling a `client`-role node and proving it is
  rejected by `clientmanager-admin-api`'s `AddClient`, mirroring the rigor already applied to the
  EKU tier check in `docs/superpowers/specs/2026-07-05-credential-tier-enforcement-design.md`.

### Documentation

Per this repo's documentation rules (`.claude/CLAUDE.md`):

- **`docs/protocols/clientmanager-admin.md`** — the `.proto` change (`role` field on
  `AddClientRequest`/`ReEnrollClientRequest`) and the new role-based authorization rule for every
  RPC on this service.
- **`docs/protocols/{backup,list,restore,catalog-sync,policy-server}.md`** — add the allowed-role
  requirement for each RPC (no message-shape changes, but the authorization rule is new protocol
  behavior).
- **`docs/components/{client-manager,clientmanager-admin-api,clientmanager-api,catalog,policy-server,bwfs}.md`**
  — document the `--role` flag, the default, and each component's own role requirement as a
  caller.
- **`docs/SECURITY.md`** — new section describing the role model, superseding the "any
  operating-tier cert may call any RPC it can reach" convention this design closes; update the
  two-tier credential model section to note roles are an orthogonal, RPC-level check layered on
  top of (not a replacement for) the bootstrap/operating tier split.
- **`README.md`** — update if the quick-start enrollment example uses `client-manager add` without
  a role (it should show the flag or note the default).
- **`docs/ARCHITECTURE.md`** — note each control-plane/agent component's expected role in the
  existing topology tables.
- **`CHANGELOG.md`** — new entry before merging to `main`, per the existing project convention.

## Non-goals (explicit)

- Any change to `issuer`'s own authorization model (already solved by the EKU tier check).
- Any change to `log-gateway` or `api-server`'s REST bearer-token layer.
- A live migration/grace-period mechanism for pre-existing certificates lacking a `role`
  attribute — explicitly not needed per this design's direction.
- Making role assignment mutable through any path other than the existing `attribute`
  set/unset and `AddClient`/`ReEnrollClient` mechanisms — no dedicated `role` RPC or CLI
  subcommand is introduced.
