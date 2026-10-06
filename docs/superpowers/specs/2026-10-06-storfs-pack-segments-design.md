# storfs: pack segments and group-commit durability

Date: 2026-10-06. Branch: `storfs-reliability-perf`. Component: `src/storage/filesystem` (+ new `src/storage/pack`).

## Problem

Chunk data lives one file per chunk (`chunks/aa/bb/<hash>`), average 64 KB. Observed problems:

**Reliability**
- `StoreChunk` never fsyncs (its comment claims it does). A crash can leave a short or empty file under its
  final name with a committed DB row; `ChunkExists` is an `os.Stat`, so later backups dedup against the bad
  chunk until a restore fails.
- `ReadChunk` does not verify the BLAKE3 hash: bit rot is restored silently.
- No ordering guarantee between chunk bytes becoming durable and the DB rows that make them visible.

**Performance / fragmentation**
- Per chunk: stat, MkdirAll, temp write, rename, plus two single-statement SQLite commits (chunk row, link
  row), each an fsync under `synchronous=FULL`. About 16M files per TB; vacuum unlinks them one by one.
- Backup writes pollute the page cache; every dedup check hits the filesystem.

## Decisions

- Linux only. No migration: stores written by the old format are rejected with a clear error (no silent
  misbehaviour).
- Chunk bytes go into **append-only segment files** (`packs/NNNNNNNNNN.pack`, default 256 MiB). Chunk
  metadata stays in SQLite (`chunk_records` gains `segment`, `offset`).
- **Group commit.** Durability is paid once per barrier, not per chunk. The barrier is
  `FinalizeFileData` (plus a size trigger and `Close`). A file only gets its checksum (becomes "complete",
  skippable by later backups) after its chunk bytes are fsynced and its index rows are committed.
- **Index rows only ever point at durable bytes.** New chunk rows and links are held in memory ("pending")
  and written in one transaction *after* the segment fsync. A crash can therefore leave unindexed bytes
  (dead space) but never a row pointing at missing data.
- **Integrity is the BLAKE3 hash.** Every record carries its hash; reads and recovery verify it. No second
  checksum.
- `fsync` failure is sticky: after one failed sync the log refuses further writes (Linux may have dropped the
  dirty pages; retrying would report false success). The server fails requests and recovers by restart.
- Deliberately not done (YAGNI, revisit with mpbench numbers): BLOB hash keys / integer chunk ids, an
  in-memory known-hash set or LRU data cache, an fd cache, `fallocate`, a `scrub` command, config knobs.

## Unit 1: `storage/pack` (no SQLite dependency)

On-disk format. Segment file = 8-byte magic `MPKSEG01`, then records back to back:

```
magic "MPKR" (4) | data length uint32 LE (4) | BLAKE3-256 of data (32) | data
```

Header is 40 bytes. `Location{Segment uint32, Offset int64, Size uint32}`: `Offset` is the record start,
`Size` is the data length. Maximum record data 16 MiB (a corrupt length cannot trigger a huge allocation).

API:

```go
type Options struct{ SegmentSize int64 } // default 256 MiB

func Open(dir string, opts Options) (*Log, error) // creates dir, recovers, opens the active segment
func (l *Log) Append(hash [32]byte, data []byte) (Location, error)
func (l *Log) Sync() error                         // everything appended so far is durable on return
func (l *Log) ActiveSegment() uint32
func (l *Log) Close() error

func Read(dir string, loc Location, hash [32]byte) ([]byte, error) // verifies header and hash
func Segments(dir string) ([]SegmentInfo, error)                   // {ID, Size}, ascending
func RemoveSegment(dir string, id uint32) error                    // + directory fsync
var ErrCorrupt error; var ErrSegmentMissing error
```

Behaviour:
- `Append` writes under one mutex with `WriteAt`. If the record does not fit in the active segment (and the
  segment holds data) it rotates: fsync the old segment, create the next (`O_EXCL`), fsync the directory.
- `Sync` coalesces: concurrent callers share one `fsync`; a caller whose bytes a finished sync already
  covers returns immediately. After the `fsync` it calls `posix_fadvise(DONTNEED)` on the synced range so
  backup writes do not evict the page cache.
- `Open` recovery: scan the **last** segment from the start, verifying each record's hash; stop at the first
  bad or short record and `Truncate` the file there (then fsync). Earlier segments were fsynced when sealed.
  A last segment whose header is bad/short (crash during creation) is recreated. Stray non-`.pack` files are
  ignored.
- `Read` uses open + `ReadAt` + close per call (no fd cache); `fs.ErrNotExist` maps to `ErrSegmentMissing`;
  length/magic/hash mismatch or a short read maps to `ErrCorrupt`.

## Unit 2: `filesystem.Store` integration

Schema: `ChunkRecord{Hash, Size, Segment, Offset, CreatedAt}` (hash stays hex text; links keep their schema,
so `restoreserver`/`list`/`resolverestorefiles` queries are untouched).

Store state: `log *pack.Log` (nil for `NewReadOnly`), and a `pending` struct under a mutex: new chunk
locations, new links, and bytes since last flush.

- `StoreChunk`: verify hash; if known (pending or DB) return; `log.Append`; record in pending; if pending bytes
  >= 32 MiB, `flush`.
- `LinkChunkToFileData`: add to pending links.
- `ChunkExists`: pending, then DB `SELECT 1 ... WHERE hash=?`.
- `flush` (serialized by its own mutex): snapshot pending; `log.Sync()`; one transaction inserting chunk rows
  then links (`OnConflict DoNothing`); then remove the snapshot from pending. Lookups consult pending until
  the transaction has committed, so there is no visibility gap.
- `FinalizeFileData`: `flush` first, then set checksum and chunk_count (as today).
- `ReadChunk`: locate (pending, then DB) -> `pack.Read`. On `ErrSegmentMissing` re-locate once and retry
  (compaction may have moved the chunk). Hash mismatch returns `ErrCorrupt`-wrapped error, which
  `restoreserver` already treats as "mark corrupted".
- `MarkChunkCorrupted`: flush, then one transaction removes links, chunk row, dependent file data. No file to
  unlink; the bytes become dead space.
- `Close`: flush (best effort, error returned), close log, close DB.
- `inBatch` and `Vacuum` flush first. With the exclusive guard held no handler is mid-call, so after the flush
  the DB is fully consistent and "chunk with no link" really means orphan.
- `New` rejects a store with a legacy `chunks/` directory.

## Unit 3: reclaiming space (vacuum and compaction)

Vacuum no longer unlinks per chunk; deleting orphan chunk rows makes their bytes dead space. Reclaiming:

- Per segment: `live = SUM(header + size)` over its rows, `size` = file size. Fully dead sealed segments are
  removed. Sealed segments with `live/size < 0.5` are **compacted**: in bounded batches (`batchSize` rows,
  each batch its own `inBatch` transaction) live records are read (hash-verified), appended to the active
  segment, rows' `segment`/`offset` updated, `log.Sync()` before commit; when a segment has no rows left it
  is removed. A crash leaves at worst duplicate dead bytes, or an empty segment file the next vacuum removes.
- The active segment is never compacted or removed.
- `Vacuum` (startup) additionally removes segment files that have no rows and are not active (crash leftovers),
  and replaces the directory walk. `VacuumOnline` does the same compaction/removal loop, honouring `ctx`.
- `VacuumResult` gains `SegmentsRemoved` and `SegmentsCompacted`; `bwfs` logs them. `BytesReclaimed` is the
  physical bytes freed by removed segments minus bytes copied.
- A compaction record that fails verification is treated like a corrupt chunk: its row is dropped through the
  `MarkChunkCorrupted` logic, and compaction continues.

## Cache and memory

- Page cache: `DONTNEED` after each sync (writes); reads rely on the kernel (`pread` of contiguous records).
- Heap: no per-chunk temp files; reads allocate exactly one buffer of the record size. Pending state holds
  locations only (about 100 bytes per chunk, bounded by the 32 MiB flush trigger, so about 500 entries per MiB
  of chunks, a few MB).
- Bounded in-flight work is already provided by brfs `streams x window`; the 32 MiB trigger bounds
  un-fsynced data in the page cache.

## Testing (TDD, each behaviour test-first)

- pack: round trip; rotation; reopen continues in the same segment; torn tail (truncate mid-record, flip a
  byte in the last record, garbage appended) recovered; bad segment header recreated; `Read` detects flipped
  byte, wrong hash, short file, missing segment; concurrent appends + syncs under `-race`; sticky sync error
  (injectable sync func).
- store: existing `store_test`/`gc_test`/bwfs integration tests keep passing (adapted only where they assert
  loose files); new: crash simulation (copy store dir after StoreChunk without flush, reopen: no dangling
  rows, file not complete); finalize durability ordering; pending visible to exists/read; read detects
  corruption and restore path marks chunk; compaction moves chunks and old segment disappears; concurrent
  compaction + reads retry; legacy store rejected; `NewReadOnly` reads packed chunks.
- mpbench before/after (baseline built from the pre-change tree, same seed/profile).

## Docs

`docs/components/bwfs.md` (storage layout, durability contract, vacuum/compaction), `docs/PERFORMANCE.md`
(measured effect), `docs/ARCHITECTURE.md` if it names the chunk layout, `src/storage/CLAUDE.md`,
`CHANGELOG.md`. No gRPC protocol changes.
