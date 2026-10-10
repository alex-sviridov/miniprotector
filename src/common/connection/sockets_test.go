package connection

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidServiceName(t *testing.T) {
	valid := []string{"catalogservice.CatalogService", "grpc.health.v1.Health", "a", "a_b.C1"}
	invalid := []string{"", ".", "..", "a..b", ".a", "a.", "a/b", "../x", "a b", "a\x00b", "a-b"}
	for _, n := range valid {
		assert.True(t, ValidServiceName(n), n)
	}
	for _, n := range invalid {
		assert.False(t, ValidServiceName(n), "%q", n)
	}
}

func TestServiceSocketPath(t *testing.T) {
	p, err := ServiceSocketPath("/run/mp", "catalogservice.CatalogService")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/run/mp", "services", "catalogservice.CatalogService.sock"), p)
	assert.Equal(t, filepath.Join("/run/mp", "services"), ServicesDir("/run/mp"))

	_, err = ServiceSocketPath("/run/mp", "../etc/passwd")
	assert.Error(t, err)
}
