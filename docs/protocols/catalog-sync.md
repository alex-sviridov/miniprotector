# Catalog Sync Protocol

`catalogsync` → `catalog`, over mTLS gRPC. Defined in [`src/api/catalog.proto`](../../src/api/catalog.proto).

## Service

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
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
  int64  source_seq = 5; // bwfs's local file_versions.seq — informational only
  int64  created_at = 6; // unix seconds; bwfs's original recording time
}

message SyncRequest {
  repeated FileVersionEntry entries = 1;
}

message SyncResponse {} // empty ack
```

## Identity

`catalog` does not trust any node identifier carried in the request payload. The persisted
`source_node` for every entry in a batch comes from the CA-verified hostname on the caller's mTLS
client certificate (first SAN, falling back to CommonName — see `common/mtls.PeerHostname`). This
is what lets `(source_node, job_id, object_id)` serve as a safe idempotency key across a fleet of
`bwfs` nodes whose `job_id`/`object_id` values are otherwise only unique per-node.

## See Also

- [catalog](../components/catalog.md)
- [catalogsync](../components/catalogsync.md)
