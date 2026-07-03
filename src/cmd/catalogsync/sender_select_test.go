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

	sender := selectSender(conf, logger, "unused")

	_, ok := sender.(*LoggingSender)
	assert.True(t, ok)
}

func TestSelectSender_UnreachableCatalogFallsBackToLoggingSender(t *testing.T) {
	conf := &config.Config{
		CatalogHost:          "127.0.0.1",
		CatalogPort:          freeTCPPort(t), // nothing listening
		ConnectionTimeOutSec: 1,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	sender := selectSender(conf, logger, "../../common/testdata/certs")

	_, ok := sender.(*LoggingSender)
	assert.True(t, ok)
}
