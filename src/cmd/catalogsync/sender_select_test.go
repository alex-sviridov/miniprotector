package main

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/config"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestSelectSender_NoCatalogHostReturnsLoggingSender(t *testing.T) {
	conf := &config.Config{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sender, err := selectSender(conf, logger, "unused")

	require.NoError(t, err)
	_, ok := sender.(*LoggingSender)
	assert.True(t, ok)
}

// An unreachable catalog at startup must not fall back to LoggingSender:
// LoggingSender.Send always reports success, and run() persists its cursor
// on success, so any batch "sent" that way is silently dropped for good.
// selectSender instead returns a real GrpcSender immediately -- gRPC dials
// lazily and keeps retrying, so the first Send just fails and retries via
// run()'s existing backoff until the catalog is reachable.
func TestSelectSender_UnreachableCatalogAtStartupReturnsGrpcSender(t *testing.T) {
	conf := &config.Config{
		CatalogHost:          "127.0.0.1",
		CatalogPort:          freeTCPPort(t), // nothing listening
		ConnectionTimeOutSec: 1,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sender, err := selectSender(conf, logger, "../../common/testdata/certs")

	require.NoError(t, err)
	_, ok := sender.(*GrpcSender)
	assert.True(t, ok)
}

func TestSelectSender_BadCertsDirReturnsError(t *testing.T) {
	conf := &config.Config{
		CatalogHost:          "127.0.0.1",
		CatalogPort:          freeTCPPort(t),
		ConnectionTimeOutSec: 1,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := selectSender(conf, logger, "/nonexistent/certs/dir")

	assert.Error(t, err)
}
