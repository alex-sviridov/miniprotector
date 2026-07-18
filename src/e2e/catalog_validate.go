//go:build e2e

package e2e

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

type catalogEntryRow struct {
	StoreNode  string
	SourceHost string
	JobID      string
	ObjectID   string
}

// countBwfsFileVersions returns the number of rows in bwfs's own
// file_version_records table — the ground truth for how many entries
// catalogsync should eventually replicate. bwfs records one row per backed
// up filesystem entity, not just files: brfs also submits directory
// entries (object IDs of the form "fs://host:d:path:mtime"), so this is
// intentionally larger than a file-only count like len(generateTestData's
// map).
func countBwfsFileVersions(t testingT, bwfsStorageDir string) int {
	dbPath := filepath.Join(bwfsStorageDir, "metadata.db")
	dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open metadata.db at %s: %v", dbPath, err)
	}
	defer sqlDB.Close()

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm open metadata.db at %s: %v", dbPath, err)
	}

	var count int64
	if err := db.Table("file_version_records").Count(&count).Error; err != nil {
		t.Fatalf("count file_version_records at %s: %v", dbPath, err)
	}
	return int(count)
}

// waitForCatalogEntryCount polls catalogStorageDir/catalog.db until it
// contains at least wantCount rows or the timeout expires, then returns the
// rows found. Polling (rather than a single read) accounts for
// catalogsync's poll/replicate loop and its own PollIntervalSec cadence.
func waitForCatalogEntryCount(t testingT, catalogStorageDir string, wantCount int) []catalogEntryRow {
	dbPath := filepath.Join(catalogStorageDir, "catalog.db")
	dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", dbPath)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		sqlDB, err := sql.Open("sqlite", dsn)
		if err == nil {
			db, gormErr := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
				Logger: logger.Default.LogMode(logger.Silent),
			})
			if gormErr == nil {
				var got []catalogEntryRow
				if err := db.Table("entry_records").
					Select("store_node, source_host, job_id, object_id").
					Find(&got).Error; err == nil && len(got) >= wantCount {
					sqlDB.Close()
					return got
				}
			}
			sqlDB.Close()
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("catalog.db at %s did not reach %d entries within 30s", dbPath, wantCount)
	return nil
}
