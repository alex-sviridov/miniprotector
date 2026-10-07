package filesystem

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenReplicaReader_FileVersionsSince_ReturnsNewRowsInOrder(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", "hosta", "/path", "f", []byte("v1"), 100, 0))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-2", "hosta", "/path", "f", []byte("v2"), 100, 0))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-3", "hosta", "/path", "f", []byte("v3"), 100, 0))

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	batch, err := reader.FileVersionsSince(t.Context(), 0, 2)
	require.NoError(t, err)
	require.Len(t, batch, 2)
	assert.Equal(t, "obj-1", batch[0].ObjectID)
	assert.Equal(t, "obj-2", batch[1].ObjectID)

	next, err := reader.FileVersionsSince(t.Context(), batch[1].Seq, 2)
	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.Equal(t, "obj-3", next[0].ObjectID)
}

func TestOpenReplicaReader_FileVersionsSince_EmptyWhenCaughtUp(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", "hosta", "/path", "f", []byte("v1"), 100, 0))

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	batch, err := reader.FileVersionsSince(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, batch, 1)

	caughtUp, err := reader.FileVersionsSince(t.Context(), batch[0].Seq, 10)
	require.NoError(t, err)
	assert.Empty(t, caughtUp)
}

func TestOpenReplicaReader_CannotWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	err = reader.db.Exec(
		"INSERT INTO file_version_records (object_id, job_id, ctime, created_at) VALUES ('x', 'y', 0, datetime('now'))",
	).Error
	assert.Error(t, err, "a mode=ro connection must reject writes")
}

func TestReplicaReader_FileVersionDeletionsSince(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	require.NoError(t, store.RawDB().Create(&[]FileVersionDeletionRecord{
		{JobID: "j", ObjectID: "a", DeletedAt: 1},
		{JobID: "j", ObjectID: "b", DeletedAt: 2},
		{JobID: "j", ObjectID: "c", DeletedAt: 3},
	}).Error)
	require.NoError(t, store.Close())

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	first, err := reader.FileVersionDeletionsSince(context.Background(), 0, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, "a", first[0].ObjectID)

	rest, err := reader.FileVersionDeletionsSince(context.Background(), first[1].Seq, 10)
	require.NoError(t, err)
	require.Len(t, rest, 1)
	assert.Equal(t, "c", rest[0].ObjectID)
}

// newDamagedFixture opens a real store (its AutoMigrate creates the schema
// and indexes the read-only reader cannot) plus a reader over it.
func newDamagedFixture(t *testing.T) (*Store, *ReplicaReader) {
	t.Helper()
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	t.Cleanup(func() { reader.Close() })
	return store, reader
}

// addFileDataRow inserts one file_data_records row for fileID: finalized
// (checksum set) or in flight (checksum NULL), and damaged or healthy.
func addFileDataRow(t *testing.T, s *Store, fileID string, finalized, damaged bool) {
	t.Helper()
	require.NoError(t, s.CreateFileData(fileID, 1))
	update := map[string]any{}
	if finalized {
		update["checksum"] = []byte{1}
	}
	if damaged {
		update["damaged_at"] = time.Now()
	}
	if len(update) == 0 {
		return
	}
	// Target the newest row of the file_id (the one just created).
	require.NoError(t, s.RawDB().Model(&FileDataRecord{}).
		Where("uuid = (SELECT uuid FROM file_data_records WHERE file_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1)", fileID).
		Updates(update).Error)
}

func TestReplicaReader_DamagedFileIDs_DamagedOnlyIsReturned(t *testing.T) {
	store, reader := newDamagedFixture(t)
	addFileDataRow(t, store, "healthy", true, false)
	addFileDataRow(t, store, "damaged", true, true)

	ids, err := reader.DamagedFileIDs(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"damaged"}, ids)
}

func TestReplicaReader_DamagedFileIDs_HealthyReuploadHidesIt(t *testing.T) {
	store, reader := newDamagedFixture(t)
	addFileDataRow(t, store, "f", true, true)
	addFileDataRow(t, store, "f", true, false)

	ids, err := reader.DamagedFileIDs(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestReplicaReader_DamagedFileIDs_InFlightReuploadDoesNotHideIt(t *testing.T) {
	store, reader := newDamagedFixture(t)
	addFileDataRow(t, store, "f", true, true)
	addFileDataRow(t, store, "f", false, false) // checksum NULL: not restorable yet

	ids, err := reader.DamagedFileIDs(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"f"}, ids)
}

func TestReplicaReader_DamagedFileIDs_TwoDamagedRowsGiveOneID(t *testing.T) {
	store, reader := newDamagedFixture(t)
	addFileDataRow(t, store, "f", true, true)
	addFileDataRow(t, store, "f", true, true)

	ids, err := reader.DamagedFileIDs(context.Background(), "", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"f"}, ids)
}

func TestReplicaReader_DamagedFileIDs_PagesEveryIDExactlyOnce(t *testing.T) {
	store, reader := newDamagedFixture(t)
	want := []string{"a", "b", "c", "d", "e"}
	for _, id := range want {
		addFileDataRow(t, store, id, true, true)
	}

	var got []string
	after := ""
	for {
		page, err := reader.DamagedFileIDs(context.Background(), after, 2)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		require.LessOrEqual(t, len(page), 2)
		got = append(got, page...)
		after = page[len(page)-1]
	}
	assert.Equal(t, want, got)
}

func TestReplicaReader_DamagedFileIDs_NoneDamagedGivesEmptySlice(t *testing.T) {
	store, reader := newDamagedFixture(t)
	addFileDataRow(t, store, "healthy", true, false)

	ids, err := reader.DamagedFileIDs(context.Background(), "", 10)
	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Empty(t, ids)
}

// The damaged set is a small fraction of a store that can hold millions of
// rows, so the query must find it through the partial damaged-file_id index
// (seeking to `after`) rather than scanning every file_data_records row.
func TestReplicaReader_DamagedFileIDs_UsesThePartialDamagedIndex(t *testing.T) {
	store, reader := newDamagedFixture(t)
	addFileDataRow(t, store, "f", true, true)

	var plan []struct{ Detail string }
	require.NoError(t, reader.db.Raw("EXPLAIN QUERY PLAN "+damagedFileIDsSQL, "", 10).Scan(&plan).Error)
	var detail string
	for _, p := range plan {
		detail += p.Detail + "\n"
	}
	assert.Contains(t, detail, "USING INDEX idx_file_data_damaged_file_id (file_id>?)", "plan was:\n"+detail)
	assert.NotContains(t, detail, "TEMP B-TREE", "a page must not sort the damaged set; plan was:\n"+detail)

	// The index must be partial: only damaged rows, not the whole table.
	var ddl string
	require.NoError(t, reader.db.Raw("SELECT sql FROM sqlite_master WHERE name = 'idx_file_data_damaged_file_id'").Scan(&ddl).Error)
	assert.Contains(t, ddl, "WHERE damaged_at IS NOT NULL")
}

// A shared chunk damages every file using it, so the damaged set can be huge.
// Paging it must cost each page only its own rows: the old query re-walked
// the whole damaged set per page (quadratic), blowing the stream deadline
// around 120k ids.
func TestReplicaReader_DamagedFileIDs_PagingALargeSetIsFast(t *testing.T) {
	store, reader := newDamagedFixture(t)
	const total, pageSize = 20000, 100
	require.NoError(t, store.RawDB().Exec(`
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
		INSERT INTO file_data_records (uuid, file_id, checksum, created_at, damaged_at)
		SELECT 'u' || i, printf('file-%06d', i), x'01', datetime('now'), datetime('now') FROM n`, total).Error)

	start := time.Now()
	seen, after := 0, ""
	for {
		page, err := reader.DamagedFileIDs(context.Background(), after, pageSize)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		seen += len(page)
		after = page[len(page)-1]
	}
	assert.Equal(t, total, seen)
	assert.Less(t, time.Since(start), 15*time.Second, "paging must not rescan the damaged set per page")
}
