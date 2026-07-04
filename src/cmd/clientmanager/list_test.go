package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func TestRunList_EmptyStore(t *testing.T) {
	store := newTestManagerStore(t)
	var out bytes.Buffer
	require.NoError(t, runList(store, &out))
	assert.Equal(t, "HOSTNAME  ADDED_AT  REVOKED  LAST_SEEN\n", out.String())
}

func TestRunList_ShowsAddedClients(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	var out bytes.Buffer
	require.NoError(t, runList(store, &out))
	assert.Contains(t, out.String(), "node-1")
	assert.Contains(t, out.String(), "unknown")
}

func TestRunShow_UnknownErrors(t *testing.T) {
	store := newTestManagerStore(t)
	err := runShow(store, &Arguments{Hostname: "ghost"}, &bytes.Buffer{})
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunShow_PrintsDescriptionsAndAttributes(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindDescription, "owner", "alice"))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindAttribute, "role", "prod-db"))

	var out bytes.Buffer
	require.NoError(t, runShow(store, &Arguments{Hostname: "node-1"}, &out))
	assert.Contains(t, out.String(), "owner=alice")
	assert.Contains(t, out.String(), "role=prod-db")
}

func TestRunRevoke_SetsFlag(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, runRevoke(store, &Arguments{Hostname: "node-1"}))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.True(t, got.Revoked)
}

func TestRunUnrevoke_ClearsFlag(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, runRevoke(store, &Arguments{Hostname: "node-1"}))
	require.NoError(t, runUnrevoke(store, &Arguments{Hostname: "node-1"}))

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.False(t, got.Revoked)
}
