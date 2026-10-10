package connection

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/common/mtls/mtlstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const healthService = "grpc.health.v1.Health"

func shortRunDir(t *testing.T) string {
	t.Helper() // unix socket paths max out near 108 bytes; t.TempDir() can be long
	dir, err := os.MkdirTemp("", "mp")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func startHealthBackend(t *testing.T, certsDir, runDir string, roles map[string][]string) (cancel context.CancelFunc, done chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done = make(chan error, 1)
	go func() {
		done <- StartServiceServer(ctx, discard(), certsDir, runDir, roles, func(s *grpc.Server) {
			healthpb.RegisterHealthServer(s, health.NewServer())
		})
	}()
	sock, err := ServiceSocketPath(runDir, healthService)
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(sock); return err == nil }, 5*time.Second, 20*time.Millisecond)
	return cancel, done
}

func dialSocket(t *testing.T, runDir string) *grpc.ClientConn {
	t.Helper()
	sock, err := ServiceSocketPath(runDir, healthService)
	require.NoError(t, err)
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func withForwarded(ctx context.Context, chain [][]byte) context.Context {
	return metadata.NewOutgoingContext(ctx, mtls.WithForwardedChain(metadata.MD{}, chain))
}

func TestStartServiceServer_SocketModeAndForwardedIdentity(t *testing.T) {
	ca := mtlstest.NewCA(t)
	certsDir := ca.CertsDir(t, ca.Issue(t, "backend"))
	runDir := shortRunDir(t)
	startHealthBackend(t, certsDir, runDir, nil)

	sock, _ := ServiceSocketPath(runDir, healthService)
	info, err := os.Stat(sock)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(ServicesDir(runDir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	client := healthpb.NewHealthClient(dialSocket(t, runDir))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.Check(ctx, &healthpb.HealthCheckRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err), "no forwarded identity")

	leaf := ca.Issue(t, "node-a")
	_, err = client.Check(withForwarded(ctx, leaf.Certificate), &healthpb.HealthCheckRequest{})
	assert.NoError(t, err)
}

func TestStartServiceServer_RoleRequirementsUseForwardedIdentity(t *testing.T) {
	ca := mtlstest.NewCA(t)
	certsDir := ca.CertsDir(t, ca.Issue(t, "backend"))
	runDir := shortRunDir(t)
	startHealthBackend(t, certsDir, runDir, map[string][]string{
		"/" + healthService + "/Check": {"store"},
	})
	client := healthpb.NewHealthClient(dialSocket(t, runDir))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store := ca.Issue(t, "s", mtlstest.WithAttributes(map[string]string{"authz-role": "store"}))
	_, err := client.Check(withForwarded(ctx, store.Certificate), &healthpb.HealthCheckRequest{})
	assert.NoError(t, err)

	other := ca.Issue(t, "c", mtlstest.WithAttributes(map[string]string{"authz-role": "client"}))
	_, err = client.Check(withForwarded(ctx, other.Certificate), &healthpb.HealthCheckRequest{})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestStartServiceServer_ReplacesStaleSocket(t *testing.T) {
	ca := mtlstest.NewCA(t)
	certsDir := ca.CertsDir(t, ca.Issue(t, "backend"))
	runDir := shortRunDir(t)
	require.NoError(t, os.MkdirAll(ServicesDir(runDir), 0o700))
	sock, _ := ServiceSocketPath(runDir, healthService)
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, ln.Close()) // file stays, nobody listening

	startHealthBackend(t, certsDir, runDir, nil)

	client := healthpb.NewHealthClient(dialSocket(t, runDir))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.Check(withForwarded(ctx, ca.Issue(t, "n").Certificate), &healthpb.HealthCheckRequest{})
	assert.NoError(t, err)
}

func TestStartServiceServer_RefusesWhenServiceAlreadyLive(t *testing.T) {
	ca := mtlstest.NewCA(t)
	certsDir := ca.CertsDir(t, ca.Issue(t, "backend"))
	runDir := shortRunDir(t)
	startHealthBackend(t, certsDir, runDir, nil)

	err := StartServiceServer(context.Background(), discard(), certsDir, runDir, nil, func(s *grpc.Server) {
		healthpb.RegisterHealthServer(s, health.NewServer())
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already")
}

func TestStartServiceServer_ShutdownRemovesSocket(t *testing.T) {
	ca := mtlstest.NewCA(t)
	certsDir := ca.CertsDir(t, ca.Issue(t, "backend"))
	runDir := shortRunDir(t)
	cancel, done := startHealthBackend(t, certsDir, runDir, nil)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("StartServiceServer did not return after cancel")
	}
	sock, _ := ServiceSocketPath(runDir, healthService)
	_, err := os.Stat(sock)
	assert.True(t, os.IsNotExist(err), "socket %s should be removed", filepath.Base(sock))
}
