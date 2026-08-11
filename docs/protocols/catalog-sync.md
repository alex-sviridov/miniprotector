# Catalog Sync Protocol

`catalogsync` → `catalog`, over mTLS gRPC. Defined in [`src/api/catalog.proto`](../../src/api/catalog.proto).
`catalog` also serves `ListEntries`, a read-only query RPC called by [api-server](../components/api-server.md)
rather than by `catalogsync` — see [ListEntries](#listentries) below.

## Service

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
  rpc ListClientFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListJobFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListDirectoryFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListStoreFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListDirectoryChildren(ListDirectoryChildrenRequest) returns (ListDirectoryChildrenResponse);
}
```

One unary call per batch — `catalogsync` already batches client-side (`CatalogSyncBatchSize`), so
a `SyncRequest` carries a whole batch in a single round trip. Any RPC failure fails the batch as a
whole; there is no partial-batch success/failure reporting, matching `catalogsync`'s existing
all-or-nothing `Sender.Send(batch) error` contract.

## Messages

```protobuf
message FileVersionEntry {
  string job_id     = 1;
  string object_id  = 2;
  bytes  metadata   = 3;
  int64  ctime      = 4;
  int64  store_seq  = 5; // bwfs's local file_versions.seq — informational only
  int64  created_at = 6; // unix seconds; bwfs's original recording time
}

message SyncRequest {
  repeated FileVersionEntry entries = 1;
}

message SyncResponse {} // empty ack
```

## Identity

`catalog` does not trust any node identifier carried in the request payload. The persisted
`store_node` for every entry in a batch comes from the CA-verified hostname on the caller's mTLS
client certificate (first SAN, falling back to CommonName — see `common/mtls.PeerHostname`). This
is what lets `(store_node, job_id, object_id)` serve as a safe idempotency key across a fleet of
`bwfs` nodes whose `job_id`/`object_id` values are otherwise only unique per-node.

`catalog` also derives `source_host` at sync time — the real originating (backed-up) host, decoded
from each entry's `metadata` blob (a gob-encoded `filesystem.FileInfo`; see
`workload/filesystem.FileInfo.Source()`) and persisted as a plain indexed column. This is distinct
from `store_node`: `store_node` identifies the `bwfs` node that sent the batch, `source_host`
identifies the machine whose files were actually backed up — they coincide only when a `bwfs`
node backs up its own filesystem. A metadata decode failure leaves `source_host` empty for that
entry rather than failing the whole batch.

`catalog` also derives `parent_directory` and `short_filename` at sync time from the same
`metadata` blob, via `splitPath` (`cmd/catalog/pathsplit.go`). `parent_directory` is the file's
exact immediate containing directory (not a subtree/prefix match); `short_filename` is its bare
name. `splitPath` picks separator style (`/` vs `\`) from the path's own shape — a leading `/`, a
drive letter, or a UNC prefix — rather than the runtime OS, since `catalog` always runs on Linux
but a path may have been recorded by a Windows-origin `bwfs` node. A root-level file's
`parent_directory` is `/` (or `C:\` on Windows), never empty — empty is reserved to mean an
undecoded/failed entry, consistent with `source_host`'s existing convention. A metadata decode
failure leaves both fields empty for that entry rather than failing the whole batch.

## ListEntries

A read-only query RPC over the same store `SyncFileVersions` writes to — added for
[api-server](../components/api-server.md)'s `GET /api/v1/catalog` endpoint, its only intended
caller today. Unlike `SyncFileVersions`, callers are not restricted to the entry's own
`store_node`; any caller may query across hosts via the `store_host` and `source_host` filters.

```protobuf
message ListEntriesRequest {
  string store_host     = 1; // exact match against the sending bwfs node's identity; empty = all
  string pattern        = 2; // substring match against object_id; empty = no filter
  int32  limit           = 3; // 1..500, default 100
  int64  starting_after  = 4; // last-seen entry ID from a previous page; 0 = first page
  string source_host    = 5; // exact match against the real originating (backed-up) host; empty = all
  int64  received_after  = 6; // unix seconds; 0 = no lower bound, filters on received_at
  int64  received_before = 7; // unix seconds; 0 = no upper bound, filters on received_at
  repeated string source_hosts = 8; // OR-matched; empty = no filter, additive to source_host
  repeated string job_names    = 9; // OR-matched against the policy name embedded in job_id
  repeated string parent_directories = 10; // OR-matched against the exact immediate containing directory
}

message ListEntriesResponse {
  repeated Entry entries = 1;
  bool has_more = 2;
}

message Entry {
  int64  id                = 1;
  string store_host        = 2;
  string job_id            = 3;
  string object_id         = 4;
  int64  ctime             = 5;
  int64  store_created_at  = 6;
  int64  received_at       = 7;
  // decoded server-side from the stored Metadata blob:
  string path      = 8;
  int64  size       = 9;
  string mode      = 10; // e.g. "-rw-r--r--", from fs.FileMode.String()
  uint32 owner     = 11; // Unix UID (or Windows SID hash) — numeric, no name resolution
  uint32 group     = 12; // Unix GID (or Windows SID hash) — numeric, no name resolution
  int64  mod_time   = 13;
  string source_host = 14; // the real originating (backed-up) host, derived from Metadata at sync time
  string parent_directory = 15; // the file's exact immediate containing directory, derived from Metadata at sync time
  string short_filename   = 16; // the file's bare name, derived from Metadata at sync time; display only, not a filter
}
```

- `store_host` — exact match against the same CA-verified hostname `SyncFileVersions` persists as
  `store_node`; empty matches every store node.
- `source_host` — exact match against the real originating (backed-up) host, persisted at sync
  time (see [Identity](#identity) above); empty matches every source host.
- `pattern` — substring match against `object_id` (which embeds the original file path, e.g.
  `fs://database:f:/var/lib/dbdata/data.db:1752400000`); empty applies no filter.
- Pagination is keyset-based on `id`: request the first page with `starting_after` unset (or `0`),
  then pass the last entry's `id` from the previous page as `starting_after` to get the next one.
  `has_more` is `true` when additional entries exist beyond the current page. `limit` defaults to
  100 and is capped at 500.
- `path`, `size`, `mode`, `owner`, `group`, and `mod_time` are decoded server-side from the same
  opaque `metadata` blob `SyncFileVersions` stores verbatim — `ListEntries` is the first RPC to
  interpret that blob's contents rather than just persisting it (`source_host` is decoded once,
  at sync time, not on every `ListEntries` call — see [Identity](#identity)).
- `parent_directories` — OR-matched against `parent_directory`, an exact match against a file's
  *immediate* containing directory only (not a recursive subtree/prefix match); empty applies no
  filter, additive to every other active filter.

## ListClientFacets / ListJobFacets / ListDirectoryFacets / ListStoreFacets

Four read-only aggregate RPCs backing the web catalog view's faceted filter panels — for
[api-server](../components/api-server.md)'s `GET /api/v1/catalog/clients`,
`GET /api/v1/catalog/jobs`, `GET /api/v1/catalog/directories`, and `GET /api/v1/catalog/stores`. All four share one
request/response shape:

```protobuf
message Facet {
  string name       = 1; // hostname, policy name, parent directory, or store host
  int64  count       = 2; // matching entries in the current scope
  int64  last_seen   = 3; // unix seconds, max(received_at) in scope
}

message ListFacetsRequest {
  int64  received_after  = 1;
  int64  received_before = 2;
  string pattern         = 3;
  repeated string source_hosts = 4; // ignored by ListClientFacets (own dimension)
  repeated string job_names    = 5; // ignored by ListJobFacets (own dimension)
  repeated string parent_directories = 6; // ignored by ListDirectoryFacets (own dimension)
}

message ListFacetsResponse {
  repeated Facet facets = 1;
}
```

`ListClientFacets` groups by `source_host`; `ListJobFacets` groups by the policy name embedded in
`job_id` (see [Identity](#identity)'s `job_id` convention); `ListDirectoryFacets` groups by
`parent_directory`; `ListStoreFacets` groups by `store_host` (the bwfs node that sent the batch).
Each RPC applies every *other* dimension's filter fields from the request but ignores its own
(e.g. `ListDirectoryFacets` applies `source_hosts`/`job_names` but ignores `parent_directories`;
`ListStoreFacets` applies `source_hosts`/`job_names` but has no own-dimension filter to ignore,
since `ListFacetsRequest` carries no `store_hosts` field): a facet list is never narrowed by its
own current selection, so a caller can implement cross-filtering (selecting in one dimension
narrows the other dimensions) by passing every other dimension's active selection. Rows with an
empty grouping key (an undecoded `source_host`/`parent_directory`, or a `job_id` that isn't
`backup:`-prefixed) are dropped rather than surfaced as a blank-named facet.

## ListDirectoryChildren

A read-only query RPC backing file-manager-style directory browsing in the web catalog view —
distinct from `ListDirectoryFacets`, which returns a flat grouped-count list over every
`parent_directory` value regardless of tree position. `ListDirectoryChildren` instead walks the
`catalog_directories` tree (populated at sync time from each entry's `parent_directory` — see
[Identity](#identity) above) one level at a time, so a caller can implement expand/collapse
navigation without loading the whole tree.

```protobuf
message ListDirectoryChildrenRequest {
  string parent_path     = 1; // "" = true roots ("/", each distinct drive/UNC root)
  int64  received_after  = 2;
  int64  received_before = 3;
  repeated string source_hosts = 4;
  repeated string job_names    = 5;
  // No pattern field: directory browsing and pattern search are mutually
  // exclusive UI modes, never combined.
}

message DirectoryChild {
  string path         = 1; // full path, e.g. "/var/lib"
  string name         = 2; // short display label, e.g. "lib"
  int64  file_count   = 3; // direct files under path matching the current date/host/job filters
  int64  last_seen    = 4; // unix seconds, max(received_at) among those files; 0 if file_count == 0
  bool   has_children = 5; // true if catalog_directories has any row with parent_path == path
}

message ListDirectoryChildrenResponse {
  repeated DirectoryChild children = 1;
}
```

- `parent_path` — exact match against `catalog_directories.parent_path`; empty returns the true
  roots (`/` and each distinct drive/UNC root observed during sync), the same convention
  `decodeDirectoryAncestors` uses when it terminates a directory's ancestor walk.
- The returned set of children is **filter-independent**: it reflects every directory ever synced
  under `parent_path`, not just ones with entries matching the request's date/host/job narrowing.
  Making existence filter-aware would require answering whether *any* descendant anywhere in a
  subtree matches — the same recursive-subtree question `ListEntries`'s `parent_directories` filter
  and `ListDirectoryFacets` both deliberately avoid (see [above](#listclientfacets--listjobfacets--listdirectoryfacets)).
- `file_count`/`last_seen`, by contrast, only need a direct (non-recursive) `parent_directory` match
  against entries, so those **do** respect `received_after`/`received_before`/`source_hosts`/
  `job_names` — computed as one grouped scan across every child rather than N+1 per-child queries.
  `last_seen` is `0` when `file_count` is `0` (no matching entries, not the Unix epoch).
- `has_children` is `true` when the child itself has any row in `catalog_directories` with
  `parent_path` equal to the child's `path` (a `DISTINCT parent_path` scan) — lets the UI show an
  expand affordance without a second round trip per row.

## See Also

- [catalog](../components/catalog.md)
- [catalogsync](../components/catalogsync.md)
- [api-server](../components/api-server.md) — calls `ListEntries`, the only intended caller today
- [REST API v1](../api/rest-v1.md) — `GET /api/v1/catalog` (`ListEntries`), `GET /api/v1/catalog/clients`
  (`ListClientFacets`), `GET /api/v1/catalog/jobs` (`ListJobFacets`), `GET /api/v1/catalog/directories`
  (`ListDirectoryFacets`), `GET /api/v1/catalog/stores` (`ListStoreFacets`), and
  `GET /api/v1/catalog/directories/children` (`ListDirectoryChildren`)
