package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gen(t *testing.T, spec DatasetSpec) *Dataset {
	t.Helper()
	spec.Dir = t.TempDir()
	ds, err := Generate(spec)
	require.NoError(t, err)
	return ds
}

func TestGenerate_SameSeedIsByteIdentical(t *testing.T) {
	spec := DatasetSpec{Files: 25, Profile: "mixed", DupRatio: 0.3, Seed: 5}
	a, b := gen(t, spec), gen(t, spec)

	require.Equal(t, a.Files, b.Files)
	require.Equal(t, a.TotalBytes, b.TotalBytes)
	require.NoError(t, CompareTrees(a.Root, b.Root))
}

func TestGenerate_DifferentSeedDiffers(t *testing.T) {
	a := gen(t, DatasetSpec{Files: 10, Profile: "small", Seed: 1})
	b := gen(t, DatasetSpec{Files: 10, Profile: "small", Seed: 2})
	assert.Error(t, CompareTrees(a.Root, b.Root))
}

func TestGenerate_ProfilesRespectSizeRanges(t *testing.T) {
	small := gen(t, DatasetSpec{Files: 40, Profile: "small", Seed: 1})
	for _, f := range small.Files {
		assert.GreaterOrEqual(t, f.Size, int64(1<<10), f.Rel)
		assert.LessOrEqual(t, f.Size, int64(32<<10), f.Rel)
	}
	large := gen(t, DatasetSpec{Files: 3, Profile: "large", Seed: 1})
	for _, f := range large.Files {
		assert.GreaterOrEqual(t, f.Size, int64(2<<20), f.Rel)
		assert.LessOrEqual(t, f.Size, int64(8<<20), f.Rel)
	}
}

func TestGenerate_OnDiskMatchesRecordedSizes(t *testing.T) {
	ds := gen(t, DatasetSpec{Files: 15, Profile: "mixed", Seed: 3})
	var total int64
	for _, f := range ds.Files {
		st, err := os.Stat(filepath.Join(ds.Root, f.Rel))
		require.NoError(t, err)
		assert.Equal(t, f.Size, st.Size())
		total += f.Size
	}
	assert.Equal(t, total, ds.TotalBytes)
	assert.Len(t, ds.Files, 15)
}

// blockHashes returns the SHA-256 of every full 64KB-aligned block of every file.
func blockHashes(t *testing.T, ds *Dataset) [][32]byte {
	t.Helper()
	var out [][32]byte
	for _, f := range ds.Files {
		data, err := os.ReadFile(filepath.Join(ds.Root, f.Rel))
		require.NoError(t, err)
		for off := 0; off+blockSize <= len(data); off += blockSize {
			out = append(out, sha256.Sum256(data[off:off+blockSize]))
		}
	}
	return out
}

func distinct(hs [][32]byte) int {
	seen := map[[32]byte]bool{}
	for _, h := range hs {
		seen[h] = true
	}
	return len(seen)
}

func TestGenerate_DupRatioZeroSharesNothing(t *testing.T) {
	hs := blockHashes(t, gen(t, DatasetSpec{Files: 3, Profile: "large", DupRatio: 0, Seed: 1}))
	require.NotEmpty(t, hs)
	assert.Equal(t, len(hs), distinct(hs), "no block may repeat")
}

func TestGenerate_DupRatioOneDrawsEveryBlockFromThePool(t *testing.T) {
	hs := blockHashes(t, gen(t, DatasetSpec{Files: 3, Profile: "large", DupRatio: 1, Seed: 1}))
	require.NotEmpty(t, hs)
	assert.LessOrEqual(t, distinct(hs), poolBlocks)
	assert.Less(t, distinct(hs), len(hs), "blocks must repeat")
}

func TestTouch_AdvancesMtimeByAtLeastTwoSecondsKeepingContent(t *testing.T) {
	ds := gen(t, DatasetSpec{Files: 5, Profile: "small", Seed: 1})
	before := map[string]time.Time{}
	for _, f := range ds.Files {
		st, err := os.Stat(filepath.Join(ds.Root, f.Rel))
		require.NoError(t, err)
		before[f.Rel] = st.ModTime()
	}
	other := gen(t, DatasetSpec{Files: 5, Profile: "small", Seed: 1})

	require.NoError(t, ds.Touch())

	for _, f := range ds.Files {
		st, err := os.Stat(filepath.Join(ds.Root, f.Rel))
		require.NoError(t, err)
		assert.GreaterOrEqual(t, st.ModTime().Sub(before[f.Rel]), 2*time.Second, f.Rel)
	}
	assert.NoError(t, CompareTrees(ds.Root, other.Root), "content must be untouched")
}

func TestCompareTrees_ReportsMissingExtraAndModified(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	write := func(root, rel, content string) {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	write(a, "same.txt", "x")
	write(b, "same.txt", "x")
	require.NoError(t, CompareTrees(a, b))

	write(a, "only-src.txt", "x")
	write(b, "only-dst.txt", "x")
	write(a, "sub/mod.txt", "1")
	write(b, "sub/mod.txt", "2")
	err := CompareTrees(a, b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing: only-src.txt")
	assert.Contains(t, err.Error(), "unexpected: only-dst.txt")
	assert.Contains(t, err.Error(), "differs: sub/mod.txt")
}

func TestGenerate_SparseProfileIsMostlyHole(t *testing.T) {
	ds := gen(t, DatasetSpec{Files: 3, Profile: "sparse", Seed: 1})
	for _, f := range ds.Files {
		assert.GreaterOrEqual(t, f.Size, int64(4<<20), f.Rel)
		assert.LessOrEqual(t, f.Size, int64(16<<20), f.Rel)
		st, err := os.Stat(filepath.Join(ds.Root, f.Rel))
		require.NoError(t, err)
		assert.Equal(t, f.Size, st.Size())
	}
	again := gen(t, DatasetSpec{Files: 3, Profile: "sparse", Seed: 1})
	require.NoError(t, CompareTrees(ds.Root, again.Root))
}

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

func TestGenerate_DefaultDupBlockEqualsExplicit64KiB(t *testing.T) {
	spec := DatasetSpec{Files: 15, Profile: "mixed", DupRatio: 0.5, Seed: 9}
	explicit := spec
	explicit.DupBlock = 64 << 10
	require.NoError(t, CompareTrees(gen(t, spec).Root, gen(t, explicit).Root))
}

func TestGenerate_DupBlockIsDeterministic(t *testing.T) {
	spec := DatasetSpec{Files: 6, Profile: "large", DupRatio: 0.5, Seed: 4, DupBlock: 1 << 20}
	require.NoError(t, CompareTrees(gen(t, spec).Root, gen(t, spec).Root))
}

func TestGenerate_DupBlockSetsDuplicateUnit(t *testing.T) {
	const unit = 1 << 20
	ds := gen(t, DatasetSpec{Files: 10, Profile: "large", DupRatio: 1, Seed: 3, DupBlock: unit})
	seen := map[[32]byte]bool{}
	for _, f := range ds.Files {
		data, err := os.ReadFile(filepath.Join(ds.Root, f.Rel))
		require.NoError(t, err)
		for off := 0; off+unit <= len(data); off += unit {
			seen[sha256.Sum256(data[off:off+unit])] = true
		}
	}
	assert.NotEmpty(t, seen)
	assert.LessOrEqual(t, len(seen), poolBlocks, "every full 1MiB block should come from the pool")
}
