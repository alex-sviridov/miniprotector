package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStorageState(t *testing.T) {
	now := time.Now()
	assert.Equal(t, "online", storageState(now.Add(-2*time.Minute), now))
	assert.Equal(t, "stale", storageState(now.Add(-5*time.Minute), now))
	assert.Equal(t, "offline", storageState(now.Add(-11*time.Minute), now))
}

func TestReportStorageStatus_StoresLatestPerHost(t *testing.T) {
	store := newStorageStatusStore()
	svc := newStorageStatusServer(store)
	svc.hostnameFn = func(context.Context) (string, error) { return "store-1", nil }

	for _, conns := range []int32{1, 7} {
		_, err := svc.ReportStorageStatus(context.Background(), &pb.ReportStorageStatusRequest{
			PolicyId: "p1", Port: 9400, Status: "serving", ActiveConnections: conns,
			DiskTotalBytes: 100, DiskUsedBytes: 40, ReportedAt: timestamppb.Now(),
		})
		require.NoError(t, err)
	}

	got := store.forPolicy("p1")
	require.Len(t, got, 1)
	assert.Equal(t, "store-1", got[0].Host)
	assert.Equal(t, int32(7), got[0].ActiveConnections)
	assert.Empty(t, store.forPolicy("other"))
}

func TestReportStorageStatus_RequiresPolicyID(t *testing.T) {
	svc := newStorageStatusServer(newStorageStatusStore())
	svc.hostnameFn = func(context.Context) (string, error) { return "h", nil }
	_, err := svc.ReportStorageStatus(context.Background(), &pb.ReportStorageStatusRequest{})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestReportStorageStatus_RejectsUnidentifiedPeer(t *testing.T) {
	svc := newStorageStatusServer(newStorageStatusStore())
	_, err := svc.ReportStorageStatus(context.Background(), &pb.ReportStorageStatusRequest{PolicyId: "p1"})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestRoleRequirements_StorageStatusIsStoreOnly(t *testing.T) {
	got := roleRequirements()["/storagestatusservice.StorageStatusService/ReportStorageStatus"]
	assert.Equal(t, []string{"store"}, got)
}

func TestHandleGetStorageStatus(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	now := time.Now()
	srv.storageStatus.now = func() time.Time { return now }
	srv.storageStatus.put("p1", storageReport{Host: "store-1", Port: 9400, Status: "serving", DiskTotalBytes: 100, DiskUsedBytes: 40})
	srv.storageStatus.now = func() time.Time { return now.Add(5 * time.Minute) }

	mux := http.NewServeMux()
	srv.registerRoutes(mux, "tok")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/storage-policies/p1/status", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct{ Reports []storageStatusDTO }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Reports, 1)
	assert.Equal(t, "store-1", body.Reports[0].Hostname)
	assert.Equal(t, "stale", body.Reports[0].State)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/storage-policies/none/status", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.JSONEq(t, `{"reports":[]}`, rec.Body.String())
}
