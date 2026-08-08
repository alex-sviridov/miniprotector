// src/storage/sqlitedb/sqlitedb_test.go
package sqlitedb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testRecord struct {
	ID   int64 `gorm:"primaryKey;autoIncrement"`
	Name string
}

func TestOpen_WritableCreatesAndMigrates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "test.db")

	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	defer func() { sqlDB, _ := db.DB(); sqlDB.Close() }()

	require.NoError(t, db.Create(&testRecord{Name: "a"}).Error)

	var count int64
	require.NoError(t, db.Model(&testRecord{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestOpen_ReadOnlyRejectsWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	writer, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	require.NoError(t, writer.Create(&testRecord{Name: "a"}).Error)
	writerSQL, err := writer.DB()
	require.NoError(t, err)
	require.NoError(t, writerSQL.Close())

	reader, err := Open(Options{Path: dbPath, ReadOnly: true})
	require.NoError(t, err)
	defer func() { sqlDB, _ := reader.DB(); sqlDB.Close() }()

	var recs []testRecord
	require.NoError(t, reader.Find(&recs).Error)
	assert.Len(t, recs, 1)

	err = reader.Create(&testRecord{Name: "b"}).Error
	assert.Error(t, err, "a read-only connection must reject writes")
}

func TestOpen_MaxConnsSetsPoolSize(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}, MaxConns: 4})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	assert.Equal(t, 4, sqlDB.Stats().MaxOpenConnections)
}

func TestOpen_DefaultMaxConnsIsOne(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	assert.Equal(t, 1, sqlDB.Stats().MaxOpenConnections)
}

func TestOpen_RespectsContextCancellation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before issuing the query

	var recs []testRecord
	err = db.WithContext(ctx).Find(&recs).Error
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestOpen_WritableSetsTransactionIsolationTimeout(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	defer func() { sqlDB, _ := db.DB(); sqlDB.Close() }()

	var timeout int
	require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&timeout).Error)
	assert.Equal(t, busyTimeoutMS, timeout)
}

func TestOpen_ReadOnlySetsTransactionIsolationTimeout(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create and populate database
	writer, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	require.NoError(t, writer.Create(&testRecord{Name: "a"}).Error)
	writerSQL, err := writer.DB()
	require.NoError(t, err)
	require.NoError(t, writerSQL.Close())

	// Open as read-only and verify busy_timeout is set
	reader, err := Open(Options{Path: dbPath, ReadOnly: true})
	require.NoError(t, err)
	defer func() { sqlDB, _ := reader.DB(); sqlDB.Close() }()

	var timeout int
	require.NoError(t, reader.Raw("PRAGMA busy_timeout").Scan(&timeout).Error)
	assert.Equal(t, busyTimeoutMS, timeout)
}
