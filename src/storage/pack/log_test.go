package pack

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"lukechampine.com/blake3"
)

func mustRead(t *testing.T, dir string, loc Location, hash [32]byte, want []byte) {
	t.Helper()
	got, err := Read(dir, loc, hash)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("Read(%+v) = %q, %v; want %q", loc, got, err, want)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	// Room for the magic plus one 100-byte record, but not two.
	l := openLog(t, dir, int64(segmentMagicSize)+HeaderSize+100+10)
	defer l.Close()

	d1, d2 := bytes.Repeat([]byte("a"), 100), bytes.Repeat([]byte("b"), 100)
	loc1, h1 := appendData(t, l, d1)
	loc2, h2 := appendData(t, l, d2)
	if loc1.Segment != 1 || loc2.Segment != 2 || loc2.Offset != int64(segmentMagicSize) {
		t.Fatalf("locations %+v %+v: second record should start segment 2", loc1, loc2)
	}
	if l.ActiveSegment() != 2 {
		t.Errorf("ActiveSegment = %d, want 2", l.ActiveSegment())
	}

	// A record larger than SegmentSize goes alone into a fresh segment.
	big := bytes.Repeat([]byte("c"), 500)
	loc3, h3 := appendData(t, l, big)
	if loc3.Segment != 3 || loc3.Offset != int64(segmentMagicSize) {
		t.Errorf("oversized record at %+v, want start of segment 3", loc3)
	}
	// ...and does not rotate again when it is the first record of an empty segment.
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}

	segs, err := Segments(dir)
	if err != nil || len(segs) != 3 || segs[0].ID != 1 || segs[2].ID != 3 {
		t.Fatalf("Segments = %+v, %v", segs, err)
	}
	if want := int64(segmentMagicSize) + HeaderSize + 100; segs[0].Size != want {
		t.Errorf("sealed segment size = %d, want %d", segs[0].Size, want)
	}
	mustRead(t, dir, loc1, h1, d1)
	mustRead(t, dir, loc2, h2, d2)
	mustRead(t, dir, loc3, h3, big)
}

func TestSegmentsIgnoresStrayFiles(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, 0)
	l.Close()
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "abc.pack"), []byte("x"), 0o644)
	segs, err := Segments(dir)
	if err != nil || len(segs) != 1 || segs[0].ID != 1 {
		t.Fatalf("Segments = %+v, %v", segs, err)
	}
}

func TestReopenContinuesActiveSegment(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, 0)
	d1 := []byte("before reopen")
	loc1, h1 := appendData(t, l, d1)
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	l.Close()

	l = openLog(t, dir, 0)
	defer l.Close()
	d2 := []byte("after reopen")
	loc2, h2 := appendData(t, l, d2)
	if want := loc1.Offset + HeaderSize + int64(len(d1)); loc2.Segment != 1 || loc2.Offset != want {
		t.Errorf("loc2 = %+v, want segment 1 offset %d", loc2, want)
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	mustRead(t, dir, loc1, h1, d1)
	mustRead(t, dir, loc2, h2, d2)
}

func TestRecovery(t *testing.T) {
	good1, good2 := []byte("good record one"), []byte("good record two")
	last := []byte("the last record")

	cases := []struct {
		name string
		// damage edits the segment file; it gets the valid end offset of good2.
		damage func(raw []byte, validEnd int64) []byte
		// keepsLast says the last record is still valid after the damage.
		keepsLast bool
	}{
		{"truncated mid-record", func(raw []byte, _ int64) []byte { return raw[:len(raw)-4] }, false},
		{"last record data flipped", func(raw []byte, _ int64) []byte { raw[len(raw)-1] ^= 0xff; return raw }, false},
		{"garbage appended", func(raw []byte, _ int64) []byte { return append(raw, "garbage bytes!!"...) }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			l := openLog(t, dir, 0)
			loc1, h1 := appendData(t, l, good1)
			loc2, h2 := appendData(t, l, good2)
			locLast, _ := appendData(t, l, last)
			if err := l.Sync(); err != nil {
				t.Fatal(err)
			}
			l.Close()
			validEnd := loc2.Offset + HeaderSize + int64(len(good2))
			if c.keepsLast {
				validEnd = locLast.Offset + HeaderSize + int64(len(last))
			}

			path := segmentPath(dir, 1)
			raw, _ := os.ReadFile(path)
			if err := os.WriteFile(path, c.damage(raw, validEnd), 0o644); err != nil {
				t.Fatal(err)
			}

			l = openLog(t, dir, 0)
			defer l.Close()
			if info, _ := os.Stat(path); info.Size() != validEnd {
				t.Errorf("file size after recovery = %d, want %d", info.Size(), validEnd)
			}
			mustRead(t, dir, loc1, h1, good1)
			mustRead(t, dir, loc2, h2, good2)

			next := []byte("appended after recovery")
			loc, h := appendData(t, l, next)
			if loc.Offset != validEnd {
				t.Errorf("new record at %d, want %d", loc.Offset, validEnd)
			}
			if err := l.Sync(); err != nil {
				t.Fatal(err)
			}
			mustRead(t, dir, loc, h, next)
		})
	}

	t.Run("bad header in last segment is recreated", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(segmentPath(dir, 1), []byte{1, 2, 3}, 0o644); err != nil {
			t.Fatal(err)
		}
		l := openLog(t, dir, 0)
		defer l.Close()
		data := []byte("fresh start")
		loc, h := appendData(t, l, data)
		if loc.Segment != 1 || loc.Offset != int64(segmentMagicSize) {
			t.Errorf("loc = %+v, want start of segment 1", loc)
		}
		if err := l.Sync(); err != nil {
			t.Fatal(err)
		}
		mustRead(t, dir, loc, h, data)
	})
}

func TestAppendRejectsOversizedData(t *testing.T) {
	l := openLog(t, t.TempDir(), 0)
	defer l.Close()
	data := make([]byte, 16<<20+1)
	if _, err := l.Append([32]byte{}, data); err == nil {
		t.Fatal("Append accepted data over 16 MiB")
	}
	if _, err := l.Append([32]byte{}, data[:16<<20]); err != nil {
		t.Fatalf("Append rejected exactly 16 MiB: %v", err)
	}
}

func TestRemoveSegment(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, int64(segmentMagicSize)+HeaderSize+10)
	appendData(t, l, []byte("0123456789"))
	appendData(t, l, []byte("abcdefghij")) // rotates into segment 2
	l.Close()

	if err := RemoveSegment(dir, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(segmentPath(dir, 1)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("segment 1 still present: %v", err)
	}
	if err := RemoveSegment(dir, 1); err != nil {
		t.Errorf("removing a missing segment = %v, want nil", err)
	}
}

func TestSyncSkipsFsyncWhenNothingNew(t *testing.T) {
	l := openLog(t, t.TempDir(), 0)
	defer l.Close()
	var calls atomic.Int32
	l.syncFile = func(f *os.File) error { calls.Add(1); return f.Sync() }

	appendData(t, l, []byte("data"))
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("fsync calls after first Sync = %d, want 1", calls.Load())
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("second Sync with no new appends fsynced again (%d calls)", calls.Load())
	}
	appendData(t, l, []byte("more"))
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("Sync after a new append: %d calls, want 2", calls.Load())
	}
}

func TestConcurrentAppendAndSync(t *testing.T) {
	dir := t.TempDir()
	// Small segments so rotation races with Sync too.
	l := openLog(t, dir, 4096)
	defer l.Close()

	type entry struct {
		loc  Location
		hash [32]byte
		data []byte
	}
	const writers, perWriter = 8, 50
	results := make([][]entry, writers)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWriter {
				data := bytes.Repeat([]byte{byte(w), byte(i)}, 50+i)
				hash := blake3.Sum256(data)
				loc, err := l.Append(hash, data)
				if err != nil {
					t.Error(err)
					return
				}
				results[w] = append(results[w], entry{loc, hash, data})
				if i%5 == 0 {
					if err := l.Sync(); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	for _, es := range results {
		for _, e := range es {
			mustRead(t, dir, e.loc, e.hash, e.data)
		}
	}
}

func TestSyncErrorIsSticky(t *testing.T) {
	l := openLog(t, t.TempDir(), 0)
	defer l.Close()
	boom := errors.New("disk on fire")
	l.syncFile = func(*os.File) error { return boom }

	appendData(t, l, []byte("data"))
	if err := l.Sync(); !errors.Is(err, boom) {
		t.Fatalf("Sync = %v, want %v", err, boom)
	}
	// Even if fsync would now succeed, the kernel may have dropped the dirty
	// pages, so the log must keep refusing.
	l.syncFile = (*os.File).Sync
	if _, err := l.Append([32]byte{}, []byte("x")); !errors.Is(err, boom) {
		t.Errorf("Append after failed sync = %v, want %v", err, boom)
	}
	if err := l.Sync(); !errors.Is(err, boom) {
		t.Errorf("Sync after failed sync = %v, want %v", err, boom)
	}
}

func TestRotationSyncFailureIsSticky(t *testing.T) {
	l := openLog(t, t.TempDir(), int64(segmentMagicSize)+HeaderSize+10)
	defer l.Close()
	boom := errors.New("disk on fire")
	l.syncFile = func(*os.File) error { return boom }

	appendData(t, l, []byte("0123456789"))
	if _, err := l.Append([32]byte{}, []byte("abcdefghij")); !errors.Is(err, boom) {
		t.Fatalf("Append that must rotate = %v, want %v", err, boom)
	}
	if err := l.Sync(); !errors.Is(err, boom) {
		t.Errorf("Sync after failed rotation = %v, want %v", err, boom)
	}
}
