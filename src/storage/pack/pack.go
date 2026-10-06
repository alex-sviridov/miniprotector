// Package pack stores chunks in append-only segment files.
//
// A segment file is an 8-byte magic followed by records back to back:
//
//	"MPKR" (4) | data length uint32 LE (4) | BLAKE3-256 of data (32) | data
//
// Writers go through Log; readers use the package-level Read, which opens the
// segment per call and verifies the header and the hash.
package pack

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lukechampine.com/blake3"
)

const (
	segmentMagic = "MPKSEG01"
	recordMagic  = "MPKR"

	segmentMagicSize = len(segmentMagic)

	// HeaderSize is the record header size; the on-disk record length is
	// HeaderSize + Size.
	HeaderSize = 40

	// maxRecordSize bounds a record so a corrupt length field can never
	// trigger a huge allocation.
	maxRecordSize = 16 << 20

	defaultSegmentSize = 256 << 20
)

var (
	// ErrCorrupt means the bytes on disk do not match the expected record.
	ErrCorrupt = errors.New("pack: corrupt record")
	// ErrSegmentMissing means the segment file does not exist.
	ErrSegmentMissing = errors.New("pack: segment missing")
)

// Location addresses one record. Offset is the record start (its header);
// Size is the data length.
type Location struct {
	Segment uint32
	Offset  int64
	Size    uint32
}

// SegmentInfo describes one segment file on disk.
type SegmentInfo struct {
	ID   uint32
	Size int64
}

func segmentPath(dir string, id uint32) string {
	return filepath.Join(dir, fmt.Sprintf("%010d.pack", id))
}

// encodeHeader builds the 40-byte record header.
func encodeHeader(hash [32]byte, size int) []byte {
	h := make([]byte, HeaderSize)
	copy(h, recordMagic)
	binary.LittleEndian.PutUint32(h[4:8], uint32(size))
	copy(h[8:], hash[:])
	return h
}

// Read returns the data at loc after verifying the record header and that the
// data hashes to the expected hash.
func Read(dir string, loc Location, hash [32]byte) ([]byte, error) {
	f, err := os.Open(segmentPath(dir, loc.Segment))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: segment %d", ErrSegmentMissing, loc.Segment)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if loc.Size > maxRecordSize || loc.Offset < 0 {
		return nil, fmt.Errorf("%w: bad location %+v", ErrCorrupt, loc)
	}
	buf := make([]byte, HeaderSize+int(loc.Size))
	if _, err := f.ReadAt(buf, loc.Offset); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: short record at segment %d offset %d", ErrCorrupt, loc.Segment, loc.Offset)
		}
		return nil, err
	}

	if string(buf[:4]) != recordMagic ||
		binary.LittleEndian.Uint32(buf[4:8]) != loc.Size ||
		!bytes.Equal(buf[8:HeaderSize], hash[:]) {
		return nil, fmt.Errorf("%w: bad header at segment %d offset %d", ErrCorrupt, loc.Segment, loc.Offset)
	}
	data := buf[HeaderSize:]
	if blake3.Sum256(data) != hash {
		return nil, fmt.Errorf("%w: hash mismatch at segment %d offset %d", ErrCorrupt, loc.Segment, loc.Offset)
	}
	return data, nil
}

// Segments lists the segment files in dir in ascending ID order. Other files
// are ignored. A missing directory yields an empty list.
func Segments(dir string) ([]SegmentInfo, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var segs []SegmentInfo
	for _, e := range entries {
		id, ok := parseSegmentName(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		segs = append(segs, SegmentInfo{ID: id, Size: info.Size()})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].ID < segs[j].ID })
	return segs, nil
}

// parseSegmentName accepts exactly the names segmentPath produces.
func parseSegmentName(name string) (uint32, bool) {
	stem, ok := strings.CutSuffix(name, ".pack")
	if !ok {
		return 0, false
	}
	var id uint32
	if _, err := fmt.Sscanf(stem, "%d", &id); err != nil || id == 0 {
		return 0, false
	}
	if name != filepath.Base(segmentPath("", id)) {
		return 0, false
	}
	return id, true
}

// RemoveSegment deletes a segment file and fsyncs the directory so the removal
// survives a crash. Removing a segment that is already gone is not an error.
func RemoveSegment(dir string, id uint32) error {
	if err := os.Remove(segmentPath(dir, id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(dir)
}

// syncDir fsyncs a directory so file creations and removals in it are durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func leUint32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
