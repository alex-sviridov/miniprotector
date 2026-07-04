package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func TestRunSanAdd_AddsAlias(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	err := runSanAdd(store, &Arguments{Hostname: "node-1", SanAlias: "node-1.internal"})
	require.NoError(t, err)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1.internal"}, got.SANsList())
}

func TestRunSanAdd_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	err := runSanAdd(store, &Arguments{Hostname: "ghost", SanAlias: "x.internal"})
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestRunSanRemove_RemovesAlias(t *testing.T) {
	store := newTestManagerStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"a.internal"}, time.Now()))

	err := runSanRemove(store, &Arguments{Hostname: "node-1", SanAlias: "a.internal"})
	require.NoError(t, err)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Empty(t, got.SANsList())
}

func TestRunSanRemove_UnknownHostnameErrors(t *testing.T) {
	store := newTestManagerStore(t)
	err := runSanRemove(store, &Arguments{Hostname: "ghost", SanAlias: "x.internal"})
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}
