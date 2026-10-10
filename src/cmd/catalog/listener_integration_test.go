package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/common/mtls/mtlstest"
	"github.com/alex-sviridov/miniprotector/common/svcproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCatalogBehindListener drives the real catalog server (real role
// matrix) through the real proxy over a real mTLS connection: a store-role
// caller reaches SyncFileVersions, a client-role caller is refused by catalog's
// own authz using the forwarded identity.
func TestCatalogBehindListener(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runDir, err := os.MkdirTemp("", "mp")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(runDir) })

	ca := mtlstest.NewCA(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv, _ := newTestCatalogServer(t)
	backendCerts := ca.CertsDir(t, ca.Issue(t, "catalog"))
	go connection.StartServiceServer(ctx, logger, backendCerts, runDir, roleRequirements(), func(s *grpc.Server) {
		pb.RegisterCatalogServiceServer(s, srv)
	})
	sock, err := connection.ServiceSocketPath(runDir, pb.CatalogService_ServiceDesc.ServiceName)
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(sock); return err == nil }, 5*time.Second, 20*time.Millisecond)

	proxy := svcproxy.New(logger, runDir)
	t.Cleanup(proxy.Close)
	creds, err := mtls.LoadServerCredentials(ca.CertsDir(t, ca.Issue(t, "listener")))
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	go connection.StartServerWithCredentials(ctx, logger, port, creds, nil, func(*grpc.Server) {}, proxy.ServerOptions()...)

	call := func(role string) error {
		dir := ca.CertsDir(t, ca.Issue(t, "bwfs-1", mtlstest.WithAttributes(map[string]string{"authz-role": role})))
		var conn *grpc.ClientConn
		require.Eventually(t, func() bool {
			c, err := connection.Connect("localhost", port, 2, dir)
			conn = c
			return err == nil
		}, 10*time.Second, 50*time.Millisecond)
		defer conn.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pb.NewCatalogServiceClient(conn).SyncFileVersions(cctx, &pb.SyncRequest{})
		return err
	}

	err = call("store")
	assert.NotEqual(t, codes.PermissionDenied, status.Code(err), "store role must pass catalog's authz: %v", err)
	assert.NotEqual(t, codes.Unauthenticated, status.Code(err), "forwarded identity must verify: %v", err)

	assert.Equal(t, codes.PermissionDenied, status.Code(call("client")))
}
