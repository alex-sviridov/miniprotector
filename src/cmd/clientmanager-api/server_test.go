// src/cmd/clientmanager-api/server_test.go
package main

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/alex-sviridov/miniprotector/api"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func newTestServer(t *testing.T) (*clientManagerAPIServer, *clientmanagerstore.Store) {
	t.Helper()
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewClientManagerAPIServer(store, logger), store
}

func TestListClients_ReturnsAllClientsWithAttributesAndDescriptions(t *testing.T) {
	srv, store := newTestServer(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", []string{"alias.internal"}, time.Now()))
	require.NoError(t, store.SetKV(t.Context(), "node-1", clientmanagerstore.KindAttribute, "role", "db"))
	require.NoError(t, store.SetKV(t.Context(), "node-1", clientmanagerstore.KindDescription, "owner", "alice"))

	resp, err := srv.ListClients(context.Background(), &pb.ListClientsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetClients(), 1)

	client := resp.GetClients()[0]
	assert.Equal(t, "node-1", client.GetHostname())
	assert.False(t, client.GetRevoked())
	assert.Equal(t, []string{"alias.internal"}, client.GetSans())
	assert.Equal(t, "db", client.GetAttributes()["role"])
	assert.Equal(t, "alice", client.GetDescriptions()["owner"])
}

func TestGetClient_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	_, err := srv.GetClient(context.Background(), &pb.GetClientRequest{Hostname: "ghost"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetClient_RevokedAndLastSeenTimestampsRoundTrip(t *testing.T) {
	srv, store := newTestServer(t)
	require.NoError(t, store.AddClient(t.Context(), "node-1", nil, time.Now()))
	revokedAt := time.Now().Truncate(time.Second)
	require.NoError(t, store.SetRevoked(t.Context(), "node-1", true, revokedAt))
	seenAt := time.Now().Truncate(time.Second)
	require.NoError(t, store.UpdateLastSeen(t.Context(), "node-1", seenAt))

	client, err := srv.GetClient(context.Background(), &pb.GetClientRequest{Hostname: "node-1"})
	require.NoError(t, err)
	assert.True(t, client.GetRevoked())
	assert.Equal(t, revokedAt.Unix(), client.GetRevokedAt())
	assert.Equal(t, seenAt.Unix(), client.GetLastSeenAt())
}
