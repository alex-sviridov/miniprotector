package filesystem

import (
	"context"
	"os"
	"time"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

// BeginBackupOp takes the shared side of the operation guard for one
// backup-stream message and returns the function that releases it.
// CleanupExpired and VacuumOnline take the exclusive side for each bounded
// batch, so a batch never interleaves with a handler call. That is what makes
// scheduled GC safe next to live backups: every invariant a backup relies on
// (chunk exists -> link it; file known -> record its version; store chunk ->
// record -> link; finalize -> record version) is completed inside one
// handler call, and a batch can neither start in the middle of one nor
// observe it half done.
func (s *Store) BeginBackupOp() func() {
	s.opGuard.RLock()
	return s.opGuard.RUnlock
}

// inBatch runs fn in one transaction under the exclusive guard.
func (s *Store) inBatch(fn func(tx *gorm.DB) error) error {
	s.opGuard.Lock()
	defer s.opGuard.Unlock()
	return s.db.Transaction(fn)
}

// expiredPredicate selects versions CleanupExpired may delete: a real
// expire_at that has passed, and a job that is not still running -- a short
// retention must never delete versions out from under an in_progress job,
// whose BackupCommit would then see a hash mismatch and fail the whole run.
const expiredPredicate = "expire_at IS NOT NULL AND expire_at <= ? AND " +
	"job_id NOT IN (SELECT job_id FROM backup_job_records WHERE status = ?)"

// CleanupExpired deletes expired file versions in batches of at most
// batchSize, each batch its own short transaction under the exclusive guard,
// and records every deletion in the deletion log (see deleteVersions). Raw
// file data and chunks are reclaimed later by VacuumOnline. With dryRun it
// only counts what it would delete.
func (s *Store) CleanupExpired(ctx context.Context, now time.Time, batchSize int, dryRun bool) (*storage.CleanupResult, error) {
	res := &storage.CleanupResult{DryRun: dryRun}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if dryRun {
		var n int64
		err := s.db.WithContext(ctx).Model(&FileVersionRecord{}).
			Where(expiredPredicate, now.Unix(), storage.JobStatusInProgress).
			Count(&n).Error
		res.VersionsExpired = n
		return res, err
	}
	for {
		var n int64
		err := s.inBatch(func(tx *gorm.DB) error {
			var seqs []int64
			if err := tx.Model(&FileVersionRecord{}).
				Where(expiredPredicate, now.Unix(), storage.JobStatusInProgress).
				Order("expire_at ASC").
				Limit(batchSize).
				Pluck("seq", &seqs).Error; err != nil {
				return err
			}
			if len(seqs) == 0 {
				return nil
			}
			var err error
			n, err = deleteVersions(tx, "seq IN ?", seqs)
			return err
		})
		res.VersionsExpired += n
		if err != nil {
			return res, err
		}
		if n < int64(batchSize) {
			return res, nil
		}
		if err := ctx.Err(); err != nil {
			return res, err
		}
	}
}

// batchLoop repeatedly runs step (one batch under the exclusive guard) until
// it reports fewer than batchSize rows, or ctx is cancelled between batches.
func (s *Store) batchLoop(ctx context.Context, batchSize int, step func(tx *gorm.DB) (int64, error)) (int64, error) {
	var total int64
	for {
		var n int64
		err := s.inBatch(func(tx *gorm.DB) error {
			var err error
			n, err = step(tx)
			return err
		})
		total += n
		if err != nil {
			return total, err
		}
		if n < int64(batchSize) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// VacuumOnline reclaims what no file version references any more, safely on
// a store that is serving backups: the same four DB-driven steps as Vacuum,
// each in bounded batches under the exclusive guard, and no disk walk (the
// chunk files to delete are exactly the hashes whose records are removed;
// strays and crash-leftover temp files stay Vacuum's startup job).
//
// Incomplete file data is only treated as abandoned after incompleteGrace,
// because unlike at startup a file may legitimately still be transferring.
// A file in transfer is otherwise protected by its own rows: its FileData
// (checksum still NULL) is never "orphaned", and its chunk links keep its
// chunks referenced.
func (s *Store) VacuumOnline(ctx context.Context, batchSize int, incompleteGrace time.Duration) (*storage.VacuumResult, error) {
	res := &storage.VacuumResult{}
	var err error

	cutoff := time.Now().Add(-incompleteGrace)
	res.IncompleteFileData, err = s.batchLoop(ctx, batchSize, func(tx *gorm.DB) (int64, error) {
		var uuids []string
		if err := tx.Model(&FileDataRecord{}).
			Where("checksum IS NULL AND created_at < ?", cutoff).
			Limit(batchSize).Pluck("uuid", &uuids).Error; err != nil || len(uuids) == 0 {
			return 0, err
		}
		r := tx.Where("uuid IN ?", uuids).Delete(&FileDataRecord{})
		return r.RowsAffected, r.Error
	})
	if err != nil {
		return res, err
	}

	res.OrphanedFileDataRemoved, err = s.batchLoop(ctx, batchSize, func(tx *gorm.DB) (int64, error) {
		var uuids []string
		if err := tx.Model(&FileDataRecord{}).
			Where("checksum IS NOT NULL AND file_id NOT IN (SELECT object_id FROM file_version_records)").
			Limit(batchSize).Pluck("uuid", &uuids).Error; err != nil || len(uuids) == 0 {
			return 0, err
		}
		r := tx.Where("uuid IN ?", uuids).Delete(&FileDataRecord{})
		return r.RowsAffected, r.Error
	})
	if err != nil {
		return res, err
	}

	// A file_id can be shared by several FileData attempts, so a chunk link
	// is only orphaned once none of them remain.
	res.OrphanedChunkLinksRemoved, err = s.batchLoop(ctx, batchSize, func(tx *gorm.DB) (int64, error) {
		r := tx.Exec("DELETE FROM file_data_chunk_records WHERE rowid IN ("+
			"SELECT rowid FROM file_data_chunk_records "+
			"WHERE file_id NOT IN (SELECT file_id FROM file_data_records) LIMIT ?)", batchSize)
		return r.RowsAffected, r.Error
	})
	if err != nil {
		return res, err
	}

	res.OrphanedChunksRemoved, err = s.batchLoop(ctx, batchSize, func(tx *gorm.DB) (int64, error) {
		var orphans []ChunkRecord
		if err := tx.Where("hash NOT IN (SELECT chunk_hash FROM file_data_chunk_records)").
			Limit(batchSize).Find(&orphans).Error; err != nil || len(orphans) == 0 {
			return 0, err
		}
		hashes := make([]string, len(orphans))
		for i, o := range orphans {
			hashes[i] = o.Hash
		}
		if err := tx.Where("hash IN ?", hashes).Delete(&ChunkRecord{}).Error; err != nil {
			return 0, err
		}
		// Files go while the exclusive guard is still held, so a concurrent
		// backup can't have just decided this chunk "already exists".
		for _, o := range orphans {
			if err := os.Remove(s.chunkPath(o.Hash)); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
			res.BytesReclaimed += o.Size
		}
		return int64(len(orphans)), nil
	})
	if err != nil {
		return res, err
	}

	if res.IncompleteFileData+res.OrphanedFileDataRemoved+res.OrphanedChunkLinksRemoved+res.OrphanedChunksRemoved > 0 {
		// Best effort: keep the WAL from staying large after a big run. A
		// busy reader (list/restore connections) can make this a no-op.
		_ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error
	}
	return res, nil
}

// PruneDeletionLog removes deletion-log rows older than olderThan -- the
// bound on how long catalogsync has to consume them.
func (s *Store) PruneDeletionLog(ctx context.Context, olderThan time.Time) (int64, error) {
	r := s.db.WithContext(ctx).Where("deleted_at < ?", olderThan.Unix()).Delete(&FileVersionDeletionRecord{})
	return r.RowsAffected, r.Error
}
