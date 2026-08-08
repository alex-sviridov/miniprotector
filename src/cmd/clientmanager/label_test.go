package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func TestParseKV_ValidPair(t *testing.T) {
	key, value, err := parseKV("owner=alice")
	require.NoError(t, err)
	assert.Equal(t, "owner", key)
	assert.Equal(t, "alice", value)
}

func TestParseKV_MissingEqualsErrors(t *testing.T) {
	_, _, err := parseKV("owner")
	assert.Error(t, err)
}

func TestRunKVSet_MultiplePairs(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))

	args := &Arguments{Hostname: "node-1", KVPairs: []string{"owner=alice", "location=rack3"}}
	require.NoError(t, runKVSet(t.Context(), store, clientmanagerstore.KindDescription, args))

	descs, err := store.KV(t.Context(), "node-1", clientmanagerstore.KindDescription)
	require.NoError(t, err)
	assert.Len(t, descs, 2)
}

func TestRunKVSet_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	args := &Arguments{Hostname: "ghost", KVPairs: []string{"owner=alice"}}
	err := runKVSet(t.Context(), store, clientmanagerstore.KindDescription, args)
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunKVUnset_RemovesKey(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))
	require.NoError(t, store.SetKV(t.Context(), "node-1", clientmanagerstore.KindAttribute, "role", "prod-db"))

	args := &Arguments{Hostname: "node-1", Key: "role"}
	require.NoError(t, runKVUnset(t.Context(), store, clientmanagerstore.KindAttribute, args))

	attrs, err := store.KV(t.Context(), "node-1", clientmanagerstore.KindAttribute)
	require.NoError(t, err)
	assert.Empty(t, attrs)
}

func TestRunKVSet_KindsAreIsolated(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))

	require.NoError(t, runKVSet(t.Context(), store, clientmanagerstore.KindDescription, &Arguments{Hostname: "node-1", KVPairs: []string{"role=not-an-attribute"}}))

	attrs, err := store.KV(t.Context(), "node-1", clientmanagerstore.KindAttribute)
	require.NoError(t, err)
	assert.Empty(t, attrs, "a description must not be visible as an attribute")
}
