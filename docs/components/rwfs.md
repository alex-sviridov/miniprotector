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
| `--streams` | 4 | Concurrent verification workers |
| `--retries` | 3 | Max retry attempts per file on stream error |
| `--quiet` | false | Suppress per-file success lines (warnings and summary always shown) |
| `--job-id` | auto-generated UUID | Correlation ID for this invocation's logs; also sent to `bwfs` as `job-id` gRPC metadata |

Every line `rwfs` logs carries `job_id`, and the same value rides each `ListFiles`/`RestoreFile`
call as outgoing `job-id` gRPC metadata, so a run's local log and `bwfs`'s server-side log for it
can be joined — the same convention `brfs`, `certclient`, and `policyclient` follow. `agent` always
passes an explicit `--job-id` for its restore-verification tasks (see
[agent](./agent.md#policy-driven-restore-verification)); a human running `rwfs` by hand gets a
generated UUID.

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
