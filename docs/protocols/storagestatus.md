# Storage Status Protocol

Each `bwfs server` → `api-server`'s `ReportStorageStatus` RPC, once a minute, mTLS (`common/mtls`).
Served on the same port as the [Job Status Protocol](jobstatus.md) (`APIServerJobStatusPort`,
default 8091) and the same mesh identity. The web UI reads the result through
`GET /api/v1/storage-policies/{id}/status` ([REST API v1](../api/rest-v1.md)).

## RPC

```proto
service StorageStatusService {
  rpc ReportStorageStatus(ReportStorageStatusRequest) returns (ReportStorageStatusResponse);
}

message ReportStorageStatusRequest {
  string policy_id = 1;          // storage policy this process serves (agent: --policy-id)
  int32 port = 2;
  string status = 3;             // "serving" while the process is up
  uint64 disk_total_bytes = 4;   // filesystem holding the store root
  uint64 disk_used_bytes = 5;
  int32 active_connections = 6;  // open gRPC client connections
  int32 in_progress_jobs = 7;    // backup jobs with activity, not yet finalized
  int64 uptime_seconds = 8;
  google.protobuf.Timestamp reported_at = 9;
}
message ReportStorageStatusResponse {}
```

## Authorization

Restricted to the `"store"` role (`cmd/api-server/authz.go`). The reporting host is read from the
verified mTLS peer certificate, never the payload, so a node cannot report as another host.

## Behavior

- `policy_id` is required (`INVALID_ARGUMENT` otherwise).
- `api-server` keeps the **latest report per `(policy_id, reporting host)` in memory only** and
  stamps its own receive time. Nothing is persisted: a restart empties the map and it refills
  within a minute. Several nodes serving one policy each get their own entry.
- The REST endpoint derives `state` from the receive time: `online` under 3 min (tolerates two
  missed reports), `stale` under 10 min, `offline` after. Entries unseen for an hour are dropped.
- This is the one REST endpoint answered from `api-server`'s own memory instead of one backend gRPC
  call.
- `bwfs` reports best-effort: it posts at startup and every 60s with a 5s timeout, logs only the
  up→down and recovery transitions, and never lets a failure affect backups. It is disabled when no
  `--policy-id` was given or `api_server_host` is not configured.
- `disk_*` come from `statfs` on the store root; `active_connections` from a gRPC stats handler.

## See Also

- [bwfs](../components/bwfs.md), [agent](../components/agent.md), [api-server](../components/api-server.md), [web](../components/web.md)
