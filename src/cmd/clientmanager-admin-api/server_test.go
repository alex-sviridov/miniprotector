// src/cmd/clientmanager-admin-api/server_test.go
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

// stubMinter is a test double for the minter function type, recording its
// last call and returning a canned token/error.
type stubMinter struct {
	token    string
	err      error
	calls    int
	lastHost string
	lastSANs []string
}

func (r *stubMinter) mint(hostname string, sans []string, opts certmint.Options) (string, error) {
	r.calls++
	r.lastHost = hostname
	r.lastSANs = sans
	if r.err != nil {
		return "", r.err
	}
	if r.token == "" {
		return "tok-default", nil
	}
	return r.token, nil
}

func newTestAdminServer(t *testing.T) (*clientManagerAdminServer, *clientmanagerstore.Store, *stubMinter) {
	t.Helper()
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	rec := &stubMinter{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewClientManagerAdminServer(store, rec.mint, certmint.Options{Provisioner: "admin@backup.internal"}, logger)
	return srv, store, rec
}

func TestAddClient_MintsAndRecordsClient(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	rec.token = "tok-abc"

	resp, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1", Sans: []string{"alias.internal"}})
	require.NoError(t, err)
	assert.Equal(t, "tok-abc", resp.GetToken())
	assert.Equal(t, "node-1", rec.lastHost)
	assert.Equal(t, []string{"alias.internal"}, rec.lastSANs)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.Hostname)
}

func TestAddClient_DuplicateHostnameReturnsAlreadyExists(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	_, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1"})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
	assert.Equal(t, 0, rec.calls, "mint must not be called for a duplicate add")
}

func TestAddClient_MintFailureDoesNotRecordClient(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	rec.err = errors.New("ca unreachable")

	_, err := srv.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "node-1"})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err))

	_, err = store.GetClient("node-1")
	assert.ErrorIs(t, err, clientmanagerstore.ErrClientNotFound)
}

func TestReEnrollClient_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, rec := newTestAdminServer(t)

	_, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "ghost"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Equal(t, 0, rec.calls)
}

func TestReEnrollClient_NoSANOverride_ReusesStoredSANs(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", []string{"alias1", "alias2"}, time.Now()))

	resp, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "node-1"})
	require.NoError(t, err)
	assert.Equal(t, "tok-default", resp.GetToken())
	assert.Equal(t, []string{"alias1", "alias2"}, rec.lastSANs)
}

func TestReEnrollClient_WithSANOverride_UsesOverrideNotStoredSANs(t *testing.T) {
	srv, store, rec := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", []string{"alias1"}, time.Now()))

	_, err := srv.ReEnrollClient(context.Background(), &pb.ReEnrollClientRequest{Hostname: "node-1", Sans: []string{"override1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"override1"}, rec.lastSANs)
}

func TestRevokeClient_SetsRevokedAndReturnsUpdatedClient(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	client, err := srv.RevokeClient(context.Background(), &pb.RevokeClientRequest{Hostname: "node-1"})
	require.NoError(t, err)
	assert.True(t, client.GetRevoked())
	assert.NotZero(t, client.GetRevokedAt())
}

func TestRevokeClient_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, _ := newTestAdminServer(t)

	_, err := srv.RevokeClient(context.Background(), &pb.RevokeClientRequest{Hostname: "ghost"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestUnrevokeClient_ClearsRevokedFlag(t *testing.T) {
	srv, store, _ := newTestAdminServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.SetRevoked("node-1", true, time.Now()))

	client, err := srv.UnrevokeClient(context.Background(), &pb.UnrevokeClientRequest{Hostname: "node-1"})
	require.NoError(t, err)
	assert.False(t, client.GetRevoked())
	assert.Zero(t, client.GetRevokedAt())
}

func TestUnrevokeClient_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _, _ := newTestAdminServer(t)

	_, err := srv.UnrevokeClient(context.Background(), &pb.UnrevokeClientRequest{Hostname: "ghost"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}
