# brfs (Backup Reader from File System)

Backup tool for reading files from a source directory and sending them to a backup writer.

## Purpose

Reads all files from a specified directory and transmits them to `bw*` (backup writer) via:
- Network connection (for remote backup writers)  
- Unix socket (for local backup writers)

## Usage

```bash
brfs <source_folder> --destination <host:port>
```

## Arguments and Flags

- `<source_folder>` - Directory to backup **(required)**
- `--destination <host:port>` - Writer destination address **(required)**
- `--streams <number>` - Number of concurrent streams *(default: config->default_streams)*
- `--window <number>` - Max chunks in flight per stream; `1` sends one chunk at a time *(default: config->`default_window`, 8; built-in 1 if the config sets none)*
- `--job-id <id>` - Backup job ID *(default: auto-generated UUID)*
- `--include <patterns>` - Comma-separated glob patterns; only matching files are backed up *(default: `*`)*
- `--exclude <patterns>` - Comma-separated glob patterns; matching files and directories are skipped *(default: none)*
- `--retention-file <path>` - JSON retention matrix resolved by `agent`; each file's `expire_at` is stamped from it *(default: none — no `expire_at` is sent)*
- `--debug` - Enable debug logging
- `--quiet` - Suppress stdout logging

Each `brfs` run is a distinct backup job. If `--job-id` is omitted, `brfs` generates a UUID at
startup; passing one explicitly is useful for correlating a run with an external scheduler's own
job identifier. The ID is sent to `bwfs` as gRPC metadata on every stream this run opens — see
[backup protocol](../protocols/backup.md) for the wire-level detail.

After all of its streams close, `brfs` computes a SHA256 over the sorted IDs of every file it
believes it sent successfully and submits it to `bwfs` via the `BackupCommit` RPC, retrying a few
times with backoff on transport error. `brfs` exits non-zero if the commit call ultimately fails to
reach the server, or if the server reports the hash didn't match what it actually received — see
[Backup Protocol](../protocols/backup.md) for the full mechanism.

## Examples

```bash
# Backup to remote writer
brfs /home/user/documents --destination 192.168.1.100:8080

# Backup to local writer with debug
brfs /var/log --destination localhost:8080 --debug --streams 5
```

`agent`'s policy-driven backup tasks (see [agent](./agent.md#policy-driven-backup-execution)) use
the job-id convention `backup:<policy-name>:<slug-of-path>:<short-filter-id>:<unix-timestamp>` — useful when grepping
`bwfs`'s job history for which policy produced a given run.

## Sliding window

Each stream keeps up to `--window` chunks in flight at once instead of waiting for a reply to every
chunk before sending the next. A chunk occupies a window slot from the moment its hash is sent until
`bwfs` either says it already has the chunk or confirms the chunk's data; the slot is then free for
the next chunk. A round trip is thereby shared by the whole window rather than paid per chunk, which
matters on high-latency links and for incremental backups, where almost every chunk is already stored
and only hashes cross the wire. `--window 1` is the previous stop-and-wait behavior.

Sizing: the window needs to cover the bandwidth-delay product, `RTT × throughput / chunk size`
(chunks are 64 KB). On a LAN `1`–`2` is enough; across a WAN with tens of milliseconds of RTT,
`8` or more helps, and a mostly-deduplicated backup (tiny hash-only requests) benefits from a
larger window than one transferring new data. Past the knee a larger window only costs memory
(`--streams × --window × 64 KB` of chunks held) and queueing delay. The window is per stream and
within one file; it drains at the end of each file, so many tiny files see little gain. Set the
site default with `default_window` in `local.conf`; `--window` overrides it per run.

The window is static. Sizing it automatically from measured RTT and throughput is tracked in the
[backlog](../../backlog.md#adaptive-brfs-window).

Replies from `bwfs` arrive in the order requests were sent, so `brfs` matches them by position and
treats any other reply as an error. A failed chunk fails its file; replies still in flight for it are
discarded when the stream moves on to the next file. A stream that fails (for example a rejected
connection) fails every file queued on it. `bwfs` rebuilds chunk order itself — see
[Backup Protocol](../protocols/backup.md#in-flight-chunks-sliding-window).

## Filtering

A pattern with no `/` matches a file's basename at any depth (`*.tmp` excludes every `.tmp` file
anywhere under the source folder); a pattern containing `/` matches the path relative to the
source folder exactly. `--exclude` is checked first: a directory that matches is pruned along with
everything beneath it; a file that matches is skipped. `--include` is then checked for files only
— directories are never filtered by it, so traversal always continues into non-excluded
directories.

```bash
# Back up only .sql files, skipping anything under a "tmp" directory
brfs /var/lib/postgres --destination localhost:8080 --include "*.sql" --exclude "tmp"
```

## Retention

When `agent` launches `brfs` for a scheduled backup it passes `--retention-file`, a JSON array of
rows `{"prefix": "<root-relative dir>", "include": ["<glob>"...], "keep_seconds": N}` in priority
order. `brfs` validates and compiles it once at start, shared read-only by all `--streams`
workers, and for each file takes the first row whose `prefix` matches on a path-segment boundary
(`""` matches everything) and whose optional `include` globs match (same glob semantics as
[Filtering](#filtering)): `expire_at = now + keep_seconds`, or `0` when `keep_seconds` is `0`
(never expire). It is sent with the file's metadata (see
[Backup Protocol](../protocols/backup.md#retention-expiry-expire_at)).

An unreadable or invalid retention file makes `brfs` exit non-zero before connecting, so a job
never silently runs without the retention it was given. Without `--retention-file` (for example a
hand-run `brfs`) no `expire_at` is sent.

## Protocol

Communicates with [bwfs](./bwfs.md) (backup writer) using the protocol specified in [doc/protocols/backup.md](../protocols/backup.md).

## Transport Security

The connection to `bwfs` is mutually authenticated TLS. `brfs` loads its identity cert and the
trusted CA from `MP_CONFIG_PATH/certs/{ca.crt,client.crt,client.key}` (`MP_CONFIG_PATH` defaults
to the binary's own directory). Missing or invalid certs are a fatal error before any backup
traffic is sent. When `--destination` is a loopback address (`localhost`, `127.0.0.1`, `::1`),
hostname verification against the server cert's SAN is skipped — the cert must still chain to
the trusted CA.

## Building

```bash
make build
```

## See Also

- [bwfs](./bwfs.md) - Backup Writer for File System
- [doc/protocols/backup.md](../protocols/backup.md) - Communication protocol
- [Architecture](../ARCHITECTURE.md) - System overview