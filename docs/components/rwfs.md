# rwfs (Restore Writer for File System)

Remote restore client — connects to a running `bwfs` server over gRPC and queries its file listings.

## Usage

```
rwfs <command> [flags]
```

## Commands

### list

List files stored on a remote `bwfs` server.

```bash
# List all files on the server
rwfs list localhost:8080

# Filter by source hostname (defaults to local hostname when omitted)
rwfs list myhost:/var/log localhost:8080

# Filter by path prefix only (leading colon = no hostname filter)
rwfs list :/var/log localhost:8080

# JSON output
rwfs list localhost:8080 --output json

# Combine positional filter with free-text filter
rwfs list myhost:/var/log localhost:8080 --filter nginx
```

**Positionals:**
- `[[server_name:]path]` — optional source/path filter (split on first colon)
- `<bwfs_host:port>` — address of the `bwfs` server **(required)**

When `server_name` is omitted from the filter (i.e. no positional, or positional has no colon), `rwfs` defaults to the local hostname — matching the source used by `brfs` running on the same machine.

| Flag | Default | Description |
|------|---------|-------------|
| `--output` | `table` | Output format: `table` or `json` |
| `--filter` | | Free-text substring filter on file path |
| `--debug` | false | Enable debug logging |
| `--quiet` | false | Suppress console logging |
| `--job-id` | auto-generated UUID | Correlation ID for this invocation's logs; also sent to `bwfs` as `job-id` gRPC metadata |

**Table columns:** SOURCE, TYPE, PATH, TIMESTAMP, SIZE, CHUNKS, VERSIONS

**JSON fields:** `file_uuid`, `source`, `type`, `path`, `timestamp`, `size`, `chunks`, `versions`, `created_at`

The JSON schema is identical to `bwfs list --output json`, so the same parsers work for both local and remote queries.

## verify

Verifies the integrity of files stored on a remote `bwfs` server. Fetches each file's
chunks via the [Restore Protocol](../protocols/restore.md) and re-verifies both per-chunk
BLAKE3 hashes and the whole-file CRC32 checksum — without writing to disk.

```bash
# Verify all files backed up from the current host
rwfs verify localhost:8080

# Verify files from a specific host and path prefix
rwfs verify myhost:/var/log localhost:8080

# Verify with 8 concurrent streams, suppress per-file success lines
rwfs verify localhost:8080 --streams 8 --quiet
```

Exits 0 if all files pass. Exits 1 if any file fails (BLAKE3 mismatch, CRC32 mismatch,
or stream error after retries). Per-file results and a summary are written via `slog`.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--filter` | | Substring filter on file path |
| `--streams` | config `default_streams` (4 if unset) | Concurrent verification workers |
| `--retries` | `RwfsRetries` config (default 3) | Max retry attempts per file on stream error |
| `--quiet` | false | Suppress per-file success lines (warnings and summary always shown) |
| `--job-id` | auto-generated UUID | Correlation ID for this invocation's logs; also sent to `bwfs` as `job-id` gRPC metadata |

Every line `rwfs` logs carries `job_id`, and the same value rides each `ListFiles`/`RestoreFile`
call as outgoing `job-id` gRPC metadata, so a run's local log and `bwfs`'s server-side log for it
can be joined — the same convention `brfs`, `certclient`, and `policyclient` follow. `agent` always
passes an explicit `--job-id` for its restore-verification tasks (see
[agent](./agent.md#policy-driven-restore-verification)); a human running `rwfs` by hand gets a
generated UUID.

`ResolveRestoreFiles` (used by `--rules-stdin`), `ListFiles` (used by `list` and by `verify`'s plain
path), and each per-file `RestoreFile` stream are all protected by the same idle-timeout watchdog
(60s, fixed): a stream that's actively producing data is never penalized for running long, but one
that goes idle that long is cancelled rather than hanging forever. The window measures stream
inactivity only — time spent handing a received row to a busy consumer (a saturated worker pool)
is explicitly excluded, so worker backpressure can never be mistaken for a stalled server.
Both `verify` and `restore` share one retry implementation (`withRetry`, `retry.go`) that waits
between retry attempts (capped, doubling backoff starting at 500ms) instead of retrying
immediately, so a struggling `bwfs` isn't hammered -- `restore` retries only network/RPC-facing
stream errors; an integrity mismatch or a local destination-side problem (existing directory,
disk error) still fails immediately, no retry, exactly as before. See
[Design: Restore Per-File Retry](../superpowers/specs/2026-08-27-restore-retry-design.md). Internally,
`verify` uses a generic worker pool; `verify --rules-stdin` and `restore` share one resolved-row
source for `ResolveRestoreFiles` consumption (`list` uses neither) — none of this is CLI-visible,
but it's the reusable shape a future file-content restore phase is expected to build on. See
[Design: rwfs Reliability, Performance, and Reuse](../superpowers/specs/2026-08-16-rwfs-reliability-performance-design.md).

### Restore rule verification (`--rules-stdin`)

```bash
# Verify exactly the files a restore policy's rules select, piped as JSON
echo '{"rules":[{"host":"web-01","path":"/var/www/index.html","include":true}]}' \
  | rwfs verify localhost:8080 --rules-stdin
```

When set, `rwfs verify` reads `{"rules":[{"host","path","include"}, ...]}` from stdin -- the same
rule shape `policy-server`'s `"restore"` policy type and the web restore cart both already use
(host-agnostic folder rules have an empty/omitted `host`; longest-matching-rule wins, exactly like
`.gitignore`). What the flag changes is the *hostname default*: without it, an omitted
`server_name` defaults to the local hostname, which would be wrong for rules that are deliberately
host-agnostic, so with it the default is suppressed and every source host is in scope. Resolution
itself no longer goes through `ListFiles` at all: `rwfs verify --rules-stdin` builds one
`RestoreFileFilter` per included rule (host, path, and that rule's `not_before`/`not_after`
timeframe) and streams them through `bwfs`'s `ResolveRestoreFiles` RPC (see
[docs/protocols/list.md#resolverestorefiles](../protocols/list.md#resolverestorefiles)), which
resolves each filter against the store's indexed columns and streams back only the rows those
filters actually select -- rather than fetching the whole store and filtering client-side. The
positional `[[server_name:]path]` filter and `--filter` have no bearing on this call -- it is built
entirely from the piped rules -- so they're never combined with `--rules-stdin` in practice;
`agent` never sets either one.

An empty rule set (`{"rules":[]}`, `{"rules":null}`, or `{}`) is rejected as an argument error
rather than treated as a no-op: it would select zero files and report success without having
verified anything, which a one-shot caller would record as permanently done.

A **file-level** rule (non-empty `host`, `include: true`) that matches no row within its timeframe
is reported as a verification failure -- it named one specific file (and, if a timeframe was given,
a specific window), and no version of it was found there. The logged `reason` distinguishes the two
causes: a rule that set `not_before` and/or `not_after` reports `no version in timeframe` (the file
may exist, just not in the requested window -- usually fixed by widening it), while a rule with no
timeframe at all reports `not found on this store` (the search covered all of history, so the file
is genuinely absent). A **folder-level** rule (empty `host`)
matching nothing is not a failure -- an empty (or fully-excluded) folder is a legitimate outcome.
"Matches nothing" is judged against every row `ResolveRestoreFiles` streams back for that rule's
filter, not just the chunk-verifiable subset: a zero-byte file or a directory row is *found* (and
simply not checksummed, there being nothing to checksum) rather than misreported as missing.

Used by `agent`'s restore-policy verification tasks (see
[agent](./agent.md#policy-driven-restore-verification)) — never combined with `--filter` or the
positional filter in that usage.

## restore

Resolves a restore policy's rules against a remote `bwfs` server's file listing, then restores it:
both phases are real, disk-mutating operations. Phase 1 creates every resolved directory (see [list
protocol](../protocols/list.md#directory-rows)) on the destination filesystem, parent before child.
Phase 2, once phase 1 has fully succeeded, fetches every resolved file's chunks via the [restore
protocol](../protocols/restore.md) and writes them to disk, verifying per-chunk BLAKE3 and the
whole-file CRC32 exactly as `rwfs verify` does. Requires `--rules-stdin` (the only way to select
anything; there is no plain-listing restore mode). See [Design: Restore File Content
Phase](../superpowers/specs/2026-08-17-restore-file-content-design.md) and [Design: Restore Directory
Structure Phase](../superpowers/specs/2026-08-16-restore-directory-structure-design.md).

```bash
# Resolve a restore policy's rules, create the resolved directory
# structure on disk, and write the resolved file content
echo '{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":"/data/photos_recovered"}]}' \
  | rwfs restore localhost:8080 --rules-stdin
```

For each resolved file, logs `source`, `path` (original), and `dest_path` (the `dest_path` rename
rule applied -- see [restore protocol](../protocols/restore.md)). Logs the run's `overwrite`
setting once at start; `overwrite` has no effect on phase 1 -- an existing directory is always
reused regardless of it; it governs phase 2 (file content), as described above.

### Flags

| Flag | Default | Description |
|------|---------|--------------|
| `--rules-stdin` | | **Required.** Read `{"rules":[...]}` from stdin -- same shape `verify --rules-stdin` uses. |
| `--overwrite` | false | A pre-existing destination file is skipped when false, overwritten when true. Has no effect on directories (always reused) or on a non-file occupying a destination path (always a hard error). |
| `--streams` | config `default_streams` (4 if unset) | Concurrent file restore workers (phase 2 only; phase 1's directory creation is sequential). Raising it hides per-file round trips on high-latency links; see [performance tuning](../PERFORMANCE.md) |
| `--retries` | `RwfsRetries` config (default 3) | Max retry attempts per file on stream error |
| `--quiet` | false | Suppress per-file resolved lines (warnings and summary always shown) |
| `--job-id` | auto-generated UUID | Correlation ID for this invocation's logs; also sent to `bwfs` as `job-id` gRPC metadata |

Exit code follows the same not-found rule `verify --rules-stdin` uses: a file-level rule matching no
row is a failure (non-zero exit); a folder-level rule matching nothing is not. A not-found failure
aborts before directory creation (phase 1) ever starts.

Phase 1 logs `creating restored directory structure` once at start, then either a `restored
directory structure created` summary (with `created`/`reused` counts) on full success, or a
`failed to create restored directory` error and an immediate abort on the first failure -- no
further directories are attempted, and the summary line is never reached. A pre-existing directory
is always reused, regardless of `--overwrite`; a pre-existing non-directory at the destination path
is always a hard error. Directories are created with `os.Mkdir`, not a recursive `MkdirAll`, so
the shallowest directory in any resolved set -- a folder rule's own `dest_path`, verbatim -- fails
immediately if its parent doesn't already exist on the destination host; that parent must be
created ahead of time.

Phase 2 (file content) runs only once phase 1 has fully succeeded. It logs `restoring file content`
once at start, fetches each resolved file's chunks via `RestoreFile` (concurrently, `--streams`
workers wide), and writes them to its `dest_path`-renamed destination -- verifying every chunk's
BLAKE3 hash and the whole-file CRC32 exactly as `rwfs verify` does. A stream error is retried first
(up to `--retries` times, see the flag above); a mismatch or a local disk-write error still aborts on
the first occurrence, with no retry. On the first terminal failure -- a mismatch or disk-write error,
or a stream error whose retries are exhausted -- every other in-flight file transfer is cancelled
immediately, the failing (partial) file is removed from disk, a `failed to restore file` error is
logged for it, and no summary line is logged -- the same abort convention phase 1 already uses. On
full success, a `restore complete` line reports `files_written`, `bytes_written`, and
`skipped` (files left untouched because they already existed and `--overwrite` was false). Per-file
success (`file verified` / `file skipped, already exists`) is logged at `Debug` level only -- pass
`--debug` to see it; it is not controlled by `--quiet`. Every created or overwritten file uses a
fixed default permission (`0o644`, directories use `0o755`) -- real captured-permission restore is
still unbuilt, for both files and directories.

### Write contract

Phase 2 never writes into a destination file directly. See [Design: rwfs Write-Path
Tuning](../superpowers/specs/2026-10-06-rwfs-write-tuning-design.md) for the measurements behind it.

- **Temp files.** Content goes to a hidden `.<name>.mptmp-<8 hex>` file in the destination directory
  (same filesystem, so the final rename is atomic). On Linux the file is preallocated with
  `fallocate`, so a full disk fails the file up front with `ENOSPC` instead of mid-stream (a
  filesystem without `fallocate` falls back to a plain truncate), and writeback is started
  (`sync_file_range`) as each buffer is written.
- **Batched durable commit.** Verified temp files are committed in batches, every
  `restore_commit_files` files or `restore_commit_bytes` bytes, and once more at the end of the run.
  A commit fsyncs each file, drops its page cache, renames it over the destination, then fsyncs each
  parent directory once. A file appears under its final name only once it is durable, and
  `files_written` / `bytes_written` count only committed files.
- **`--overwrite`** replaces the destination atomically by rename. The old file is never destroyed
  before the new content has been fully received and verified, so a failed or interrupted restore
  leaves the old file intact.
- **Mode.** A replaced file takes the standard `0o644` mode; previously an overwritten file kept its
  old mode.
- **Crashes.** After a crash only `.mptmp-*` files can be left behind, never a torn file under a final
  name. The next `rwfs restore` removes stale `.mptmp-*` files from the destination directories it
  touches, so skip-existing stays trustworthy. On any failure the run aborts and the uncommitted
  temp files are removed; already committed files stay.
- **Windows** uses a plain truncate for sizing and has no writeback, cache-drop or directory-sync
  hooks (file `Sync` maps to `FlushFileBuffers`).

Tuning keys (config only, no flags):

| Key | Default | Range | Meaning |
|-----|---------|-------|---------|
| `restore_commit_files` | `64` | 0-1024 | Files per commit batch. Pending files keep their descriptors open, hence the cap. `0` = commit after every file |
| `restore_commit_bytes` | `67108864` (64 MiB) | >= 0 | Bytes per commit batch. `0` = commit after every file |

The temp-file contract is not free: measured against the previous direct-write restore, it costs about
7-8% on small and mixed trees and nothing visible on large files. Committing after every file
(`restore_commit_files=0` or `1`) takes roughly 30-85% longer than the defaults on small and mixed trees (neutral on large, within noise); see [performance
tuning](../PERFORMANCE.md#restore-write-tuning).

## Transport Security

Connections to `bwfs` (`list`, `verify`, and `restore` -- all three dial through the same `connection.Connect`) are mutually authenticated TLS. `rwfs` loads
its identity cert and the trusted CA from `MP_CONFIG_PATH/certs/{ca.crt,client.crt,client.key}`
(`MP_CONFIG_PATH` defaults to the binary's own directory). Missing or invalid certs are a fatal
error before any query is sent. When the `bwfs_host:port` target's host is loopback (`localhost`,
`127.0.0.1`, `::1`), hostname verification against the server cert's SAN is skipped — the cert
must still chain to the trusted CA.

## Building

```bash
make build
```

## See Also

- [bwfs](./bwfs.md) — Backup Writer; the server `rwfs` connects to
- [brfs](./brfs.md) — Backup Reader for File System
- [list protocol](../protocols/list.md) — gRPC protocol `rwfs` uses to query `bwfs`
- [restore protocol](../protocols/restore.md) — gRPC protocol `rwfs verify` uses to verify stored files
- [Design: rwfs Write-Path Tuning](../superpowers/specs/2026-10-06-rwfs-write-tuning-design.md) — restore write contract and measurements
- [Architecture](../ARCHITECTURE.md) — System overview
