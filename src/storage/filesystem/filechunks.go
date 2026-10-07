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
	// location is invalid. ReadLocatedChunk then looks the chunk up again.
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
// the restore must report that chunk if it is still missing when read (and
// mark it, so the file is uploaded again) rather than silently produce a
// shorter file.
//
// The locations are a snapshot: compaction may move a chunk afterwards.
// ReadLocatedChunk handles that.
func (s *Store) LocateFileChunks(fileID string) ([]FileChunk, error) {
	var rows []fileChunkRow
	err := s.db.Raw("SELECT l.chunk_hash AS link_hash, l.`index` AS link_index, "+
		"c.hash, c.segment, c.`offset`, c.size "+
		"FROM file_data_chunk_records l "+
		"LEFT JOIN chunk_records c ON c.hash = l.chunk_hash "+
		"WHERE l.file_id = ? ORDER BY l.`index`, l.chunk_hash", fileID).
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
		// Same order as locate: a pending location (writable store only)
		// is newer than any row, e.g. a re-append repairing an invalid row.
		if loc, ok := s.pending.lookup(r.LinkHash); ok {
			c.loc = loc
		} else if r.Hash.Valid {
			c.loc, c.err = rowLocation(ChunkRecord{
				Hash:    r.Hash.String,
				Segment: r.Segment.Int64,
				Offset:  r.Offset.Int64,
				Size:    r.Size.Int64,
			})
		} else {
			c.err = storage.ErrChunkNotFound
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
//
// A snapshot entry that could not be located is not trusted either: a
// backup may have stored the chunk again, or StoreChunk repaired its invalid
// row, after the snapshot. It is looked up afresh with ReadChunk, so only a
// chunk that is still missing or broken when read is reported (and marked).
func (s *Store) ReadLocatedChunk(c FileChunk) ([]byte, error) {
	var sum [32]byte
	if len(c.Hash) != len(sum) {
		return nil, storage.ErrChunkNotFound
	}
	if c.err != nil {
		return s.ReadChunk(c.Hash)
	}
	copy(sum[:], c.Hash)
	data, err := s.readLocated(hex.EncodeToString(c.Hash), sum, c.loc)
	return data, classifyRead(err)
}
