# bwfs (Backup Writer from File System)

Backup storage server — receives files from a backup reader and stores them on disk with deduplication.

## Authorization

Every RPC across `BackupService`, `ListService`, and `RestoreService` requires the `client` role.
See [Security Model](../SECURITY.md#role-based-rpc-authorization).

## Usage

```
bwfs <storage_path> <command> [flags]
```

`storage_path` is required by all commands. Only one `bwfs server` process may open a given storage path at a time; `bwfs list` can run alongside a live server.

## Commands

### server

Start the gRPC server. Receives files from `brfs` and stores chunks and metadata. Also serves the `ListService` subprotocol so `rwfs list` can query this server remotely.

The server registers `BackupService`, `ListService`, and `RestoreService` on the same
port. See [Restore Protocol](../protocols/restore.md) for the restore subprotocol.

```bash
bwfs /home/user/backup server
bwfs /home/user/backup server --port 8080 --debug
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | config `default_port` | Port to listen on |
| `--debug` | false | Enable debug logging |
| `--quiet` | false | Suppress console logging |

On startup, before accepting connections, the server runs a full vacuum pass over the store
(removes incomplete/orphaned `FileData`, orphaned chunk links and orphaned chunk records, then
compacts and removes pack segments, see [Storage layout](#storage-layout)) and logs the results
(`segments_removed`, `segments_compacted`, `bytes_reclaimed`). A vacuum failure is fatal — the server exits
rather than serving against a store it couldn't clean up.

Opening the store also runs a one-time backfill of the `source_host`/`path`/`mtime` columns on
`file_data_records`. Those columns were added after the store format already existed, and schema
migration only creates them — it does not populate them — so any file backed up before they existed
would otherwise stay invisible to `ResolveRestoreFiles`, which matches on `path`. The backfill
derives all three from each row's `file_id` in bounded batches, is resumable if interrupted, and
after its first run costs a single indexed lookup that matches nothing.

On `SIGTERM`/`SIGINT`, `bwfs` now shuts down gracefully: `grpc.Server.GracefulStop()` lets any
in-flight `BackupService`/`ListService`/`RestoreService` call finish before the process exits,
rather than killing it mid-stream — the same behavior every other gRPC server in this repo already
had. This matters for [agent](./agent.md#storage-policy-supervision), which supervises a `bwfs
server` process per storage policy targeting this node and routinely sends it `SIGTERM` (on its own
shutdown, or when a storage policy is edited/removed).

#### Chunk ordering within a file

`brfs` may have several chunks of one file in flight (see
[brfs sliding window](brfs.md#sliding-window)), so a chunk already stored can be accounted for ahead
of an earlier chunk whose data has not arrived yet. Per file, `bwfs` folds chunk CRCs into the file
checksum in index order through a small reorder buffer (`chunkOrder`): only chunks ahead of a gap are
held, at most `maxPendingChunks` (1024) of them, and the file is finalized once the `eof` chunk has
been folded in. See [Backup Protocol](../protocols/backup.md#in-flight-chunks-sliding-window).

#### Backup Job Tracking & Completion Verification

Every stream `bwfs` accepts must carry `job-id` gRPC metadata (sent by `brfs` — see
[brfs](brfs.md)). `bwfs` records each job in a `backup_jobs` table (`job_id`, `source_host`,
`started_at`, `finished_at`, `status`) and tags every row in `file_versions` with the `job_id` of
the run that produced it. `source_host` is read from the client's verified mTLS identity, not
anything the client reports in-band.

A job starts `status=in_progress` and is only finalized (`success` or `failure`, with
`finished_at` set) by one of three paths:

1. `brfs` calls the unary `BackupCommit` RPC after all its streams close; `bwfs` recomputes a
   SHA256 over its own `file_versions` for that job and compares it to the hash `brfs` submits —
   match is `success`, mismatch is `failure` (and purges that job's `file_versions`).
2. The stall watchdog fails any job with no activity for longer than the `JobTimeoutSec` config
   key (default 30 seconds).
3. On startup, `bwfs` fails any job left `in_progress` by a previous, uncleanly-terminated process,
   before accepting new connections.

Each `file_versions` row also carries a nullable `expire_at` (unix seconds) taken from the file's
`FileInfo.expire_at`, stamped by `brfs` from its job's retention matrix, on both the new-file and
already-known-file paths. NULL means no expiry was recorded or the file never expires — rows from
before this column existed stay NULL — and nothing treats NULL as expired. The scheduled
**cleanup** described next deletes versions whose `expire_at` has passed.

#### Scheduled cleanup and vacuum

While `bwfs server` runs, two background loops keep the store from only ever growing:

- **Cleanup** (every `StoreCleanupIntervalSec`, default 3600) deletes expired file versions — a
  real `expire_at` that has passed, belonging to a job that is **not** still `in_progress` (a
  short retention must never delete versions out from under a running job, whose `BackupCommit`
  would then fail its hash check). It is plain `expire_at` semantics: no version is protected for
  being a host's most recent backup, so a host that stops backing up loses everything once its
  retention passes. It also prunes the deletion log (below) past `StoreDeletionLogRetentionSec`.
- **Vacuum** (every `StoreVacuumIntervalSec`, default 86400) reclaims what no version references
  any more: file data with no version, chunk links with no file data, chunk records with no link,
  and those chunks' files on disk (bytes reclaimed are logged). Incomplete file data is only
  treated as abandoned after `StoreIncompleteFileDataGraceSec` (default 24 h) — unlike at startup, a
  file may legitimately still be transferring. It never walks the chunk directory (stray and
  crash-leftover files remain the startup vacuum's job).

Both loops run in batches of `StoreGCBatchSize` rows (default 500), each batch one short
transaction, so backups are paused for milliseconds, never for a whole run; the first run of each is
one interval after startup, runs never overlap, a failure is logged and retried next tick (never
fatal after startup), and an interval of `0` disables that loop.

**Each run is a job in the Jobs view.** A run's `job_id` is `cleanup:<host>:<unix>` or
`vacuum:<host>:<unix>` (the host keeps several stores' runs from colliding on the same second),
logged with the same `event=start` / `event=finish` lines every other job uses — nothing more
detailed than that. The finish line carries `status` (`success` / `failure`), `duration` and the
run's statistics: cleanup `versions_expired`, `deletion_log_pruned` and `dry_run`; vacuum
`incomplete_file_data_removed`, `orphaned_file_data_removed`, `orphaned_chunk_links_removed`,
`orphaned_chunks_removed` and `bytes_reclaimed`. A failed run logs that line at Error level with
`status=failure` and the `error` text. Vacuum is always a job (it runs daily, and "ran, reclaimed
nothing" is worth seeing); cleanup is a job only when it has something to report — a cheap indexed
count decides first, and an hourly run that finds nothing expired is just a Debug line, so it
doesn't bury the jobs that matter. See [web](web.md) for how the job page shows them.
`StoreCleanupDryRun=true` makes cleanup log how many versions it *would* delete without deleting
anything — worth running first on an existing store.

**Why this is safe next to live backups.** The startup vacuum assumes nothing is in flight; run
periodically it would corrupt backups — it could delete a chunk between a backup deciding it
"already exists" and linking it, remove a file's data between a backup finding it known and
recording its version, or delete a just-stored chunk before its record is written. `bwfs` therefore
guards the store: every backup-stream message handler runs under the shared side of an operation
guard (released as soon as its store work is done, before it replies, so a client that has stopped
reading can't hold it), and each cleanup/vacuum batch takes the exclusive side. Each of those
invariants lives inside a single handler call, so a batch can neither start in the middle of one nor
see it half done. A file in transfer between messages is protected by its own rows: its
incomplete file data is never orphaned and its chunk links keep its chunks referenced.

**Deletion log.** Every version delete — cleanup, and the purge of a failed or stale job's
versions — also inserts a row into `file_version_deletions` (`job_id`, `object_id`, `deleted_at`) in
the same transaction. `catalogsync` replicates it so the catalog drops the version too; see
[catalogsync](catalogsync.md#deletions).

| Config key | Default | Meaning |
|---|---|---|
| `StoreCleanupIntervalSec` | 3600 | how often expired versions are deleted; `0` disables |
| `StoreVacuumIntervalSec` | 86400 | how often unreferenced data and chunks are reclaimed; `0` disables |
| `StoreGCBatchSize` | 500 | rows per batch (bounds how long backups can be paused) |
| `StoreCleanupDryRun` | false | log what cleanup would delete, delete nothing |
| `StoreIncompleteFileDataGraceSec` | 86400 | age after which online vacuum treats incomplete file data as abandoned |
| `StoreDeletionLogRetentionSec` | 2592000 (30 days) | how long `catalogsync` has to consume deletions before they're pruned; `0` keeps them forever |
| `grpc_window_bytes` | 0 (gRPC's dynamic default) | fixed HTTP/2 flow-control window in bytes (65536 – 1073741824), also read by `brfs` and `rwfs`; opt-in, see [performance tuning](../PERFORMANCE.md#grpc_window_bytes-config--http2-flow-control-window-off-by-default) |

See [Backup Protocol](../protocols/backup.md) for the full RPC and lifecycle.

**Server configuration keys:**
- `JobTimeoutSec` — seconds of no activity before an in_progress backup job is marked failed *(default: 30)*

### list

List stored file data from the local SQLite store. Can run concurrently with a live server.

```bash
bwfs /home/user/backup list
bwfs /home/user/backup list myhost
bwfs /home/user/backup list myhost:/var/log
bwfs /home/user/backup list :/var/log
bwfs /home/user/backup list --output json
bwfs /home/user/backup list --filter nginx
```

**Positional:** `[[server_name:]path]` — optional filter, split on the first colon only:
- `myhost` — path-only filter (no colon → treated as path prefix)
- `myhost:/var/log` — exact hostname + path prefix
- `:/var/log` — path prefix with no hostname filter
- `myhost:C:/Users` — Windows paths with colons work correctly

| Flag | Default | Description |
|------|---------|-------------|
| `--output` | `table` | Output format: `table` or `json` |
| `--filter` | | Free-text substring filter on file path (composes with positional) |
| `--debug` | false | Enable debug logging |

**Table columns:** SOURCE, TYPE, PATH, TIMESTAMP, SIZE, CHUNKS, VERSIONS

**JSON fields:** `file_uuid`, `source`, `type`, `path`, `timestamp`, `size`, `chunks`, `versions`, `created_at`

### RestoreService

Provides file reconstruction via server-streaming gRPC RPC. Given a `file_uuid` (UUID from `ListService.ListFiles`), returns file metadata followed by all chunks in index order.

**Lookup semantics:** The handler first queries `file_data_records` by the `file_uuid` (column `uuid`) to obtain the `file_id` (fs:// path reference — the natural key, distinct from `file_uuid`), then uses that `file_id` to query `file_data_chunk_records` in index order. The file must be finalized (with a non-NULL checksum) before restore is allowed.

**Error codes:** Returns gRPC `codes.NotFound` when the `file_uuid` doesn't exist in `file_data_records` or the record is unfinalized. Returns gRPC `codes.Internal` when a database error occurs or a chunk cannot be read or fails hash verification — a chunk-read failure also marks that chunk corrupted server-side (see [backup protocol](../protocols/backup.md)) so it heals on the next backup. See [Restore Protocol](../protocols/restore.md) for detailed protocol flow and client-side verification responsibilities.

## Transport Security

All gRPC connections (`BackupService`, `ListService`, `RestoreService`) require mutual TLS.
`bwfs` loads its identity cert and the trusted CA from `MP_CONFIG_PATH/certs/{ca.crt,client.crt,client.key}`
(`MP_CONFIG_PATH` defaults to the binary's own directory). Any client presenting a cert signed
by that CA is trusted — there's no additional per-client allowlist. Missing or invalid certs
are a fatal startup error; there is no plaintext fallback. Cert issuance itself is out of scope
for `bwfs` — see the [control plane setup](../../deploy/control-plane/README.md) for how certs are provisioned today.

## Storage layout

Chunk bytes live in append-only **pack segments**, not one file per chunk:

```
<storage_path>/packs/0000000001.pack      # 10-digit segment id, rotated at 256 MiB
<storage_path>/<sqlite database>          # metadata and the chunk index
```

A segment is the 8-byte magic `MPKSEG01` followed by records back to back; each record is
`MPKR | data length (uint32 LE) | BLAKE3-256 of data | data` (40-byte header, data up to 16 MiB).
`chunk_records` carries each chunk's `segment` and `offset`, so a read is one `pread`. This
replaces roughly one file and inode per 64 KB chunk (about 16M files per TB) with a handful of
large files. The package is `src/storage/pack`; `src/storage/filesystem` owns the SQLite index.

A store written by the previous layout (it has a `chunks/` directory) is rejected at open with
an error. There is no migration: start a fresh store.

### Durability contract

- Acknowledging a chunk to `brfs` happens **before** its bytes are fsynced. Durability is paid
  once per barrier (group commit), not per chunk.
- The barrier is file finalize. `FinalizeFileData` fsyncs the active segment, then commits all
  pending chunk rows and links in one SQLite transaction, and only then sets the file's checksum.
  A file is therefore "complete" (skippable by later backups) only after its bytes are durable and
  indexed. Other barriers: 32 MiB of unflushed chunk bytes, vacuum, marking a chunk corrupt, and
  `Close`.
- Index rows only ever point at durable bytes. New rows are held in memory until after the fsync.
  A crash leaves at worst unindexed dead bytes in a segment, never a row pointing at missing data.
- An fsync failure is sticky: the log refuses further writes (the kernel may have dropped the dirty
  pages, so a retry could falsely succeed). Requests fail; restarting the server recovers.
- After each sync `posix_fadvise(DONTNEED)` is applied to the synced range so backup writes do not
  evict the page cache.

### Recovery and verified reads

- On open the **last** segment is scanned record by record, checking each BLAKE3 hash; a torn or
  corrupt tail is truncated at the first bad record (earlier segments were fsynced when sealed). A
  last segment that is only a partial magic (crash during creation) is recreated. A bad magic on a
  segment that holds data refuses to open the store.
- Every read verifies the record header and BLAKE3 hash. A mismatch at restore or verify marks the
  chunk corrupt exactly as before (rows dropped, dependent `FileData` invalidated, healed by the next
  backup); the bytes stay behind as dead space. A read that finds its segment gone (compaction moved
  the chunk) re-locates the chunk once and retries.

### Vacuum and compaction

Vacuum first deletes orphan rows; deleted chunks become dead space inside segments. It then
reclaims that space per sealed segment: a segment with no live records is removed, and one that is
under 50% live is **compacted** (live records are read hash-verified, appended to the active
segment, synced, and their rows repointed in bounded batches; then the emptied segment is
removed). The active segment is never compacted or removed, and a crash during compaction leaves at
worst duplicate dead bytes. The result and the log line carry `segments_removed`,
`segments_compacted` (a compacted segment counts only as compacted) and `bytes_reclaimed`, which is
the physical size of removed segments minus the bytes copied during compaction.

## Platform-specific code

Chunk storage is Linux only (`src/storage/pack`, which uses `posix_fadvise`). The exclusive store
lock is `src/storage/filesystem/storelock_linux.go` (`flock`); disk usage for status reports is
`src/cmd/bwfs/diskspace_{linux,windows}.go`.

## Building

```bash
make build
```

### Status reporting

When started with `--policy-id <id>` (which `agent` always passes) and `api_server_host` is
configured, `bwfs server` posts its status to `api-server` once a minute: serving state, disk
total/used of the filesystem holding the store, open gRPC connections, in-progress backup jobs and
uptime. It is best-effort — an unreachable `api-server` is logged once and retried next tick, and
never affects backups. See [Storage Status Protocol](../protocols/storagestatus.md).

| Flag | Default | Description |
|------|---------|-------------|
| `--policy-id` | empty | Storage policy ID; empty disables status reporting |

## See Also

- [brfs](./brfs.md) — Backup Reader for File System
- [rwfs](./rwfs.md) — Remote list/restore client for this server
- [backup protocol](../protocols/backup.md) — brfs → bwfs wire protocol
- [list protocol](../protocols/list.md) — rwfs → bwfs list subprotocol
- [restore protocol](../protocols/restore.md) — rwfs → bwfs restore/verify subprotocol
- [storage status protocol](../protocols/storagestatus.md) — bwfs → api-server status reports
- [Architecture](../ARCHITECTURE.md) — System overview
