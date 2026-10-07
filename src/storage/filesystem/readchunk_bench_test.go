package filesystem

import (
	"encoding/hex"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage/pack"
)

// Restore-path benchmarks: one finalized file of benchChunks chunks of
// benchChunkSize bytes, read back through a NewReadOnly store as
// restoreserver does. Each sub-benchmark times one part of ReadChunk so their
// shares of the whole can be compared. The writer drops the segments from the
// page cache after fsync, so a restore right after a backup reads from disk:
// FullCold measures that, the rest run warm (FullCold measures nothing
// different if TMPDIR is a tmpfs, which has no backing disk to read from). The parts do not add up: run
// alone, a lookup's working set stays in the CPU caches, interleaved with
// 64 KiB reads and hashing it does not, so InSitu times both halves inside
// one ReadChunk-shaped loop. Run with
//
//	go test -run '^$' -bench ReadChunk ./storage/filesystem/
const (
	benchFileID    = "fs://host:f:/bench:1"
	benchChunks    = 3000
	benchChunkSize = 64 << 10
)

type readBench struct {
	ro     *Store
	hashes [][32]byte
	hexes  []string
	locs   []pack.Location
}

func setupReadBench(b *testing.B) *readBench {
	b.Helper()
	dir := b.TempDir()
	w, err := New(dir)
	if err != nil {
		b.Fatal(err)
	}
	if err := w.CreateFileData(benchFileID, benchChunks*benchChunkSize); err != nil {
		b.Fatal(err)
	}
	rb := &readBench{}
	rng := rand.NewChaCha8([32]byte{1})
	data := make([]byte, benchChunkSize)
	for i := range benchChunks {
		_, _ = rng.Read(data)
		sum := blake3.Sum256(data)
		if err := w.StoreChunk(sum[:], data); err != nil {
			b.Fatal(err)
		}
		if err := w.LinkChunkToFileData(sum[:], benchFileID, int64(i)); err != nil {
			b.Fatal(err)
		}
		rb.hashes = append(rb.hashes, sum)
		rb.hexes = append(rb.hexes, hex.EncodeToString(sum[:]))
	}
	if err := w.FinalizeFileData(benchFileID, []byte{1}); err != nil {
		b.Fatal(err)
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}

	rb.ro, err = NewReadOnly(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { rb.ro.Close() })
	for _, h := range rb.hexes {
		loc, ok, err := rb.ro.locate(h)
		if err != nil || !ok {
			b.Fatalf("locate %s: ok=%v err=%v", h, ok, err)
		}
		rb.locs = append(rb.locs, loc)
	}
	return rb
}

// dropCache evicts every segment from the page cache, as the writer does
// after an fsync.
func (rb *readBench) dropCache(b *testing.B) {
	b.Helper()
	paths, err := filepath.Glob(filepath.Join(rb.ro.packDir(), "*.pack"))
	if err != nil {
		b.Fatal(err)
	}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			b.Fatal(err)
		}
		_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
		f.Close()
	}
}

func BenchmarkReadChunk(b *testing.B) {
	rb := setupReadBench(b)

	b.Run("FullCold", func(b *testing.B) {
		b.SetBytes(benchChunkSize)
		i := 0
		for b.Loop() {
			if i%benchChunks == 0 {
				b.StopTimer()
				rb.dropCache(b)
				b.StartTimer()
			}
			if _, err := rb.ro.ReadChunk(rb.hashes[i%benchChunks][:]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
	// Warm the page cache once so the remaining sub-benchmarks compare CPU
	// and syscall cost, not disk reads.
	for k := range benchChunks {
		if _, err := pack.Read(rb.ro.packDir(), rb.locs[k], rb.hashes[k]); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("Full", func(b *testing.B) {
		b.SetBytes(benchChunkSize)
		i := 0
		for b.Loop() {
			if _, err := rb.ro.ReadChunk(rb.hashes[i%benchChunks][:]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
	b.Run("LocateOnly", func(b *testing.B) {
		i := 0
		for b.Loop() {
			if _, _, err := rb.ro.locate(rb.hexes[i%benchChunks]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
	b.Run("PackReadOnly", func(b *testing.B) {
		b.SetBytes(benchChunkSize)
		i := 0
		for b.Loop() {
			k := i % benchChunks
			if _, err := pack.Read(rb.ro.packDir(), rb.locs[k], rb.hashes[k]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
	// Batched is the restore path: one LocateFileChunks per file (its cost
	// is spread over the file's chunks), then ReadLocatedChunk per chunk.
	b.Run("Batched", func(b *testing.B) {
		b.SetBytes(benchChunkSize)
		var chunks []FileChunk
		i := 0
		for b.Loop() {
			k := i % benchChunks
			if k == 0 {
				var err error
				if chunks, err = rb.ro.LocateFileChunks(benchFileID); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := rb.ro.ReadLocatedChunk(chunks[k]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
	b.Run("LocateFile", func(b *testing.B) {
		for b.Loop() {
			if _, err := rb.ro.LocateFileChunks(benchFileID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("InSitu", func(b *testing.B) {
		var locate, read time.Duration
		i := 0
		for b.Loop() {
			k := i % benchChunks
			t0 := time.Now()
			loc, _, err := rb.ro.locate(rb.hexes[k])
			if err != nil {
				b.Fatal(err)
			}
			t1 := time.Now()
			if _, err := pack.Read(rb.ro.packDir(), loc, rb.hashes[k]); err != nil {
				b.Fatal(err)
			}
			read += time.Since(t1)
			locate += t1.Sub(t0)
			i++
		}
		b.ReportMetric(float64(locate.Nanoseconds())/float64(b.N), "locate-ns/op")
		b.ReportMetric(float64(read.Nanoseconds())/float64(b.N), "read-ns/op")
	})
	b.Run("Blake3Only", func(b *testing.B) {
		b.SetBytes(benchChunkSize)
		data := make([]byte, benchChunkSize)
		for b.Loop() {
			_ = blake3.Sum256(data)
		}
	})
}
