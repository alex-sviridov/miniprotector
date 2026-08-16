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

	gotReq *pb.GetPoliciesRequest
}

func (f *fakePolicyServiceClient) GetPolicies(_ context.Context, req *pb.GetPoliciesRequest, _ ...grpc.CallOption) (*pb.GetPoliciesResponse, error) {
	f.gotReq = req
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
				Destinations: []string{"bwfs-east.internal:8080"},
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
	assert.Equal(t, []string{"bwfs-east.internal:8080"}, got[0].Destinations)
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

func TestRunFetch_StoragePolicyCarriesPortAndConfig(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:        "storage-uuid-1",
				Name:      "east-1-storage",
				CreatedAt: created,
				UpdatedAt: updated,
				Type:      "storage",
				Port:      9400,
				Config:    `{"backend": "filesystem", "root": "/data/storage"}`,
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
	assert.Equal(t, "storage", got[0].Type)
	assert.EqualValues(t, 9400, got[0].Port)
	assert.JSONEq(t, `{"backend": "filesystem", "root": "/data/storage"}`, got[0].Config)
}

func TestRunFetch_DisabledAtRoundTrips(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")

	disabledAt := timestamppb.New(time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{Id: "id-1", Name: "one-shot", Type: "backup", DisabledAt: disabledAt},
			{Id: "id-2", Name: "ordinary", Type: "backup"},
		},
	}}

	require.NoError(t, runFetch(context.Background(), fake, cachePath, fetchTestLogger()))

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	var cached []CachedPolicy
	require.NoError(t, json.Unmarshal(data, &cached))
	require.Len(t, cached, 2)
	assert.Equal(t, disabledAt.AsTime(), cached[0].DisabledAt)
	assert.True(t, cached[1].DisabledAt.IsZero(), "an unset disabled_at must cache as the zero time, not the Unix epoch")
}

func TestRunFetch_RestorePolicyCarriesStoragePolicyIdAndRules(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:              "restore-uuid-1",
				Name:            "web01-emergency",
				CreatedAt:       created,
				UpdatedAt:       updated,
				Type:            "restore",
				StoragePolicyId: "sp-1",
				Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
				Destinations:    []string{"bwfs-east.internal:8080"},
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
	assert.Equal(t, "restore", got[0].Type)
	assert.Equal(t, []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}}, got[0].Rules)
	assert.Equal(t, []string{"bwfs-east.internal:8080"}, got[0].Destinations)
}

func TestBootstrapRefreshFailure_ReadsFailingEntry(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `{"bootstrap-refresh":{"last_attempt_at":"2026-08-16T12:00:00Z","last_error":"renew request: connection refused"}}`)

	lastErr, lastAt := bootstrapRefreshFailure(dir)
	assert.Equal(t, "renew request: connection refused", lastErr)
	assert.Equal(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC).Unix(), lastAt)
}

func TestBootstrapRefreshFailure_HealthyEntryReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `{"bootstrap-refresh":{"last_attempt_at":"2026-08-16T12:00:00Z","last_error":""},"operating-refresh":{"last_error":"unrelated failure"}}`)

	lastErr, lastAt := bootstrapRefreshFailure(dir)
	assert.Equal(t, "", lastErr, "a healthy bootstrap-refresh entry must report nothing, even if another task is failing")
	assert.Equal(t, int64(0), lastAt)
}

func TestBootstrapRefreshFailure_MissingKeyReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `{"operating-refresh":{"last_error":"unrelated"}}`)

	lastErr, lastAt := bootstrapRefreshFailure(dir)
	assert.Equal(t, "", lastErr)
	assert.Equal(t, int64(0), lastAt)
}

func TestBootstrapRefreshFailure_MissingFileReturnsEmpty(t *testing.T) {
	lastErr, lastAt := bootstrapRefreshFailure(t.TempDir()) // no agent-state.json written
	assert.Equal(t, "", lastErr)
	assert.Equal(t, int64(0), lastAt)
}

func TestBootstrapRefreshFailure_MalformedFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `not valid json`)

	lastErr, lastAt := bootstrapRefreshFailure(dir)
	assert.Equal(t, "", lastErr)
	assert.Equal(t, int64(0), lastAt)
}

func writeAgentState(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agent-state.json"), []byte(content), 0o644))
}

func TestRunFetch_IncludesBootstrapRefreshStatusFromAgentState(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `{"bootstrap-refresh":{"last_attempt_at":"2026-08-16T12:00:00Z","last_error":"renew request: connection refused"}}`)
	cachePath := filepath.Join(dir, "policies-cache.json")

	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{}}
	require.NoError(t, runFetch(context.Background(), fake, cachePath, fetchTestLogger()))

	require.NotNil(t, fake.gotReq)
	assert.Equal(t, "renew request: connection refused", fake.gotReq.GetBootstrapRefreshLastError())
	assert.Equal(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC).Unix(), fake.gotReq.GetBootstrapRefreshLastAttemptAt())
}

// agentRestoreRuleShape mirrors cmd/agent/restore.go's RestoreRule field
// for field, json tag for json tag. agent is the only consumer of the
// rules policyclient caches, and it reads them out of policies-cache.json
// by these exact JSON keys -- so decoding the cache file through this
// struct is the real end-to-end contract check, not a Go-struct-identity
// check that would still pass if the tags drifted apart.
type agentRestoreRuleShape struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	NotBefore int64  `json:"not_before,omitempty"`
	NotAfter  int64  `json:"not_after,omitempty"`
}

// TestRunFetch_RestoreRuleTimeframeSurvivesIntoCache guards the hop that
// silently dropped not_before/not_after: policy-server carries them, agent
// reads them, but policyclient sits in between and is the only thing that
// writes policies-cache.json. Decoding through agentRestoreRuleShape
// proves the values reach agent under the keys agent actually looks for.
func TestRunFetch_RestoreRuleTimeframeSurvivesIntoCache(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")

	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:              "restore-uuid-tf",
				Name:            "web01-windowed",
				CreatedAt:       timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)),
				UpdatedAt:       timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)),
				Type:            "restore",
				StoragePolicyId: "sp-1",
				Rules: []*pb.RestoreRule{
					{Host: "web-01", Path: "/var/www/index.html", Include: true, NotBefore: 1750000000, NotAfter: 1760000000},
					{Host: "", Path: "/var/log", Include: true},
				},
			},
		},
	}}

	require.NoError(t, runFetch(context.Background(), fake, cachePath, fetchTestLogger()))

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)

	var got []struct {
		Rules []agentRestoreRuleShape `json:"rules"`
	}
	require.NoError(t, json.Unmarshal(data, &got))
	require.Len(t, got, 1)
	assert.Equal(t, []agentRestoreRuleShape{
		{Host: "web-01", Path: "/var/www/index.html", Include: true, NotBefore: 1750000000, NotAfter: 1760000000},
		{Host: "", Path: "/var/log", Include: true},
	}, got[0].Rules, "not_before/not_after must survive the policyclient hop into policies-cache.json")

	// An unset timeframe must stay absent from the file entirely (omitempty),
	// so an unbounded rule is textually indistinguishable from one written
	// before this feature existed.
	assert.NotContains(t, string(data), `"not_before": 0`)
}

func TestToCachedPolicies_RestoreRuleDestPathRoundTrips(t *testing.T) {
	policies := []*pb.Policy{
		{
			Type: "restore",
			Rules: []*pb.RestoreRule{
				{Host: "web-01", Path: "/etc/nginx/nginx.conf", Include: true, DestPath: "/etc/nginx/nginx.conf.bak"},
			},
		},
	}
	cached := toCachedPolicies(policies)
	require.Len(t, cached, 1)
	require.Len(t, cached[0].Rules, 1)
	assert.Equal(t, "/etc/nginx/nginx.conf.bak", cached[0].Rules[0].DestPath)
}

// This is the one hop where a dropped mode/overwrite would silently degrade
// every restore policy back to plain verification, with no error anywhere
// -- agent branches purely on CachedPolicy.Mode (see cmd/agent's restore
// dispatch), so a field lost here is never caught by a proto/RPC-level
// check, only by exercising this exact conversion.
func TestToCachedPolicies_RestoreModeAndOverwriteRoundTrip(t *testing.T) {
	policies := []*pb.Policy{
		{
			Type:      "restore",
			Mode:      "restore",
			Overwrite: true,
		},
	}
	cached := toCachedPolicies(policies)
	require.Len(t, cached, 1)
	assert.Equal(t, "restore", cached[0].Mode)
	assert.True(t, cached[0].Overwrite)
}
