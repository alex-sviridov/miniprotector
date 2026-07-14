# Design: api-server — unified read-only REST API for the control plane

**Date:** 2026-07-14
**Status:** Approved for planning

## Problem

The control plane today exposes its data only through per-component gRPC-over-mTLS RPCs (or, for
`client-manager`, only a local CLI against its SQLite store). There is no way for a browser or
admin tool to look at "what clients are enrolled" or "what's in the catalog" without a mesh client
certificate and a gRPC client. This adds `api-server`, a new control-plane component that exposes a
unified REST API in front of `client-manager` and `catalog`, starting read-only with two resource
groups: clients and catalog entries.

## Scope

**In scope (v1):**
- New standalone `api-server` binary, REST/JSON over HTTP(S)
- Read-only `GET /api/v1/clients`, `GET /api/v1/clients/{hostname}`
- Read-only `GET /api/v1/catalog` with filtering and pagination
- New gRPC read RPCs on `client-manager` (which also needs a first-ever daemon mode) and `catalog`

**Out of scope (v1):**
- RBAC / authorization beyond a single shared bearer token
- Write endpoints (enrolling/revoking clients, mutating catalog data)
- Cross-service aggregation (e.g. joining client attributes onto catalog rows) — each REST resource
  maps to exactly one backend gRPC service
- Any component beyond `client-manager` and `catalog` (policy-server, agents, etc. are future pages)

## Architecture

`api-server` is a new standalone binary (`src/cmd/api-server`) and a new control-plane mesh member:
it enrolls the same way any other node does (bootstrap credential → `certclient` → `issuer`
operating cert) and then acts purely as a **gRPC client** to `client-manager` and `catalog` over
mTLS, translating their responses into REST/JSON. It introduces the system's first REST surface;
everything else stays gRPC-over-mTLS, including api-server's own outbound calls.

Because REST callers (browsers, `curl`, admin tools) won't hold a mesh client certificate, the REST
listener is guarded by a single config-supplied bearer token instead of mTLS — deliberately simple,
matching the "no RBAC yet" scope. This is the only new trust boundary the design introduces; every
other authorization property is unchanged from today (any operating-tier mTLS cert may call any RPC
it can reach — see "Authorization" below).

api-server is a **thin translation layer**: each REST endpoint maps to one backend RPC call with no
business logic beyond param validation, gRPC-error-to-HTTP-status mapping, and (for catalog)
decoding an opaque metadata blob into JSON fields.

```
 browser/curl --Bearer token--> [api-server] --mTLS gRPC--> client-manager (new server mode)
                                            \-mTLS gRPC--> catalog (existing daemon, new RPC)
```

## client-manager changes

`client-manager` is pure CLI today (`add`, `list`, `mint`, `label`, `san` — each opens the SQLite
store, does one thing, exits). It gains a new **`clientmanager server`** subcommand that opens the
same store and starts a long-running gRPC listener via the existing `connection.StartServer` helper
(the same pattern `policy-server` uses), bound with mTLS. Existing CLI subcommands are unchanged and
continue to operate directly against the store; there's no write path added to the new RPCs, so
there's no new concurrent-write concern between CLI admin commands and the daemon.

New `ClientManagerService` (`src/api/clientmanager.proto`):

```protobuf
service ClientManagerService {
  rpc ListClients(ListClientsRequest) returns (ListClientsResponse);
  rpc GetClient(GetClientRequest) returns (Client);
}

message Client {
  string hostname = 1;
  bool revoked = 2;
  int64 revoked_at = 3;      // unix seconds, 0 if never revoked
  int64 last_seen_at = 4;    // unix seconds, 0 if never seen
  repeated string sans = 5;
  map<string, string> attributes = 6;
  map<string, string> descriptions = 7;
}

message ListClientsRequest {}
message ListClientsResponse { repeated Client clients = 1; }
message GetClientRequest { string hostname = 1; }
```

`GetClient` returns `NotFound` (translated to HTTP 404) for an unknown hostname. `attributes` and
`descriptions` are the existing `ClientKVRecord` rows for that hostname, keyed by their `Key`.

## catalog changes

`catalog` is currently write-only (`SyncFileVersions`) plus an internal `Count()`. It gains one new
RPC, `ListEntries`:

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);   // existing
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);  // new
}

message ListEntriesRequest {
  string source_host = 1;   // exact match; empty = all hosts
  string pattern = 2;       // substring match against object_id; empty = no filter
  int32 limit = 3;          // 1..500, default 100
  int64 starting_after = 4; // last-seen entry ID from a previous page; 0 = first page
}

message ListEntriesResponse {
  repeated Entry entries = 1;
  bool has_more = 2;
}

message Entry {
  int64 id = 1;
  string source_host = 2;
  string job_id = 3;
  string object_id = 4;
  int64 ctime = 5;
  int64 source_created_at = 6;
  int64 received_at = 7;
  // decoded from the stored Metadata blob:
  string path = 8;
  int64 size = 9;
  string mode = 10;
  string owner = 11;
  string group = 12;
  int64 mod_time = 13;
}
```

Filtering: `source_host` matches `EntryRecord.SourceNode` exactly. `pattern` is a plain SQL
`LIKE '%pattern%'` against the existing `object_id` column — `ObjectID` already embeds
`fs://hostname:type:path:mtime` (see `src/workload/filesystem/fileinfo.go`), so path substring
matching works directly against the existing indexed-ish string column with **no schema change**
and no need to decode `Metadata` at query time just to filter. This is an unindexed infix scan; fine
at current catalog scale, a known limitation if the table grows large — not addressed now.

Pagination: results ordered by `ID` descending (newest first), `limit` capped server-side at 500,
`starting_after` is the last `ID` seen on the previous page (keyset pagination — cheap and stable
under concurrent inserts, unlike offset pagination).

`Metadata` (a Gob-encoded `FileInfo`) is decoded server-side in `catalog`'s RPC handler into the
`path`/`size`/`mode`/`owner`/`group`/`mod_time` fields above, so `api-server` never needs to know
about the Gob format.

## Authorization

No component in this codebase does per-caller RPC authorization today — the existing convention is
"any operating-tier mTLS cert may call any RPC it can reach" (credential *tier* is enforced at the
TLS handshake; individual caller identity is not checked against an allowlist anywhere, including on
sensitive existing RPCs like `catalog.SyncFileVersions`). The new `ListClients`/`GetClient`/
`ListEntries` RPCs follow this same convention — no new authorization logic is added, consistent
with "no RBAC yet." This means any node holding a valid operating credential could call these new
read RPCs directly (bypassing api-server's bearer token), which is an accepted, explicit
continuation of the existing trust model, not a new gap introduced by this design.

## api-server

**Binary:** `src/cmd/api-server`, a new control-plane component with its own `local.conf` (via the
existing `config` package), deployed in `deploy/control-plane/api-server/` and wired into `demo/`
so the full path is exercisable end-to-end (`make demo-up` + `curl`).

**Startup:** standard node enrollment (bootstrap credential → `certclient` → `issuer` operating
cert), then dials `client-manager` and `catalog` as gRPC clients over mTLS, same as any other mesh
component. Config supplies both backend addresses plus the REST listener's port and bearer token.

**Transport:** plain `net/http` with Go 1.22+ `ServeMux` pattern routing — no third-party router,
matching `log-gateway`'s existing precedent of not pulling in a framework.

**Auth:** every request requires `Authorization: Bearer <token>`, checked against the single
config-supplied token; missing or mismatched → 401. No sessions, no per-user identity — this is the
only auth layer in v1.

**Endpoints** (Stripe/GitHub-style conventions — plain query params for filters, `limit` +
`starting_after` cursor for pagination, `{"data": [...]}` envelope, `has_more` flag):

- `GET /api/v1/clients` → `{"data": [Client, ...]}` (no pagination — client list is expected to
  stay small; add pagination later if that assumption breaks)
- `GET /api/v1/clients/{hostname}` → single `Client` object, 404 if unknown
- `GET /api/v1/catalog?source_host=&pattern=&limit=&starting_after=` →
  `{"data": [Entry, ...], "has_more": bool}`

`Client` and `Entry` JSON shapes mirror their gRPC proto messages field-for-field (snake_case).

**Error translation:** gRPC `NotFound` → 404, `InvalidArgument` → 400, anything else (including
`Unavailable`) → 502. Malformed query params (non-numeric `limit`, `limit` out of `1..500`) → 400
before any gRPC call is made.

## Testing plan

- **`client-manager`**: unit tests for `ListClients`/`GetClient` gRPC handlers against the existing
  store (table-driven, following existing `store_test.go` conventions); a smoke test that `server`
  subcommand wiring starts and serves.
- **`catalog`**: unit tests for `ListEntries` — `source_host` filter, `pattern` substring filter,
  pagination boundaries (`has_more` correctness at the last page, `starting_after` continuation),
  `Metadata` decode-to-fields correctness.
- **`api-server`**: HTTP handler tests via `httptest` against an in-process gRPC server standing in
  for `client-manager`/`catalog`; bearer-token auth tests (missing/wrong/correct); query-param
  validation tests (bad `limit`, unknown host); gRPC-error-to-HTTP-status translation tests.
- **Integration:** wire `api-server` into `demo/` (compose + `local.conf`); manual `curl` against
  `/api/v1/clients` and `/api/v1/catalog` using the demo lab's existing fixture data serves as the
  end-to-end smoke test.

## Documentation

- New `docs/components/api-server.md` — role, config, how it fits the mesh (mirrors other component
  docs).
- New `docs/api/rest-v1.md` — the REST API itself: endpoints, auth, filtering/pagination
  conventions, JSON shapes. (Distinct from `docs/protocols/`, which documents internal gRPC
  protocols, not this external-facing REST surface.)
- `docs/components/client-manager.md` — document the new `server` subcommand and daemon mode.
- `docs/components/catalog.md` — document `ListEntries`, replacing the current "no query/report API
  yet" note.
- `docs/protocols/catalog-sync.md` or a new `docs/protocols/catalog-server.md` — the new
  `CatalogService.ListEntries` RPC shape, alongside the existing `SyncFileVersions` documentation.
- `docs/ARCHITECTURE.md` — add api-server to the component list/diagram as the system's first REST
  entry point.
- `CHANGELOG.md` — one dated entry.

## Out of scope

- No RBAC, no per-user identity, no per-caller RPC authorization (see "Authorization" above).
- No write endpoints anywhere in this design.
- No cross-service aggregation/joining in api-server.
- No pagination on `/api/v1/clients` (revisit if the enrolled-client list grows large).
- No indexed path search on `catalog` (the `LIKE '%pattern%'` scan is unindexed by design, revisit
  if it becomes a performance problem).
