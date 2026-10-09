package filesystem

import (
	"bytes"
	"fmt"
	mrand "math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/alex-sviridov/miniprotector/workload"
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

func reassemble(path string) ([]byte, error) {
	var out bytes.Buffer
	for c, err := range (FileInfo{path: path}).ChunkIterator() {
		if err != nil {
			return nil, err
		}
		out.Write(c.Data())
	}
	return out.Bytes(), nil
}

// Guards against scan-buffer sharing bugs: pooled buffers must never leak
// between files read sequentially or concurrently.
func TestCDC_PooledBuffersAcrossFiles(t *testing.T) {
	const nFiles = 6
	dir := t.TempDir()
	type tf struct {
		path string
		data []byte
	}
	mk := func(seed int64, n int) tf {
		p := filepath.Join(dir, fmt.Sprintf("f%d", seed))
		d := seededBytes(seed, n)
		require.NoError(t, os.WriteFile(p, d, 0o600))
		return tf{p, d}
	}

	var files []tf
	for i := 0; i < nFiles; i++ {
		files = append(files, mk(int64(100+i), MaxChunkSize*(i%3+1)+i*7919+1))
	}

	for _, f := range files {
		got, err := reassemble(f.path)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(f.data, got), "sequential %s", f.path)
	}

	const goroutines = 8
	errs := make(chan error, goroutines*nFiles)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range files {
				f := files[(i+g)%nFiles]
				got, err := reassemble(f.path)
				if err != nil {
					errs <- err
				} else if !bytes.Equal(f.data, got) {
					errs <- fmt.Errorf("goroutine %d: %s did not reassemble", g, f.path)
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
