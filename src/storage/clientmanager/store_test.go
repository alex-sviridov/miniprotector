package clientmanager

import (
	"errors"
	"sync"
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

func TestAddClient_ThenGetClient_RoundTrips(t *testing.T) {
	store := newTestStore(t)
	addedAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.AddClient("node-1", nil, addedAt))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.Hostname)
	assert.True(t, addedAt.Equal(got.AddedAt))
	assert.False(t, got.Revoked)
}

func TestAddClient_WithSANs_ThenGetClient_RoundTripsSANsList(t *testing.T) {
	store := newTestStore(t)
	sans := []string{"alias1", "alias2"}

	require.NoError(t, store.AddClient("node-1", sans, time.Now()))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, sans, got.SANsList())
}

func TestAddClient_WithNilSANs_ThenGetClient_SANsListReturnsNil(t *testing.T) {
	store := newTestStore(t)

	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Nil(t, got.SANsList())
}

func TestAddClient_DuplicateReturnsErrClientExists(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	err := store.AddClient("node-1", nil, time.Now())
	assert.ErrorIs(t, err, ErrClientExists)
}

func TestGetClient_UnknownReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetClient("ghost")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestListClients_OrderedByHostname(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("zebra", nil, time.Now()))
	require.NoError(t, store.AddClient("apple", nil, time.Now()))

	clients, err := store.ListClients()
	require.NoError(t, err)
	require.Len(t, clients, 2)
	assert.Equal(t, "apple", clients[0].Hostname)
	assert.Equal(t, "zebra", clients[1].Hostname)
}

func TestSetRevoked_ThenGetClient_ReflectsFlag(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	revokedAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.SetRevoked("node-1", true, revokedAt))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.True(t, got.Revoked)
	require.NotNil(t, got.RevokedAt)
	assert.True(t, revokedAt.Equal(*got.RevokedAt))

	require.NoError(t, store.SetRevoked("node-1", false, time.Now()))
	got, err = store.GetClient("node-1")
	require.NoError(t, err)
	assert.False(t, got.Revoked)
	assert.Nil(t, got.RevokedAt)
}

func TestSetRevoked_UnknownReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.SetRevoked("ghost", true, time.Now())
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestSetKV_ThenKV_RoundTrips(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "alice"))
	require.NoError(t, store.SetKV("node-1", KindAttribute, "role", "prod-db"))

	descs, err := store.KV("node-1", KindDescription)
	require.NoError(t, err)
	require.Len(t, descs, 1)
	assert.Equal(t, "owner", descs[0].Key)
	assert.Equal(t, "alice", descs[0].Value)

	attrs, err := store.KV("node-1", KindAttribute)
	require.NoError(t, err)
	require.Len(t, attrs, 1)
	assert.Equal(t, "role", attrs[0].Key)
	assert.Equal(t, "prod-db", attrs[0].Value)
}

func TestSetKV_UpsertOverwritesValue(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "alice"))
	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "bob"))

	descs, err := store.KV("node-1", KindDescription)
	require.NoError(t, err)
	require.Len(t, descs, 1)
	assert.Equal(t, "bob", descs[0].Value)
}

func TestSetKV_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.SetKV("ghost", KindDescription, "owner", "alice")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestUnsetKV_RemovesRow(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "alice"))

	require.NoError(t, store.UnsetKV("node-1", KindDescription, "owner"))

	descs, err := store.KV("node-1", KindDescription)
	require.NoError(t, err)
	assert.Empty(t, descs)
}

func TestAddClient_ConcurrentDuplicatePreservesSentinel(t *testing.T) {
	store := newTestStore(t)
	hostname := "concurrent-node"
	addedAt := time.Now()

	var wg sync.WaitGroup
	results := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = store.AddClient(hostname, nil, addedAt)
	}()
	go func() {
		defer wg.Done()
		results[1] = store.AddClient(hostname, nil, addedAt)
	}()
	wg.Wait()

	// Exactly one should succeed, the other should return ErrClientExists
	successCount := 0
	for _, err := range results {
		if err == nil {
			successCount++
		} else if !errors.Is(err, ErrClientExists) {
			t.Fatalf("expected ErrClientExists but got: %v", err)
		}
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent AddClient should succeed")
}

func TestNew_OpensAndClosesCleanly(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

func TestAddSAN_AppendsToEmptyList(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	require.NoError(t, store.AddSAN("node-1", "node-1.internal"))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1.internal"}, got.SANsList())
}

func TestAddSAN_DuplicateIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"a.internal"}, time.Now()))

	require.NoError(t, store.AddSAN("node-1", "a.internal"))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.internal"}, got.SANsList())
}

func TestAddSAN_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.AddSAN("ghost", "a.internal")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestRemoveSAN_RemovesExistingAlias(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"a.internal", "b.internal"}, time.Now()))

	require.NoError(t, store.RemoveSAN("node-1", "a.internal"))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"b.internal"}, got.SANsList())
}

func TestRemoveSAN_NonExistentAliasIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"a.internal"}, time.Now()))

	require.NoError(t, store.RemoveSAN("node-1", "z.internal"))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a.internal"}, got.SANsList())
}

func TestRemoveSAN_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.RemoveSAN("ghost", "a.internal")
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestUpdateLastSeen_SetsTimestamp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	seenAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.UpdateLastSeen("node-1", seenAt))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	require.NotNil(t, got.LastSeenAt)
	assert.True(t, seenAt.Equal(*got.LastSeenAt))
}

func TestUpdateLastSeen_OverwritesPreviousValue(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.UpdateLastSeen("node-1", time.Now().Add(-time.Hour)))

	newSeenAt := time.Now().Truncate(time.Second)
	require.NoError(t, store.UpdateLastSeen("node-1", newSeenAt))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.True(t, newSeenAt.Equal(*got.LastSeenAt))
}

func TestUpdateLastSeen_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateLastSeen("ghost", time.Now())
	assert.ErrorIs(t, err, ErrClientNotFound)
}

func TestGetClient_NewClientHasNilLastSeenAt(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Nil(t, got.LastSeenAt)
}

func TestLoadClientView_ReturnsFullRecordWithKVAndSANs(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"alias.internal"}, time.Now()))
	require.NoError(t, store.SetKV("node-1", KindAttribute, "role", "db"))
	require.NoError(t, store.SetKV("node-1", KindDescription, "owner", "alice"))

	view, err := store.LoadClientView("node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", view.Hostname)
	assert.False(t, view.Revoked)
	assert.Equal(t, []string{"alias.internal"}, view.SANs)
	assert.Equal(t, "db", view.Attributes["role"])
	assert.Equal(t, "alice", view.Descriptions["owner"])
}

func TestLoadClientView_UnknownHostnameReturnsErrClientNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.LoadClientView("ghost")
	assert.ErrorIs(t, err, ErrClientNotFound)
}
