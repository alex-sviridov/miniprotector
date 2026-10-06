package pack

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"lukechampine.com/blake3"
)

// appendData hashes data and appends it, failing the test on error.
func appendData(t *testing.T, l *Log, data []byte) (Location, [32]byte) {
	t.Helper()
	hash := blake3.Sum256(data)
	loc, err := l.Append(hash, data)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return loc, hash
}

func openLog(t *testing.T, dir string, size int64) *Log {
	t.Helper()
	l, err := Open(dir, Options{SegmentSize: size})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return l
}

func TestRoundTripAndConsecutiveOffsets(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, 0)
	defer l.Close()

	a, b := []byte("first chunk"), []byte("second, longer chunk")
	locA, hashA := appendData(t, l, a)
	locB, hashB := appendData(t, l, b)
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}

	if locA.Offset != 8 {
		t.Errorf("first record offset = %d, want 8 (after segment magic)", locA.Offset)
	}
	if want := locA.Offset + HeaderSize + int64(len(a)); locB.Offset != want {
		t.Errorf("second offset = %d, want %d", locB.Offset, want)
	}
	if locA.Segment != locB.Segment || locA.Size != uint32(len(a)) {
		t.Errorf("unexpected locations %+v %+v", locA, locB)
	}

	for _, c := range []struct {
		loc  Location
		hash [32]byte
		want []byte
	}{{locA, hashA, a}, {locB, hashB, b}} {
		got, err := Read(dir, c.loc, c.hash)
		if err != nil || !bytes.Equal(got, c.want) {
			t.Errorf("Read(%+v) = %q, %v; want %q", c.loc, got, err, c.want)
		}
	}
}

func TestReadErrors(t *testing.T) {
	dir := t.TempDir()
	l := openLog(t, dir, 0)
	data := []byte("some chunk data")
	loc, hash := appendData(t, l, data)
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	l.Close()
	path := filepath.Join(dir, "00000001.pack")

	t.Run("wrong expected hash", func(t *testing.T) {
		if _, err := Read(dir, loc, [32]byte{1}); !errors.Is(err, ErrCorrupt) {
			t.Errorf("err = %v, want ErrCorrupt", err)
		}
	})

	t.Run("flipped data byte", func(t *testing.T) {
		raw, _ := os.ReadFile(path)
		raw[loc.Offset+HeaderSize] ^= 0xff
		bad := t.TempDir()
		os.WriteFile(filepath.Join(bad, "00000001.pack"), raw, 0o644)
		if _, err := Read(bad, loc, hash); !errors.Is(err, ErrCorrupt) {
			t.Errorf("err = %v, want ErrCorrupt", err)
		}
	})

	t.Run("file shorter than record", func(t *testing.T) {
		raw, _ := os.ReadFile(path)
		short := t.TempDir()
		os.WriteFile(filepath.Join(short, "00000001.pack"), raw[:len(raw)-3], 0o644)
		if _, err := Read(short, loc, hash); !errors.Is(err, ErrCorrupt) {
			t.Errorf("err = %v, want ErrCorrupt", err)
		}
	})

	t.Run("size mismatch", func(t *testing.T) {
		wrong := loc
		wrong.Size++
		if _, err := Read(dir, wrong, hash); !errors.Is(err, ErrCorrupt) {
			t.Errorf("err = %v, want ErrCorrupt", err)
		}
	})

	t.Run("missing segment", func(t *testing.T) {
		missing := loc
		missing.Segment = 99
		if _, err := Read(dir, missing, hash); !errors.Is(err, ErrSegmentMissing) {
			t.Errorf("err = %v, want ErrSegmentMissing", err)
		}
	})
}
