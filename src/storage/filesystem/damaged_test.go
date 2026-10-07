package filesystem

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/storage"
	"modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// Damaged file data: a chunk that turns out to be unusable flags the FileData
// rows that used it (damaged_at) instead of deleting them, so the loss stays
// visible until the file versions referencing it expire.

const (
	damagedFileA = "fs://hosta:f:/data/a.txt:100"
	damagedFileB = "fs://hosta:f:/data/b.txt:100"
)

// fileDataRows returns every FileData row of fileID, oldest first.
func fileDataRows(t *testing.T, s *Store, fileID string) []FileDataRecord {
	t.Helper()
	var rows []FileDataRecord
	require.NoError(t, s.RawDB().Where("file_id = ?", fileID).Order("created_at ASC").Find(&rows).Error)
	return rows
}

func linkCount(t *testing.T, s *Store, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.RawDB().Model(&FileDataChunkRecord{}).Where(where, args...).Count(&n).Error)
	return n
}

func TestMarkChunkCorrupted_FlagsDependentFileDataInsteadOfDeleting(t *testing.T) {
	s := newTestStore(t)
	hashesA := writeFile(t, s, damagedFileA, []byte("a bad chunk"), []byte("a good chunk"))
	writeFile(t, s, damagedFileB, []byte("unrelated chunk"))
	bad, good := hashesA[0], hashesA[1]

	require.NoError(t, s.MarkChunkCorrupted(bad))

	rowsA := fileDataRows(t, s, damagedFileA)
	require.Len(t, rowsA, 1, "the file data must be kept, not deleted")
	require.NotNil(t, rowsA[0].DamagedAt, "and flagged damaged")
	assert.False(t, chunkKnown(s, bad), "the corrupt chunk's row is gone")
	assert.Zero(t, linkCount(t, s, "chunk_hash = ?", hex.EncodeToString(bad)), "and its links")
	assert.Equal(t, int64(1), linkCount(t, s, "chunk_hash = ?", hex.EncodeToString(good)),
		"links of other chunks stay: a healthy re-upload of the file shares them")
	assert.True(t, chunkKnown(s, good))

	rowsB := fileDataRows(t, s, damagedFileB)
	require.Len(t, rowsB, 1)
	assert.Nil(t, rowsB[0].DamagedAt, "an unrelated file is untouched")

	// Marking again -- the same chunk, or another chunk of the same file --
	// keeps the time the damage was first found.
	first := *rowsA[0].DamagedAt
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, s.MarkChunkCorrupted(bad))
	require.NoError(t, s.MarkChunkCorrupted(good))
	rowsA = fileDataRows(t, s, damagedFileA)
	require.Len(t, rowsA, 1)
	require.NotNil(t, rowsA[0].DamagedAt)
	assert.True(t, first.Equal(*rowsA[0].DamagedAt), "damaged_at must not move: was %v, now %v", first, *rowsA[0].DamagedAt)
}

func TestDamagedFileData_IgnoredByDedupAndHealedByReupload(t *testing.T) {
	s := newTestStore(t)
	payloads := [][]byte{[]byte("chunk zero"), []byte("chunk one")}
	hashes := writeFile(t, s, damagedFileA, payloads...)
	damagedUUID := fileDataRows(t, s, damagedFileA)[0].UUID

	require.NoError(t, s.MarkChunkCorrupted(hashes[0]))

	exists, err := s.FileDataExists(damagedFileA)
	require.NoError(t, err)
	assert.False(t, exists, "dedup must ignore damaged data so the next backup uploads the file again")
	_, err = s.FileData(damagedFileA)
	assert.ErrorContains(t, err, "not found")

	// The next backup of the unchanged file: same file_id, same chunks.
	time.Sleep(5 * time.Millisecond) // a distinct created_at for "latest per file_id"
	writeFile(t, s, damagedFileA, payloads...)

	exists, err = s.FileDataExists(damagedFileA)
	require.NoError(t, err)
	assert.True(t, exists)
	fd, err := s.FileData(damagedFileA)
	require.NoError(t, err)
	assert.NotEqual(t, damagedUUID, fd.UUID, "FileData must serve the healthy row")

	// The restore resolver and bwfs list pick the latest finalized row per
	// file_id; the re-upload is newer, so it wins over the damaged one.
	var latest []string
	require.NoError(t, s.RawDB().Table("file_data_records fd").
		Where("fd.checksum IS NOT NULL").
		Where("fd.created_at = (SELECT MAX(fd2.created_at) FROM file_data_records fd2 "+
			"WHERE fd2.file_id = fd.file_id AND fd2.checksum IS NOT NULL)").
		Where("fd.file_id = ?", damagedFileA).
		Pluck("fd.uuid", &latest).Error)
	assert.Equal(t, []string{fd.UUID}, latest)

	rows := fileDataRows(t, s, damagedFileA)
	require.Len(t, rows, 2, "the damaged row stays until its versions expire")
	assert.NotNil(t, rows[0].DamagedAt)
	assert.Nil(t, rows[1].DamagedAt)
	assert.Equal(t, int64(2), linkCount(t, s, "file_id = ?", damagedFileA), "the re-upload restored the dropped link")
}

func TestFinalizeFileData_FailsWhenTheFileInTransferWasFlagged(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.CreateFileData(damagedFileA, 1))
	h := makeChunk(t, []byte("x"))
	require.NoError(t, s.StoreChunk(h, []byte("x")))
	require.NoError(t, s.LinkChunkToFileData(h, damagedFileA, 0))
	require.NoError(t, s.MarkChunkCorrupted(h))

	err := s.FinalizeFileData(damagedFileA, []byte{1})
	require.ErrorContains(t, err, "no longer exists", "a version must not be recorded for damaged data")
	rows := fileDataRows(t, s, damagedFileA)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].Checksum, "the flagged row must not be completed")
}

// vacuumFuncs runs each test against both vacuum entry points.
var vacuumFuncs = map[string]func(s *Store) error{
	"Vacuum": func(s *Store) error { _, err := s.Vacuum(); return err },
	"VacuumOnline": func(s *Store) error {
		_, err := s.VacuumOnline(context.Background(), 2, time.Hour)
		return err
	},
}

func TestVacuum_RemovesDamagedFileDataOnlyOnceNoVersionReferencesIt(t *testing.T) {
	for name, vacuum := range vacuumFuncs {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)
			hashes := addFile(t, s, "job-1", damagedFileA, 0, "damaged chunk", "healthy chunk")
			require.NoError(t, s.MarkChunkCorrupted(hashes[0]))

			require.NoError(t, vacuum(s))
			rows := fileDataRows(t, s, damagedFileA)
			require.Len(t, rows, 1, "a version still references the damaged data")
			assert.NotNil(t, rows[0].DamagedAt)
			assert.True(t, chunkKnown(s, hashes[1]), "its remaining chunks stay too")

			require.NoError(t, s.RemoveFileVersion("job-1", damagedFileA))
			require.NoError(t, vacuum(s))
			assert.Empty(t, fileDataRows(t, s, damagedFileA), "no version left: the damaged row goes")
			assert.Zero(t, linkCount(t, s, "file_id = ?", damagedFileA))
			assert.False(t, chunkKnown(s, hashes[1]))
		})
	}
}

func TestMarkChunkCorrupted_LogsTheDamage(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := newTestStore(t)
	hashes := writeFile(t, s, damagedFileA, []byte("shared chunk"))
	writeFile(t, s, damagedFileB, []byte("shared chunk"))

	require.NoError(t, s.MarkChunkCorrupted(hashes[0]))

	out := buf.String()
	assert.Contains(t, out, "level=ERROR")
	assert.Contains(t, out, "chunk marked corrupt")
	assert.Contains(t, out, "chunk_hash="+hex.EncodeToString(hashes[0]))
	assert.Contains(t, out, "file_versions_damaged=2")
	assert.Contains(t, out, "/data/a.txt")
	assert.Contains(t, out, "/data/b.txt")

	// Nothing new to flag: no second report.
	buf.Reset()
	require.NoError(t, s.MarkChunkCorrupted(hashes[0]))
	assert.NotContains(t, buf.String(), "chunk marked corrupt")
}

func TestMarkChunkCorrupted_LogsAtMostFivePaths(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := newTestStore(t)
	var h [][]byte
	for _, name := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7"} {
		h = writeFile(t, s, "fs://hosta:f:/many/"+name+":1", []byte("shared by seven"))
	}
	require.NoError(t, s.MarkChunkCorrupted(h[0]))

	out := buf.String()
	assert.Contains(t, out, "file_versions_damaged=7")
	assert.Equal(t, 5, bytes.Count([]byte(out), []byte("/many/")), out)
}

// The interface contract stays: callers see the chunk as unknown afterwards.
func TestMarkChunkCorrupted_ChunkUnknownAfterwards(t *testing.T) {
	s := newTestStore(t)
	hashes := writeFile(t, s, damagedFileA, []byte("gone"))
	require.NoError(t, s.MarkChunkCorrupted(hashes[0]))
	assert.ErrorIs(t, s.ChunkExists(hashes[0]), storage.ErrChunkNotFound)
}

// A store created before damaged_at existed gains the column on open, and its
// existing file data counts as healthy (NULL) without any backfill.
func TestOpen_AddsDamagedAtToAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	require.NoError(t, err)
	writeFile(t, s, damagedFileA, []byte("written before the column existed"))
	require.NoError(t, s.RawDB().Exec("ALTER TABLE file_data_records DROP COLUMN damaged_at").Error)
	require.NoError(t, s.Close())

	s, err = New(dir)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	assert.True(t, s.RawDB().Migrator().HasColumn(&FileDataRecord{}, "damaged_at"))
	exists, err := s.FileDataExists(damagedFileA)
	require.NoError(t, err)
	assert.True(t, exists)
}

// sqlVariableLimit stands in for SQLite's real limit of 32,766 bound
// variables per statement: the test lowers the connection's limit to it, so
// a chunk with manyDependents files exercises the same failure (a statement
// binding one variable per dependent) as a chunk shared by 33,000 files --
// a zero block, one file on many hosts -- without inserting that many rows,
// which takes ~40s under -race with the pure-Go SQLite.
const (
	sqlVariableLimit = 1000
	manyDependents   = 1200
)

// limitSQLVariables lowers the bound-variable limit of the store's single
// database connection (openDB allows only one, so every statement uses it).
func limitSQLVariables(t *testing.T, s *Store, n int) {
	t.Helper()
	sqlDB, err := s.RawDB().DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	_, err = sqlite.Limit(conn, sqlitelib.SQLITE_LIMIT_VARIABLE_NUMBER, n)
	require.NoError(t, err)
	got, err := sqlite.Limit(conn, sqlitelib.SQLITE_LIMIT_VARIABLE_NUMBER, -1) // -1 only reads it
	require.NoError(t, err)
	require.Equal(t, n, got)
}

// addDependents links n extra finalized files to the chunk with two
// set-based inserts, so setup binds no per-row variables.
func addDependents(t *testing.T, s *Store, hash []byte, n int) {
	t.Helper()
	const seq = "WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?) "
	require.NoError(t, s.RawDB().Exec(seq+
		"INSERT INTO file_data_records (uuid, file_id, source_host, path, mtime, size, checksum, chunk_count, created_at) "+
		"SELECT 'many-' || i, 'fs://hosta:f:/many/' || i || ':1', 'hosta', '/many/' || i, 1, 1, x'01', 1, ? FROM seq",
		n, time.Now()).Error)
	require.NoError(t, s.RawDB().Exec(seq+
		"INSERT INTO file_data_chunk_records (file_id, chunk_hash, `index`) "+
		"SELECT 'fs://hosta:f:/many/' || i || ':1', ?, 0 FROM seq",
		n, hex.EncodeToString(hash)).Error)
}

func damagedCount(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.RawDB().Model(&FileDataRecord{}).Where("damaged_at IS NOT NULL").Count(&n).Error)
	return n
}

func TestMarkChunkCorrupted_ChunkSharedByMoreFilesThanSQLiteVariables(t *testing.T) {
	s := newTestStore(t)
	hashes := writeFile(t, s, damagedFileA, []byte("shared by very many files"))
	addDependents(t, s, hashes[0], manyDependents)
	limitSQLVariables(t, s, sqlVariableLimit)

	require.NoError(t, s.MarkChunkCorrupted(hashes[0]))

	assert.Equal(t, int64(manyDependents+1), damagedCount(t, s))
	assert.False(t, chunkKnown(s, hashes[0]))
	assert.Zero(t, linkCount(t, s, "chunk_hash = ?", hex.EncodeToString(hashes[0])))
}

func TestMarkChunkCorrupted_LogsEachPathOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := newTestStore(t)
	// Two contents of the same path (different mtime) sharing a chunk.
	hashes := writeFile(t, s, "fs://hosta:f:/data/twice.txt:100", []byte("same block"))
	writeFile(t, s, "fs://hosta:f:/data/twice.txt:200", []byte("same block"))

	require.NoError(t, s.MarkChunkCorrupted(hashes[0]))

	out := buf.String()
	assert.Contains(t, out, "file_versions_damaged=2")
	assert.Equal(t, 1, bytes.Count([]byte(out), []byte("/data/twice.txt")), out)
}

func TestStoreInfo_ExcludesDamagedFileData(t *testing.T) {
	s := newTestStore(t)
	hashes := writeFile(t, s, damagedFileA, []byte("will be damaged"))
	writeFile(t, s, damagedFileB, []byte("stays healthy"))
	require.NoError(t, s.MarkChunkCorrupted(hashes[0]))

	info, err := s.StoreInfo()
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.TotalFileData)
}
