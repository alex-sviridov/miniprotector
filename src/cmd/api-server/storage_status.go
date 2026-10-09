// src/cmd/api-server/storage_status.go
package main

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A storage node reports once a minute, so a report under staleAfter old is
// "online" (it tolerates two missed reports), one under offlineAfter is
// "stale", anything older is "offline".
const (
	storageStaleAfter   = 3 * time.Minute
	storageOfflineAfter = 10 * time.Minute
)

type storageStatusKey struct{ policyID, host string }

type storageReport struct {
	Host              string
	Port              int32
	Status            string
	DiskTotalBytes    uint64
	DiskUsedBytes     uint64
	ActiveConnections int32
	InProgressJobs    int32
	UptimeSeconds     int64
	ReportedAt        time.Time // bwfs's clock, informational only
	ReceivedAt        time.Time // api-server's clock, drives State
}

// storageStatusStore keeps the latest report per (policy, reporting host) in
// memory only: it repopulates within a minute of an api-server restart, so
// persisting it would add the first database to api-server for no gain.
type storageStatusStore struct {
	mu      sync.Mutex
	reports map[storageStatusKey]storageReport
	now     func() time.Time
}

func newStorageStatusStore() *storageStatusStore {
	return &storageStatusStore{reports: map[storageStatusKey]storageReport{}, now: time.Now}
}

func (s *storageStatusStore) put(policyID string, r storageReport) {
	r.ReceivedAt = s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports[storageStatusKey{policyID, r.Host}] = r
}

// forPolicy returns policyID's reports ordered by host. Reports older than
// storageOfflineAfter x 6 are dropped so a decommissioned node's card doesn't
// linger forever.
func (s *storageStatusStore) forPolicy(policyID string) []storageReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-6 * storageOfflineAfter)
	var out []storageReport
	for k, r := range s.reports {
		if r.ReceivedAt.Before(cutoff) {
			delete(s.reports, k)
			continue
		}
		if k.policyID == policyID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func storageState(receivedAt, now time.Time) string {
	switch age := now.Sub(receivedAt); {
	case age < storageStaleAfter:
		return "online"
	case age < storageOfflineAfter:
		return "stale"
	default:
		return "offline"
	}
}

// storageStatusServer is StorageStatusService. The reporting host comes from
// the verified mTLS identity (hostnameFn), never the payload.
type storageStatusServer struct {
	pb.UnimplementedStorageStatusServiceServer
	store      *storageStatusStore
	hostnameFn func(context.Context) (string, error)
}

func newStorageStatusServer(store *storageStatusStore) *storageStatusServer {
	return &storageStatusServer{store: store, hostnameFn: mtls.PeerHostname}
}

func (s *storageStatusServer) ReportStorageStatus(ctx context.Context, req *pb.ReportStorageStatusRequest) (*pb.ReportStorageStatusResponse, error) {
	if req.GetPolicyId() == "" {
		return nil, status.Error(codes.InvalidArgument, "policy_id is required")
	}
	host, err := s.hostnameFn(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "resolve peer identity: %v", err)
	}
	s.store.put(req.GetPolicyId(), storageReport{
		Host:              host,
		Port:              req.GetPort(),
		Status:            req.GetStatus(),
		DiskTotalBytes:    req.GetDiskTotalBytes(),
		DiskUsedBytes:     req.GetDiskUsedBytes(),
		ActiveConnections: req.GetActiveConnections(),
		InProgressJobs:    req.GetInProgressJobs(),
		UptimeSeconds:     req.GetUptimeSeconds(),
		ReportedAt:        req.GetReportedAt().AsTime(),
	})
	return &pb.ReportStorageStatusResponse{}, nil
}

type storageStatusDTO struct {
	Hostname          string    `json:"hostname"`
	Port              int32     `json:"port"`
	State             string    `json:"state"`
	Status            string    `json:"status"`
	DiskTotalBytes    uint64    `json:"disk_total_bytes"`
	DiskUsedBytes     uint64    `json:"disk_used_bytes"`
	ActiveConnections int32     `json:"active_connections"`
	InProgressJobs    int32     `json:"in_progress_jobs"`
	UptimeSeconds     int64     `json:"uptime_seconds"`
	ReportedAt        time.Time `json:"reported_at"`
}

// handleGetStorageStatus serves GET /api/v1/storage-policies/{id}/status from
// api-server's own memory -- the one REST endpoint with no backend gRPC call.
func (s *server) handleGetStorageStatus(w http.ResponseWriter, r *http.Request) {
	now := s.storageStatus.now()
	reports := s.storageStatus.forPolicy(r.PathValue("id"))
	out := make([]storageStatusDTO, 0, len(reports))
	for _, rep := range reports {
		out = append(out, storageStatusDTO{
			Hostname: rep.Host, Port: rep.Port,
			State: storageState(rep.ReceivedAt, now), Status: rep.Status,
			DiskTotalBytes: rep.DiskTotalBytes, DiskUsedBytes: rep.DiskUsedBytes,
			ActiveConnections: rep.ActiveConnections, InProgressJobs: rep.InProgressJobs,
			UptimeSeconds: rep.UptimeSeconds, ReportedAt: rep.ReportedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": out})
}
