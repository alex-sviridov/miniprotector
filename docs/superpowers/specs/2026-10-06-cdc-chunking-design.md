# Content-defined chunking (CDC) — design

Branch: `cdc-chunking`. Date: 2026-10-06.

## Goal

Replace fixed 64 KB chunking in `brfs` with content-defined chunking so deduplication survives
insertions, deletions and shifts inside a file. Library: `github.com/PlakarKorp/go-cdc-chunkers`,
algorithm FastCDC.

## Decisions

- **Sizes:** min 16 KB, normal (average) 64 KB, max 256 KB, as constants in
  `workload/filesystem/chunker.go`. The average equals today's chunk size, so round-trip count, DB
  rows and per-chunk server cost stay comparable and the benchmark delta isolates CDC. The library's
  own benchmark uses 2/8/64 KiB, which would hurt here: backup is limited by per-chunk cost, not by
  chunking speed (library: 2-3 GB/s; backup: ~12 MB/s). No config keys (YAGNI).
- **Algorithm:** FastCDC. Speed does not distinguish the algorithms at this scale.
- **Compatibility:** none required. Breaking changes are acceptable. Existing fixed-size chunks stay
  readable (chunks are keyed by BLAKE3 with offset and size), but will not dedup against CDC chunks,
  so the first backup after the change re-uploads data.

## Changes

### Chunker (`src/workload/filesystem/chunker.go`)

- Replace `ChunkSize` and `loadChunk` with a CDC reader over the open file. `ChunkIterator` keeps its
  signature (`iter.Seq2[workload.Chunk, error]`) and `Chunk` fields.
- `index` is a running offset (sum of earlier chunk sizes). `eof` is true on the last chunk, determined
  by the library returning `io.EOF` on the following call, or by offset reaching the file size.
- Each chunk's data is copied out of the library's buffer before it is yielded. Verify in the library
  source whether `Next()` reuses its buffer; copy regardless, since chunks stay in flight in the window
  while the reader advances.
- An empty file yields no chunks, as today.
- Read errors from the library are yielded as `(nil, err)` and end iteration, as today.

### Fixed-size assumptions

- Memory bound in `docs/PERFORMANCE.md`: `streams × window × 64 KB` becomes worst case
  `streams × window × 256 KB` (about 32 MB at 8 × 16), typical about 64 KB per slot.
- `restoreWriteBufferSize` comment in `src/cmd/rwfs/restorefile.go` ("~16 chunks per Write") reworded;
  the value is unchanged.
- `ChunkSize` references in docs and the `mpbench` dataset comment updated.
- Protocol doc claims 512 KB chunks; correct it to describe variable-size chunks (16-256 KB).
  No `.proto` change, so no new protocol doc is required.

### mpbench

- Existing profiles keep working. Their 64 KB-aligned duplicate blocks no longer align with CDC
  boundaries, so dup ratios reported under CDC will differ from the baseline; note this in results.
- Add a shifted-duplicate dataset option: duplicate content placed at a non-aligned offset (a short
  random prefix inserted before the shared blocks). This is the case CDC exists for and fixed-size
  chunking cannot dedup. Exact flag name is settled in the plan.

## Testing

- Unit (`chunker_test.go`):
  - Reassembling all chunk data reproduces the file byte-for-byte.
  - Every chunk except the last is within [min, max]; the last is at most max.
  - `index` values are contiguous offsets; exactly one chunk has `eof`.
  - Empty file, 1-byte file, file smaller than min, file exactly max.
  - Determinism: the same content yields identical hashes across runs.
  - Shift resilience: inserting bytes at the front of a file leaves most chunk hashes unchanged
    (assert a high shared fraction, not equality).
  - Yielded chunk data is not corrupted after the iterator advances (guards buffer reuse).
- Existing `brfs`, `bwfs` and `rwfs` integration tests run unchanged; fix any that assume 64 KB
  boundaries.
- Benchmark: run `mpbench` on `main` and on the branch with identical flags; report backup and
  restore time, peak memory and dedup change against the baseline, per the saved preference. Include
  the shifted-duplicate dataset.

## Error handling

No new failure modes: chunker errors surface through the existing iterator error path. `bwfs`
already validates chunk offset, size and ordering (`chunkorder.go`), which covers variable sizes.

## Docs (per CLAUDE.md)

`docs/components/brfs.md`, `docs/components/mpbench.md`, `docs/PERFORMANCE.md`, protocol doc wording,
`docs/ARCHITECTURE.md` if it mentions chunk size, and a `CHANGELOG.md` entry before merging.

## Out of scope

Configurable sizes, algorithm selection, migration of existing stores, server-side re-chunking.
