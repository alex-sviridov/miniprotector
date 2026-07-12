package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildAndRunAsOwnExecutable copies a trivial no-op script/binary to dir
// under name, then re-execs the current test binary with a temp
// GOOS-appropriate wrapper so os.Executable() resolves to a path inside
// dir -- mirrors TestRealExec_ResolvesBinaryColocatedWithOwnExecutable's
// existing technique in reconcile_test.go (same package, same trick).
func TestResolveVectorBinary_FindsColocatedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("colocated-binary resolution test assumes a POSIX layout")
	}
	dir := t.TempDir()
	vectorPath := filepath.Join(dir, "vector")
	require.NoError(t, os.WriteFile(vectorPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	got, err := resolveVectorBinaryIn(dir)
	require.NoError(t, err)
	assert.Equal(t, vectorPath, got)
}

func TestResolveVectorBinary_MissingBinaryFailsLoudly(t *testing.T) {
	dir := t.TempDir() // empty -- no vector binary present

	_, err := resolveVectorBinaryIn(dir)
	assert.Error(t, err, "must fail loudly, never fall back to $PATH")
}
