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

func openDB(basePath string) (*gorm.DB, error) {
	dbPath := filepath.Join(basePath, "metadata.db") + "?_busy_timeout=5000"

	// Open via database/sql with modernc driver (registered as "sqlite")
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// One connection: all goroutines queue through the pool instead of
	// racing on the SQLite write lock and returning SQLITE_BUSY.
	sqlDB.SetMaxOpenConns(1)

	// Set WAL mode before handing to GORM
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Hand the already-open *sql.DB to GORM using its dialector
	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	if err := db.AutoMigrate(
		&ChunkRecord{},
		&FileDataRecord{},
		&FileDataChunkRecord{},
		&FileVersionRecord{},
		&BackupJobRecord{},
	); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("automigrate: %w", err)
	}

	if err := backfillFileDataColumns(db); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("backfill file data columns: %w", err)
	}
	return db, nil
}

// backfillFileDataColumns populates source_host/path/mtime on
// file_data_records rows written before those columns existed. AutoMigrate
// adds a column but never fills it, so without this every pre-existing row
// would keep an empty path forever -- and since ResolveRestoreFiles matches
// on path, those rows would be permanently invisible to restore: a folder
// rule would report success having verified none of them, and a file rule
// would report a file missing that is in fact sitting on the store.
//
// An empty path is the marker for "not yet backfilled", and it is exact
// rather than heuristic: parseFileID's malformed-input fallback returns the
// raw file_id as the path, so it never yields an empty path for any
// non-empty file_id. A backfilled row therefore can never re-match this
// probe, which is what makes the backfill genuinely one-time.
//
// Cost on repeat startups is one indexed lookup that finds nothing: path is
// the leading column of idx_file_data_path_host, so SQLite satisfies the
// empty-path equality probe from that index instead of scanning the table
// (asserted in TestOpenDB_BackfillProbeUsesPathIndex).
//
// The work runs in bounded batches rather than one big read plus one big
// transaction. That keeps memory flat no matter how many rows predate the
// migration, and keeps each write lock short -- bwfs opens three Stores
// back to back at startup and `bwfs list` can open another from a separate
// process, so a multi-minute transaction here would push those opens past
// their busy_timeout and fail the server's start. Batching is safe because
// each row's update is independent and idempotent, and a crash partway
// simply leaves the remainder still marked stale for the next start to
// resume. No cursor is held open across the writes: this pool is
// SetMaxOpenConns(1), so a streaming read would deadlock against them.
func backfillFileDataColumns(db *gorm.DB) error {
	const batchSize = 1000
	for {
		var stale []FileDataRecord
		if err := db.Select("uuid", "file_id").
			Where("path = ?", "").
			Limit(batchSize).
			Find(&stale).Error; err != nil {
			return fmt.Errorf("select stale rows: %w", err)
		}
		if len(stale) == 0 {
			return nil
		}

		// Each batch must strictly shrink the stale set or this loop would
		// never end. A row whose file_id parses to an empty path (only
		// reachable for a degenerate id like "fs://host:f::1000") cannot
		// shrink it, so it is left alone; a batch that makes no progress at
		// all means only such rows remain and there is nothing further to
		// do. Those rows are unrestorable regardless -- an empty path names
		// no file.
		progressed := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, r := range stale {
				source, path, mtime := parseFileID(r.FileID)
				if path == "" {
					continue
				}
				if err := tx.Model(&FileDataRecord{}).
					Where("uuid = ?", r.UUID).
					Updates(map[string]any{
						"source_host": source,
						"path":        path,
						"mtime":       mtime,
					}).Error; err != nil {
					return fmt.Errorf("update row %s: %w", r.UUID, err)
				}
				progressed++
			}
			return nil
		})
		if err != nil {
			return err
		}
		if progressed == 0 {
			return nil
		}
	}
}
