package filesystem

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/storage/pack"
	"gorm.io/gorm"
)

// locate finds a chunk's record: pending first (appended, not yet flushed),
// then the database. ok is false when the chunk is unknown.
func (s *Store) locate(hexHash string) (pack.Location, bool, error) {
	if loc, ok := s.pending.lookup(hexHash); ok {
		return loc, true, nil
	}
	var rec ChunkRecord
	err := s.db.Where("hash = ?", hexHash).Limit(1).Find(&rec).Error
	if err != nil {
		return pack.Location{}, false, fmt.Errorf("look up chunk: %w", err)
	}
	if rec.Hash == "" {
		return pack.Location{}, false, nil
	}
	loc, err := rowLocation(rec)
	if err != nil {
		return pack.Location{}, false, err
	}
	return loc, true, nil
}

// rowLocation converts a row to a Location, refusing values that would wrap
// around in the narrower Location fields: a damaged row must read as corrupt,
// not as some other record.
func rowLocation(rec ChunkRecord) (pack.Location, error) {
	if rec.Segment <= 0 || rec.Segment > math.MaxUint32 ||
		rec.Size < 0 || rec.Size > math.MaxUint32 || rec.Offset < 0 {
		return pack.Location{}, fmt.Errorf("%w: chunk %s has an invalid location (segment %d, offset %d, size %d)",
			pack.ErrCorrupt, rec.Hash, rec.Segment, rec.Offset, rec.Size)
	}
	return pack.Location{Segment: uint32(rec.Segment), Offset: rec.Offset, Size: uint32(rec.Size)}, nil
}

// ChunkExists reports storage.ErrChunkNotFound for an unknown chunk, and also
// for one whose row holds an impossible location: the client then sends the
// data again and StoreChunk repairs the row, instead of the whole backup
// stream failing on one damaged row.
func (s *Store) ChunkExists(chunkHash []byte) error {
	_, ok, err := s.locate(hex.EncodeToString(chunkHash))
	if errors.Is(err, pack.ErrCorrupt) {
		return storage.ErrChunkNotFound
	}
	if err != nil {
		return err
	}
	if !ok {
		return storage.ErrChunkNotFound
	}
	return nil
}

// StoreChunk appends the chunk to the pack log and remembers it as pending.
// It is not durable on return: FinalizeFileData (or the size trigger, or
// Close) flushes it, and only then does it get a database row.
func (s *Store) StoreChunk(chunkHash []byte, data []byte) error {
	sum := blake3.Sum256(data)
	if !bytes.Equal(chunkHash, sum[:]) {
		return fmt.Errorf("chunk hash mismatch")
	}
	if s.log == nil {
		return errors.New("store is read-only")
	}

	hexHash := hex.EncodeToString(chunkHash)
	_, known, err := s.locate(hexHash)
	// A row with an invalid location counts as unknown: append the chunk
	// again; the flush overwrites the row with the new location.
	if err != nil && !errors.Is(err, pack.ErrCorrupt) {
		return err
	}
	if known {
		return nil
	}

	loc, err := s.log.Append(sum, data)
	if err != nil {
		return fmt.Errorf("append chunk: %w", err)
	}
	if s.pending.addChunk(hexHash, loc) >= flushThreshold {
		return s.flush()
	}
	return nil
}

// LinkChunkToFileData records the link as pending; it is committed by the
// next flush together with (or after) the chunk row it references.
func (s *Store) LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error {
	if s.log == nil {
		return errors.New("store is read-only")
	}
	s.pending.addLink(FileDataChunkRecord{
		FileID:    fileID,
		ChunkHash: hex.EncodeToString(chunkHash),
		Index:     index,
	})
	return nil
}

// MarkChunkCorrupted drops a chunk that failed to read correctly (missing or
// otherwise unusable) and flags every file data that used it as damaged, so
// those files are uploaded afresh by the next backup while the loss stays
// visible. Its bytes stay in the segment as dead space for compaction to
// reclaim.
func (s *Store) MarkChunkCorrupted(chunkHash []byte) error {
	// Flush first so a pending row or link for this chunk cannot be
	// committed after the delete and bring it back.
	if err := s.flush(); err != nil {
		return err
	}
	hexHash := hex.EncodeToString(chunkHash)
	var d damage
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		d, err = dropChunk(tx, hexHash)
		return err
	})
	if err != nil {
		return err
	}
	d.log() // only once committed: a rolled-back drop damaged nothing
	return nil
}

// damage describes what one dropChunk call newly flagged.
type damage struct {
	hash    string
	flagged int64    // file data rows newly flagged damaged
	paths   []string // up to maxDamagedPaths of their paths
}

// maxDamagedPaths bounds the paths in the log line: a chunk shared by
// thousands of files must not produce a log line of that size.
const maxDamagedPaths = 5

// log reports the damage at Error. The Store has no logger of its own, so it
// uses the process default (bwfs sets it up). A chunk whose files were all
// flagged before is not reported again.
func (d damage) log() {
	if d.flagged == 0 {
		return
	}
	slog.Error("chunk marked corrupt",
		"chunk_hash", d.hash,
		"file_versions_damaged", d.flagged,
		"paths", d.paths)
}

// dropChunk deletes a chunk's row and links and flags every file data that
// used it as damaged. It is how both MarkChunkCorrupted and compaction react
// to unusable bytes.
//
// The file data is flagged, not deleted: deleting it made the loss invisible
// (restore listed no such file and reported success). Only this chunk's links
// go; the file's other links stay, because they are keyed by file_id and a
// healthy re-upload of the same file shares them. Rows flagged earlier keep
// their damaged_at, the time the damage was first found.
func dropChunk(tx *gorm.DB, hexHash string) (damage, error) {
	d := damage{hash: hexHash}
	// The dependents are selected by subquery, never collected into a Go
	// list: a chunk shared by more files than SQLite's bound-variable limit
	// (a zero block, one file on many hosts) must still be droppable. Both
	// statements run before the links go, while the links still name them.
	newlyDamaged := func() *gorm.DB {
		return tx.Model(&FileDataRecord{}).Where("damaged_at IS NULL AND file_id IN (?)",
			tx.Model(&FileDataChunkRecord{}).Select("file_id").Where("chunk_hash = ?", hexHash))
	}
	// Paths for the report, taken while "not yet flagged" still tells which
	// rows this call damages. Several contents of one path share it: list it
	// once.
	if err := newlyDamaged().Distinct("path").Order("path").Limit(maxDamagedPaths).
		Pluck("path", &d.paths).Error; err != nil {
		return d, fmt.Errorf("find dependent file data: %w", err)
	}
	res := newlyDamaged().Update("damaged_at", time.Now())
	if res.Error != nil {
		return d, fmt.Errorf("flag dependent file data damaged: %w", res.Error)
	}
	d.flagged = res.RowsAffected

	if err := tx.Where("chunk_hash = ?", hexHash).Delete(&FileDataChunkRecord{}).Error; err != nil {
		return d, fmt.Errorf("remove chunk links: %w", err)
	}
	if err := tx.Where("hash = ?", hexHash).Delete(&ChunkRecord{}).Error; err != nil {
		return d, fmt.Errorf("remove chunk record: %w", err)
	}
	return d, nil
}

// ReadChunk returns the chunk's data after the pack layer verified its hash.
// A damaged record yields an error wrapping pack.ErrCorrupt.
func (s *Store) ReadChunk(chunkHash []byte) ([]byte, error) {
	var sum [32]byte
	if len(chunkHash) != len(sum) {
		return nil, storage.ErrChunkNotFound
	}
	copy(sum[:], chunkHash)
	hexHash := hex.EncodeToString(chunkHash)

	loc, ok, err := s.locate(hexHash)
	if err != nil {
		return nil, classifyRead(err)
	}
	if !ok {
		return nil, storage.ErrChunkNotFound
	}
	data, err := s.readLocated(hexHash, sum, loc)
	return data, classifyRead(err)
}

// classifyRead marks errors that mean the chunk's bytes are lost for good
// (failed verification, damaged row, segment gone even after re-locating)
// with storage.ErrChunkCorrupt, keeping the pack error in the chain. Anything
// else -- an I/O or database error -- may be transient, and callers must not
// drop the chunk over it.
func classifyRead(err error) error {
	if errors.Is(err, pack.ErrCorrupt) || errors.Is(err, pack.ErrSegmentMissing) {
		return fmt.Errorf("%w: %w", storage.ErrChunkCorrupt, err)
	}
	return err
}

// readLocated reads the record at loc. If its segment is gone, compaction may
// have moved the chunk (updated the row, then removed the old segment) between
// our lookup and the read, so it looks the chunk up again and retries. It
// keeps doing so only while the location keeps changing, and at most
// maxReadAttempts times: a location that did not change means the segment
// really is missing.
func (s *Store) readLocated(hexHash string, sum [32]byte, loc pack.Location) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		data, err := pack.Read(s.packDir(), loc, sum)
		if err == nil || !errors.Is(err, pack.ErrSegmentMissing) || attempt == maxReadAttempts {
			return data, wrapRead(err)
		}
		if relocateHook != nil {
			relocateHook()
		}
		fresh, ok, lerr := s.locate(hexHash)
		if lerr != nil {
			return nil, lerr
		}
		if !ok {
			return nil, storage.ErrChunkNotFound // removed meanwhile
		}
		if fresh == loc {
			return nil, wrapRead(err) // not moved: the segment really is missing
		}
		loc = fresh
	}
}

// maxReadAttempts bounds how often readLocated reads a chunk that compaction
// keeps moving.
const maxReadAttempts = 3

// relocateHook, when set by a test, runs before readLocated re-locates.
var relocateHook func()

func wrapRead(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("read chunk: %w", err)
}
