package filesystem

import (
	"context"
	"fmt"
	"path/filepath"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage/sqlitedb"
)

// ReplicaReader is a strictly read-only accessor for an existing bwfs
// store's metadata.db, for use by a separate process (catalogsync) that
// must never be able to write to bwfs's data, even by accident. It opens
// the database via SQLite's `mode=ro` URI flag — enforced by the driver —
// unlike Store's NewReadOnly, which still opens a normal read-write
// connection (needed elsewhere for MarkChunkCorrupted).
type ReplicaReader struct {
	db *gorm.DB
}

// OpenReplicaReader opens basePath/metadata.db read-only. The database must
// already exist and have its schema migrated (by a real bwfs Store) — a
// read-only connection cannot create it.
func OpenReplicaReader(basePath string) (*ReplicaReader, error) {
	db, err := sqlitedb.Open(sqlitedb.Options{
		Path:     filepath.Join(basePath, "metadata.db"),
		ReadOnly: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	return &ReplicaReader{db: db}, nil
}

// FileVersionsSince returns up to limit file_versions rows with seq greater
// than cursor, ordered ascending by seq — catalogsync's replication cursor.
func (r *ReplicaReader) FileVersionsSince(ctx context.Context, cursor int64, limit int) ([]FileVersionRecord, error) {
	var records []FileVersionRecord
	err := r.db.WithContext(ctx).
		Where("seq > ?", cursor).
		Order("seq ASC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

// FileVersionDeletionsSince returns up to limit file_version_deletions rows
// with seq greater than cursor, ascending -- catalogsync's second
// replication cursor, for telling the catalog which versions are gone.
func (r *ReplicaReader) FileVersionDeletionsSince(ctx context.Context, cursor int64, limit int) ([]FileVersionDeletionRecord, error) {
	var records []FileVersionDeletionRecord
	err := r.db.WithContext(ctx).
		Where("seq > ?", cursor).
		Order("seq ASC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

// damagedFileIDsSQL selects file ids that are currently damaged: some row is
// flagged damaged_at and no row of the same file_id is a healthy finalized
// copy (a healed re-upload shares the file_id; an in-flight one, with a NULL
// checksum, cannot be restored yet and so heals nothing). The leading
// damaged_at predicate is driven by the partial index on file_id over damaged
// rows, pinned with INDEXED BY (without statistics the planner prefers the
// full file_id index and walks every row). The index is ordered by file_id, so
// each page seeks to `after` and reads only its own rows -- no per-page pass
// over the whole damaged set, which made a full listing quadratic.
const damagedFileIDsSQL = `
SELECT DISTINCT d.file_id
FROM file_data_records d INDEXED BY idx_file_data_damaged_file_id
WHERE d.damaged_at IS NOT NULL
  AND d.file_id > ?
  AND NOT EXISTS (
    SELECT 1 FROM file_data_records h
    WHERE h.file_id = d.file_id AND h.checksum IS NOT NULL AND h.damaged_at IS NULL)
ORDER BY d.file_id
LIMIT ?`

// DamagedFileIDs returns up to limit distinct file ids that are currently
// damaged, ascending, with file_id greater than after -- catalogsync pages
// through them to mirror the damage into the catalog.
func (r *ReplicaReader) DamagedFileIDs(ctx context.Context, after string, limit int) ([]string, error) {
	ids := []string{}
	err := r.db.WithContext(ctx).Raw(damagedFileIDsSQL, after, limit).Scan(&ids).Error
	return ids, err
}

func (r *ReplicaReader) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
