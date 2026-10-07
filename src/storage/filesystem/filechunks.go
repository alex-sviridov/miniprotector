package filesystem

import (
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/storage/pack"
)

// FileChunk is one link of a file together with where its chunk was stored
// when LocateFileChunks ran. Read it with ReadLocatedChunk.
type FileChunk struct {
	Hash  []byte
	Index int64

	loc pack.Location
	// err is set when the chunk could not be located: storage.ErrChunkNotFound
	// for a link without a chunk row, or pack.ErrCorrupt for a row whose
	// location is invalid. ReadLocatedChunk returns it, classified like
	// ReadChunk's errors.
	err error
}

// fileChunkRow is one row of LocateFileChunks' join. The chunk columns are
// NULL when the link has no chunk row.
type fileChunkRow struct {
	LinkHash string         `gorm:"column:link_hash"`
	Index    int64          `gorm:"column:link_index"`
	Hash     sql.NullString `gorm:"column:hash"`
	Segment  sql.NullInt64  `gorm:"column:segment"`
	Offset   sql.NullInt64  `gorm:"column:offset"`
	Size     sql.NullInt64  `gorm:"column:size"`
}

// LocateFileChunks returns the file's chunks in link order, each with its
// location, using one query for the whole file. A restore would otherwise
// look every chunk up on its own, and that per-chunk query costs a large
// share of reading a 64 KiB chunk.
//
// A link whose chunk row is missing is kept, in its place, as a not-found
// entry: a backup may have linked a chunk that was dropped concurrently, and
// the restore must report that chunk (and mark it, so the file is uploaded
// again) rather than silently produce a shorter file.
//
// The locations are a snapshot: compaction may move a chunk afterwards.
// ReadLocatedChunk handles that.
func (s *Store) LocateFileChunks(fileID string) ([]FileChunk, error) {
	var rows []fileChunkRow
	err := s.db.Raw("SELECT l.chunk_hash AS link_hash, l.`index` AS link_index, "+
		"c.hash, c.segment, c.`offset`, c.size "+
		"FROM file_data_chunk_records l "+
		"LEFT JOIN chunk_records c ON c.hash = l.chunk_hash "+
		"WHERE l.file_id = ? ORDER BY l.`index`", fileID).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("locate file chunks: %w", err)
	}

	chunks := make([]FileChunk, len(rows))
	for i, r := range rows {
		hash, err := hex.DecodeString(r.LinkHash)
		if err != nil {
			return nil, fmt.Errorf("decode chunk hash %q: %w", r.LinkHash, err)
		}
		c := FileChunk{Hash: hash, Index: r.Index}
		switch {
		case r.Hash.Valid:
			c.loc, c.err = rowLocation(ChunkRecord{
				Hash:    r.Hash.String,
				Segment: r.Segment.Int64,
				Offset:  r.Offset.Int64,
				Size:    r.Size.Int64,
			})
		default:
			// Not in the database; a writable store may still hold it as
			// pending (a read-only one never does).
			loc, ok := s.pending.lookup(r.LinkHash)
			if ok {
				c.loc = loc
			} else {
				c.err = storage.ErrChunkNotFound
			}
		}
		chunks[i] = c
	}
	return chunks, nil
}

// ReadLocatedChunk is ReadChunk for a chunk LocateFileChunks already located:
// same hash verification, same errors. If the snapshot's segment is gone,
// compaction moved the chunk after the snapshot (a restore does not hold the
// store's guard), so readLocated looks the chunk up again in the database
// rather than trusting the stale location, and only a chunk whose row still
// points at the missing segment is reported as lost.
func (s *Store) ReadLocatedChunk(c FileChunk) ([]byte, error) {
	var sum [32]byte
	if len(c.Hash) != len(sum) {
		return nil, storage.ErrChunkNotFound
	}
	if c.err != nil {
		return nil, classifyRead(c.err)
	}
	copy(sum[:], c.Hash)
	data, err := s.readLocated(hex.EncodeToString(c.Hash), sum, c.loc)
	return data, classifyRead(err)
}
