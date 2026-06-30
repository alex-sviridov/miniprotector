# rwfs verify — Design Spec

Date: 2026-06-30

## Overview

`rwfs verify` is a new subcommand that performs integrity verification of backed-up files
against a remote `bwfs` server. It reuses the same filters as `rwfs list`, fetches chunks
via a new `RestoreService` gRPC protocol, and verifies both per-chunk BLAKE3 integrity and
whole-file CRC32 integrity — without writing anything to disk. Verification is entirely the
responsibility of `rwfs`; `bwfs` serves data as it would for a real restore and knows
nothing about whether the caller is verifying or restoring.

Priorities: **reliability first, simplicity second, performance third.**

---

## 1. Restore Protocol (`src/api/restore.proto`)

New `restore.proto` following the same pattern as `list.proto`. `RestoreService` is
registered on the same gRPC server as `BackupService` and `ListService` — no new port,
no new process.

```proto
syntax = "proto3";
package restoreservice;
option go_package = "./proto";

service RestoreService {
  rpc RestoreFile(RestoreRequest) returns (stream RestoreEvent);
}

message RestoreRequest {
  string file_data_id = 1;  // FileDataRecord.ID from ListResponse
}

message RestoreEvent {
  oneof payload {
    RestoreFileMeta meta  = 1;  // first event only
    RestoreChunk    chunk = 2;  // subsequent events
  }
}

message RestoreFileMeta {
  int64  size              = 1;
  int32  chunk_count       = 2;
  bytes  expected_checksum = 3;  // 4-byte big-endian CRC32 from FileDataRecord.Checksum
}

message RestoreChunk {
  int64 index = 1;
  bytes hash  = 2;  // BLAKE3 hash from DB — rwfs verifies blake3.Sum256(data) == hash
  bytes data  = 3;
  bool  eof   = 4;
}
```

The `hash` field is served from `FileDataChunkRecord.ChunkHash` so rwfs can verify chunk
integrity without prior knowledge of the expected hash. bwfs does not re-verify BLAKE3
before sending — it trusts its own storage. If a chunk file is missing or unreadable,
bwfs returns a gRPC `Internal` error and the stream terminates.

---

## 2. bwfs Restore Server (`src/cmd/bwfs/restoreserver.go`)

Read-only handler registered alongside `BackupService` and `ListService` in `server.go`.

```
RestoreFile(req, stream):
  1. Look up FileDataRecord by file_data_id → gRPC NotFound if missing
  2. Send RestoreFileMeta{size, chunk_count, expected_checksum}
  3. For each FileDataChunkRecord (ordered by index ASC):
       a. ReadChunk(hash) from filesystem
       b. Send RestoreChunk{index, hash, data, eof=(last chunk)}
       c. ReadChunk failure → return gRPC Internal error
```

bwfs streams one chunk at a time and holds no more than one chunk in memory per active
stream. It is unaware of retry logic or verification semantics.

---

## 3. rwfs verify Command

### Arguments

```
rwfs verify [[server_name:]path] <host:port> [--filter <substr>] [--streams <int>] [--retries <int>] [--quiet]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--streams` | 4 | Number of concurrent file verification workers |
| `--retries` | 3 | Max RestoreFile retry attempts per file on stream error |
| `--quiet` | false | Suppress per-file success lines; only print warnings and summary |

`[server_name:]path` positional defaults to `common.GetHostname()` when omitted (same
behaviour as `rwfs list`). `--filter` is a free-text substring filter, identical to
`rwfs list`.

### Flow

```
1. Dial bwfs, create ListServiceClient + RestoreServiceClient (shared connection)
2. Call ListService.ListFiles with server_name / path / filter
3. Filter rows: skip type != 'f' or size == 0 (no chunks to verify)
4. Push remaining FileRows into a buffered work channel (capacity = len(rows))
5. Close work channel
6. Start N worker goroutines (--streams); each loops:
     a. Pull FileRow from work channel (exit loop when channel drained)
     b. verifyFile(row) with up to --retries attempts on stream error
     c. Send VerifyResult{row, ok, reason, chunkIndex} to buffered results channel
7. After all workers done, close results channel
8. Collector (main goroutine) reads results:
     - Log per-file line (slog.Info on pass, slog.Warn on fail)
     - Increment counters
9. Log summary line
10. Exit non-zero if any failure
```

### Per-File Verification (`verifyFile`)

```
1. ctx, cancel := context.WithCancel(parent); defer cancel()
2. stream = client.RestoreFile(ctx, &RestoreRequest{FileDataId: row.FileDataID})
3. First event must be RestoreFileMeta → record expected_checksum, chunk_count
4. hasher = crc32.NewIEEE()
5. For each subsequent RestoreChunk event:
     a. computed = blake3.Sum256(chunk.Data)
     b. if computed != chunk.Hash:
          → cancel(), return VerifyResult{fail, reason:"blake3_mismatch", chunkIndex:chunk.Index}
     c. feedChecksum(hasher, crc32.ChecksumIEEE(chunk.Data))
     d. if chunk.Eof: break
6. var buf [4]byte; binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
7. if buf[:] != meta.expected_checksum:
     → return VerifyResult{fail, reason:"crc_mismatch"}
8. return VerifyResult{ok}
```

`feedChecksum` (moved to `common/checksum`) feeds a chunk's CRC32 as 4-byte big-endian
into the incremental file hasher. Both `bwfs/handler.go` and `rwfs/verify.go` import it
from there. The final 4-byte value must match `FileDataRecord.Checksum`.

On stream error (not BLAKE3/CRC failure), the worker retries the full `RestoreFile` call
up to `--retries` times. Retry resets the hasher and starts from chunk 0.

### Structured Logging

All output uses `slog` (same logger wired by `main.go`).

```
# Per-file success (suppressed by --quiet):
slog.Info("verified",
    "source", row.Source, "path", row.Path,
    "file_data_id", row.FileDataID,
    "chunks", meta.ChunkCount, "size", meta.Size)

# Per-file failure (always printed):
slog.Warn("verification failed",
    "source", row.Source, "path", row.Path,
    "file_data_id", row.FileDataID,
    "reason", "crc_mismatch" | "blake3_mismatch" | "stream_error",
    "chunk_index", idx)   // omitted for crc_mismatch and stream_error

# Summary (always printed):
slog.Info("summary", "verified", total, "warnings", warnings)
```

Exit code: 0 if `warnings == 0`, 1 otherwise.

### Memory and Resource Safety

- **Chunk data**: each `RestoreChunk.Data` (64KB) is processed (BLAKE3 + CRC32) and goes
  out of scope at the next loop iteration. Never accumulated. Peak memory per worker: ~64KB.
- **Stream cleanup**: each file verification runs under a `context.WithCancel`. The deferred
  `cancel()` ensures the gRPC stream is torn down on early exit (BLAKE3 failure, signal,
  parent context cancellation).
- **Shared connection**: all N workers share one `*grpc.ClientConn` (HTTP/2 multiplexes
  streams). Not one connection per worker.
- **Buffered results channel**: sized to total file count so workers never block waiting
  for the collector, avoiding goroutine stalls that would hold open gRPC streams.

---

## 4. Files Changed / Created

| Path | Change |
|------|--------|
| `src/api/restore.proto` | New |
| `src/api/restore.pb.go`, `restore_grpc.pb.go` | Generated |
| `src/common/checksum/checksum.go` | New — extract `feedChecksum` here so both bwfs and rwfs can import it |
| `src/cmd/bwfs/handler.go` | Remove local `feedChecksum`; import from `common/checksum` |
| `src/cmd/bwfs/restoreserver.go` | New — RestoreService handler |
| `src/cmd/bwfs/server.go` | Register RestoreService |
| `src/cmd/rwfs/verify.go` | New — runVerify, verifyFile, worker pool |
| `src/cmd/rwfs/arguments.go` | Add verify subcommand and its flags |
| `src/cmd/rwfs/main.go` | Wire verify subcommand |
| `docs/protocols/restore.md` | New protocol doc |
| `docs/components/rwfs.md` | Document verify subcommand |
| `docs/components/bwfs.md` | Document RestoreService |
| `README.md` | Add verify to quick-start, link restore protocol |
| `docs/ARCHITECTURE.md` | rwfs restore flow is no longer "planned" |

---

## 5. Testing

Two new e2e test cases alongside existing backup/list tests:

1. **Happy path**: backup a set of files, run `rwfs verify`, assert all files pass, exit
   code 0, summary shows `warnings=0`.

2. **Corruption detection**: backup a file, corrupt one chunk on disk (flip bytes in the
   chunk file under `chunks/`), run `rwfs verify`, assert a WARNING is logged for that
   file with `reason=blake3_mismatch`, exit code non-zero.

No new unit tests are required: `feedChecksum` and CRC32 accumulation are already
exercised by the backup e2e path.

---

## Key Design Decisions

**Why unary-per-file server streaming instead of bidi streaming?**
Reliability first: one stream per file means failure isolation is clear — a stream error
affects one file, not the whole batch. The worker pool pattern is simple and correct.

**Why file-level retry instead of chunk-level resume?**
Simplicity. For 64KB chunks and thousands of files, re-receiving a partial file from chunk 0
is negligible overhead. Resume-capable retry requires storing per-chunk CRC32 state and
adds complexity without meaningful reliability benefit.

**Why does bwfs not validate BLAKE3 before sending?**
bwfs trusts its own storage. Sending the hash alongside the data lets rwfs detect
corruption that occurred after the chunk was stored — exactly what `rwfs verify` exists
to catch.

**Why is per-chunk CRC32 not stored in the DB?**
The existing design stores only the file-level CRC32 (composed from chunk CRC32s via
`feedChecksum`). Verification replicates the backup-time accumulation on the received
chunks to reproduce this value without needing per-chunk CRC32 persistence.

**Why skip non-'f' types and size-0 files?**
Only regular files with content have chunks stored in bwfs. Directories, symlinks, and
empty files have no `FileDataChunkRecord` rows — there is nothing to verify.
