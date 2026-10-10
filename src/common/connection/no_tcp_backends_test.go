package connection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fronted backends must serve via StartServiceServer; a stray StartServer in
// one of these mains would reopen a TCP port behind the listener's back.
func TestFrontedBackendsDoNotOpenTCP(t *testing.T) {
	for _, svc := range []string{"catalog", "policy-server", "clientmanager-api", "clientmanager-admin-api", "api-server"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "cmd", svc, "main.go"))
		require.NoError(t, err, svc)
		text := string(src)
		assert.Contains(t, text, "StartServiceServer", svc)
		assert.False(t, strings.Contains(text, "connection.StartServer("), "%s must not call connection.StartServer", svc)
		assert.False(t, strings.Contains(text, "connection.StartServerWithCredentials("), "%s must not call connection.StartServerWithCredentials", svc)
	}
}
