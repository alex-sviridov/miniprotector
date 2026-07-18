package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func TestSlugify(t *testing.T) {
	assert.Equal(t, "nightly-db-backup", slugify("Nightly DB Backup!"))
	assert.Equal(t, "a-b-c", slugify("  a__b--c  "))
	assert.Equal(t, "", slugify("!!!"))
}

func TestUniqueFilename_ReturnsBaseWhenFree(t *testing.T) {
	dir := t.TempDir()
	got, err := uniqueFilename(dir, "nightly-db-backup")
	require.NoError(t, err)
	assert.Equal(t, "nightly-db-backup.json", got)
}

func TestUniqueFilename_AppendsSuffixOnCollision(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly-db-backup.json", `{}`)
	got, err := uniqueFilename(dir, "nightly-db-backup")
	require.NoError(t, err)
	assert.Equal(t, "nightly-db-backup-2.json", got)
}

func TestUniqueFilename_SkipsMultipleCollisions(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "x.json", `{}`)
	writePolicyFile(t, dir, "x-2.json", `{}`)
	got, err := uniqueFilename(dir, "x")
	require.NoError(t, err)
	assert.Equal(t, "x-3.json", got)
}

func newTestWriteServer(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, dir, testLogger())
}

func TestCreatePolicy_WritesFileAndReturnsPolicyWithID(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "Nightly DB Backup",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/var/lib/postgres"}},
		Rpo:           "24h",
		BackupWindow:  []string{"0 2 * * *"},
		Destination:   "bwfs:8080",
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "Nightly DB Backup", resp.Name)
	require.Len(t, resp.ObjectFilters, 1)
	assert.NotEmpty(t, resp.ObjectFilters[0].Id)

	data, err := os.ReadFile(filepath.Join(dir, "nightly-db-backup.json"))
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.Equal(t, "Nightly DB Backup", onDisk["metadata"].(map[string]any)["name"])
}

func TestCreatePolicy_SecondCallWithSameNameGetsDistinctFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	req := &pb.CreatePolicyRequest{Name: "dup", Destination: "bwfs:8080"}
	first, err := srv.CreatePolicy(context.Background(), req)
	require.NoError(t, err)
	second, err := srv.CreatePolicy(context.Background(), req)
	require.NoError(t, err)

	assert.NotEqual(t, first.Id, second.Id)
	_, err = os.Stat(filepath.Join(dir, "dup.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "dup-2.json"))
	require.NoError(t, err)
}

func TestCreatePolicy_MissingNameReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_InvalidGlobPatternReturnsInvalidArgumentAndWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "broken",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/data", Include: []string{"["}}},
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no file should be written when validation fails")
}

// TestCreatePolicy_ConcurrentCreatesForDifferentNamesBothSurvive guards
// against a stale-reload race: gRPC dispatches each unary RPC to its own
// goroutine, so two CreatePolicy calls for two different policies can run
// concurrently against the same server. Without serializing the write RPCs
// against each other, one RPC's Reload could glob+parse a stale snapshot of
// the directory before the other RPC's write lands on disk, then win the
// lock-and-swap race and silently overwrite the cache with that stale
// snapshot -- reverting the other RPC's just-created policy from the
// in-memory cache even though its file is correctly on disk. This doesn't
// reliably reproduce the race mid-flight (that's inherently
// timing-dependent); it proves that with writeMu in place, two concurrent
// creates can no longer both succeed without both being visible in the
// final cache state.
func TestCreatePolicy_ConcurrentCreatesForDifferentNamesBothSurvive(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	names := []string{"policy-one", "policy-two"}
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
				Name:        name,
				Destination: "bwfs:8080",
			})
			errs[i] = err
		}(i, name)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "CreatePolicy for %q should succeed", names[i])
	}

	got := map[string]bool{}
	for _, p := range srv.cache.Policies() {
		got[p.Metadata.Name] = true
	}
	for _, name := range names {
		assert.True(t, got[name], "policy %q must be visible in cache after both concurrent creates complete", name)
	}
}

func TestCreatePolicy_ClientFiltersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "web",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
	})

	require.NoError(t, err)
	require.NotNil(t, resp.ClientFilters)
	assert.Equal(t, []string{"web-*"}, resp.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, resp.ClientFilters.Labels)
}

func TestUpdatePolicy_OverwritesFileKeepsIDAndCreatedAt(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly", "created_at": "2026-07-01T00:00:00Z", "updated_at": "2026-07-01T00:00:00Z"},
		"object_filters": [{"path": "/old"}],
		"destination": "bwfs:8080"
	}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	resp, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:            original.Metadata.ID,
		Name:          "nightly-renamed",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/new"}},
		Destination:   "bwfs:9090",
	})

	require.NoError(t, err)
	assert.Equal(t, original.Metadata.ID, resp.Id, "id must stay stable across an update")
	assert.Equal(t, "nightly-renamed", resp.Name)
	assert.Equal(t, "bwfs:9090", resp.Destination)
	assert.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), resp.CreatedAt.AsTime())
	assert.NotEqual(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), resp.UpdatedAt.AsTime())
}

func TestUpdatePolicy_UnknownIDReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{Id: "does-not-exist", Name: "x"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestUpdatePolicy_InvalidInputReturnsInvalidArgumentLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	before, err := os.ReadFile(filepath.Join(dir, "nightly.json"))
	require.NoError(t, err)

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{Id: original.Metadata.ID, Name: ""})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	after, err := os.ReadFile(filepath.Join(dir, "nightly.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "file must be unchanged when validation fails")
}

func TestDeletePolicy_RemovesFileAndReloads(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: original.Metadata.ID})

	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "nightly.json"))
	assert.True(t, os.IsNotExist(err))
	assert.Empty(t, srv.cache.Policies())
}

func TestDeletePolicy_UnknownIDReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: "does-not-exist"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestDeletePolicy_LeavesOtherPoliciesIntact(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "policy-b"}}`)
	srv := newTestWriteServer(t, dir)
	var target Policy
	for _, p := range srv.cache.Policies() {
		if p.Metadata.Name == "policy-a" {
			target = p
		}
	}

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: target.Metadata.ID})

	require.NoError(t, err)
	remaining := srv.cache.Policies()
	require.Len(t, remaining, 1)
	assert.Equal(t, "policy-b", remaining[0].Metadata.Name)
}
