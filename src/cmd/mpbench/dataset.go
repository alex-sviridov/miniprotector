package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// blockSize is the generator's unit of duplication. brfs uses content-defined
// chunking (average 64KB), so duplicate blocks only approximately map to shared
// chunks.
const blockSize = 64 * 1024

// poolBlocks is how many distinct blocks the duplicate pool holds.
const poolBlocks = 16

// baseTime is every generated file's initial mtime, fixed so file IDs are
// reproducible across runs.
var baseTime = time.Unix(1_700_000_000, 0)

type DatasetSpec struct {
	Dir      string
	Files    int
	Profile  string
	DupRatio float64
	Seed     uint64
	// Shift prefixes each non-sparse file with 1..4095 random bytes so pooled
	// blocks sit off 64KB alignment. It does not apply to the sparse profile.
	Shift bool
}

type DatasetFile struct {
	Rel  string
	Size int64
}

type Dataset struct {
	Root       string
	Files      []DatasetFile
	TotalBytes int64
}

func fillRandom(r *rand.Rand, b []byte) {
	for i := 0; i < len(b); i += 8 {
		v := r.Uint64()
		for j := 0; j < 8 && i+j < len(b); j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
}

func pickSize(r *rand.Rand, profile string) int64 {
	span := func(lo, hi int64) int64 { return lo + r.Int64N(hi-lo+1) }
	switch profile {
	case "small":
		return span(1<<10, 32<<10)
	case "large":
		return span(2<<20, 8<<20)
	case "sparse":
		return span(4<<20, 16<<20)
	default: // mixed
		switch x := r.Float64(); {
		case x < 0.70:
			return span(1<<10, 32<<10)
		case x < 0.95:
			return span(64<<10, 1<<20)
		default:
			return span(2<<20, 8<<20)
		}
	}
}

// filePath places file i at a random depth (0..3) in a small directory tree.
func filePath(r *rand.Rand, i int) string {
	depth := r.IntN(4)
	parts := make([]string, 0, depth+1)
	for d := 0; d < depth; d++ {
		parts = append(parts, fmt.Sprintf("d%d", r.IntN(4)))
	}
	parts = append(parts, fmt.Sprintf("f%05d.bin", i))
	return filepath.Join(parts...)
}

// Generate writes a deterministic dataset under spec.Dir. Each full 64KB
// block of a file is, with probability DupRatio, copied from a shared pool of
// poolBlocks blocks (so dedup is exercised within and across files),
// otherwise fresh seeded-random bytes. With spec.Shift each non-sparse file
// starts with a random 1..4095 byte prefix, moving the blocks off alignment.
func Generate(spec DatasetSpec) (*Dataset, error) {
	r := rand.New(rand.NewPCG(spec.Seed, spec.Seed^0x9e3779b97f4a7c15))
	pool := make([][]byte, poolBlocks)
	for i := range pool {
		pool[i] = make([]byte, blockSize)
		fillRandom(r, pool[i])
	}

	ds := &Dataset{Root: spec.Dir}
	for i := 0; i < spec.Files; i++ {
		size := pickSize(r, spec.Profile)
		rel := filePath(r, i)
		if spec.Profile == "sparse" {
			if err := writeSparse(r, filepath.Join(spec.Dir, rel), size); err != nil {
				return nil, err
			}
			ds.Files = append(ds.Files, DatasetFile{Rel: rel, Size: size})
			ds.TotalBytes += size
			continue
		}
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
			if end-off == blockSize && r.Float64() < spec.DupRatio {
				copy(blk, pool[r.IntN(poolBlocks)])
			} else {
				fillRandom(r, blk)
			}
		}
		full := filepath.Join(spec.Dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			return nil, err
		}
		if err := os.Chtimes(full, baseTime, baseTime); err != nil {
			return nil, err
		}
		ds.Files = append(ds.Files, DatasetFile{Rel: rel, Size: size + prefix})
		ds.TotalBytes += size + prefix
	}
	return ds, nil
}

// sparseDataRatio is the fraction of a sparse-profile file's blocks that hold
// data; the rest is a hole.
const sparseDataRatio = 0.1

// writeSparse creates a file of the given apparent size that is mostly hole:
// it is truncated to size and a random sparseDataRatio of its 64KB blocks are
// filled with seeded-random bytes.
func writeSparse(r *rand.Rand, full string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	f, err := os.Create(full)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	buf := make([]byte, blockSize)
	for off := int64(0); off < size; off += blockSize {
		if r.Float64() >= sparseDataRatio {
			continue
		}
		b := buf[:min(blockSize, size-off)]
		fillRandom(r, b)
		if _, err := f.WriteAt(b, off); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(full, baseTime, baseTime)
}

// Touch advances every file's mtime by two seconds. A file's ID embeds its
// whole-second mtime, so after Touch brfs treats each file as new, while every
// chunk is already stored: only hashes cross the wire.
func (d *Dataset) Touch() error {
	for _, f := range d.Files {
		p := filepath.Join(d.Root, f.Rel)
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		t := st.ModTime().Add(2 * time.Second)
		if err := os.Chtimes(p, t, t); err != nil {
			return err
		}
	}
	return nil
}

func hashTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", h.Sum(nil))
		return nil
	})
	return out, err
}

// CompareTrees returns an error describing every regular file that is missing
// from dst, extra in dst, or different between the two trees.
func CompareTrees(src, dst string) error {
	a, err := hashTree(src)
	if err != nil {
		return fmt.Errorf("hash %s: %w", src, err)
	}
	b, err := hashTree(dst)
	if err != nil {
		return fmt.Errorf("hash %s: %w", dst, err)
	}
	var diffs []string
	for rel, h := range a {
		switch hb, ok := b[rel]; {
		case !ok:
			diffs = append(diffs, "missing: "+rel)
		case hb != h:
			diffs = append(diffs, "differs: "+rel)
		}
	}
	for rel := range b {
		if _, ok := a[rel]; !ok {
			diffs = append(diffs, "unexpected: "+rel)
		}
	}
	if len(diffs) == 0 {
		return nil
	}
	sort.Strings(diffs)
	shown := diffs
	if len(shown) > 10 {
		shown = shown[:10]
	}
	return fmt.Errorf("trees differ (%d differences):\n  %s", len(diffs), strings.Join(shown, "\n  "))
}
