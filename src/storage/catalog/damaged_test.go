package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// Damaged files: catalogsync periodically reports the complete set of file
// ids a bwfs node currently considers damaged, and the catalog replaces that
// node's set wholesale. Absent means healthy.

func newDamagedTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

// damagedRows returns every catalog_damaged_files row as "node/object".
func damagedRows(t *testing.T, s *Store) []string {
	t.Helper()
	var rows []DamagedFileRecord
	require.NoError(t, s.writeDB.Find(&rows).Error)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.StoreNode + "/" + r.ObjectID
	}
	return out
}

func damagedRowCount(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.writeDB.Model(&DamagedFileRecord{}).Count(&n).Error)
	return n
}

func TestReplaceDamagedFiles_ReplacesOnlyThatStoreNodesSet(t *testing.T) {
	s := newDamagedTestStore(t)
	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"o1", "o2"}))
	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-b", []string{"o1"}))

	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"o2", "o3"}))

	assert.ElementsMatch(t, []string{"bwfs-a/o2", "bwfs-a/o3", "bwfs-b/o1"}, damagedRows(t, s))
}

func TestReplaceDamagedFiles_EmptySetClearsThatStoreNode(t *testing.T) {
	s := newDamagedTestStore(t)
	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"o1"}))
	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-b", []string{"o1"}))

	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", nil))

	assert.ElementsMatch(t, []string{"bwfs-b/o1"}, damagedRows(t, s))
}

func TestReplaceDamagedFiles_DuplicateIDsAreStoredOnce(t *testing.T) {
	s := newDamagedTestStore(t)

	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"o1", "o1", "o2"}))

	assert.ElementsMatch(t, []string{"bwfs-a/o1", "bwfs-a/o2"}, damagedRows(t, s))
}

// A failure part-way through the insert (after the old rows were deleted and
// earlier batches inserted) must leave the previous set exactly as it was.
func TestReplaceDamagedFiles_FailureLeavesPreviousSet(t *testing.T) {
	s := newDamagedTestStore(t)
	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"old-1", "old-2"}))
	require.NoError(t, s.writeDB.Exec(`CREATE TRIGGER fail_damaged BEFORE INSERT ON catalog_damaged_files
		WHEN NEW.object_id = 'boom' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`).Error)

	ids := make([]string, 0, 3*damagedInsertBatch)
	for i := range 2 * damagedInsertBatch {
		ids = append(ids, fmt.Sprintf("new-%d", i))
	}
	ids = append(ids, "boom") // lands in a later batch than the first inserts

	err := s.ReplaceDamagedFiles(t.Context(), "bwfs-a", ids)

	require.ErrorContains(t, err, "injected failure")
	assert.ElementsMatch(t, []string{"bwfs-a/old-1", "bwfs-a/old-2"}, damagedRows(t, s))
}

// limitWriterSQLVariables lowers the bound-variable limit of the store's
// single writer connection (sqlitedb opens the writer with one connection,
// so every write statement uses it).
func limitWriterSQLVariables(t *testing.T, s *Store, n int) {
	t.Helper()
	sqlDB, err := s.writeDB.DB()
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

// More ids than SQLite allows bound variables in one statement must still go
// through: the insert is batched. The test lowers the writer's limit to 999
// (SQLite's historic default) instead of sending more than the real 32,766,
// which exercises the same failure without inserting 33,000 rows (~6s under
// -race with the pure-Go SQLite). Passing it also shows the batch size is
// safe on any SQLite build.
func TestReplaceDamagedFiles_MoreIDsThanSQLiteVariableLimit(t *testing.T) {
	s := newDamagedTestStore(t)
	limitWriterSQLVariables(t, s, 999)
	ids := make([]string, 2000) // 4,000 variables if sent in one statement
	for i := range ids {
		ids[i] = fmt.Sprintf("o%d", i)
	}

	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", ids))

	assert.Equal(t, int64(2000), damagedRowCount(t, s))
}

func damagedByObject(t *testing.T, recs []EntryRecord) map[string]bool {
	t.Helper()
	out := make(map[string]bool, len(recs))
	for _, r := range recs {
		out[r.StoreNode+"/"+r.JobID+"/"+r.ObjectID] = r.Damaged
	}
	return out
}

func TestListEntries_MarksDamagedEntriesOfThatStoreNodeOnly(t *testing.T) {
	s := newDamagedTestStore(t)
	now := time.Now()
	require.NoError(t, s.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "j1", ObjectID: "bad", StoreCreatedAt: now},
		{StoreNode: "bwfs-a", JobID: "j2", ObjectID: "bad", StoreCreatedAt: now}, // every version of the file id
		{StoreNode: "bwfs-a", JobID: "j1", ObjectID: "good", StoreCreatedAt: now},
		{StoreNode: "bwfs-b", JobID: "j1", ObjectID: "bad", StoreCreatedAt: now}, // same id, other node
	}))
	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"bad"}))

	recs, _, err := s.ListEntries(t.Context(), ListEntriesFilter{})
	require.NoError(t, err)

	assert.Equal(t, map[string]bool{
		"bwfs-a/j1/bad":  true,
		"bwfs-a/j2/bad":  true,
		"bwfs-a/j1/good": false,
		"bwfs-b/j1/bad":  false,
	}, damagedByObject(t, recs))
}

// Damage can be reported before catalogsync has replicated the version row
// (the two passes are independent); the flag must show once the row lands.
func TestListEntries_MarksDamageReportedBeforeTheVersionRow(t *testing.T) {
	s := newDamagedTestStore(t)
	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"bad"}))

	require.NoError(t, s.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "j1", ObjectID: "bad", StoreCreatedAt: time.Now()},
	}))
	recs, _, err := s.ListEntries(t.Context(), ListEntriesFilter{})
	require.NoError(t, err)

	require.Len(t, recs, 1)
	assert.True(t, recs[0].Damaged)
}

func TestListEntries_HealedFileIsNoLongerMarked(t *testing.T) {
	s := newDamagedTestStore(t)
	require.NoError(t, s.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "j1", ObjectID: "bad", StoreCreatedAt: time.Now()},
	}))
	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"bad"}))

	require.NoError(t, s.ReplaceDamagedFiles(t.Context(), "bwfs-a", nil))
	recs, _, err := s.ListEntries(t.Context(), ListEntriesFilter{})
	require.NoError(t, err)

	require.Len(t, recs, 1)
	assert.False(t, recs[0].Damaged)
}
