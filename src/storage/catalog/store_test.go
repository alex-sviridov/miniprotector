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
	require.NoError(t, store.EnsureEntries(t.Context(), batch))

	count, err := store.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestEnsureEntries_DuplicateSameStoreNodeIsNoOp(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()}}
	require.NoError(t, store.EnsureEntries(t.Context(), batch))
	require.NoError(t, store.EnsureEntries(t.Context(), batch)) // resend, e.g. after a retried RPC

	count, err := store.Count(t.Context())
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
	require.NoError(t, store.EnsureEntries(t.Context(), batch))

	count, err := store.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestEnsureEntries_EmptyBatchSucceeds(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	assert.NoError(t, store.EnsureEntries(t.Context(), nil))
}

func TestEnsureEntries_PersistsSourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{})
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

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	entries, hasMore, err := store.ListEntries(t.Context(), ListEntriesFilter{StoreNode: "bwfs-a"})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, entries, 1)
	assert.Equal(t, "bwfs-a", entries[0].StoreNode)
}

func TestListEntries_FiltersBySourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	entries, hasMore, err := store.ListEntries(t.Context(), ListEntriesFilter{SourceHost: "database"})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, entries, 1)
	assert.Equal(t, "database", entries[0].SourceHost)
}

func TestListEntries_FiltersByStoreNodeAndSourceHostCombined(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-3", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{StoreNode: "bwfs-a", SourceHost: "database"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "obj-1", entries[0].ObjectID)
}

func TestListEntries_FiltersByPatternSubstringOnObjectID(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "fs://bwfs-a:f:/var/log/syslog:100", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "fs://bwfs-a:f:/etc/passwd:100", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{Pattern: "/var/log"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].ObjectID, "/var/log/syslog")
}

func TestListEntries_PaginationHasMoreAndStartingAfter(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	for i := 0; i < 5; i++ {
		require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
			{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: fmt.Sprintf("obj-%d", i), StoreCreatedAt: time.Now()},
		}))
	}

	page1, hasMore, err := store.ListEntries(t.Context(), ListEntriesFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.True(t, hasMore)
	// Newest first (highest ID first).
	assert.Greater(t, page1[0].ID, page1[1].ID)

	page2, hasMore, err := store.ListEntries(t.Context(), ListEntriesFilter{Limit: 2, StartingAfter: page1[1].ID})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.True(t, hasMore)
	assert.Less(t, page2[0].ID, page1[1].ID)

	page3, hasMore, err := store.ListEntries(t.Context(), ListEntriesFilter{Limit: 2, StartingAfter: page2[1].ID})
	require.NoError(t, err)
	require.Len(t, page3, 1)
	assert.False(t, hasMore)
}

func TestListEntries_LimitDefaultsAndCaps(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{Limit: 0})
	require.NoError(t, err)
	assert.Len(t, entries, 1) // default 100, well above the 1 row present

	entries, _, err = store.ListEntries(t.Context(), ListEntriesFilter{Limit: 10000})
	require.NoError(t, err)
	assert.Len(t, entries, 1) // capped at 500, still well above the 1 row present
}

func TestListEntries_FiltersByReceivedAtRange(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}))

	included, _, err := store.ListEntries(t.Context(), ListEntriesFilter{ReceivedAfter: time.Now().Add(-1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, included, 1)

	excluded, _, err := store.ListEntries(t.Context(), ListEntriesFilter{ReceivedAfter: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, excluded, 0)

	includedBefore, _, err := store.ListEntries(t.Context(), ListEntriesFilter{ReceivedBefore: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, includedBefore, 1)

	excludedBefore, _, err := store.ListEntries(t.Context(), ListEntriesFilter{ReceivedBefore: time.Now().Add(-1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, excludedBefore, 0)
}

func TestListEntries_FiltersBySourceHostsMultiValue(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", SourceHost: "mail", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{SourceHosts: []string{"database", "mail"}})
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

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1752400000", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:1752400010", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:root:fedcba98:1752400020", ObjectID: "obj-3", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{JobNames: []string{"nightly-db", "weekly-full"}})
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

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:root:fedcba98:2", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:var-lib:abcd5678:3", ObjectID: "obj-3", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{
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

	sqlDB, err := store.writeDB.DB()
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

func TestEnsureDirectories_PersistsBatch(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories(t.Context(), []DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}))

	children, err := store.ListDirectoryChildren(t.Context(), "", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "/", children[0].Path)
}

func TestEnsureDirectories_DuplicatePathIsNoOp(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []DirectoryAncestor{{Path: "/var", ParentPath: "/", Name: "var", Depth: 1}}
	require.NoError(t, store.EnsureDirectories(t.Context(), batch))
	require.NoError(t, store.EnsureDirectories(t.Context(), batch)) // resend, e.g. after a retried sync

	children, err := store.ListDirectoryChildren(t.Context(), "/", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
}

func TestEnsureDirectories_EmptyBatchSucceeds(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories(t.Context(), nil))
}

func TestListDirectoryChildren_ReturnsChildrenOfGivenParentPath(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories(t.Context(), []DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
		{Path: "/var/www", ParentPath: "/var", Name: "www", Depth: 2},
	}))

	children, err := store.ListDirectoryChildren(t.Context(), "/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 2)
	names := []string{children[0].Name, children[1].Name}
	assert.ElementsMatch(t, []string{"lib", "www"}, names)
}

func TestListDirectoryChildren_EmptyParentPathReturnsTrueRoots(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories(t.Context(), []DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: `C:\`, ParentPath: "", Name: `C:\`, Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}))

	children, err := store.ListDirectoryChildren(t.Context(), "", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 2)
	names := []string{children[0].Name, children[1].Name}
	assert.ElementsMatch(t, []string{"/", `C:\`}, names)
}

func TestListDirectoryChildren_UnknownParentPathReturnsEmpty(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	children, err := store.ListDirectoryChildren(t.Context(), "/nope", FacetFilter{})
	require.NoError(t, err)
	assert.Empty(t, children)
}

func TestListDirectoryChildren_FileCountAndLastSeenRespectFilters(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories(t.Context(), []DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: older},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: newer},
	}))
	// EnsureEntries stamps ReceivedAt = time.Now(); simulate a range that
	// excludes nothing so both count, then a range that excludes both.
	children, err := store.ListDirectoryChildren(t.Context(), "/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(2), children[0].FileCount)

	future := time.Now().Add(24 * time.Hour)
	children, err = store.ListDirectoryChildren(t.Context(), "/var", FacetFilter{ReceivedAfter: future})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(0), children[0].FileCount)
	assert.True(t, children[0].LastSeen.IsZero())
}

func TestListDirectoryChildren_FileCountRespectsSourceHostsAndJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories(t.Context(), []DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))
	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-lib:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/lib", StoreCreatedAt: time.Now()},
	}))

	children, err := store.ListDirectoryChildren(t.Context(), "/var", FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(1), children[0].FileCount)

	children, err = store.ListDirectoryChildren(t.Context(), "/var", FacetFilter{JobNames: []string{"hourly-web"}})
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
	require.NoError(t, store.EnsureDirectories(t.Context(), []DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
		{Path: "/var/lib/dbdata", ParentPath: "/var/lib", Name: "dbdata", Depth: 3},
	}))

	children, err := store.ListDirectoryChildren(t.Context(), "/var", FacetFilter{})
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

	require.NoError(t, store.EnsureDirectories(t.Context(), []DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))

	children, err := store.ListDirectoryChildren(t.Context(), "/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.False(t, children[0].HasChildren)
}

func TestSyncBatch_PersistsEntriesAndDirectories(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	entries := []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}
	directories := []DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}
	require.NoError(t, store.SyncBatch(t.Context(), entries, directories))

	count, err := store.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	children, err := store.ListDirectoryChildren(t.Context(), "/", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "/var", children[0].Path)
}

func TestSyncBatch_RollsBackEntriesIfDirectoriesInsertFails(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	// Force the directories phase to fail with a genuine SQL error --
	// EnsureDirectories/EnsureEntries both use ON CONFLICT DO NOTHING, so no
	// ordinary bad input produces a real constraint violation.
	require.NoError(t, store.writeDB.Exec("DROP TABLE catalog_directories").Error)

	entries := []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}
	directories := []DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}
	err = store.SyncBatch(t.Context(), entries, directories)
	require.Error(t, err)
	assert.ErrorContains(t, err, "ensure directories")

	count, err := store.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "entries insert must roll back when the directories insert fails")
}
