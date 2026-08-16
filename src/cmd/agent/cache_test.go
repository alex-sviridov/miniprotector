package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCache_MissingFileReturnsEmptyCache(t *testing.T) {
	dir := t.TempDir()
	c, err := readCache(filepath.Join(dir, "agent-state.json"))
	require.NoError(t, err)
	assert.Empty(t, c)
}

func TestReadCache_CorruptFileReturnsEmptyCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

	c, err := readCache(path)
	require.NoError(t, err)
	assert.Empty(t, c)
}

func TestWriteCacheThenReadCache_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")

	now := time.Now().UTC().Truncate(time.Second)
	c := Cache{
		"cert-refresh": {LastSuccessAt: &now, ConsecutiveFailures: 0},
	}
	require.NoError(t, writeCache(path, c))

	got, err := readCache(path)
	require.NoError(t, err)
	require.Contains(t, got, "cert-refresh")
	assert.True(t, got["cert-refresh"].LastSuccessAt.Equal(now))
	assert.Equal(t, 0, got["cert-refresh"].ConsecutiveFailures)
}

func TestWriteCache_CreatesParentDirectoryIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "agent-state.json")

	require.NoError(t, writeCache(path, Cache{}))

	_, err := os.Stat(path)
	assert.NoError(t, err)
}

func TestWriteCache_OverwritesPreviousValueAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")

	require.NoError(t, writeCache(path, Cache{"a": {ConsecutiveFailures: 1}}))
	require.NoError(t, writeCache(path, Cache{"a": {ConsecutiveFailures: 2}}))

	got, err := readCache(path)
	require.NoError(t, err)
	assert.Equal(t, 2, got["a"].ConsecutiveFailures)

	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err), "no leftover temp file after a successful write")
}

// policyclientBootstrapRefreshShape mirrors the anonymous struct
// cmd/policyclient/fetch.go's bootstrapRefreshFailure decodes
// agent-state.json into, field for field, json tag for json tag.
//
// policyclient is a second reader of this file. It lives in a different
// main package, so it cannot import Cache/PolicyState and instead declares
// its own decode target with these tags hardcoded. Nothing in the compiler
// or the build ties the two together. Decoding what writeCache actually
// emits through this mirror is therefore the real end-to-end contract
// check, not a Go-struct-identity check that would still pass if the tags
// drifted apart.
type policyclientBootstrapRefreshShape struct {
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastError     string     `json:"last_error,omitempty"`
}

// TestWriteCache_BootstrapRefreshEntryMatchesPolicyclientReadShape pins the
// whole cross-binary contract policyclient depends on: the top-level key
// ("bootstrap-refresh", from policy.go's Policy.ID) and the two nested JSON
// tags (last_attempt_at, last_error, from cache.go's PolicyState).
//
// The failure mode this guards is uniquely nasty. If a future change renames
// either the task ID or one of those tags, bootstrapRefreshFailure doesn't
// error -- it returns ("", 0) forever, which policy-server records as a
// perfectly healthy node. A stuck bootstrap renewal then reads as green on
// GET /api/v1/clients/{hostname}/cert-status: the exact outcome this whole
// feature exists to prevent, failing silently inside its own blind spot.
//
// It follows the precedent set by cmd/policyclient/fetch_test.go's
// agentRestoreRuleShape, which guards policies-cache.json the same way in
// the other direction (writer's test package, mirror of the reader's shape).
func TestWriteCache_BootstrapRefreshEntryMatchesPolicyclientReadShape(t *testing.T) {
	// Sourced from policies() rather than retyped, so renaming the task ID
	// fails here instead of silently making policyclient's lookup miss.
	const bootstrapID = "bootstrap-refresh"
	var found *Policy
	for _, p := range policies(&config.Config{}) {
		if p.ID == bootstrapID {
			found = &p
			break
		}
	}
	require.NotNil(t, found, "policies() must still expose a task with ID %q -- cmd/policyclient/fetch.go's bootstrapRefreshFailure looks up exactly this key in agent-state.json, and a rename here silently turns every node's status into a permanent, indistinguishable 'healthy'", bootstrapID)
	require.Equal(t, "certclient", found.Binary, "sanity: %q is the certclient bootstrap renewal task", bootstrapID)

	path := filepath.Join(t.TempDir(), "agent-state.json")
	attemptedAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	require.NoError(t, writeCache(path, Cache{
		found.ID:        {LastAttemptAt: &attemptedAt, ConsecutiveFailures: 3, LastError: "renew request: connection refused"},
		"policy-update": {LastSuccessAt: &attemptedAt},
	}))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	// Decoded exactly the way policyclient decodes it: a map keyed by task
	// ID, whose values carry only the two fields policyclient reads.
	var got map[string]policyclientBootstrapRefreshShape
	require.NoError(t, json.Unmarshal(data, &got))

	entry, ok := got[bootstrapID]
	require.True(t, ok, "agent-state.json must carry the bootstrap task under top-level key %q; decoded %v", bootstrapID, got)
	assert.Equal(t, "renew request: connection refused", entry.LastError, `policyclient reads the failure from the nested "last_error" tag; an empty value here is indistinguishable from a healthy node`)
	require.NotNil(t, entry.LastAttemptAt, `policyclient reads the timestamp from the nested "last_attempt_at" tag`)
	assert.Equal(t, attemptedAt.Unix(), entry.LastAttemptAt.Unix())
}
