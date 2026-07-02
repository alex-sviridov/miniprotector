# bwfs (Backup Writer from File System)

Backup storage server — receives files from a backup reader and stores them on disk with deduplication.

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

On startup, before accepting connections, the server runs a vacuum pass over the store
(removes incomplete/orphaned `FileData`, orphaned chunk links, orphaned chunk records, and
orphaned chunk files) and logs the results. A vacuum failure is fatal — the server exits
rather than serving against a store it couldn't clean up.

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

**Error codes:** Returns gRPC `codes.NotFound` when the `file_uuid` doesn't exist in `file_data_records` or the record is unfinalized. Returns gRPC `codes.Internal` when a database error occurs or a chunk file cannot be read from disk — a chunk-read failure also marks that chunk corrupted server-side (see [backup protocol](../protocols/backup.md)) so it heals on the next backup. See [Restore Protocol](../protocols/restore.md) for detailed protocol flow and client-side verification responsibilities.

## Transport Security

All gRPC connections (`BackupService`, `ListService`, `RestoreService`) require mutual TLS.
`bwfs` loads its identity cert and the trusted CA from `MP_CONFIG_PATH/certs/{ca.crt,client.crt,client.key}`
(`MP_CONFIG_PATH` defaults to the binary's own directory). Any client presenting a cert signed
by that CA is trusted — there's no additional per-client allowlist. Missing or invalid certs
are a fatal startup error; there is no plaintext fallback. Cert issuance itself is out of scope
for `bwfs` — see the `ca/` step-ca setup for how certs are provisioned today.

## Building

```bash
make build
```

## See Also

- [brfs](./brfs.md) — Backup Reader for File System
- [rwfs](./rwfs.md) — Remote list/restore client for this server
- [backup protocol](../protocols/backup.md) — brfs → bwfs wire protocol
- [list protocol](../protocols/list.md) — rwfs → bwfs list subprotocol
- [restore protocol](../protocols/restore.md) — rwfs → bwfs restore/verify subprotocol
- [Architecture](../ARCHITECTURE.md) — System overview
