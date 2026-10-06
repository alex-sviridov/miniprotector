package filesystem

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

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

// MarkChunkCorrupted removes a chunk that failed to read correctly (missing
// or otherwise unusable) along with every DB record that depends on it, so
// affected files are treated as needing a fresh upload on the next backup.
// Its bytes stay in the segment as dead space for compaction to reclaim.
func (s *Store) MarkChunkCorrupted(chunkHash []byte) error {
	// Flush first so a pending row or link for this chunk cannot be
	// committed after the delete and bring it back.
	if err := s.flush(); err != nil {
		return err
	}
	hexHash := hex.EncodeToString(chunkHash)
	return s.db.Transaction(func(tx *gorm.DB) error { return dropChunk(tx, hexHash) })
}

// dropChunk deletes a chunk's row and links and invalidates every file data
// that used it, so those files are uploaded afresh by the next backup. It is
// how both MarkChunkCorrupted and compaction react to unusable bytes.
func dropChunk(tx *gorm.DB, hexHash string) error {
	var links []FileDataChunkRecord
	if err := tx.Where("chunk_hash = ?", hexHash).Find(&links).Error; err != nil {
		return fmt.Errorf("find files depending on chunk: %w", err)
	}

	if err := tx.Where("chunk_hash = ?", hexHash).Delete(&FileDataChunkRecord{}).Error; err != nil {
		return fmt.Errorf("remove chunk links: %w", err)
	}
	if err := tx.Where("hash = ?", hexHash).Delete(&ChunkRecord{}).Error; err != nil {
		return fmt.Errorf("remove chunk record: %w", err)
	}

	fileIDs := make([]string, len(links))
	for i, link := range links {
		fileIDs[i] = link.FileID
	}
	if len(fileIDs) > 0 {
		if err := tx.Where("file_id IN ?", fileIDs).Delete(&FileDataRecord{}).Error; err != nil {
			return fmt.Errorf("invalidate dependent file data: %w", err)
		}
	}
	return nil
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
		return nil, err
	}
	if !ok {
		return nil, storage.ErrChunkNotFound
	}
	return s.readLocated(hexHash, sum, loc)
}

// readLocated reads the record at loc. If its segment is gone, compaction may
// have moved the chunk (updated the row, then removed the old segment) between
// our lookup and the read, so it looks the chunk up once more and retries.
func (s *Store) readLocated(hexHash string, sum [32]byte, loc pack.Location) ([]byte, error) {
	data, err := pack.Read(s.packDir(), loc, sum)
	if err == nil || !errors.Is(err, pack.ErrSegmentMissing) {
		return data, wrapRead(err)
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
	data, err = pack.Read(s.packDir(), fresh, sum)
	return data, wrapRead(err)
}

func wrapRead(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("read chunk: %w", err)
}
