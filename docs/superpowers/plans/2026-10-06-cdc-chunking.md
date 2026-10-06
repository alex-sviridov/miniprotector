# CDC Chunking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace fixed 64 KB chunking in `brfs` with FastCDC content-defined chunking (min 16 KB / avg 64 KB / max 256 KB).

**Architecture:** Only `ChunkIterator` in `src/workload/filesystem/chunker.go` decides chunk boundaries; it will stream the open file through `go-cdc-chunkers` and build the same `Chunk` values (BLAKE3 hash, CRC32, byte offset, eof). The wire protocol, `bwfs` ordering logic and the store are already offset/size based, so nothing downstream changes except tests and docs that assumed exactly 64 KB.

**Tech Stack:** Go 1.26, `github.com/PlakarKorp/go-cdc-chunkers` v1.1.0 (algorithm `fastcdc-v1.0.0`), testify.

**Spec:** `docs/superpowers/specs/2026-10-06-cdc-chunking-design.md`

## Global Constraints

- Work on branch `cdc-chunking` (already created). Never commit to `main`.
- Sizes are constants: min `16 * 1024`, normal `64 * 1024`, max `256 * 1024`. No config keys.
- Algorithm string is exactly `"fastcdc-v1.0.0"` (the plain `"fastcdc"` name is the legacy variant). `NormalSize` must be a power of two.
- No `.proto` change, no wire or storage format change, no migration code. Breaking changes are acceptable.
- Library slices returned by `Chunker.Next()` alias its internal buffer: every chunk's data MUST be copied before it is yielded.
- Go code lives under `src/` (module `github.com/alex-sviridov/miniprotector`, run `go` commands from `/home/alex/miniprotector/src`).
- Commit messages end with these two lines:
  `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr`
- Per `CLAUDE.md`: update `docs/components/<component>.md` for each affected component, `README.md` and `docs/ARCHITECTURE.md` only if they mention chunk size, and add a `CHANGELOG.md` entry (most recent first) before merge.

---

### Task 1: CDC chunker

**Files:**
- Modify: `src/workload/filesystem/chunker.go`
- Modify: `src/workload/filesystem/chunker_test.go` (size-dependent tests)
- Create: `src/workload/filesystem/chunker_cdc_test.go`
- Modify: `src/go.mod`, `src/go.sum`

**Interfaces:**
- Consumes: `openForRead(path string) (*os.File, error)` (per-OS file, unchanged), `NewChunk(hash []byte, index int64, eof bool, data []byte) *Chunk` (existing in `chunker.go`; computes BLAKE3 hash and CRC32 when `hash == nil`).
- Produces (later tasks rely on these exact names):
  - `const MinChunkSize = 16 * 1024`
  - `const NormalChunkSize = 64 * 1024`
  - `const MaxChunkSize = 256 * 1024`
  - `ChunkSize` is **removed**. `ChunkIterator()` signature is unchanged: `func (fi FileInfo) ChunkIterator() iter.Seq2[workload.Chunk, error]`.

- [ ] **Step 1: Add the dependency**

```bash
cd /home/alex/miniprotector/src && go get github.com/PlakarKorp/go-cdc-chunkers@v1.1.0
```

- [ ] **Step 2: Write the failing tests** in `src/workload/filesystem/chunker_cdc_test.go`

Reuse the existing helpers `createTempFile(t, data) string` and `collectChunks(t, fi) []workload.Chunk` from `chunker_test.go` (read them first; if their signatures differ, adapt the calls, not the assertions).

```go
package filesystem

import (
	"bytes"
	mrand "math/rand"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seededBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	mrand.New(mrand.NewSource(seed)).Read(b)
	return b
}

func chunksOf(t *testing.T, data []byte) []workload.Chunk {
	t.Helper()
	path := createTempFile(t, data)
	defer os.Remove(path)
	return collectChunks(t, FileInfo{path: path})
}

func TestCDC_ReassemblesFileAndOffsetsAreContiguous(t *testing.T) {
	data := seededBytes(1, 3*MaxChunkSize+12345)
	chunks := chunksOf(t, data)

	var out bytes.Buffer
	var want int64
	for i, c := range chunks {
		assert.Equal(t, want, c.Index(), "chunk %d offset", i)
		assert.Equal(t, i == len(chunks)-1, c.IsEOF(), "eof only on last chunk (chunk %d)", i)
		out.Write(c.Data())
		want += int64(len(c.Data()))
	}
	assert.Equal(t, data, out.Bytes())
}

func TestCDC_ChunkSizesWithinBounds(t *testing.T) {
	chunks := chunksOf(t, seededBytes(2, 8*MaxChunkSize))
	require.Greater(t, len(chunks), 4)
	for i, c := range chunks {
		assert.LessOrEqual(t, len(c.Data()), MaxChunkSize, "chunk %d", i)
		if i < len(chunks)-1 {
			assert.GreaterOrEqual(t, len(c.Data()), MinChunkSize, "chunk %d", i)
		}
	}
}

func TestCDC_EdgeSizes(t *testing.T) {
	for _, size := range []int{0, 1, MinChunkSize - 1, MinChunkSize, MaxChunkSize, MaxChunkSize + 1} {
		data := seededBytes(3, size)
		chunks := chunksOf(t, data)
		if size == 0 {
			assert.Empty(t, chunks)
			continue
		}
		var out []byte
		for i, c := range chunks {
			out = append(out, c.Data()...)
			assert.Equal(t, i == len(chunks)-1, c.IsEOF(), "size %d chunk %d", size, i)
		}
		assert.Equal(t, data, out, "size %d", size)
	}
}

func TestCDC_Deterministic(t *testing.T) {
	data := seededBytes(4, 2*MaxChunkSize)
	a, b := chunksOf(t, data), chunksOf(t, data)
	require.Equal(t, len(a), len(b))
	for i := range a {
		assert.Equal(t, a[i].Hash(), b[i].Hash())
	}
}

func TestCDC_InsertionAtFrontKeepsMostChunks(t *testing.T) {
	base := seededBytes(5, 4<<20)
	shifted := append(seededBytes(6, 1000), base...)

	seen := map[string]bool{}
	for _, c := range chunksOf(t, base) {
		seen[string(c.Hash())] = true
	}
	shiftedChunks := chunksOf(t, shifted)
	shared := 0
	for _, c := range shiftedChunks {
		if seen[string(c.Hash())] {
			shared++
		}
	}
	frac := float64(shared) / float64(len(shiftedChunks))
	assert.Greater(t, frac, 0.8, "fixed-size chunking would share ~0 chunks here; got %.2f", frac)
}

func TestCDC_YieldedDataSurvivesIteratorAdvancing(t *testing.T) {
	data := seededBytes(7, 4*MaxChunkSize)
	path := createTempFile(t, data)
	defer os.Remove(path)

	var held [][]byte
	for c, err := range (FileInfo{path: path}).ChunkIterator() {
		require.NoError(t, err)
		held = append(held, c.Data()) // deliberately not copied: iterator must hand over owned data
	}
	assert.Equal(t, data, bytes.Join(held, nil))
}
```

Add the missing import `"github.com/alex-sviridov/miniprotector/workload"` to the file.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd /home/alex/miniprotector/src && go test ./workload/filesystem/ -run TestCDC -v`
Expected: FAIL to compile (`MinChunkSize`/`MaxChunkSize` undefined).

- [ ] **Step 4: Implement the CDC chunker** in `src/workload/filesystem/chunker.go`

Replace `const ChunkSize = 64 * 1024 // 64KB` with:

```go
// FastCDC content-defined chunking: boundaries follow the data, so inserting
// or deleting bytes in a file only changes the chunks around the edit.
// NormalChunkSize must be a power of two (library requirement).
const (
	MinChunkSize    = 16 * 1024
	NormalChunkSize = 64 * 1024
	MaxChunkSize    = 256 * 1024

	// "fastcdc" alone is the library's legacy variant; the versioned name is the current one.
	cdcAlgorithm = "fastcdc-v1.0.0"
)
```

Add imports `chunkers "github.com/PlakarKorp/go-cdc-chunkers"` and the blank import `_ "github.com/PlakarKorp/go-cdc-chunkers/chunkers/fastcdc"` (registers the algorithm). Remove the `loadChunk` function and the now-unused `os`/`io.ReadFull` usage if any import becomes unused. Replace the body of `ChunkIterator` (and its doc comment, which must now say "content-defined chunks of 16-256 KB, average 64 KB") with:

```go
func (fi FileInfo) ChunkIterator() iter.Seq2[workload.Chunk, error] {
	return func(yield func(workload.Chunk, error) bool) {
		file, err := openForRead(fi.path)
		if err != nil {
			yield(nil, err)
			return
		}
		defer file.Close()

		fileInfo, err := file.Stat()
		if err != nil {
			yield(nil, err)
			return
		}
		fileSize := fileInfo.Size()

		chunker, err := chunkers.NewChunker(cdcAlgorithm, file, &chunkers.ChunkerOpts{
			MinSize:    MinChunkSize,
			NormalSize: NormalChunkSize,
			MaxSize:    MaxChunkSize,
		})
		if err != nil {
			yield(nil, err)
			return
		}

		position := int64(0)
		for {
			data, err := chunker.Next()
			if err != nil && err != io.EOF {
				yield(nil, err)
				return
			}
			if len(data) > 0 {
				// data aliases the chunker's scan buffer and is overwritten by the
				// next call; chunks outlive that (they sit in the send window).
				owned := make([]byte, len(data))
				copy(owned, data)
				eof := err == io.EOF || position+int64(len(owned)) >= fileSize
				chunk := NewChunk(nil, position, eof, owned)
				if !yield(*chunk, nil) {
					return
				}
				position += int64(len(owned))
				if eof {
					return
				}
			}
			if err == io.EOF {
				return
			}
		}
	}
}
```

- [ ] **Step 5: Update the old fixed-size tests** in `src/workload/filesystem/chunker_test.go`

Every reference to `ChunkSize` there assumed fixed boundaries (lines ~75-440). For each test:
- Where `ChunkSize` is only used to pick a *file size* (e.g. "large file"), replace with `NormalChunkSize` or `MaxChunkSize` as appropriate.
- Where a test asserts an exact chunk count or `len(chunk.Data()) == ChunkSize` for non-final chunks (the `expectedChunks` / "Non-final chunk should be full size" / `two_chunks_exact`-style cases), replace the exact assertion with the CDC invariants: sizes within `[MinChunkSize, MaxChunkSize]` for non-final chunks, last chunk `<= MaxChunkSize`, contiguous offsets, reassembled bytes equal the file. Do not delete coverage; convert it.
- Keep every integrity / hash / CRC / eof / locking / error-path test.

- [ ] **Step 6: Run the package tests**

Run: `cd /home/alex/miniprotector/src && go test ./workload/... -count=1`
Expected: PASS. Also run `go vet ./workload/...`.

- [ ] **Step 7: Commit**

```bash
cd /home/alex/miniprotector && git add src/go.mod src/go.sum src/workload/filesystem/ && git commit -m "feat(chunker): content-defined chunking with FastCDC (16/64/256 KB)

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 2: Fix dependent tests across the repo

**Files:**
- Modify: `src/cmd/brfs/window_test.go` (~line 185 `randChunks`)
- Modify: `src/cmd/bwfs/pipelined_integration_test.go` (~lines 90-105)
- Modify: any other test that fails to compile or fails after Task 1 (find with the commands below)

**Interfaces:**
- Consumes: `filesystem.NormalChunkSize`, `filesystem.MaxChunkSize`, `FileInfo.ChunkIterator()` from Task 1. `ChunkSize` no longer exists.
- Produces: a green `go test ./...`.

- [ ] **Step 1: Find the breakage**

Run: `cd /home/alex/miniprotector/src && go vet ./... 2>&1 | head -40 && go test ./... -count=1 2>&1 | tail -60`
Expected: compile errors for `ChunkSize` in `cmd/brfs` and `cmd/bwfs` tests; possibly failures where a test assumed 64 KB boundaries.

- [ ] **Step 2: Fix `randChunks` in `window_test.go`**

`randChunks` builds fake chunk payloads for window logic tests that never go through the chunker; replace `filesystem.ChunkSize` with `filesystem.NormalChunkSize` there (the window logic is size-agnostic).

- [ ] **Step 3: Fix `TestIntegration_Pipelined_CachedChunksAheadOfInFlightData`**

The test relied on a 64 KB random block being exactly one chunk. With CDC it is not. Keep the scenario (a cached chunk between two new chunks; the EOF chunk cached while the one before it still needs data) but derive it from real chunk boundaries:
1. Build `shared` as `randBytes(t, 4*wfs.MaxChunkSize)` and write it as `seed.bin`.
2. Because identical content chunked from offset 0 yields identical chunks, `middle.bin` = new random bytes + `shared` + new random bytes still re-uses most of `shared`'s chunks (CDC resynchronises after the first boundary), and `last.bin` = new random bytes + `shared` ends on cached chunk(s). Keep the existing assertions (`serverHash == clientHash`).
3. Add one assertion that proves the premise: use `byName["seed.bin"].ChunkIterator()` to collect seed hashes, iterate `byName["middle.bin"]`, and `require` that at least one chunk is in the seed set AND at least one is not, and that for `last.bin` the final chunk (`IsEOF()`) hash is in the seed set. If the premise fails with the random data, adjust the sizes, not the assertion.

- [ ] **Step 4: Fix every remaining failure**

For any test still failing, decide whether it assumed a fixed boundary (rewrite it to derive expectations from `ChunkIterator` or use CDC invariants) or found a real bug (stop and report it). Tests in `cmd/rwfs`, `cmd/bwfs` (gc, restore, list) and `storage/` that only store and restore arbitrary bytes should pass untouched.

- [ ] **Step 5: Run everything**

Run: `cd /home/alex/miniprotector/src && go vet ./... && go test ./... -count=1`
Expected: all PASS. Report any pre-existing failure separately (verify by checking whether it also fails on `main`).

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector && git add -A src && git commit -m "test: derive chunk expectations from CDC instead of fixed 64 KB

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 3: mpbench `--shift` dataset option

**Files:**
- Modify: `src/cmd/mpbench/dataset.go` (`DatasetSpec`, `Generate`, comments mentioning `workload/filesystem.ChunkSize`)
- Modify: `src/cmd/mpbench/args.go` (flag, struct field, validation if any)
- Modify: `src/cmd/mpbench/runner.go` (or wherever `DatasetSpec` is built from args; find with `grep -n "DatasetSpec{" src/cmd/mpbench/*.go`)
- Test: `src/cmd/mpbench/dataset_test.go`, `src/cmd/mpbench/args_test.go`

**Interfaces:**
- Consumes: existing `DatasetSpec`, `Generate`, `fillRandom(r *rand.Rand, b []byte)`, `blockSize`, `poolBlocks`.
- Produces: `DatasetSpec.Shift bool`; CLI flag `--shift` (bool, default false). When true, each non-sparse file begins with a random-length prefix of 1..4095 fresh random bytes, so pooled duplicate blocks land at non-aligned offsets. `Dataset.Files[i].Size` includes the prefix. Default behavior (flag off) must stay byte-identical to today for the same seed.

- [ ] **Step 1: Write failing tests** in `dataset_test.go`

```go
func TestGenerate_ShiftOffIsUnchanged(t *testing.T) {
	spec := DatasetSpec{Files: 15, Profile: "mixed", DupRatio: 0.5, Seed: 9}
	a, b := gen(t, spec), gen(t, spec)
	require.NoError(t, CompareTrees(a.Root, b.Root))
}

func TestGenerate_ShiftIsDeterministicAndDiffersFromAligned(t *testing.T) {
	aligned := DatasetSpec{Files: 15, Profile: "mixed", DupRatio: 0.5, Seed: 9}
	shifted := aligned
	shifted.Shift = true

	s1, s2 := gen(t, shifted), gen(t, shifted)
	require.NoError(t, CompareTrees(s1.Root, s2.Root), "same seed must be byte-identical")
	assert.Error(t, CompareTrees(gen(t, aligned).Root, s1.Root))
}

func TestGenerate_ShiftPlacesPoolBlocksOffBoundary(t *testing.T) {
	ds := gen(t, DatasetSpec{Files: 20, Profile: "large", DupRatio: 1, Seed: 3, Shift: true})
	// With DupRatio 1 and no shift, every full 64KB block of a file is a pool block, so at most
	// poolBlocks distinct aligned blocks exist. With a shift the aligned blocks are all different.
	seen := map[[32]byte]bool{}
	for _, f := range ds.Files {
		data, err := os.ReadFile(filepath.Join(ds.Root, f.Rel))
		require.NoError(t, err)
		for off := 0; off+blockSize <= len(data); off += blockSize {
			seen[sha256.Sum256(data[off:off+blockSize])] = true
		}
	}
	assert.Greater(t, len(seen), poolBlocks, "aligned blocks should not collapse onto the pool when shifted")
}
```

Add a test in `args_test.go` that parses `--shift` and asserts the field is true, and that it defaults to false (mirror how neighbouring tests parse flags).

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/mpbench/ -run "Shift" -v`
Expected: FAIL to compile (`Shift` undefined).

- [ ] **Step 3: Implement**

In `DatasetSpec` add `Shift bool`. In `Generate`, inside the non-sparse branch, after computing `size := pickSize(...)` and only when `spec.Shift`, draw the prefix length from the same RNG **after** the existing draws for that file are unaffected when the flag is off (guard the RNG call with `if spec.Shift`, so the default stream is untouched):

```go
prefix := int64(0)
if spec.Shift {
	prefix = 1 + r.Int64N(blockSize/16-1) // 1..4095 bytes
}
data := make([]byte, size+prefix)
fillRandom(r, data[:prefix])
body := data[prefix:]
for off := int64(0); off < size; off += blockSize {
	end := min(off+blockSize, size)
	blk := body[off:end]
	...existing dup/fresh logic unchanged...
}
```
and record `Size: size + prefix` in `DatasetFile` and `TotalBytes`. Keep the sparse profile unchanged (shift does not apply; document that). Add the flag in `args.go` next to `--dup-ratio`:

```go
fs.BoolVar(&a.Shift, "shift", false, "prefix each file with 1-4095 random bytes so duplicate blocks sit off 64KB alignment (what content-defined chunking is for)")
```
Thread `a.Shift` into the `DatasetSpec` construction. Update the comment on `blockSize` and `Generate` to say blocks are the generator's unit, and that brfs uses content-defined chunking (avg 64 KB), so dup blocks only approximately map to shared chunks.

- [ ] **Step 4: Run the package tests**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/mpbench/ -count=1`
Expected: PASS (the integration test, if it runs binaries, may be skipped or need `make build`; follow its existing conventions).

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector && git add -A src/cmd/mpbench && git commit -m "feat(mpbench): --shift dataset option for off-alignment duplicates

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 4: Documentation and comments

**Files:**
- Modify: `docs/components/brfs.md`, `docs/components/mpbench.md`, `docs/PERFORMANCE.md`, `docs/protocols/backup.md`
- Modify (only if they mention chunk size): `docs/ARCHITECTURE.md`, `README.md`, `docs/components/bwfs.md`, `docs/components/rwfs.md`
- Modify: `src/cmd/rwfs/restorefile.go` (comment above `restoreWriteBufferSize`)

**Interfaces:** Consumes the behavior from Tasks 1 and 3: sizes 16/64/256 KB, `--shift` flag. Produces: docs matching the code. No code behavior change.

- [ ] **Step 1: Find every stale statement**

Run: `cd /home/alex/miniprotector && grep -rniE "64 ?KB|512 ?KB|fixed.size|chunk size|ChunkSize" docs README.md src --include=*.md --include=*.go | grep -v "docs/superpowers" | grep -v pb.go | grep -v node_modules`
(Historical files under `docs/superpowers/` and past `CHANGELOG.md` entries are records; leave them.)

- [ ] **Step 2: Update each hit**

- `docs/components/brfs.md`: chunking section describes FastCDC, 16/64/256 KB, hash = BLAKE3, why content-defined (dedup survives insertions/shifts), and that old fixed-size chunks do not dedup against new ones.
- `docs/PERFORMANCE.md`: replace "Chunk size is fixed at 64 KB (the protocol doc still says 512 KB...)" with the CDC description; change the memory bound `streams × window × 64 KB` to "typically 64 KB per slot, worst case 256 KB" with the recomputed worst case (8 × 16 × 256 KB = 32 MB); keep the per-chunk-cost discussion.
- `docs/protocols/backup.md`: fix the 512 KB statements (lines ~4, 8, 19-26) to variable-size chunks of 16-256 KB average 64 KB; remove the "Future evolution" sentence since it is now done. Keep the rest of the protocol text accurate: chunks are still individually sent and hashed.
- `docs/components/mpbench.md`: add the `--shift` row to the flag table; adjust the `--dup-ratio` description ("64 KB blocks" are the generator's unit, mapped approximately onto CDC chunks); note that dedup ratios are not comparable with pre-CDC runs.
- `src/cmd/rwfs/restorefile.go`: reword the comment to "coalesces chunks (16-256 KB, average 64 KB; see workload/filesystem/chunker.go) into far fewer syscalls -- about 16 average chunks per Write". Value unchanged.

- [ ] **Step 3: Verify**

Run: `cd /home/alex/miniprotector/src && go build ./... && cd .. && grep -rniE "ChunkSize\b" docs README.md src --include=*.md --include=*.go | grep -v "docs/superpowers" | grep -v pb.go | grep -v node_modules`
Expected: build OK; no remaining `ChunkSize` hits outside historical docs.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector && git add -A docs README.md src && git commit -m "docs: describe content-defined chunking

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

---

### Task 5: Benchmark against baseline and changelog

This task is run by the controller (not a fresh implementer), because the baseline must be built from `main` and the numbers are reported to the user.

**Files:**
- Modify: `CHANGELOG.md`
- Scratch only: baseline binaries and JSON under the session scratchpad

- [ ] **Step 1: Build baseline binaries from `main`**

```bash
cd /home/alex/miniprotector && git worktree add /tmp/claude-1000/-home-alex-miniprotector/5f753bdb-61db-4e31-a6fe-3788944d3e53/scratchpad/base main
cd /tmp/claude-1000/-home-alex-miniprotector/5f753bdb-61db-4e31-a6fe-3788944d3e53/scratchpad/base && make build && make mpbench
```
(`mpbench` from the branch is used for both, since baseline `mpbench` lacks `--shift`; build the branch's `mpbench` in the main tree with `make mpbench`, and pass `--bin-dir` pointing at the baseline or branch `bin/` for `brfs/bwfs/rwfs`. If `--bin-dir` needs `mpbench` alongside, copy it. Branch `mpbench` without `--shift` generates the same data as baseline `mpbench`, so datasets match.)

- [ ] **Step 2: Run matched benchmarks**

For each of `--profile mixed`, `--profile small`, `--profile large` (no shift), and `--profile mixed --shift`, run baseline bins and branch bins with identical flags, for example:

```bash
mpbench --bin-dir <bindir> --files 500 --profile mixed --dup-ratio 0.3 --rtt 0 --streams 4 --runs 3 --seed 1 --json <out>.json
```
Also one run with `--rtt 20ms --bandwidth 1gbit`. Record per-phase medians (backup-cold, backup-warm, restore), wire bytes, peak memory, and the dedup outcome (store size or chunk counts if reported).

- [ ] **Step 3: Interpret**

Expected: `--shift` shows a large dedup gain for CDC (baseline should show ~no dedup of pool blocks; CDC does). Unshifted profiles may show different dedup because pooled 64 KB blocks no longer align with chunk boundaries, and a small throughput change from chunking cost and copy. Report deltas honestly, including regressions; a regression over about 10% on cold backup is worth flagging to the user before merge.

- [ ] **Step 4: Changelog**

Add at the top of `CHANGELOG.md` (below the intro, above the newest entry) a dated entry `## 2026-10-06 — Content-defined chunking (FastCDC)` with a short paragraph: what changed (fixed 64 KB chunks replaced by FastCDC, 16/64/256 KB), why (dedup survives insertions and shifts), compatibility (no format change; old chunks do not dedup against new ones, first backup re-uploads), mpbench `--shift`, and the headline benchmark numbers. Not a file-by-file diff.

- [ ] **Step 5: Commit and clean up**

```bash
cd /home/alex/miniprotector && git worktree remove --force /tmp/claude-1000/-home-alex-miniprotector/5f753bdb-61db-4e31-a6fe-3788944d3e53/scratchpad/base
git add CHANGELOG.md && git commit -m "docs: changelog for CDC chunking

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr"
```

Do not merge to `main` without the user's go-ahead.
