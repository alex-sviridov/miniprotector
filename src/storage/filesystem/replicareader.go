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

func (r *ReplicaReader) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
