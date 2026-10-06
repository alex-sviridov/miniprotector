//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOsPreallocate_SetsSizeAndAllocatesBlocks(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, osPreallocate(f, 1<<20))

	info, err := f.Stat()
	require.NoError(t, err)
	assert.EqualValues(t, 1<<20, info.Size())
	st := info.Sys().(*syscall.Stat_t)
	assert.Greater(t, st.Blocks, int64(0), "fallocate should allocate blocks, not make a hole")
}

func TestOsPreallocate_ZeroSizeIsNoOp(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, osPreallocate(f, 0))
}

func TestOsPreallocate_FullFilesystemFailsWithENOSPC(t *testing.T) {
	// A tmpfs cannot satisfy an absurd reservation; skip where /dev/shm is
	// missing or the kernel reports something other than ENOSPC.
	f, err := os.CreateTemp("/dev/shm", "mptest")
	if err != nil {
		t.Skip("no /dev/shm")
	}
	defer os.Remove(f.Name())
	defer f.Close()

	err = osPreallocate(f, 1<<50)
	if err == nil || !errors.Is(err, syscall.ENOSPC) {
		t.Skipf("kernel did not report ENOSPC (got %v)", err)
	}
}

func TestOsStartWritebackAndDropCache_DoNotPanicOrCorrupt(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Write([]byte("hello"))
	require.NoError(t, err)

	osStartWriteback(f, 0, 5)
	require.NoError(t, f.Sync())
	osDropCache(f)

	got, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestOsSyncDir(t *testing.T) {
	require.NoError(t, osSyncDir(t.TempDir()))
	assert.Error(t, osSyncDir(filepath.Join(t.TempDir(), "missing")))
}
