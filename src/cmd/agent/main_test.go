package main

import (
	"path/filepath"
	"testing"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewDerivedFunc_MissingCacheFileStillRunsStaticPolicies is a
// regression guard for the bootstrap deadlock this branch found and fixed
// in Task 8: a fresh install has no policies-cache.json yet, and the
// reconcile loop must still run the three static policies -- especially
// policy-update, the only thing that ever creates that file -- or the
// agent can never come up.
func TestNewDerivedFunc_MissingCacheFileStillRunsStaticPolicies(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "policies-cache.json")
	conf := &config.Config{}

	derivedFunc := newDerivedFunc(missingPath, t.TempDir(), testLogger(), conf, "bwfs-bin", "catalogsync-bin")
	policyList, storageTaskList, ok := derivedFunc()

	assert.False(t, ok, "a missing cache file must report ok=false, so run() doesn't prune live task state")
	assert.Empty(t, storageTaskList, "storage tasks have no static-policy equivalent, so they're empty on a failed read")
	require.Len(t, policyList, 3, "the three static policies must still run despite the failed read")
	ids := []string{policyList[0].ID, policyList[1].ID, policyList[2].ID}
	assert.Contains(t, ids, "bootstrap-refresh")
	assert.Contains(t, ids, "operating-refresh")
	assert.Contains(t, ids, "policy-update", "policy-update is what (re)creates policies-cache.json -- if this is missing here, a fresh install deadlocks forever")
}
