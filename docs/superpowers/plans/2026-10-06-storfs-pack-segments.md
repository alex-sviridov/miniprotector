# storfs pack segments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace one-file-per-chunk storage with append-only pack segments and group-commit durability, with hash-verified reads, crash recovery and segment compaction.

**Architecture:** New dependency-free package `storage/pack` owns the segment file format, append, fsync and recovery. `filesystem.Store` keeps chunk locations in SQLite, holds new rows in memory until the segment is fsynced, then commits them in one transaction. Vacuum deletes rows and compacts/removes segments.

**Tech Stack:** Go 1.26, `lukechampine.com/blake3`, `golang.org/x/sys/unix` (already in go.mod), GORM + modernc SQLite, Linux only.

**Spec:** `docs/superpowers/specs/2026-10-06-storfs-pack-segments-design.md` (read it first; it is the contract).

## Global Constraints

- Linux only; no Windows variants. Delete `writer_windows.go`, `storelock_windows.go` in `src/storage/filesystem` (writer_linux is replaced).
- No migration; legacy `chunks/` directory makes `New` return an error.
- Record = `"MPKR"(4) | len uint32 LE (4) | BLAKE3-256 (32) | data`; segment file starts with `MPKSEG01`; files named `%010d.pack` in `<store>/packs`; default segment size 256 MiB; max record data 16 MiB.
- Index rows must only ever point at fsynced bytes. fsync failure is sticky.
- TDD: every behaviour gets a failing test first. Run `cd src && go test -race ./storage/... ./cmd/bwfs/...` before each commit. `go vet ./...` clean.
- Style: plain Go, small functions, comments explain why. Match surrounding code. No new config keys. No gRPC changes.
- Commit trailer (every commit):
  `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr`.

---

### Task 1: `storage/pack` package

**Files:**
- Create: `src/storage/pack/pack.go` (format, `Location`, `Read`, `Segments`, `RemoveSegment`, errors), `src/storage/pack/log.go` (`Log`, `Open`, `Append`, `Sync`, rotation, recovery), `src/storage/pack/fadvise_linux.go`
- Test: `src/storage/pack/pack_test.go`, `src/storage/pack/log_test.go`

**Interfaces — Produces (exact):**
```go
package pack
type Options struct{ SegmentSize int64 } // 0 => 256 MiB
type Location struct{ Segment uint32; Offset int64; Size uint32 } // Offset = record start; Size = data length
const HeaderSize = 40 // record header bytes; on-disk record length = HeaderSize + Size
type SegmentInfo struct{ ID uint32; Size int64 }
var ErrCorrupt, ErrSegmentMissing error
func Open(dir string, opts Options) (*Log, error)
func (l *Log) Append(hash [32]byte, data []byte) (Location, error)
func (l *Log) Sync() error
func (l *Log) ActiveSegment() uint32
func (l *Log) Close() error
func Read(dir string, loc Location, hash [32]byte) ([]byte, error)
func Segments(dir string) ([]SegmentInfo, error)
func RemoveSegment(dir string, id uint32) error
```

Steps (each: failing test, see it fail, minimal code, see it pass):
- [ ] Round trip: Append then `Read` returns the data; `Location` offsets are consecutive.
- [ ] `Read` errors: flipped data byte -> `ErrCorrupt`; wrong expected hash -> `ErrCorrupt`; file shorter than record -> `ErrCorrupt`; missing segment -> `ErrSegmentMissing` (use `errors.Is`).
- [ ] Rotation: with `SegmentSize` small, appends spill into segment 2; record never straddles; a record larger than SegmentSize still goes alone into a fresh segment; `Segments` lists both; sealed segment content is intact.
- [ ] Reopen: `Close`, `Open` again continues in the last segment at the right offset; earlier data still readable.
- [ ] Recovery: (a) file truncated mid-record, (b) last record's data byte flipped, (c) garbage bytes appended, (d) last segment of 3 bytes (bad header) — after `Open`, valid prefix is intact and readable, file is truncated to the valid end (or recreated for d), and new appends work and are readable.
- [ ] `Append` rejects data larger than 16 MiB (it trusts the caller's hash; the Store verifies it).
- [ ] `Sync`: after `Append`+`Sync`, a second `Sync` with no new appends does not call fsync again (inject `syncFile func(*os.File) error` field on `Log`, default `(*os.File).Sync`; count calls). Concurrent appenders + syncers under `-race`: every location returned reads back after a final `Sync`.
- [ ] Sticky error: injected sync returns error -> `Sync` errors, and later `Append` and `Sync` both return that error.
- [ ] `RemoveSegment` deletes the file and fsyncs the dir; removing a missing one returns nil.
- [ ] After a successful sync call `posix_fadvise(DONTNEED)` (in `fadvise_linux.go`, errors ignored — advisory).
- [ ] Commit: `feat(pack): append-only segment log with recovery`.

Design notes for the implementer: one mutex `mu` guards active file, size, and a monotonically increasing write counter `seq`; `Sync` takes `syncMu`, captures `(file, seq)` under `mu`, returns early if `syncedSeq >= seq`, fsyncs outside `mu`, then records `syncedSeq`. Rotation fsyncs the sealed file under `mu` before creating the next file with `O_CREATE|O_EXCL`, writing the 8-byte header, and fsyncing the directory. Recovery scans the last segment with a `bufio.Reader`, verifying blake3 per record.

---

### Task 2: Store integration (write path, read path, recovery)

**Files:**
- Modify: `src/storage/filesystem/{models.go,store.go,chunks.go,filedata.go,gc.go (inBatch only),info.go (Vacuum start: flush)}`
- Delete: `writer_linux.go`, `writer_windows.go`, `storelock_windows.go`
- Create: `src/storage/filesystem/pending.go` (pending state + `flush`)
- Test: `src/storage/filesystem/chunks_pack_test.go`; adapt `store_test.go`/`gc_test.go` only where they poke `chunkPath`/loose files.

**Interfaces:**
- Consumes: Task 1 API.
- Produces: `ChunkRecord{Hash string; Size int64; Segment int64; Offset int64; CreatedAt}`; `(*Store).flush() error` (sync pack, then one transaction: chunk rows, then links, `OnConflict DoNothing`; clears flushed pending entries only after commit; serialized by `flushMu`); `(*Store).locate(hash string) (pack.Location, bool, error)` (pending first, then DB); `(*Store).packDir() string` = `<base>/packs`.

Steps:
- [ ] Test `StoreChunk` bad hash still errors; `ChunkExists` true right after `StoreChunk` (pending) and after `flush`; `ReadChunk` returns the data in both states.
- [ ] Test ordering: after `StoreChunk` + `LinkChunkToFileData` and *no* flush, a fresh read-only DB view shows no chunk row and no link; after `FinalizeFileData`, both exist and checksum is set.
- [ ] Test crash simulation: write chunks, `FinalizeFileData` for file A (flushed), then `StoreChunk` more (unflushed); copy the store directory aside (simulates power loss; SQLite WAL files included), open the copy with `New`: A complete and readable, unflushed chunk `ChunkExists` -> `ErrChunkNotFound`, no dangling rows (every `chunk_records` row reads back verified).
- [ ] Test size trigger: pending bytes >= 32 MiB (use a package-level `flushThreshold` var lowered in the test) flush without `FinalizeFileData`.
- [ ] Test `ReadChunk` on a corrupted packed byte returns an error wrapping `pack.ErrCorrupt`; unknown hash returns `storage.ErrChunkNotFound`; `MarkChunkCorrupted` (after flush) removes the row, links and dependent file data, and `ChunkExists` becomes false.
- [ ] Test `ReadChunk` retry: delete the segment file between locate and read is hard to time; instead unit test helper `readLocated` that re-locates once on `ErrSegmentMissing` by updating the row's location to a copy.
- [ ] Test `New` on a dir containing `chunks/` returns an error mentioning "legacy"; `NewReadOnly` reads a packed chunk and writes nothing.
- [ ] Test `Close` flushes pending (reopen: finalized file complete; unfinalized chunk row present only if it was linked-and-flushed — assert no panic and consistency).
- [ ] Implement. `New`: reject legacy `chunks/`, `pack.Open(packDir)`. `inBatch` and `Vacuum` call `flush` first (explain why in a comment: exclusive guard means no handler mid-call, so rows and links are consistent). Duplicate concurrent `StoreChunk` of the same hash: keep first location.
- [ ] Run full `go test -race ./storage/... ./cmd/bwfs/...`; fix adapted tests.
- [ ] Commit: `feat(storfs): store chunks in pack segments with group commit`.

---

### Task 3: Vacuum, compaction, reporting

**Files:**
- Modify: `src/storage/filesystem/gc.go`, `src/storage/filesystem/info.go`, `src/storage/interface.go` (`VacuumResult` += `SegmentsRemoved`, `SegmentsCompacted` int64), `src/cmd/bwfs/gc.go` (log both fields), `src/cmd/bwfs/main.go` if it logs the startup result
- Create: `src/storage/filesystem/compact.go`
- Test: `src/storage/filesystem/compact_test.go`; update `gc_test.go`

**Interfaces:**
- Consumes: `locate`, `flush`, `inBatch`, `batchLoop`, pack API.
- Produces: `(*Store).reclaimSegments(ctx context.Context, batchSize int) (removed, compacted, bytes int64, err error)`; constant `compactBelow = 0.5`.

Steps:
- [ ] Test orphan chunk rows deleted by `VacuumOnline` no longer unlink anything; `BytesReclaimed` counts physical bytes only when segments go.
- [ ] Test fully dead sealed segment removed (use small `SegmentSize` via an unexported `newWithOptions(base, pack.Options)` used by tests); active segment never removed even if all dead.
- [ ] Test compaction: sealed segment at 25% live is compacted: live chunks still readable with identical bytes, their rows now point to a newer segment, old file gone, `SegmentsCompacted == 1`; segment at 75% live untouched.
- [ ] Test batching: `batchSize` 2 over 10 live records compacts correctly across batches; cancelling `ctx` between batches leaves a consistent store (every row readable) and a later run finishes.
- [ ] Test a corrupt record found during compaction is dropped through the `MarkChunkCorrupted` logic and compaction continues.
- [ ] Test startup `Vacuum` removes a stray segment with no rows (not active) and no longer walks `chunks/`.
- [ ] Implement `reclaimSegments`: segment stats via `SELECT segment, SUM(? + size) ... GROUP BY segment` plus `pack.Segments`; each compaction batch runs in `inBatch`: read verified records, `log.Append`, `log.Sync()`, update rows, commit; remove the segment file after its last row moved. Wire into `VacuumOnline` (after orphan rows) and `Vacuum`.
- [ ] `go test -race ./storage/... ./cmd/bwfs/...`; commit: `feat(storfs): segment compaction and dead-segment removal`.

---

### Task 4: Docs, changelog, benchmark

**Files:**
- Modify: `docs/components/bwfs.md`, `docs/PERFORMANCE.md`, `docs/ARCHITECTURE.md` (only if it describes chunk files), `src/storage/CLAUDE.md`, `README.md` only if it mentions per-chunk files, `CHANGELOG.md` (new dated entry, most recent first)

Steps:
- [ ] Grep docs for `chunks/`, `aa/bb`, "chunk file" and update every occurrence to the pack layout.
- [ ] bwfs.md: storage layout, durability contract (barrier at file finalize, ack-before-fsync semantics, sticky fsync failure -> restart), recovery on open, vacuum + compaction, legacy store rejection and what the operator must do (start a fresh store).
- [ ] Build new binaries (`make build mpbench`), run `mpbench --bin-dir bin --files 500 --profile mixed --dup-ratio 0.3 --streams 4 --runs 3 --seed 1 --json /tmp/claude-1000/new.json` and compare with the baseline in `/tmp/claude-1000/base.json` (built from the pre-change tree). Record the table in PERFORMANCE.md with honest numbers, including regressions.
- [ ] Commit docs: `docs: storfs pack segments`.
