package filesystem

import (
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage/pack"
)

// flushThreshold is how many chunk bytes may be appended but not yet flushed
// before StoreChunk flushes on its own. It bounds the un-fsynced data in the
// page cache and the size of the pending state. A variable so tests can lower
// it.
var flushThreshold int64 = 32 << 20

// insertBatchSize keeps each INSERT well below SQLite's bound-variable limit.
const insertBatchSize = 500

// pending holds what has been appended to the pack log but is not yet in the
// database: chunk locations and the links that reference them. Rows are only
// written after the log is fsynced, so the database never points at bytes a
// crash could lose. Until then lookups consult this state.
type pending struct {
	mu     sync.Mutex
	chunks map[string]pack.Location // hex hash -> location
	// links is append-only between flushes: a flush commits a prefix and then
	// drops exactly that prefix.
	links []FileDataChunkRecord
	bytes int64 // data bytes of chunks
}

func newPending() *pending {
	return &pending{chunks: make(map[string]pack.Location)}
}

func (p *pending) lookup(hexHash string) (pack.Location, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	loc, ok := p.chunks[hexHash]
	return loc, ok
}

// addChunk records a freshly appended chunk and returns the pending byte
// total. If two writers raced to append the same chunk, the first location
// wins and the other copy is dead space.
func (p *pending) addChunk(hexHash string, loc pack.Location) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.chunks[hexHash]; !ok {
		p.chunks[hexHash] = loc
		p.bytes += int64(loc.Size)
	}
	return p.bytes
}

func (p *pending) addLink(link FileDataChunkRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.links = append(p.links, link)
}

// snapshot copies the current pending state for one flush.
func (p *pending) snapshot() (map[string]pack.Location, []FileDataChunkRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	chunks := make(map[string]pack.Location, len(p.chunks))
	for h, loc := range p.chunks {
		chunks[h] = loc
	}
	links := append([]FileDataChunkRecord(nil), p.links...)
	return chunks, links
}

// drop removes a committed snapshot. Entries added meanwhile stay: a chunk in
// the snapshot stayed in pending until now, so addChunk could not replace it
// (new chunks have other keys), and new links were appended after the
// snapshot's prefix.
func (p *pending) drop(chunks map[string]pack.Location, nLinks int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for h, loc := range chunks {
		delete(p.chunks, h)
		p.bytes -= int64(loc.Size)
	}
	p.links = append([]FileDataChunkRecord(nil), p.links[nLinks:]...)
}

// flush makes everything appended so far durable and indexed: fsync the log,
// then commit the chunk rows and their links in one transaction. The snapshot
// leaves pending only after the commit, so a concurrent lookup always finds a
// chunk in one place or the other. On error nothing is dropped and the next
// flush retries; a failed fsync is sticky in the log, so the store keeps
// failing writes instead of claiming durability it does not have.
//
// flushMu serializes flushes; that is what makes "drop the snapshot" exact.
func (s *Store) flush() error {
	if s.log == nil {
		return nil // read-only store: nothing is ever pending
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	chunks, links := s.pending.snapshot()
	if len(chunks) == 0 && len(links) == 0 {
		return nil
	}
	if err := s.log.Sync(); err != nil {
		return fmt.Errorf("sync pack log: %w", err)
	}

	now := time.Now()
	rows := make([]ChunkRecord, 0, len(chunks))
	for h, loc := range chunks {
		rows = append(rows, ChunkRecord{
			Hash:      h,
			Size:      int64(loc.Size),
			Segment:   int64(loc.Segment),
			Offset:    loc.Offset,
			CreatedAt: now,
		})
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// A chunk row may already exist: a racing duplicate append was
		// re-added after an earlier flush, or StoreChunk re-appended a chunk
		// whose row held an invalid location. Overwriting the location is
		// right in both cases, since the new bytes were just fsynced; the
		// bytes the old row pointed at become dead space. Links are
		// idempotent, so they just skip duplicates.
		if len(rows) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "hash"}},
				DoUpdates: clause.AssignmentColumns([]string{"size", "segment", "offset"}),
			}).
				CreateInBatches(rows, insertBatchSize).Error; err != nil {
				return fmt.Errorf("insert chunk rows: %w", err)
			}
		}
		if len(links) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
				CreateInBatches(links, insertBatchSize).Error; err != nil {
				return fmt.Errorf("insert chunk links: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.pending.drop(chunks, len(links))
	return nil
}
