package filesystem

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage/pack"
)

// compactBelow is the live fraction under which a sealed segment is
// compacted. Copying a segment costs its live bytes and frees its whole size,
// so below one half every copied byte frees more than one byte.
const compactBelow = 0.5

// reclaimBatchSize bounds one compaction batch for the startup Vacuum, which
// has no caller-supplied batch size.
const reclaimBatchSize = 1000

// segmentPlan is what reclaimSegments decided to do with one sealed segment.
type segmentPlan struct {
	id      uint32
	size    int64 // file size on disk
	compact bool  // false: no rows reference it, just remove the file
}

// reclaimSegments frees disk space held by dead records: sealed segments no
// row references are removed, and sealed segments whose live bytes are below
// compactBelow of their size are compacted -- their live records are copied
// to the active segment and the old file removed. The active segment is never
// touched. bytes is the size of the removed files minus the bytes copied.
//
// Every step is one inBatch transaction under the exclusive guard, and ctx is
// checked between batches. Crash safety comes from the ordering inside a
// batch: copy, fsync the log, then commit the new locations; a segment file
// is removed only once a committed transaction shows it has no rows. A crash
// therefore leaves at worst duplicate dead bytes in the active segment, or a
// segment with no rows that the next run removes.
func (s *Store) reclaimSegments(ctx context.Context, batchSize int) (removed, compacted, bytes int64, err error) {
	if s.log == nil {
		return 0, 0, 0, nil // read-only store: it never writes segments
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, 0, err
	}
	var plans []segmentPlan
	// Planning happens under the guard, after inBatch's flush: pending is
	// empty, so every appended chunk has its row and a sealed segment with no
	// rows really is dead. Chunks appended later land in the segment active now
	// or a newer one, which the plan excludes, so the plan stays valid while
	// the batches below run.
	if err := s.inBatch(func(tx *gorm.DB) error {
		var err error
		plans, err = s.planSegments(tx)
		return err
	}); err != nil {
		return 0, 0, 0, err
	}

	// Segments that could not be read are skipped so that one bad sector
	// does not stop the rest of the reclaim; they are reported at the end.
	var skipped []error
	for _, p := range plans {
		if p.compact {
			copied, err := s.compactSegment(ctx, p.id, batchSize)
			bytes -= copied
			var readErr *segmentReadError
			if errors.As(err, &readErr) {
				skipped = append(skipped, err)
				continue
			}
			if err != nil {
				return removed, compacted, bytes, err
			}
		}
		if err := ctx.Err(); err != nil {
			return removed, compacted, bytes, err
		}
		gone, err := s.removeIfEmpty(p.id)
		if err != nil {
			return removed, compacted, bytes, err
		}
		if !gone {
			continue // a record could not be moved; the next run retries
		}
		bytes += p.size
		if p.compact {
			compacted++
		} else {
			removed++
		}
	}
	if len(skipped) > 0 {
		return removed, compacted, bytes, fmt.Errorf("skipped %d unreadable segment(s): %w", len(skipped), errors.Join(skipped...))
	}
	return removed, compacted, bytes, nil
}

// segmentReadError is a compaction read failure that is not corruption (for
// example EIO or EACCES). Its batch is rolled back and the segment is left as
// it is: dropping chunks over an error that may be transient would lose data.
type segmentReadError struct {
	id  uint32
	err error
}

func (e *segmentReadError) Error() string {
	return fmt.Sprintf("segment %d: read: %v", e.id, e.err)
}

func (e *segmentReadError) Unwrap() error { return e.err }

// planSegments compares each sealed segment's live bytes (the records rows
// still point at) with its file size.
func (s *Store) planSegments(tx *gorm.DB) ([]segmentPlan, error) {
	var stats []struct {
		Segment int64
		Live    int64
	}
	if err := tx.Model(&ChunkRecord{}).
		Select("segment, SUM(? + size) AS live", pack.HeaderSize).
		Group("segment").Scan(&stats).Error; err != nil {
		return nil, fmt.Errorf("segment stats: %w", err)
	}
	live := make(map[int64]int64, len(stats))
	for _, st := range stats {
		live[st.Segment] = st.Live
	}

	segs, err := pack.Segments(s.packDir())
	if err != nil {
		return nil, fmt.Errorf("list segments: %w", err)
	}
	active := s.log.ActiveSegment()
	var plans []segmentPlan
	for _, seg := range segs {
		// Newer than active cannot exist in a healthy store; never touch it.
		if seg.ID >= active {
			continue
		}
		l, hasRows := live[int64(seg.ID)]
		switch {
		case !hasRows:
			plans = append(plans, segmentPlan{id: seg.ID, size: seg.Size})
		case float64(l) < compactBelow*float64(seg.Size):
			plans = append(plans, segmentPlan{id: seg.ID, size: seg.Size, compact: true})
		}
	}
	return plans, nil
}

// compactSegment moves the segment's live records to the active segment in
// batches of batchSize rows and returns the bytes copied.
func (s *Store) compactSegment(ctx context.Context, id uint32, batchSize int) (copied int64, err error) {
	for {
		if err := ctx.Err(); err != nil {
			return copied, err
		}
		var n int
		var batchCopied int64
		var dropped []damage
		err := s.inBatch(func(tx *gorm.DB) error {
			var err error
			n, batchCopied, dropped, err = s.moveBatch(tx, id, batchSize)
			return err
		})
		if err != nil {
			return copied, err
		}
		for _, d := range dropped {
			d.log(s.logOrDefault()) // after the commit, like MarkChunkCorrupted
		}
		copied += batchCopied
		if n < batchSize {
			return copied, nil
		}
	}
}

// moveBatch copies up to batchSize records out of segment id and points their
// rows at the copies. It returns how many rows it handled (moved or dropped
// as corrupt), the bytes copied, and the damage the dropped ones caused.
func (s *Store) moveBatch(tx *gorm.DB, id uint32, batchSize int) (int, int64, []damage, error) {
	var rows []ChunkRecord
	if err := tx.Where("segment = ?", id).Order("`offset`").Limit(batchSize).Find(&rows).Error; err != nil {
		return 0, 0, nil, fmt.Errorf("list segment %d rows: %w", id, err)
	}

	type move struct {
		hash string
		loc  pack.Location
	}
	var moves []move
	var copied int64
	var dropped []damage
	for _, row := range rows {
		data, sum, err := s.readRow(row)
		if errors.Is(err, pack.ErrCorrupt) {
			// The bytes are gone for good; treat it exactly as a restore
			// that hit a corrupt chunk would: its files are flagged damaged
			// and get re-uploaded by the next backup.
			d, err := dropChunk(tx, row.Hash)
			if err != nil {
				return 0, 0, nil, err
			}
			dropped = append(dropped, d)
			continue
		}
		if err != nil {
			return 0, 0, nil, &segmentReadError{id: id, err: err}
		}
		loc, err := s.log.Append(sum, data)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("compact append: %w", err)
		}
		moves = append(moves, move{row.Hash, loc})
		copied += pack.HeaderSize + int64(loc.Size)
	}

	// The copies must be durable before any row points at them.
	if len(moves) > 0 {
		if err := s.log.Sync(); err != nil {
			return 0, 0, nil, fmt.Errorf("sync pack log: %w", err)
		}
	}
	for _, m := range moves {
		if err := tx.Model(&ChunkRecord{}).Where("hash = ?", m.hash).
			Updates(map[string]any{"segment": int64(m.loc.Segment), "offset": m.loc.Offset}).Error; err != nil {
			return 0, 0, nil, fmt.Errorf("update chunk location: %w", err)
		}
	}
	return len(rows), copied, dropped, nil
}

// readRow reads and verifies the record a row points at. Anything wrong with
// the row or the bytes is reported as pack.ErrCorrupt.
func (s *Store) readRow(row ChunkRecord) ([]byte, [32]byte, error) {
	var sum [32]byte
	raw, err := hex.DecodeString(row.Hash)
	if err != nil || len(raw) != len(sum) {
		return nil, sum, fmt.Errorf("%w: chunk row has a malformed hash %q", pack.ErrCorrupt, row.Hash)
	}
	copy(sum[:], raw)
	loc, err := rowLocation(row)
	if err != nil {
		return nil, sum, err
	}
	data, err := pack.Read(s.packDir(), loc, sum)
	return data, sum, err
}

// removeIfEmpty deletes the segment file if, in a committed view taken under
// the guard, no row references it. Readers that looked a chunk up before its
// move committed find the old segment gone and re-locate (readLocated).
func (s *Store) removeIfEmpty(id uint32) (bool, error) {
	var gone bool
	err := s.inBatch(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&ChunkRecord{}).Where("segment = ?", id).Count(&n).Error; err != nil {
			return fmt.Errorf("count segment %d rows: %w", id, err)
		}
		if n > 0 {
			return nil
		}
		if err := pack.RemoveSegment(s.packDir(), id); err != nil {
			return fmt.Errorf("remove segment %d: %w", id, err)
		}
		gone = true
		return nil
	})
	return gone, err
}
