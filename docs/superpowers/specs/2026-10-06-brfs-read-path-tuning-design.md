# Design: brfs Read-Path Tuning

> Builds on the per-OS reader split (`workload/filesystem/reader_{linux,windows}.go`). Makes file
> reading cheaper on page cache and faster on queue-depth-hungry storage, without a protocol change
> and without needing to know what disk is underneath.

## Decisions

- **No disk-type detection.** In containers and VMs the device class is unknowable (virtio lies,
  overlayfs hides the device). Defaults assume flash-like storage; the one thing that matters —
  queue depth — is **found at runtime**.
- **Adaptive read-ahead is per file, not across files.** Reading upcoming files would waste I/O on
  every warm backup, where bwfs answers "not needed" for nearly all of them. Read-ahead therefore
  starts only once a file is known to be needed.
- **No O_DIRECT, extent ordering, `statfs` gating, io_uring or `statx`** in this change. They can
  be added behind the same seam if a real need shows up.
- **Chunk stream is unchanged**: same 64 KB chunks, hashes, indexes and EOF flag as before.

## Components (`src/workload/filesystem`)

| File | Role |
|------|------|
| `readsource.go` | Portable. Reads a file as 256 KiB *blocks*, up to `depth` blocks ahead (launched − consumed), via concurrent `ReadAt`. Files ≤ one block are read inline with no goroutines. Skips holes. |
| `readtuner.go` | Portable. Hill-climbing controller for `depth`. |
| `reader_linux.go` | `openForRead` (`O_NOATIME`, retry without on `EPERM`), `adviseSequential` (`POSIX_FADV_SEQUENTIAL`), `dropCache` (`POSIX_FADV_DONTNEED`), `dataExtents` (`SEEK_DATA`/`SEEK_HOLE`). |
| `reader_windows.go` | `openForRead` (`FILE_FLAG_SEQUENTIAL_SCAN`); the other hooks are no-ops (`dataExtents` reports "unsupported"). |

`ChunkIterator` consumes blocks and slices them into 64 KB chunks, hashing as before.

### Adaptive depth

Start at 4 blocks in flight per file, range 1–16. A ticker (500 ms, process-wide, started on first
use) measures aggregate bytes read per second and feeds `tuner.observe`:

- after an increase, throughput gained < 5 % → revert the increase, hold 8 ticks;
- otherwise probe upward by 2 until the cap.

If the consumer (network) is the bottleneck throughput does not rise, so depth settles at the start
value; on NVMe/cloud volumes it climbs; on a seek-bound disk the first probe hurts and reverts.
`read_prefetch=N` (N>0) pins the depth and disables the controller.

### Cache hygiene

`SEQUENTIAL` is set on files ≥ 1 block. After each block of a file ≥ `read_drop_cache_min_size`
(default 1 MiB, `0` disables) is read, its range gets `DONTNEED`, so a full backup stops evicting
the user's working set. Smaller files are left cached: they are the likelier working set. Known
limit: a large file that was already hot is dropped too (no `mincore` check).

### Sparse files (Linux)

Only when `st_blocks*512 < size`, the data extents are mapped once with `SEEK_DATA`/`SEEK_HOLE`
(abandoned if > 32768 extents or on any error → plain read). A block lying wholly in a hole is
produced without I/O and emitted as zero chunks using a precomputed hash and CRC and a shared
read-only zero buffer. Partially-hole blocks are read normally (holes read as zeros), so the chunk
stream is byte-identical to a full read.

### Short reads and growth

Blocks cover `[0, size)` where `size` is read at open. A short read (file shrank) ends the stream;
the last chunk delivered carries EOF. Growth past `size` is ignored.

## Configuration

| Key | Default | Meaning |
|-----|---------|---------|
| `read_prefetch` | `0` | Pin read-ahead depth in blocks; `0` = adaptive |
| `read_drop_cache_min_size` | `1048576` | Drop page cache behind files at least this big; `0` = never |

brfs applies them once at start through `filesystem.SetReadOptions`.

## Testing

- Tuner: pure function against fake throughput curves (saturating at 12 → settles near 12;
  degrading above 3 → settles ≤ 6; fixed depth stays fixed).
- Chunk stream equivalence: sparse file chunks == chunks of the same bytes in a dense file;
  multi-block files (incl. non-block-aligned size) match the existing chunker tests;
  early termination does not leak goroutines; shrinking file ends with EOF flag.
- `O_NOATIME` fallback: forced `EPERM` path via a hook.
- Existing chunker, brfs, bwfs tests unchanged. `GOOS=windows go build` for touched packages.
- mpbench: before/after vs `main`, plus a new `sparse` dataset profile and `--cold-cache`
  (drops caches when permitted) so the cache effects can be seen.

## Docs

`docs/components/brfs.md` (read path, config keys), `docs/PERFORMANCE.md` (tuning guidance),
`docs/components/mpbench.md` (profile/flag), `CHANGELOG.md` at merge.
