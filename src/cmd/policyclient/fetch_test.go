package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func fetchTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakePolicyServiceClient struct {
	resp *pb.GetPoliciesResponse
	err  error
}

func (f *fakePolicyServiceClient) GetPolicies(_ context.Context, _ *pb.GetPoliciesRequest, _ ...grpc.CallOption) (*pb.GetPoliciesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestRunFetch_Success_WritesCacheFile(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "nested", "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:        "policy-uuid-123",
				Name:      "daily-db-backup",
				CreatedAt: created,
				UpdatedAt: updated,
				ObjectFilters: []*pb.ObjectFilter{
					{Id: "filter-uuid-1", Path: "/var/lib/postgres", Include: []string{"*.sql"}},
					{Id: "filter-uuid-2", Path: "/etc/postgres", Exclude: []string{"*.bak"}},
				},
				Rpo:          "24h",
				BackupWindow: []string{"0 2 * * *"},
				Destination:  "bwfs-east.internal:8080",
				Type:         "backup",
			},
		},
	}}

	err := runFetch(context.Background(), fake, cachePath, fetchTestLogger())
	require.NoError(t, err)

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)

	var got []CachedPolicy
	require.NoError(t, json.Unmarshal(data, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "policy-uuid-123", got[0].ID)
	assert.Equal(t, "daily-db-backup", got[0].Name)
	assert.True(t, created.AsTime().Equal(got[0].CreatedAt))
	assert.True(t, updated.AsTime().Equal(got[0].UpdatedAt))
	assert.Equal(t, []ObjectFilter{
		{ID: "filter-uuid-1", Path: "/var/lib/postgres", Include: []string{"*.sql"}},
		{ID: "filter-uuid-2", Path: "/etc/postgres", Exclude: []string{"*.bak"}},
	}, got[0].ObjectFilters)
	assert.Equal(t, "24h", got[0].RPO)
	assert.Equal(t, []string{"0 2 * * *"}, got[0].BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", got[0].Destination)
	assert.Equal(t, "backup", got[0].Type)
}

func TestRunFetch_EmptyPoliciesWritesEmptyArrayNotNull(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{}}

	require.NoError(t, runFetch(context.Background(), fake, cachePath, fetchTestLogger()))

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	assert.JSONEq(t, "[]", string(data))
}

func TestRunFetch_ErrorPropagates_ExistingCacheUntouched(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	require.NoError(t, os.WriteFile(cachePath, []byte("previous-good-cache"), 0o644))

	fake := &fakePolicyServiceClient{err: assert.AnError}
	err := runFetch(context.Background(), fake, cachePath, fetchTestLogger())
	assert.Error(t, err)

	data, readErr := os.ReadFile(cachePath)
	require.NoError(t, readErr)
	assert.Equal(t, "previous-good-cache", string(data))
}
