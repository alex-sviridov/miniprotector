package connection

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

const (
	fixtureCertsDir   = "../testdata/certs"
	untrustedCertsDir = "../testdata/certs-untrusted"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestStartServerConnect_RoundTripSucceeds(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(ctx, testLogger(), port, fixtureCertsDir, func(s *grpc.Server) {})
	}()
	time.Sleep(100 * time.Millisecond)

	conn, err := Connect("127.0.0.1", port, 5, fixtureCertsDir)
	require.NoError(t, err)
	conn.Close()

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("StartServer did not shut down in time")
	}
}

func TestStartServerConnect_UntrustedClientCertRejected(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(ctx, testLogger(), port, fixtureCertsDir, func(s *grpc.Server) {})
	}()
	time.Sleep(100 * time.Millisecond)

	_, err := Connect("127.0.0.1", port, 2, untrustedCertsDir)
	assert.Error(t, err)

	cancel()
	<-errCh
}

func TestStartServer_MissingCertsDirFailsFast(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := StartServer(ctx, testLogger(), port, "does-not-exist", func(s *grpc.Server) {})
	assert.Error(t, err)
}

func TestConnect_MissingCertsDirFailsFast(t *testing.T) {
	_, err := Connect("127.0.0.1", 1, 1, "does-not-exist")
	assert.Error(t, err)
}

func TestConnectWithIdentity_RoundTripSucceeds(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(ctx, testLogger(), port, fixtureCertsDir, func(s *grpc.Server) {})
	}()
	time.Sleep(100 * time.Millisecond)

	conn, err := ConnectWithIdentity("127.0.0.1", port, 5, fixtureCertsDir, "client.crt", "client.key")
	require.NoError(t, err)
	conn.Close()

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("StartServer did not shut down in time")
	}
}
