package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveToken_FlagTakesPriority(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "env-token")
	got, err := resolveToken("flag-token", strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "flag-token", got)
}

func TestResolveToken_EnvVarUsedWhenFlagEmpty(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "env-token")
	got, err := resolveToken("", strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "env-token", got)
}

func TestResolveToken_FallsBackToStdin(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "")
	got, err := resolveToken("", strings.NewReader("stdin-token\n"))
	require.NoError(t, err)
	assert.Equal(t, "stdin-token", got)
}
