package catalog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))
	after := time.Now().Add(1 * time.Second)

	facets, err := store.ListClientFacets(t.Context(), FacetFilter{})
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

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(t.Context(), FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListClientFacets_NarrowedByJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(t.Context(), FacetFilter{JobNames: []string{"nightly-db"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListJobFacets_GroupsByPolicyNameAcrossManyRuns(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:ef567890:2", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:root:fedcba98:3", ObjectID: "obj-3", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(t.Context(), FacetFilter{})
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

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "operating-refresh:1752400000", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(t.Context(), FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}

func TestListJobFacets_NarrowedBySourceHosts(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(t.Context(), FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}

func TestEnsureEntries_PersistsParentDirectoryAndShortFilename(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "/var/lib/dbdata", entries[0].ParentDirectory)
	assert.Equal(t, "data.db", entries[0].ShortFilename)
}

func TestListEntries_FiltersByParentDirectoriesMultiValue(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", ShortFilename: "index.html", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", ParentDirectory: "/etc", ShortFilename: "passwd", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(t.Context(), ListEntriesFilter{ParentDirectories: []string{"/var/lib/dbdata", "/etc"}})
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

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(t.Context(), FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListJobFacets_NarrowedByParentDirectories(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(t.Context(), FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}

func TestListDirectoryFacets_GroupsByParentDirectoryWithCountAndLastSeen(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(t.Context(), FacetFilter{})
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

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(t.Context(), FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/www", facets[0].Name)
}

func TestListDirectoryFacets_NarrowedBySourceHostsAndJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(t.Context(), FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/lib/dbdata", facets[0].Name)

	facets, err = store.ListDirectoryFacets(t.Context(), FacetFilter{JobNames: []string{"hourly-web"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/www", facets[0].Name)
}

func TestListDirectoryFacets_IgnoresOwnDimension(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	// A ParentDirectories value on the request itself must not narrow
	// ListDirectoryFacets -- it's this facet's own dimension.
	facets, err := store.ListDirectoryFacets(t.Context(), FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	assert.Len(t, facets, 2)
}

func TestAggregateFacets_EmptyInputReturnsEmptySlice(t *testing.T) {
	facets := aggregateFacets(nil)
	assert.Empty(t, facets)
}

func TestAggregateFacets_SingleName(t *testing.T) {
	now := time.Now()
	facets := aggregateFacets([]facetRow{
		{Name: "host-a", ReceivedAt: now},
	})
	require.Len(t, facets, 1)
	assert.Equal(t, "host-a", facets[0].Name)
	assert.Equal(t, int64(1), facets[0].Count)
	assert.Equal(t, now, facets[0].LastSeen)
}

func TestAggregateFacets_MultipleNamesCountAndTrackLatestReceivedAt(t *testing.T) {
	earlier := time.Now().Add(-time.Hour)
	later := time.Now()
	facets := aggregateFacets([]facetRow{
		{Name: "host-a", ReceivedAt: earlier},
		{Name: "host-b", ReceivedAt: later},
		{Name: "host-a", ReceivedAt: later}, // later than host-a's first row -- LastSeen must advance
	})
	require.Len(t, facets, 2)
	assert.Equal(t, "host-a", facets[0].Name) // first-seen order preserved
	assert.Equal(t, int64(2), facets[0].Count)
	assert.Equal(t, later, facets[0].LastSeen)
	assert.Equal(t, "host-b", facets[1].Name)
	assert.Equal(t, int64(1), facets[1].Count)
}

func TestAggregateFacets_DropsEmptyNameRows(t *testing.T) {
	facets := aggregateFacets([]facetRow{
		{Name: "", ReceivedAt: time.Now()},
		{Name: "host-a", ReceivedAt: time.Now()},
	})
	require.Len(t, facets, 1)
	assert.Equal(t, "host-a", facets[0].Name)
}
