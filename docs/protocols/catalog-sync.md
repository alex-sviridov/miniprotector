# Catalog Sync Protocol

`catalogsync` → `catalog`, over mTLS gRPC. Defined in [`src/api/catalog.proto`](../../src/api/catalog.proto).
`catalog` also serves `ListEntries`, a read-only query RPC called by [api-server](../components/api-server.md)
rather than by `catalogsync` — see [ListEntries](#listentries) below.

## Service

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
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

## See Also

- [catalog](../components/catalog.md)
- [catalogsync](../components/catalogsync.md)
- [api-server](../components/api-server.md) — calls `ListEntries`, the only intended caller today
- [REST API v1](../api/rest-v1.md) — the `GET /api/v1/catalog` endpoint backed by `ListEntries`
