package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"syscall"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"google.golang.org/grpc/stats"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// statusReportInterval is how often bwfs posts its status to api-server.
const statusReportInterval = time.Minute

// statusReportTimeout bounds one post, so a hung api-server never stalls the
// next tick.
const statusReportTimeout = 5 * time.Second

// connCounter is a grpc stats.Handler that counts open client connections.
type connCounter struct{ n atomic.Int32 }

func (c *connCounter) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (c *connCounter) HandleRPC(context.Context, stats.RPCStats)                       {}
func (c *connCounter) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (c *connCounter) HandleConn(_ context.Context, s stats.ConnStats) {
	switch s.(type) {
	case *stats.ConnBegin:
		c.n.Add(1)
	case *stats.ConnEnd:
		c.n.Add(-1)
	}
}

func (c *connCounter) Active() int32 { return c.n.Load() }

// diskUsage returns total and used bytes of the filesystem holding path.
func diskUsage(path string) (total, used uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	total = st.Blocks * uint64(st.Bsize)
	used = total - st.Bavail*uint64(st.Bsize)
	return total, used, nil
}

type activeJobs interface{ Active() int }

// statusSource is everything one report is built from.
type statusSource struct {
	policyID string
	port     int
	root     string
	conns    *connCounter
	jobs     activeJobs
	started  time.Time
}

func (s statusSource) report() *pb.ReportStorageStatusRequest {
	req := &pb.ReportStorageStatusRequest{
		PolicyId:          s.policyID,
		Port:              int32(s.port),
		Status:            "serving",
		ActiveConnections: s.conns.Active(),
		InProgressJobs:    int32(s.jobs.Active()),
		UptimeSeconds:     int64(time.Since(s.started).Seconds()),
		ReportedAt:        timestamppb.Now(),
	}
	// A failed statfs still reports the rest rather than going silent.
	req.DiskTotalBytes, req.DiskUsedBytes, _ = diskUsage(s.root)
	return req
}

// runStatusReporter posts a report immediately and then every interval until
// ctx is cancelled. It is best-effort: a failure is logged once on the
// up->down transition (and once on recovery), never retried faster than the
// next tick, and never affects backups.
func runStatusReporter(ctx context.Context, logger *slog.Logger, client pb.StorageStatusServiceClient, src statusSource, interval time.Duration) {
	healthy := true
	post := func() {
		cctx, cancel := context.WithTimeout(ctx, statusReportTimeout)
		defer cancel()
		_, err := client.ReportStorageStatus(cctx, src.report())
		switch {
		case err != nil && healthy:
			logger.Warn("status report to api-server failed; will retry", "error", err)
		case err == nil && !healthy:
			logger.Info("status reporting to api-server recovered")
		}
		healthy = err == nil
	}
	post()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			post()
		}
	}
}

// startStatusReporter starts reporting in the background. It is skipped, with
// a log line, when no policy id was given (not agent-launched) or no
// api_server_host is configured -- status reporting is optional.
func startStatusReporter(ctx context.Context, logger *slog.Logger, conf *config.Config, certsDir string, src statusSource) {
	if src.policyID == "" || conf.APIServerHost == "" {
		logger.Info("status reporting disabled", "policy_id_set", src.policyID != "", "api_server_host_set", conf.APIServerHost != "")
		return
	}
	conn, err := connection.DialNonBlocking(conf.APIServerHost, conf.APIServerJobStatusPort, certsDir)
	if err != nil {
		logger.Warn("status reporting disabled: dial api-server failed", "error", err)
		return
	}
	src.started = time.Now()
	go func() {
		defer conn.Close()
		runStatusReporter(ctx, logger, pb.NewStorageStatusServiceClient(conn), src, statusReportInterval)
	}()
}
