package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestSuccessFileHash_OnlyIncludesSuccessfulFilesAndIsOrderIndependent(t *testing.T) {
	stateA := map[string]bool{"b": true, "a": true, "c": false}
	stateB := map[string]bool{"a": true, "c": false, "b": true}

	assert.Equal(t, successFileHash(stateA), successFileHash(stateB))
}

func TestSuccessFileHash_DiffersWhenSuccessSetDiffers(t *testing.T) {
	withB := map[string]bool{"a": true, "b": true}
	withoutB := map[string]bool{"a": true, "b": false}

	assert.NotEqual(t, successFileHash(withB), successFileHash(withoutB))
}

// fakeBackupCommitClient implements just enough of pb.BackupServiceClient to
// drive commitBackupJob's retry loop; every other method panics if called.
type fakeBackupCommitClient struct {
	pb.BackupServiceClient
	calls    int
	failN    int // number of leading calls that return an error
	response *pb.BackupCommitResponse
}

func (f *fakeBackupCommitClient) BackupCommit(ctx context.Context, req *pb.BackupCommitRequest, opts ...grpc.CallOption) (*pb.BackupCommitResponse, error) {
	f.calls++
	if f.calls <= f.failN {
		return nil, errors.New("transport error")
	}
	return f.response, nil
}

func TestCommitBackupJob_SucceedsAfterTransientFailures(t *testing.T) {
	client := &fakeBackupCommitClient{failN: 1, response: &pb.BackupCommitResponse{Success: true}}
	logger := slog.Default()

	success, err := commitBackupJob(context.Background(), logger, client, []byte("hash"))
	require.NoError(t, err)
	assert.True(t, success)
	assert.Equal(t, 2, client.calls, "should have retried once after the first transient failure")
}

func TestCommitBackupJob_ReturnsErrorAfterExhaustingRetries(t *testing.T) {
	client := &fakeBackupCommitClient{failN: commitMaxAttempts}
	logger := slog.Default()

	_, err := commitBackupJob(context.Background(), logger, client, []byte("hash"))
	require.Error(t, err)
	assert.Equal(t, commitMaxAttempts, client.calls)
}

func TestCommitBackupJob_PropagatesServerRejection(t *testing.T) {
	client := &fakeBackupCommitClient{response: &pb.BackupCommitResponse{Success: false}}
	logger := slog.Default()

	success, err := commitBackupJob(context.Background(), logger, client, []byte("hash"))
	require.NoError(t, err, "a clean false response is not a transport error, must not retry or error")
	assert.False(t, success)
	assert.Equal(t, 1, client.calls)
}
