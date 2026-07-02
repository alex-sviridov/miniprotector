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
	return db, nil
}
