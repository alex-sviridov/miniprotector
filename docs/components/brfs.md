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
- `--job-id <id>` - Backup job ID *(default: auto-generated UUID)*
- `--include <patterns>` - Comma-separated glob patterns; only matching files are backed up *(default: `*`)*
- `--exclude <patterns>` - Comma-separated glob patterns; matching files and directories are skipped *(default: none)*
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