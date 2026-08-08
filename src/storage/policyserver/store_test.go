package policyserver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestNew_OpensAndClosesCleanly(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

func TestRecordCheckin_ThenCheckinsForPolicy_RoundTrips(t *testing.T) {
	store := newTestStore(t)
	seenAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "host-a", seenAt))

	records, err := store.CheckinsForPolicy(t.Context(), "policy-1")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "policy-1", records[0].PolicyID)
	assert.Equal(t, "host-a", records[0].Hostname)
	assert.True(t, seenAt.Equal(records[0].LastSeenAt))
}

func TestRecordCheckin_UpsertOverwritesTimestampRatherThanDuplicating(t *testing.T) {
	store := newTestStore(t)
	first := time.Now().Add(-time.Hour).Truncate(time.Second)
	second := time.Now().Truncate(time.Second)

	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "host-a", first))
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "host-a", second))

	records, err := store.CheckinsForPolicy(t.Context(), "policy-1")
	require.NoError(t, err)
	require.Len(t, records, 1, "same (policy, host) pair must upsert, not duplicate")
	assert.True(t, second.Equal(records[0].LastSeenAt))
}

func TestCheckinsForPolicy_ScopesByPolicyID(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "host-a", time.Now()))
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-2", "host-b", time.Now()))

	records, err := store.CheckinsForPolicy(t.Context(), "policy-1")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "host-a", records[0].Hostname)
}

func TestCheckinsForPolicy_OrderedByLastSeenAtDescending(t *testing.T) {
	store := newTestStore(t)
	older := time.Now().Add(-time.Hour).Truncate(time.Second)
	newer := time.Now().Truncate(time.Second)
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "apple", older))
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "zebra", newer))

	records, err := store.CheckinsForPolicy(t.Context(), "policy-1")
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "zebra", records[0].Hostname, "the most recently checked-in host must come first")
	assert.Equal(t, "apple", records[1].Hostname)
}

func TestCheckinsForPolicy_UnknownPolicyReturnsEmpty(t *testing.T) {
	store := newTestStore(t)
	records, err := store.CheckinsForPolicy(t.Context(), "ghost")
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestDeleteOlderThan_RemovesOnlyStaleRecords(t *testing.T) {
	store := newTestStore(t)
	stale := time.Now().Add(-2 * time.Hour)
	fresh := time.Now()
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "stale-host", stale))
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "fresh-host", fresh))

	deleted, err := store.DeleteOlderThan(t.Context(), time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	records, err := store.CheckinsForPolicy(t.Context(), "policy-1")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "fresh-host", records[0].Hostname)
}

func TestDeleteOlderThan_ExactlyAtCutoffIsNotDeleted(t *testing.T) {
	store := newTestStore(t)
	cutoff := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "host-a", cutoff))

	deleted, err := store.DeleteOlderThan(t.Context(), cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted, "a record exactly at cutoff is not strictly older than it")
}

func TestDeleteForPolicy_RemovesOnlyThatPolicysRows(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "host-a", time.Now()))
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-1", "host-b", time.Now()))
	require.NoError(t, store.RecordCheckin(t.Context(), "policy-2", "host-c", time.Now()))

	require.NoError(t, store.DeleteForPolicy(t.Context(), "policy-1"))

	records, err := store.CheckinsForPolicy(t.Context(), "policy-1")
	require.NoError(t, err)
	assert.Empty(t, records, "policy-1's rows must be gone")

	records, err = store.CheckinsForPolicy(t.Context(), "policy-2")
	require.NoError(t, err)
	require.Len(t, records, 1, "policy-2's rows must be untouched")
	assert.Equal(t, "host-c", records[0].Hostname)
}
