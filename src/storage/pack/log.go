package pack

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"lukechampine.com/blake3"
)

var errLogClosed = errors.New("pack: log is closed")

// Options configures a Log.
type Options struct {
	SegmentSize int64 // 0 means 256 MiB
	// SyncFile replaces fsync; nil means (*os.File).Sync. Only tests set it,
	// to inject fsync failures from outside the package.
	SyncFile func(*os.File) error
}

// Log appends records to the active segment, rotating to a new segment when
// the active one is full.
type Log struct {
	dir         string
	segmentSize int64

	// syncFile is fsync; a field so tests can count calls and inject failures.
	syncFile func(*os.File) error

	// syncMu serializes Sync calls so concurrent callers share one fsync.
	// Lock order: syncMu before mu.
	syncMu    sync.Mutex
	syncedSeq uint64 // guarded by syncMu: writes up to this seq are durable

	mu       sync.Mutex // guards everything below
	active   *os.File
	activeID uint32
	size     int64
	seq      uint64 // counts successful appends
	closed   bool
	// sealed holds rotated-out segments (already fsynced). They stay open
	// until the next Sync because a Sync that started before the rotation may
	// still be fsyncing one of them.
	sealed []*os.File
	// err is the first fsync/rotation failure. It is sticky: after a failed
	// fsync the kernel may have dropped the dirty pages, so a later successful
	// fsync would not prove the data is on disk.
	err error
}

// Open creates dir if needed, recovers the last segment from a possible crash
// and opens it for appending.
func Open(dir string, opts Options) (*Log, error) {
	if opts.SegmentSize == 0 {
		opts.SegmentSize = defaultSegmentSize
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &Log{
		dir:         dir,
		segmentSize: opts.SegmentSize,
		syncFile:    (*os.File).Sync,
	}
	if opts.SyncFile != nil {
		l.syncFile = opts.SyncFile
	}

	segs, err := Segments(dir)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		if err := l.createSegment(1); err != nil {
			return nil, err
		}
		return l, nil
	}
	last := segs[len(segs)-1].ID
	f, size, err := recoverSegment(dir, last, l.syncFile)
	if err != nil {
		return nil, err
	}
	if f == nil {
		// The file is no bigger than the magic: the crash happened while the
		// segment was being created, so no record in it was ever acknowledged.
		if err := os.Remove(segmentPath(dir, last)); err != nil {
			return nil, err
		}
		if err := l.createSegment(last); err != nil {
			return nil, err
		}
		return l, nil
	}
	l.active, l.activeID, l.size = f, last, size
	return l, nil
}

// createSegment makes a new segment file with its magic and fsyncs the
// directory so the file itself survives a crash. O_EXCL guards against ever
// overwriting existing data.
func (l *Log) createSegment(id uint32) error {
	f, err := os.OpenFile(segmentPath(l.dir, id), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt([]byte(segmentMagic), 0); err != nil {
		f.Close()
		return err
	}
	if err := l.syncFile(f); err != nil {
		f.Close()
		return err
	}
	if err := syncDir(l.dir); err != nil {
		f.Close()
		return err
	}
	l.active, l.activeID, l.size = f, id, int64(segmentMagicSize)
	return nil
}

// recoverSegment scans a segment from the start, verifying every record, and
// truncates the file after the last good one. It returns a nil file when the
// segment is no bigger than its magic (crash while it was being created).
// Earlier segments were fsynced when sealed, so only the last one can hold a
// torn tail.
//
// Only a damaged tail (short read, bad record magic, absurd length, hash
// mismatch) is treated as a crash artifact and cut off. Any other read error,
// or a bad segment magic on a file that holds data, is returned instead:
// truncating then could destroy good records.
func recoverSegment(dir string, id uint32, syncFile func(*os.File) error) (*os.File, int64, error) {
	f, err := os.OpenFile(segmentPath(dir, id), os.O_RDWR, 0)
	if err != nil {
		return nil, 0, err
	}
	fail := func(err error) (*os.File, int64, error) {
		f.Close()
		return nil, 0, err
	}

	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if info.Size() <= int64(segmentMagicSize) {
		f.Close()
		return nil, 0, nil
	}

	r := bufio.NewReaderSize(f, 1<<20)
	magic := make([]byte, segmentMagicSize)
	if _, err := io.ReadFull(r, magic); err != nil {
		return fail(err)
	}
	if string(magic) != segmentMagic {
		return fail(fmt.Errorf("%w: segment %d has a bad magic", ErrCorrupt, id))
	}

	valid := int64(segmentMagicSize)
	header := make([]byte, HeaderSize)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if isShortRead(err) {
				break
			}
			return fail(err)
		}
		length := leUint32(header[4:8])
		if string(header[:4]) != recordMagic || length > maxRecordSize {
			break
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(r, data); err != nil {
			if isShortRead(err) {
				break
			}
			return fail(err)
		}
		if string(header[8:]) != string(sumBytes(data)) {
			break
		}
		valid += HeaderSize + int64(length)
	}

	if info.Size() != valid {
		if err := f.Truncate(valid); err != nil {
			return fail(err)
		}
		if err := syncFile(f); err != nil {
			return fail(err)
		}
	}
	return f, valid, nil
}

// isShortRead reports whether err just means the file ended mid-record.
func isShortRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func sumBytes(data []byte) []byte {
	sum := blake3.Sum256(data)
	return sum[:]
}

// Append writes one record and returns where it landed. The caller supplies
// the hash; the Store above verifies it against the data.
func (l *Log) Append(hash [32]byte, data []byte) (Location, error) {
	if len(data) > maxRecordSize {
		return Location{}, fmt.Errorf("pack: record of %d bytes exceeds the %d byte limit", len(data), maxRecordSize)
	}
	recLen := int64(HeaderSize + len(data))

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Location{}, errLogClosed
	}
	if l.err != nil {
		return Location{}, l.err
	}

	// A record never straddles segments. An empty segment takes any record,
	// so one larger than SegmentSize still lands (alone) in a fresh segment.
	if l.size > int64(segmentMagicSize) && l.size+recLen > l.segmentSize {
		if err := l.rotate(); err != nil {
			l.err = err
			return Location{}, err
		}
	}

	rec := append(encodeHeader(hash, len(data)), data...)
	if _, err := l.active.WriteAt(rec, l.size); err != nil {
		// Not sticky: size is not advanced, so partial bytes are overwritten by the
		// next append or truncated by recovery; writeback errors surface at fsync,
		// which is sticky.
		return Location{}, fmt.Errorf("pack: write: %w", err)
	}
	loc := Location{Segment: l.activeID, Offset: l.size, Size: uint32(len(data))}
	l.size += recLen
	l.seq++
	return loc, nil
}

// rotate seals the active segment and starts the next one. The sealed segment
// is fsynced first so that recovery, which only inspects the last segment, can
// trust every earlier one. Caller holds mu.
func (l *Log) rotate() error {
	if err := l.syncFile(l.active); err != nil {
		return fmt.Errorf("pack: sync sealed segment: %w", err)
	}
	old := l.active
	if err := l.createSegment(l.activeID + 1); err != nil {
		return err // old is still l.active; Close will close it
	}
	l.sealed = append(l.sealed, old)
	return nil
}

// ActiveSegment returns the ID of the segment currently being appended to.
func (l *Log) ActiveSegment() uint32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.activeID
}

// Sync makes everything appended so far durable. Concurrent callers share
// one fsync, and a caller whose writes an earlier Sync already covered returns
// without touching the disk.
func (l *Log) Sync() error {
	l.syncMu.Lock()
	defer l.syncMu.Unlock()

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return errLogClosed
	}
	if l.err != nil {
		defer l.mu.Unlock()
		return l.err
	}
	file, seq, sealed := l.active, l.seq, l.sealed
	if l.syncedSeq >= seq {
		l.mu.Unlock()
		return nil
	}
	l.sealed = nil
	l.mu.Unlock()

	// fsync outside mu so appends are not blocked while the disk works. Writes
	// that land meanwhile get a later seq and are covered by the next Sync.
	if err := l.syncFile(file); err != nil {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.err = fmt.Errorf("pack: fsync: %w", err)
		closeAll(sealed)
		return l.err
	}
	l.syncedSeq = seq
	closeAll(sealed)
	dropPageCache(file)
	return nil
}

// Close closes the open segments. It does not sync; call Sync first.
func (l *Log) Close() error {
	l.syncMu.Lock()
	defer l.syncMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	closeAll(l.sealed)
	l.sealed = nil
	return l.active.Close()
}

func closeAll(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
}
