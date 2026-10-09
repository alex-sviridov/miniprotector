package main

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

func TestConnCounter(t *testing.T) {
	c := &connCounter{}
	c.HandleConn(context.Background(), &stats.ConnBegin{})
	c.HandleConn(context.Background(), &stats.ConnBegin{})
	c.HandleConn(context.Background(), &stats.ConnEnd{})
	assert.Equal(t, int32(1), c.Active())
}

func TestDiskUsage(t *testing.T) {
	total, used, err := diskUsage(t.TempDir())
	require.NoError(t, err)
	assert.Greater(t, total, uint64(0))
	assert.LessOrEqual(t, used, total)
}

func TestLivenessActive(t *testing.T) {
	l := newJobLiveness()
	l.Touch("a")
	l.Touch("b")
	l.Complete("a")
	assert.Equal(t, 1, l.Active())
}

type fakeStatusClient struct {
	mu   sync.Mutex
	reqs []*pb.ReportStorageStatusRequest
	err  error
}

func (f *fakeStatusClient) ReportStorageStatus(_ context.Context, in *pb.ReportStorageStatusRequest, _ ...grpc.CallOption) (*pb.ReportStorageStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, in)
	return &pb.ReportStorageStatusResponse{}, f.err
}

func (f *fakeStatusClient) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func TestRunStatusReporter_PostsImmediatelyThenPeriodically(t *testing.T) {
	conns := &connCounter{}
	conns.HandleConn(context.Background(), &stats.ConnBegin{})
	src := statusSource{policyID: "p1", port: 9400, root: t.TempDir(), conns: conns, jobs: newJobLiveness(), started: time.Now()}
	fake := &fakeStatusClient{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runStatusReporter(ctx, quietLogger(), fake, src, 20*time.Millisecond); close(done) }()
	require.Eventually(t, func() bool { return fake.count() >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done

	first := fake.reqs[0]
	assert.Equal(t, "p1", first.PolicyId)
	assert.Equal(t, int32(9400), first.Port)
	assert.Equal(t, "serving", first.Status)
	assert.Equal(t, int32(1), first.ActiveConnections)
	assert.Greater(t, first.DiskTotalBytes, uint64(0))
}

func TestRunStatusReporter_SurvivesFailures(t *testing.T) {
	src := statusSource{policyID: "p1", root: t.TempDir(), conns: &connCounter{}, jobs: newJobLiveness(), started: time.Now()}
	fake := &fakeStatusClient{err: assert.AnError}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runStatusReporter(ctx, quietLogger(), fake, src, 10*time.Millisecond); close(done) }()
	require.Eventually(t, func() bool { return fake.count() >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	<-done
}
