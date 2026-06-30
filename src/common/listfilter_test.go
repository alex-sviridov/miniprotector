package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseServerPath_Empty(t *testing.T) {
	server, path, err := ParseServerPath("")
	require.NoError(t, err)
	assert.Equal(t, "", server)
	assert.Equal(t, "", path)
}

func TestParseServerPath_PathOnly(t *testing.T) {
	server, path, err := ParseServerPath("/home/user")
	require.NoError(t, err)
	assert.Equal(t, "", server)
	assert.Equal(t, "/home/user", path)
}

func TestParseServerPath_ServerAndPath(t *testing.T) {
	server, path, err := ParseServerPath("myhost:/home/user")
	require.NoError(t, err)
	assert.Equal(t, "myhost", server)
	assert.Equal(t, "/home/user", path)
}

func TestParseServerPath_PathWithColon(t *testing.T) {
	// First colon only splits server from path — remaining colons stay in path.
	server, path, err := ParseServerPath("myhost:C:/Users/foo")
	require.NoError(t, err)
	assert.Equal(t, "myhost", server)
	assert.Equal(t, "C:/Users/foo", path)
}

func TestParseServerPath_LeadingColonMeansNoServer(t *testing.T) {
	server, path, err := ParseServerPath(":/home/user")
	require.NoError(t, err)
	assert.Equal(t, "", server)
	assert.Equal(t, "/home/user", path)
}

func TestParseServerPath_EmptyPathAfterColonIsError(t *testing.T) {
	_, _, err := ParseServerPath("myhost:")
	assert.Error(t, err)
}
