package catalog

import (
	"fmt"
	"path/filepath"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage/sqlitedb"
)

// readerPoolSize is a small fixed constant, not a config knob: SQLite's
// read concurrency benefit under WAL plateaus quickly, and there's no
// measured read-QPS to size against yet (see
// docs/superpowers/specs/2026-08-08-storage-connection-foundation-design.md).
const readerPoolSize = 4

// openDBs opens catalog's two connections against basePath/catalog.db: a
// single-connection writer that also migrates the schema, and a
// multi-connection read-only pool for the read-heavy query RPCs (see
// store.go). The writer must open first -- WAL is a database-file-level
// setting the writer establishes, and the schema must exist before the
// reader pool touches the file.
func openDBs(basePath string) (writeDB, readDB *gorm.DB, err error) {
	dbPath := filepath.Join(basePath, "catalog.db")

	writeDB, err = sqlitedb.Open(sqlitedb.Options{
		Path:   dbPath,
		Models: []any{&EntryRecord{}, &DirectoryRecord{}},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open catalog db: %w", err)
	}

	readDB, err = sqlitedb.Open(sqlitedb.Options{Path: dbPath, ReadOnly: true, MaxConns: readerPoolSize})
	if err != nil {
		if sqlDB, dbErr := writeDB.DB(); dbErr == nil {
			sqlDB.Close()
		}
		return nil, nil, fmt.Errorf("open catalog reader pool: %w", err)
	}
	return writeDB, readDB, nil
}
