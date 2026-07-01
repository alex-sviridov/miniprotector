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
| `--streams` | 4 | Concurrent verification workers |
| `--retries` | 3 | Max retry attempts per file on stream error |
| `--quiet` | false | Suppress per-file success lines (warnings and summary always shown) |

## Transport Security

Connections to `bwfs` (both `list` and `verify`) are mutually authenticated TLS. `rwfs` loads
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
- [Architecture](../ARCHITECTURE.md) — System overview
