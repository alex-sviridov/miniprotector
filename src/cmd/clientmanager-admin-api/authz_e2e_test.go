// authz_e2e_test.go proves role enforcement over a real mTLS + gRPC round
// trip -- not just a fabricated context -- using the same
// connection.StartServer/connection.Connect helpers production code uses
// and the project's real testdata certs (see common/mtls/peer_test.go).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/connection"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

const fixtureCertsDir = "../../common/testdata/certs"

func TestAddClient_RealMTLSRoundTrip_NonControlPlaneRoleDenied(t *testing.T) {
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewClientManagerAdminServer(store, (&stubMinter{token: "tok-abc"}).mint, certmint.Options{}, logger)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close()) // release the port; connection.StartServer re-binds it

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, roleRequirements(), func(s *grpc.Server) {
			pb.RegisterClientManagerAdminServiceServer(s, srv)
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "server did not start listening")

	// fixtureCertsDir's client.crt carries no authz-role attribute at
	// all -- exactly the state a real, already-enrolled bwfs/brfs/rwfs
	// node is in. It must be denied.
	conn, err := connection.Connect("localhost", port, 5, fixtureCertsDir)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewClientManagerAdminServiceClient(conn)
	_, err = client.AddClient(context.Background(), &pb.AddClientRequest{Hostname: "attacker-controlled"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	_, getErr := store.GetClient(t.Context(), "attacker-controlled")
	assert.ErrorIs(t, getErr, clientmanagerstore.ErrClientNotFound, "the denied AddClient must not have recorded a client")
}
