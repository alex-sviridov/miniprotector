//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_CatalogReceivesReplicatedFileVersions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dataDir := t.TempDir()
	records := generateTestData(t, dataDir)
	require.NotEmpty(t, records)

	networkID := createNetwork(ctx, t)

	bwfsStorageDir := t.TempDir()
	bwfsHostPort := startBwfsContainer(ctx, t, testImageID, networkID, bwfsStorageDir)
	require.NoError(t, waitForBwfs(ctx, bwfsHostPort))

	catalogStorageDir := t.TempDir()
	startCatalogContainer(ctx, t, testImageID, networkID, catalogStorageDir)

	runCatalogsyncContainer(ctx, t, testImageID, networkID, bwfsStorageDir)

	// Find the bwfs container's own network alias — startBwfsContainer
	// registers it as "bwfs.internal".
	exitCode := runBrfsContainer(ctx, t, testImageID, networkID, dataDir, "bwfs.internal", 4, "e2e-src-host")
	require.Equal(t, 0, exitCode)

	// bwfs records one file_versions row per backed-up filesystem entity,
	// including directories — not just the files generateTestData tracked
	// — so the ground truth for "did everything replicate" is bwfs's own
	// row count, not len(records).
	wantCount := countBwfsFileVersions(t, bwfsStorageDir)
	require.Greater(t, wantCount, len(records), "sanity check: bwfs should record more rows than files alone (directories too)")

	rows := waitForCatalogEntryCount(t, catalogStorageDir, wantCount)
	assert.Len(t, rows, wantCount)
	for _, row := range rows {
		assert.Equal(t, "bwfs.internal", row.SourceNode)
		assert.NotEmpty(t, row.JobID)
		assert.NotEmpty(t, row.ObjectID)
	}
}
