package catalog

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureEntries_PersistsBatch(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", Ctime: 100, StoreSeq: 1, StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", Ctime: 200, StoreSeq: 2, StoreCreatedAt: time.Now()},
	}
	require.NoError(t, store.EnsureEntries(batch))

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestEnsureEntries_DuplicateSameStoreNodeIsNoOp(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()}}
	require.NoError(t, store.EnsureEntries(batch))
	require.NoError(t, store.EnsureEntries(batch)) // resend, e.g. after a retried RPC

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestEnsureEntries_SameJobObjectDifferentStoreNodeAreDistinctRows(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}
	require.NoError(t, store.EnsureEntries(batch))

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestEnsureEntries_EmptyBatchSucceeds(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	assert.NoError(t, store.EnsureEntries(nil))
}

func TestEnsureEntries_PersistsSourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "database", entries[0].SourceHost)
}

func TestNew_CreatesMissingStorageDir(t *testing.T) {
	base := t.TempDir() + "/does/not/exist/yet"

	store, err := New(base)
	require.NoError(t, err)
	defer store.Close()
}

func TestListEntries_FiltersByStoreNode(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	entries, hasMore, err := store.ListEntries(ListEntriesFilter{StoreNode: "bwfs-a"})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, entries, 1)
	assert.Equal(t, "bwfs-a", entries[0].StoreNode)
}

func TestListEntries_FiltersBySourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	entries, hasMore, err := store.ListEntries(ListEntriesFilter{SourceHost: "database"})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, entries, 1)
	assert.Equal(t, "database", entries[0].SourceHost)
}

func TestListEntries_FiltersByStoreNodeAndSourceHostCombined(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-3", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{StoreNode: "bwfs-a", SourceHost: "database"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "obj-1", entries[0].ObjectID)
}

func TestListEntries_FiltersByPatternSubstringOnObjectID(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "fs://bwfs-a:f:/var/log/syslog:100", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "fs://bwfs-a:f:/etc/passwd:100", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{Pattern: "/var/log"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].ObjectID, "/var/log/syslog")
}

func TestListEntries_PaginationHasMoreAndStartingAfter(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	for i := 0; i < 5; i++ {
		require.NoError(t, store.EnsureEntries([]Entry{
			{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: fmt.Sprintf("obj-%d", i), StoreCreatedAt: time.Now()},
		}))
	}

	page1, hasMore, err := store.ListEntries(ListEntriesFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.True(t, hasMore)
	// Newest first (highest ID first).
	assert.Greater(t, page1[0].ID, page1[1].ID)

	page2, hasMore, err := store.ListEntries(ListEntriesFilter{Limit: 2, StartingAfter: page1[1].ID})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.True(t, hasMore)
	assert.Less(t, page2[0].ID, page1[1].ID)

	page3, hasMore, err := store.ListEntries(ListEntriesFilter{Limit: 2, StartingAfter: page2[1].ID})
	require.NoError(t, err)
	require.Len(t, page3, 1)
	assert.False(t, hasMore)
}

func TestListEntries_LimitDefaultsAndCaps(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{Limit: 0})
	require.NoError(t, err)
	assert.Len(t, entries, 1) // default 100, well above the 1 row present

	entries, _, err = store.ListEntries(ListEntriesFilter{Limit: 10000})
	require.NoError(t, err)
	assert.Len(t, entries, 1) // capped at 500, still well above the 1 row present
}

func TestListEntries_FiltersByReceivedAtRange(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}))

	included, _, err := store.ListEntries(ListEntriesFilter{ReceivedAfter: time.Now().Add(-1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, included, 1)

	excluded, _, err := store.ListEntries(ListEntriesFilter{ReceivedAfter: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, excluded, 0)

	includedBefore, _, err := store.ListEntries(ListEntriesFilter{ReceivedBefore: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, includedBefore, 1)

	excludedBefore, _, err := store.ListEntries(ListEntriesFilter{ReceivedBefore: time.Now().Add(-1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, excludedBefore, 0)
}

func TestListEntries_FiltersBySourceHostsMultiValue(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", SourceHost: "mail", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{SourceHosts: []string{"database", "mail"}})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var hosts []string
	for _, e := range entries {
		hosts = append(hosts, e.SourceHost)
	}
	assert.ElementsMatch(t, []string{"database", "mail"}, hosts)
}

func TestListEntries_FiltersByJobNamesMultiValue(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1752400000", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:1752400010", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:root:fedcba98:1752400020", ObjectID: "obj-3", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{JobNames: []string{"nightly-db", "weekly-full"}})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var objIDs []string
	for _, e := range entries {
		objIDs = append(objIDs, e.ObjectID)
	}
	assert.ElementsMatch(t, []string{"obj-1", "obj-3"}, objIDs)
}

func TestNew_CreatesIndexesOnReceivedAtAndJobID(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	sqlDB, err := store.db.DB()
	require.NoError(t, err)

	rows, err := sqlDB.Query(`SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name='entry_records'`)
	require.NoError(t, err)
	defer rows.Close()

	var indexDefs []string
	for rows.Next() {
		var def sql.NullString
		require.NoError(t, rows.Scan(&def))
		if def.Valid {
			indexDefs = append(indexDefs, def.String)
		}
	}
	joined := strings.Join(indexDefs, "\n")
	assert.Contains(t, joined, "received_at")
	assert.Contains(t, joined, "job_id")
}
