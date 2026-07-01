//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/stretchr/testify/require"
)

var testImageID string

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Locate repo root (two levels up from src/e2e/)
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	var t fakeT
	testImageID = buildImage(ctx, &t, repoRoot)
	if t.failed {
		fmt.Fprintln(os.Stderr, "failed to build e2e Docker image")
		os.Exit(1)
	}

	code := m.Run()

	// Clean up image
	cli := newDockerClient(&t)
	_, _ = cli.ImageRemove(context.Background(), testImageID, image.RemoveOptions{Force: true})
	cli.Close()

	os.Exit(code)
}

// fakeT satisfies testingT for TestMain, where a real *testing.T is unavailable.
type fakeT struct{ failed bool }

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	f.failed = true
}
func (f *fakeT) Log(args ...any)                 { fmt.Println(args...) }
func (f *fakeT) Logf(format string, args ...any) { fmt.Printf(format+"\n", args...) }
func (f *fakeT) Cleanup(func())                  {}
func (f *fakeT) Errorf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
func (f *fakeT) FailNow() { f.failed = true }

func TestE2E_SingleSubfolderBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Generate test data
	dataDir := t.TempDir()
	allRecords := generateTestData(t, dataDir)

	// Split into subA and subB records. brfs is pointed directly at the subA
	// directory, so its scan root is subA/ itself: paths it reports (and
	// thus the expected paths for assertFilesPresent/assertFilesAbsent,
	// which prepend "/testdata/") must NOT carry the "subA/" prefix.
	// subBRecords keep their original "subB/..." relative paths because
	// they're only used for assertFilesAbsent, which just needs paths that
	// are guaranteed not to appear in the list.
	subARecords := make(map[string]fileRecord)
	subBRecords := make(map[string]fileRecord)
	for rel, rec := range allRecords {
		if len(rel) >= 4 && rel[:4] == "subA" {
			withoutPrefix := filepath.ToSlash(rel)[len("subA/"):]
			subARecords[withoutPrefix] = rec
		} else {
			subBRecords[rel] = rec
		}
	}

	// Create isolated network and storage
	networkID := createNetwork(ctx, t)
	storageDir := t.TempDir()

	// Start bwfs server
	hostPort := startBwfsContainer(ctx, t, testImageID, networkID, storageDir)
	require.NoError(t, waitForBwfs(ctx, hostPort))

	// Run brfs for subA only, 1 stream
	exitCode := runBrfsContainer(ctx, t, testImageID, networkID,
		filepath.Join(dataDir, "subA"), "bwfs.internal", 1, "")
	require.Equal(t, 0, exitCode, "brfs exited with non-zero code")

	// Validate with bwfs list
	listJSON := runBwfsListContainer(ctx, t, testImageID, networkID, storageDir)
	t.Logf("bwfs list output: %s", string(listJSON))
	list := parseListOutput(t, listJSON)

	// subA files present with correct size and checksum
	assertFilesPresent(t, list, subARecords, storageDir)
	// subB files absent
	assertFilesAbsent(t, list, subBRecords)
}

func TestE2E_AllFoldersBackup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Generate test data
	dataDir := t.TempDir()
	allRecords := generateTestData(t, dataDir)

	// Create isolated network and storage
	networkID := createNetwork(ctx, t)
	storageDir := t.TempDir()

	// Start bwfs server
	hostPort := startBwfsContainer(ctx, t, testImageID, networkID, storageDir)
	require.NoError(t, waitForBwfs(ctx, hostPort))

	// Run brfs for all folders, 4 streams
	exitCode := runBrfsContainer(ctx, t, testImageID, networkID, dataDir, "bwfs.internal", 4, "")
	require.Equal(t, 0, exitCode, "brfs exited with non-zero code")

	// Validate with bwfs list
	listJSON := runBwfsListContainer(ctx, t, testImageID, networkID, storageDir)
	t.Logf("bwfs list output: %s", string(listJSON))
	list := parseListOutput(t, listJSON)

	// All files present with correct size and checksum
	assertFilesPresent(t, list, allRecords, storageDir)
}

func TestE2E_Verify_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir := t.TempDir()
	generateTestData(t, dataDir)

	networkID := createNetwork(ctx, t)
	storageDir := t.TempDir()

	hostPort := startBwfsContainer(ctx, t, testImageID, networkID, storageDir)
	require.NoError(t, waitForBwfs(ctx, hostPort))

	exitCode := runBrfsContainer(ctx, t, testImageID, networkID,
		filepath.Join(dataDir, "subA"), "bwfs.internal", 4, "brfs-source")
	require.Equal(t, 0, exitCode, "brfs should exit 0")

	exitCode = runRwfsVerifyContainer(ctx, t, testImageID, networkID, false)
	require.Equal(t, 0, exitCode, "rwfs verify should pass on clean backup")
}

func TestE2E_Verify_CorruptionDetection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir := t.TempDir()
	generateTestData(t, dataDir)

	networkID := createNetwork(ctx, t)
	storageDir := t.TempDir()

	hostPort := startBwfsContainer(ctx, t, testImageID, networkID, storageDir)
	require.NoError(t, waitForBwfs(ctx, hostPort))

	// Back up subA only (8 files, known chunk layout)
	exitCode := runBrfsContainer(ctx, t, testImageID, networkID,
		filepath.Join(dataDir, "subA"), "bwfs.internal", 1, "brfs-source")
	require.Equal(t, 0, exitCode, "brfs should exit 0")

	// Confirm baseline passes
	exitCode = runRwfsVerifyContainer(ctx, t, testImageID, networkID, true)
	require.Equal(t, 0, exitCode, "baseline verify should pass")

	// Corrupt one chunk on the host filesystem (shared with the container via bind mount)
	corruptOneChunk(t, storageDir)

	// Verify must now detect the corruption and exit non-zero
	exitCode = runRwfsVerifyContainer(ctx, t, testImageID, networkID, false)
	require.NotEqual(t, 0, exitCode, "verify must fail when a chunk is corrupted")
}

func TestE2E_Backup_HealsCorruptedChunk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir := t.TempDir()
	generateTestData(t, dataDir)

	networkID := createNetwork(ctx, t)
	storageDir := t.TempDir()

	hostPort := startBwfsContainer(ctx, t, testImageID, networkID, storageDir)
	require.NoError(t, waitForBwfs(ctx, hostPort))

	// Back up subA only (8 files, known chunk layout)
	exitCode := runBrfsContainer(ctx, t, testImageID, networkID,
		filepath.Join(dataDir, "subA"), "bwfs.internal", 1, "brfs-source")
	require.Equal(t, 0, exitCode, "brfs should exit 0")

	// Confirm baseline passes
	exitCode = runRwfsVerifyContainer(ctx, t, testImageID, networkID, true)
	require.Equal(t, 0, exitCode, "baseline verify should pass")

	// Delete one chunk from the host filesystem (shared with the container via
	// bind mount) — this is what actually happened in the field: a chunk file
	// went missing from the chunk store while the DB still thinks it's there.
	deleteOneChunk(t, storageDir)
	exitCode = runRwfsVerifyContainer(ctx, t, testImageID, networkID, false)
	require.NotEqual(t, 0, exitCode, "verify must fail when a chunk is missing")

	// Re-run backup on the exact same source: the file whose chunk was
	// deleted has an unchanged mtime, so the server must not skip it just
	// because a finalized DB record exists — it must detect the missing
	// chunk and re-upload it.
	exitCode = runBrfsContainer(ctx, t, testImageID, networkID,
		filepath.Join(dataDir, "subA"), "bwfs.internal", 1, "brfs-source")
	require.Equal(t, 0, exitCode, "repeat brfs should exit 0")

	// Corruption must now be healed.
	exitCode = runRwfsVerifyContainer(ctx, t, testImageID, networkID, false)
	require.Equal(t, 0, exitCode, "verify must pass after repeat backup heals the corrupted chunk")
}
