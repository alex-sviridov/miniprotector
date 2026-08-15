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
	"google.golang.org/protobuf/types/known/timestamppb"

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
	return NewPolicyServerServer(c, dir, testLogger(), newTestCheckinStore(t))
}

// createTestStoragePolicy creates a real "storage" policy on srv and
// returns its id, for tests whose backup policy needs a StoragePolicyId
// that actually resolves.
func createTestStoragePolicy(t *testing.T, srv *policyServerServer, hostname string, port int32) string {
	t.Helper()
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "storage-for-" + hostname,
		Type:          "storage",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{hostname}},
		Port:          port,
		Config:        "{}",
	})
	require.NoError(t, err)
	require.NoError(t, srv.checkins.RecordCheckin(t.Context(), resp.Id, hostname, time.Now()))
	return resp.Id
}

func TestCreatePolicy_WritesFileAndReturnsPolicyWithID(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "Nightly DB Backup",
		Type:            "backup",
		ObjectFilters:   []*pb.ObjectFilter{{Path: "/var/lib/postgres"}},
		Rpo:             "24h",
		BackupWindow:    []string{"0 2 * * *"},
		StoragePolicyId: storageID,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "Nightly DB Backup", resp.Name)
	assert.Equal(t, []string{"bwfs:8080"}, resp.Destinations, "destinations must resolve from the referenced storage policy's checkins")
	require.Len(t, resp.ObjectFilters, 1)
	assert.NotEmpty(t, resp.ObjectFilters[0].Id)

	data, err := os.ReadFile(filepath.Join(dir, "backup", "nightly-db-backup.json"))
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.Equal(t, "Nightly DB Backup", onDisk["metadata"].(map[string]any)["name"])
}

func TestCreatePolicy_SecondCallWithSameNameGetsDistinctFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	req := &pb.CreatePolicyRequest{Name: "dup", Type: "backup", StoragePolicyId: storageID}
	first, err := srv.CreatePolicy(context.Background(), req)
	require.NoError(t, err)
	second, err := srv.CreatePolicy(context.Background(), req)
	require.NoError(t, err)

	assert.NotEqual(t, first.Id, second.Id)
	_, err = os.Stat(filepath.Join(dir, "backup", "dup.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "backup", "dup-2.json"))
	require.NoError(t, err)
}

func TestCreatePolicy_MissingNameReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Type: "backup"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_InvalidGlobPatternReturnsInvalidArgumentAndWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "broken",
		Type:          "backup",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/data", Include: []string{"["}}},
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no backup/ directory (and no file) should be written when validation fails")
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
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	names := []string{"policy-one", "policy-two"}
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
				Name:            name,
				Type:            "backup",
				StoragePolicyId: storageID,
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
		got[p.Meta().Name] = true
	}
	for _, name := range names {
		assert.True(t, got[name], "policy %q must be visible in cache after both concurrent creates complete", name)
	}
}

func TestCreatePolicy_ClientFiltersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "web",
		Type:            "backup",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
		StoragePolicyId: storageID,
	})

	require.NoError(t, err)
	require.NotNil(t, resp.ClientFilters)
	assert.Equal(t, []string{"web-*"}, resp.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, resp.ClientFilters.Labels)
}

func TestUpdatePolicy_OverwritesFileKeepsIDAndCreatedAt(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{
		"metadata": {"name": "nightly", "created_at": "2026-07-01T00:00:00Z", "updated_at": "2026-07-01T00:00:00Z"},
		"object_filters": [{"path": "/old"}],
		"storage_policy_id": "placeholder"
	}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]
	storageID := createTestStoragePolicy(t, srv, "bwfs", 9090)

	resp, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:              original.Meta().ID,
		Name:            "nightly-renamed",
		ObjectFilters:   []*pb.ObjectFilter{{Path: "/new"}},
		StoragePolicyId: storageID,
	})

	require.NoError(t, err)
	assert.Equal(t, original.Meta().ID, resp.Id, "id must stay stable across an update")
	assert.Equal(t, "nightly-renamed", resp.Name)
	assert.Equal(t, []string{"bwfs:9090"}, resp.Destinations)
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
	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}, "storage_policy_id": "placeholder"}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	before, err := os.ReadFile(filepath.Join(dir, "backup", "nightly.json"))
	require.NoError(t, err)

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{Id: original.Meta().ID, Name: ""})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	after, err := os.ReadFile(filepath.Join(dir, "backup", "nightly.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "file must be unchanged when validation fails")
}

func TestDeletePolicy_RemovesFileAndReloads(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}, "storage_policy_id": "placeholder"}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: original.Meta().ID})

	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "backup", "nightly.json"))
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

func TestDeletePolicy_RemovesCheckinRows(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]},
		"storage_policy_id": "sp-1"
	}`)
	srv := newTestWriteServer(t, dir)

	getResp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, getResp.Policies, 1)
	policyID := getResp.Policies[0].Id

	records, err := srv.checkins.CheckinsForPolicy(t.Context(), policyID)
	require.NoError(t, err)
	require.Len(t, records, 1, "sanity check: a check-in should be recorded before delete")

	_, err = srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: policyID})
	require.NoError(t, err)

	records, err = srv.checkins.CheckinsForPolicy(t.Context(), policyID)
	require.NoError(t, err)
	assert.Empty(t, records, "DeletePolicy must not orphan check-in rows for the deleted policy")
}

func TestDeletePolicy_LeavesOtherPoliciesIntact(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}, "storage_policy_id": "placeholder"}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "b.json", `{"metadata": {"name": "policy-b"}, "storage_policy_id": "placeholder"}`)
	srv := newTestWriteServer(t, dir)
	var target Policy
	for _, p := range srv.cache.Policies() {
		if p.Meta().Name == "policy-a" {
			target = p
		}
	}

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: target.Meta().ID})

	require.NoError(t, err)
	remaining := srv.cache.Policies()
	require.Len(t, remaining, 1)
	assert.Equal(t, "policy-b", remaining[0].Meta().Name)
}

func TestCreatePolicy_ResponseIncludesBackupType(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Name: "nightly", Type: "backup", StoragePolicyId: storageID})

	require.NoError(t, err)
	assert.Equal(t, "backup", resp.Type)
}

func TestCreatePolicy_UnknownTypeReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Name: "x", Type: "quux"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_StoragePolicyWritesIntoStorageDir(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:   "East 1 Storage",
		Type:   "storage",
		Port:   9400,
		Config: `{"backend": "filesystem"}`,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "storage", resp.Type)
	assert.Equal(t, int32(9400), resp.Port)
	assert.JSONEq(t, `{"backend": "filesystem"}`, resp.Config)

	_, err = os.Stat(filepath.Join(dir, "storage", "east-1-storage.json"))
	require.NoError(t, err)
}

func TestCreatePolicy_StorageTypeWithBackupFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "bad",
		Type:            "storage",
		Port:            9400,
		Config:          `{}`,
		StoragePolicyId: "sp-1",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_BackupTypeWithStorageFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "bad",
		Type: "backup",
		Port: 9400,
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestUpdatePolicy_StoragePolicyRoundTripsAndTypeStaysImmutable(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1"},
		"port": 1111,
		"config": {"a": 1}
	}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	resp, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:     original.Meta().ID,
		Name:   "east-1-renamed",
		Port:   2222,
		Config: `{"a": 2}`,
	})

	require.NoError(t, err)
	assert.Equal(t, original.Meta().ID, resp.Id, "id must stay stable across an update")
	assert.Equal(t, "storage", resp.Type, "type must stay \"storage\" -- UpdatePolicy cannot change it")
	assert.Equal(t, int32(2222), resp.Port)
	assert.JSONEq(t, `{"a": 2}`, resp.Config)
}

func TestUpdatePolicy_StorageTypeWithBackupFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1"}, "port": 1111, "config": {}
	}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	_, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:              original.Meta().ID,
		Name:            "east-1",
		Port:            1111,
		Config:          `{}`,
		StoragePolicyId: "sp-1",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_DisabledAtRoundTrips(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	disabledAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "one-shot",
		Type:            "backup",
		StoragePolicyId: storageID,
		DisabledAt:      timestamppb.New(disabledAt),
	})

	require.NoError(t, err)
	require.NotNil(t, resp.DisabledAt)
	assert.Equal(t, disabledAt, resp.DisabledAt.AsTime())
}

func TestCreatePolicy_NoDisabledAtLeavesItUnset(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "ordinary",
		Type:            "backup",
		StoragePolicyId: storageID,
	})

	require.NoError(t, err)
	assert.Nil(t, resp.DisabledAt, "an omitted disabled_at must stay unset, not become the Unix epoch")
}

func TestCreatePolicy_PastDisabledAtAcceptedWithoutError(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "already-expired",
		Type:            "backup",
		StoragePolicyId: storageID,
		DisabledAt:      timestamppb.New(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)),
	})

	require.NoError(t, err)
	require.NotNil(t, resp.DisabledAt)
}

func TestUpdatePolicy_DisabledAtRoundTrips(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)
	created, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "will-be-disabled",
		Type:            "backup",
		StoragePolicyId: storageID,
	})
	require.NoError(t, err)

	disabledAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	updated, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:              created.Id,
		Name:            created.Name,
		StoragePolicyId: storageID,
		DisabledAt:      timestamppb.New(disabledAt),
	})

	require.NoError(t, err)
	require.NotNil(t, updated.DisabledAt)
	assert.Equal(t, disabledAt, updated.DisabledAt.AsTime())
}

func TestUpdatePolicy_OmittingDisabledAtClearsIt(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)
	created, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "temp",
		Type:            "backup",
		StoragePolicyId: storageID,
		DisabledAt:      timestamppb.New(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
	})
	require.NoError(t, err)
	require.NotNil(t, created.DisabledAt)

	updated, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:              created.Id,
		Name:            created.Name,
		StoragePolicyId: storageID,
		// DisabledAt intentionally omitted
	})

	require.NoError(t, err)
	assert.Nil(t, updated.DisabledAt, "omitting disabled_at on UpdatePolicy must clear it -- full-replace, same as every other editable field")
}

func TestCreatePolicy_MissingStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Name: "no-storage", Type: "backup"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_UnknownStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "orphan",
		Type:            "backup",
		StoragePolicyId: "does-not-exist",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestUpdatePolicy_UnknownStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)
	created, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "nightly",
		Type:            "backup",
		StoragePolicyId: storageID,
	})
	require.NoError(t, err)

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:              created.Id,
		Name:            "nightly",
		StoragePolicyId: "does-not-exist",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestDeletePolicy_StoragePolicyInUseByBackupPolicyRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)
	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "nightly-db-backup",
		Type:            "backup",
		StoragePolicyId: storageID,
	})
	require.NoError(t, err)

	_, err = srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: storageID})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "nightly-db-backup")
	_, statErr := os.Stat(filepath.Join(dir, "storage", "storage-for-bwfs.json"))
	assert.NoError(t, statErr, "storage policy file must remain when the delete is rejected")
}

func TestDeletePolicy_UnreferencedStoragePolicySucceeds(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: storageID})

	require.NoError(t, err)
	assert.Empty(t, srv.cache.Policies())
}

func TestCreatePolicy_RestorePolicyWritesIntoRestoreDir(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "Web01 Emergency Restore",
		Type:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true, DestPath: "/var/www/index-restored.html"}},
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "restore", resp.Type)
	assert.Equal(t, storageID, resp.StoragePolicyId)
	require.Len(t, resp.Rules, 1)
	assert.Equal(t, "/var/www/index.html", resp.Rules[0].Path)
	assert.Equal(t, "/var/www/index-restored.html", resp.Rules[0].DestPath)

	_, err = os.Stat(filepath.Join(dir, "restore", "web01-emergency-restore.json"))
	require.NoError(t, err)
}

// TestCreatePolicy_RestoreRuleTimeframePersistsToDisk guards the first hop
// of the not_before/not_after chain: buildPolicyForCreate is what turns a
// CreatePolicyRequest into the RestorePolicy that gets serialized into
// restore/<name>.json. If it drops the timeframe, nothing downstream can
// ever recover it -- the on-disk policy is the source of truth every later
// GetPolicies response is rebuilt from. Asserting on the file itself (not
// just the response) is deliberate: the response is built from the same
// in-memory value, so a disk check is what proves persistence.
func TestCreatePolicy_RestoreRuleTimeframePersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "windowed-restore",
		Type:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules: []*pb.RestoreRule{
			{Host: "web-01", Path: "/var/www/index.html", Include: true, NotBefore: 1750000000, NotAfter: 1760000000},
		},
	})

	require.NoError(t, err)
	require.Len(t, resp.Rules, 1)
	assert.Equal(t, int64(1750000000), resp.Rules[0].NotBefore)
	assert.Equal(t, int64(1760000000), resp.Rules[0].NotAfter)

	data, err := os.ReadFile(filepath.Join(dir, "restore", "windowed-restore.json"))
	require.NoError(t, err)
	var onDisk struct {
		Rules []struct {
			NotBefore int64 `json:"not_before"`
			NotAfter  int64 `json:"not_after"`
		} `json:"rules"`
	}
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.Len(t, onDisk.Rules, 1)
	assert.Equal(t, int64(1750000000), onDisk.Rules[0].NotBefore)
	assert.Equal(t, int64(1760000000), onDisk.Rules[0].NotAfter)
}

func TestCreatePolicy_ResponseIncludesRestoreType(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "quick-restore", Type: "restore", StoragePolicyId: storageID,
		Rules: []*pb.RestoreRule{{Path: "/x", Include: true}},
	})

	require.NoError(t, err)
	assert.Equal(t, "restore", resp.Type)
}

func TestCreatePolicy_RestoreTypeWithBackupFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	// storage_policy_id is now required for restore, not disqualifying -- rpo
	// (a genuine backup-only field) is what this test must set instead to
	// stay meaningful.
	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "bad",
		Type:            "restore",
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
		Rpo:             "24h",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_RestoreTypeWithPortRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "bad",
		Type:            "restore",
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
		Port:            9400,
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestUpdatePolicy_RestoreTypeRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)
	created, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "quick-restore", Type: "restore", StoragePolicyId: storageID,
		Rules: []*pb.RestoreRule{{Path: "/x", Include: true}},
	})
	require.NoError(t, err)

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:   created.Id,
		Name: "renamed",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())

	data, readErr := os.ReadFile(filepath.Join(dir, "restore", "quick-restore.json"))
	require.NoError(t, readErr)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.Equal(t, "quick-restore", onDisk["metadata"].(map[string]any)["name"], "the file must be left untouched when the update is rejected")
}

func TestCreatePolicy_RestoreMissingStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "no-storage-ref", Type: "restore", Rules: []*pb.RestoreRule{{Path: "/x", Include: true}},
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_RestoreRejectsConfigField(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "x",
		Type:            "restore",
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
		Config:          `{"a":1}`,
	})
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "config")
}

func TestCreatePolicy_StorageRejectsRulesField(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:   "x",
		Type:   "storage",
		Port:   8080,
		Config: `{"backend":"filesystem","root":"/data"}`,
		Rules:  []*pb.RestoreRule{{Path: "/x", Include: true}},
	})
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "rules")
}

func TestCreatePolicy_BackupRejectsRulesField(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "x",
		Type:            "backup",
		Rpo:             "24h",
		BackupWindow:    []string{"0 2 * * *"},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
	})
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "rules")
}

func TestCreatePolicy_RestoreUnknownStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "orphan-restore",
		Type:            "restore",
		StoragePolicyId: "does-not-exist",
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
	})
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}
