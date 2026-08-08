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

func TestListEntries_JobNamesCombinedWithSourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:root:fedcba98:2", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:var-lib:abcd5678:3", ObjectID: "obj-3", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{
		SourceHost: "database",
		JobNames:   []string{"nightly-db", "weekly-full"},
	})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var objIDs []string
	for _, e := range entries {
		objIDs = append(objIDs, e.ObjectID)
	}
	assert.ElementsMatch(t, []string{"obj-1", "obj-2"}, objIDs)
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

func TestPolicyNameFromJobID(t *testing.T) {
	assert.Equal(t, "nightly-db", policyNameFromJobID("backup:nightly-db:var-lib:abcd1234:1752400000"))
	assert.Equal(t, "", policyNameFromJobID("operating-refresh:1752400000"))
	assert.Equal(t, "", policyNameFromJobID("backup"))
	assert.Equal(t, "", policyNameFromJobID(""))
}

func TestListClientFacets_GroupsByHostWithCountAndLastSeen(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	before := time.Now().Add(-1 * time.Second)
	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))
	after := time.Now().Add(1 * time.Second)

	facets, err := store.ListClientFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 2)

	byName := map[string]Facet{}
	for _, f := range facets {
		byName[f.Name] = f
	}
	assert.Equal(t, int64(2), byName["database"].Count)
	assert.Equal(t, int64(1), byName["webserver"].Count)

	// Verify LastSeen timestamps are real and reasonably recent
	assert.False(t, byName["database"].LastSeen.IsZero(), "database LastSeen should not be zero")
	assert.True(t, byName["database"].LastSeen.After(before), "database LastSeen should be after test start")
	assert.True(t, byName["database"].LastSeen.Before(after), "database LastSeen should be before test end")

	assert.False(t, byName["webserver"].LastSeen.IsZero(), "webserver LastSeen should not be zero")
	assert.True(t, byName["webserver"].LastSeen.After(before), "webserver LastSeen should be after test start")
	assert.True(t, byName["webserver"].LastSeen.Before(after), "webserver LastSeen should be before test end")
}

func TestListClientFacets_ExcludesEmptySourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListClientFacets_NarrowedByJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(FacetFilter{JobNames: []string{"nightly-db"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListJobFacets_GroupsByPolicyNameAcrossManyRuns(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:ef567890:2", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:root:fedcba98:3", ObjectID: "obj-3", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 2)

	byName := map[string]Facet{}
	for _, f := range facets {
		byName[f.Name] = f
	}
	assert.Equal(t, int64(2), byName["nightly-db"].Count)
	assert.Equal(t, int64(1), byName["weekly-full"].Count)
}

func TestListJobFacets_ExcludesNonBackupJobKind(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "operating-refresh:1752400000", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}

func TestListJobFacets_NarrowedBySourceHosts(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}

func TestEnsureEntries_PersistsParentDirectoryAndShortFilename(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "/var/lib/dbdata", entries[0].ParentDirectory)
	assert.Equal(t, "data.db", entries[0].ShortFilename)
}

func TestListEntries_FiltersByParentDirectoriesMultiValue(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", ShortFilename: "index.html", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", ParentDirectory: "/etc", ShortFilename: "passwd", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{ParentDirectories: []string{"/var/lib/dbdata", "/etc"}})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var objIDs []string
	for _, e := range entries {
		objIDs = append(objIDs, e.ObjectID)
	}
	assert.ElementsMatch(t, []string{"obj-1", "obj-3"}, objIDs)
}

func TestListClientFacets_NarrowedByParentDirectories(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListJobFacets_NarrowedByParentDirectories(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}

func TestListDirectoryFacets_GroupsByParentDirectoryWithCountAndLastSeen(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 2)

	byName := map[string]Facet{}
	for _, f := range facets {
		byName[f.Name] = f
	}
	assert.Equal(t, int64(2), byName["/var/lib/dbdata"].Count)
	assert.Equal(t, int64(1), byName["/var/www"].Count)
}

func TestListDirectoryFacets_ExcludesEmptyParentDirectory(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/www", facets[0].Name)
}

func TestListDirectoryFacets_NarrowedBySourceHostsAndJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/lib/dbdata", facets[0].Name)

	facets, err = store.ListDirectoryFacets(FacetFilter{JobNames: []string{"hourly-web"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/www", facets[0].Name)
}

func TestListDirectoryFacets_IgnoresOwnDimension(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	// A ParentDirectories value on the request itself must not narrow
	// ListDirectoryFacets -- it's this facet's own dimension.
	facets, err := store.ListDirectoryFacets(FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	assert.Len(t, facets, 2)
}

func TestEnsureDirectories_PersistsBatch(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}))

	children, err := store.ListDirectoryChildren("", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "/", children[0].Path)
}

func TestEnsureDirectories_DuplicatePathIsNoOp(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []DirectoryAncestor{{Path: "/var", ParentPath: "/", Name: "var", Depth: 1}}
	require.NoError(t, store.EnsureDirectories(batch))
	require.NoError(t, store.EnsureDirectories(batch)) // resend, e.g. after a retried sync

	children, err := store.ListDirectoryChildren("/", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
}

func TestEnsureDirectories_EmptyBatchSucceeds(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories(nil))
}

func TestListDirectoryChildren_ReturnsChildrenOfGivenParentPath(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
		{Path: "/var/www", ParentPath: "/var", Name: "www", Depth: 2},
	}))

	children, err := store.ListDirectoryChildren("/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 2)
	names := []string{children[0].Name, children[1].Name}
	assert.ElementsMatch(t, []string{"lib", "www"}, names)
}

func TestListDirectoryChildren_EmptyParentPathReturnsTrueRoots(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: `C:\`, ParentPath: "", Name: `C:\`, Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}))

	children, err := store.ListDirectoryChildren("", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 2)
	names := []string{children[0].Name, children[1].Name}
	assert.ElementsMatch(t, []string{"/", `C:\`}, names)
}

func TestListDirectoryChildren_UnknownParentPathReturnsEmpty(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	children, err := store.ListDirectoryChildren("/nope", FacetFilter{})
	require.NoError(t, err)
	assert.Empty(t, children)
}

func TestListDirectoryChildren_FileCountAndLastSeenRespectFilters(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: older},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: newer},
	}))
	// EnsureEntries stamps ReceivedAt = time.Now(); simulate a range that
	// excludes nothing so both count, then a range that excludes both.
	children, err := store.ListDirectoryChildren("/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(2), children[0].FileCount)

	future := time.Now().Add(24 * time.Hour)
	children, err = store.ListDirectoryChildren("/var", FacetFilter{ReceivedAfter: future})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(0), children[0].FileCount)
	assert.True(t, children[0].LastSeen.IsZero())
}

func TestListDirectoryChildren_FileCountRespectsSourceHostsAndJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))
	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-lib:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/lib", StoreCreatedAt: time.Now()},
	}))

	children, err := store.ListDirectoryChildren("/var", FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(1), children[0].FileCount)

	children, err = store.ListDirectoryChildren("/var", FacetFilter{JobNames: []string{"hourly-web"}})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(1), children[0].FileCount)
}

func TestListDirectoryChildren_ChildWithNoMatchingFilesStillAppears(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	// /var/lib has no direct files (only a subfolder, /var/lib/dbdata,
	// does) -- existence must still surface it so the UI can navigate
	// through it, per the design's filter-independent-existence rule.
	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
		{Path: "/var/lib/dbdata", ParentPath: "/var/lib", Name: "dbdata", Depth: 3},
	}))

	children, err := store.ListDirectoryChildren("/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "/var/lib", children[0].Path)
	assert.Equal(t, int64(0), children[0].FileCount)
	assert.True(t, children[0].HasChildren)
}

func TestListDirectoryChildren_HasChildrenFalseForLeafDirectory(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))

	children, err := store.ListDirectoryChildren("/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.False(t, children[0].HasChildren)
}
