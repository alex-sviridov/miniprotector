package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeJobStatusClient struct {
	resp  *pb.GetPolicyJobStatusResponse
	err   error
	calls []*pb.GetPolicyJobStatusRequest
}

func (f *fakeJobStatusClient) GetPolicyJobStatus(ctx context.Context, req *pb.GetPolicyJobStatusRequest, opts ...grpc.CallOption) (*pb.GetPolicyJobStatusResponse, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func createTestRestorePolicy(t *testing.T, srv *policyServerServer, name, storageID string) *pb.Policy {
	t.Helper()
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            name,
		Type:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/var/www", Include: true}},
	})
	require.NoError(t, err)
	return resp
}

func TestSweepRestorePolicies_DeletesWhenFinishedPastGracePeriod(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{
		Finished:   true,
		FinishedAt: timestamppb.New(time.Now().Add(-time.Hour)),
	}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, testLogger())

	_, ok := srv.cache.FindByID(p.Id)
	assert.False(t, ok, "policy must be deleted")
	require.Len(t, fake.calls, 1)
	assert.Equal(t, p.JobId, fake.calls[0].JobId)
}

func TestSweepRestorePolicies_SkipsWhenNotFinished(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{Finished: false}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, testLogger())

	_, ok := srv.cache.FindByID(p.Id)
	assert.True(t, ok, "policy must survive")
}

func TestSweepRestorePolicies_SkipsWithinGracePeriod(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{
		Finished:   true,
		FinishedAt: timestamppb.New(time.Now()), // just finished
	}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Hour, testLogger())

	_, ok := srv.cache.FindByID(p.Id)
	assert.True(t, ok, "policy must survive until the grace period elapses")
}

func TestSweepRestorePolicies_SkipsOnQueryError(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	fake := &fakeJobStatusClient{err: assert.AnError}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, testLogger())

	_, ok := srv.cache.FindByID(p.Id)
	assert.True(t, ok, "a query failure must never delete the policy")
}

func TestSweepRestorePolicies_IgnoresNonRestorePolicies(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	_ = createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{Finished: true, FinishedAt: timestamppb.New(time.Now().Add(-time.Hour))}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, testLogger())

	assert.Empty(t, fake.calls, "must never query job status for a non-restore policy")
	_, err := os.Stat(filepath.Join(dir, "storage", "storage-for-bwfs-east.json"))
	require.NoError(t, err, "the storage policy must be untouched")
}

// writeLegacyRestorePolicy writes a restore-policy JSON file directly to
// disk with no "job_id" key at all -- reproducing a policy written before
// RestorePolicy carried a JobID (rather than one created through
// CreatePolicy, which always generates one) -- and reloads the cache so it
// picks the file up.
func writeLegacyRestorePolicy(t *testing.T, srv *policyServerServer, dir, storageID, name string) {
	t.Helper()
	content := `{
		"metadata": {"name": "` + name + `"},
		"client_filters": {"hostnames": ["web-01"]},
		"storage_policy_id": "` + storageID + `",
		"rules": [{"path": "/var/www", "include": true}]
	}`
	writePolicyFile(t, filepath.Join(dir, "restore"), name+".json", content)
	require.NoError(t, srv.cache.Reload(dir, testLogger()))
}

func TestSweepRestorePolicies_SkipsLegacyPolicyWithNoJobID(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	writeLegacyRestorePolicy(t, srv, dir, storageID, "legacy-no-jobid")

	logger, buf := testLoggerWithBuffer()
	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{Finished: true, FinishedAt: timestamppb.New(time.Now().Add(-time.Hour))}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, logger)

	assert.Empty(t, fake.calls, "must never call GetPolicyJobStatus for a policy with no job_id")
	_, err := os.Stat(filepath.Join(dir, "restore", "legacy-no-jobid.json"))
	require.NoError(t, err, "the legacy policy must survive the sweep")
	assert.Contains(t, buf.String(), "no job_id")
	assert.NotContains(t, buf.String(), `"level":"ERROR"`, "a missing job_id is an expected state, not an error")
}

func TestSweepRestorePolicies_DoesNotLogDeletedWhenDeletePolicyFails(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)
	p := createTestRestorePolicy(t, srv, "web01-emergency", storageID)

	// Remove the on-disk file out from under the cache so DeletePolicy's
	// os.Remove(existing.Path()) fails, while the cache (loaded at
	// createTestRestorePolicy time) still thinks the policy exists --
	// forcing DeletePolicy to return an error without needing to touch
	// directory permissions.
	found, ok := srv.cache.FindByID(p.Id)
	require.True(t, ok)
	require.NoError(t, os.Remove(found.Path()))

	logger, buf := testLoggerWithBuffer()
	fake := &fakeJobStatusClient{resp: &pb.GetPolicyJobStatusResponse{
		Finished:   true,
		FinishedAt: timestamppb.New(time.Now().Add(-time.Hour)),
	}}
	srv.sweepRestorePolicies(context.Background(), fake, time.Minute, logger)

	out := buf.String()
	assert.NotContains(t, out, `"event":"deleted"`, "must not claim the policy was deleted when DeletePolicy failed")
	assert.Contains(t, out, "DeletePolicy failed")
}
