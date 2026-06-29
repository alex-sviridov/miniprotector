package filesystem

import (
	"fmt"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

func openDB(basePath string) (*gorm.DB, error) {
	dbPath := filepath.Join(basePath, "metadata.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if err := db.AutoMigrate(
		&ChunkRecord{},
		&FileDataRecord{},
		&FileDataChunkRecord{},
		&FileVersionRecord{},
	); err != nil {
		return nil, fmt.Errorf("automigrate: %w", err)
	}
	return db, nil
}
