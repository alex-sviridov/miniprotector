//go:build e2e

package e2e

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

type listRecord struct {
	FileDataID string `json:"file_data_id"`
	Source     string `json:"source"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	Timestamp  int64  `json:"timestamp"`
	Size       int64  `json:"size"`
	Chunks     int    `json:"chunks"`
	Versions   int64  `json:"versions"`
	CreatedAt  string `json:"created_at"`
}

func parseListOutput(t *testing.T, data []byte) []listRecord {
	t.Helper()
	var records []listRecord
	require.NoError(t, json.Unmarshal(data, &records), "failed to parse bwfs list JSON: %s", string(data))
	return records
}

// assertFilesPresent verifies every file in expected appears in list with correct
// size, and that the checksum stored in metadata.db matches the precomputed value.
// expected keys are relative paths like "subA/file0.bin"; list paths are absolute
// inside the container like "/testdata/subA/file0.bin".
func assertFilesPresent(t *testing.T, list []listRecord, expected map[string]fileRecord, storagePath string) {
	t.Helper()

	// Index list by path for O(1) lookup
	byPath := make(map[string]listRecord, len(list))
	for _, r := range list {
		byPath[r.Path] = r
	}

	db := openMetadataDB(t, storagePath)

	for rel, want := range expected {
		absPath := "/testdata/" + filepath.ToSlash(rel)
		rec, ok := byPath[absPath]
		if !assert.True(t, ok, "expected path %q not found in bwfs list", absPath) {
			continue
		}
		assert.Equal(t, want.size, rec.Size, "size mismatch for %s", rel)

		// Query the checksum stored by bwfs for this file_data_id
		stored := queryChecksum(t, db, rec.FileDataID)
		assert.Equal(t, want.checksum, stored, "checksum mismatch for %s", rel)
	}
}

// assertFilesAbsent verifies none of the files in absent appear in list.
func assertFilesAbsent(t *testing.T, list []listRecord, absent map[string]fileRecord) {
	t.Helper()
	byPath := make(map[string]struct{}, len(list))
	for _, r := range list {
		byPath[r.Path] = struct{}{}
	}
	for rel := range absent {
		absPath := "/testdata/" + filepath.ToSlash(rel)
		assert.NotContains(t, byPath, absPath, "path %q should not be in bwfs list", absPath)
	}
}

type fileDataRecord struct {
	ID       string `gorm:"column:id"`
	Checksum []byte `gorm:"column:checksum"`
}

func openMetadataDB(t *testing.T, storagePath string) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(storagePath, "metadata.db")
	dsn := "file:" + dbPath + "?mode=ro"

	// Open via database/sql with the pure-Go modernc driver so the
	// `file:` URI and `mode=ro` query parameter are honored natively,
	// guaranteeing this connection cannot write to metadata.db even
	// while a live bwfs server holds it open for writes.
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err, "failed to open metadata.db at %s", dbPath)
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "failed to open metadata.db at %s", dbPath)
	return db
}

func queryChecksum(t *testing.T, db *gorm.DB, fileDataID string) uint32 {
	t.Helper()
	var rec fileDataRecord
	err := db.Table("file_data_records").
		Select("id, checksum").
		Where("id = ?", fileDataID).
		First(&rec).Error
	require.NoError(t, err, "failed to query checksum for file_data_id %s", fileDataID)
	require.Len(t, rec.Checksum, 4, "checksum should be 4 bytes")
	return binary.BigEndian.Uint32(rec.Checksum)
}
