package filesystem

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
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
	dbPath := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", filepath.Join(basePath, "metadata.db"))

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open read-only: %w", err)
	}
	return &ReplicaReader{db: db}, nil
}

// FileVersionsSince returns up to limit file_versions rows with seq greater
// than cursor, ordered ascending by seq — catalogsync's replication cursor.
func (r *ReplicaReader) FileVersionsSince(cursor int64, limit int) ([]FileVersionRecord, error) {
	var records []FileVersionRecord
	err := r.db.
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
