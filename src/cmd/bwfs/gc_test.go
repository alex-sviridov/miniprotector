package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	wfsfile "github.com/alex-sviridov/miniprotector/workload/filesystem"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// spyStore records the GC calls the scheduler makes; every other
// BackupStore method is the nil embedded interface's and must not be used.
type spyStore struct {
	storage.BackupStore
	mu                        sync.Mutex
	cleanups, vacuums, prunes int
	dryRun                    bool
	batch                     int
	grace                     time.Duration
	pruneOlderThan            time.Time
	cleanupErr                error
}

func (s *spyStore) CleanupExpired(ctx context.Context, now time.Time, batch int, dryRun bool) (*storage.CleanupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanups++
	s.batch, s.dryRun = batch, dryRun
	return &storage.CleanupResult{VersionsExpired: 3, DryRun: dryRun}, s.cleanupErr
}

func (s *spyStore) VacuumOnline(ctx context.Context, batch int, grace time.Duration) (*storage.VacuumResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vacuums++
	s.batch, s.grace = batch, grace
	return &storage.VacuumResult{OrphanedChunksRemoved: 2, BytesReclaimed: 10}, nil
}

func (s *spyStore) PruneDeletionLog(ctx context.Context, olderThan time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prunes++
	s.pruneOlderThan = olderThan
	return 0, nil
}

func (s *spyStore) counts() (cleanups, vacuums, prunes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanups, s.vacuums, s.prunes
}

func TestGCSettingsFrom_ConvertsConfig(t *testing.T) {
	conf := &config.Config{
		StoreCleanupIntervalSec: 60, StoreVacuumIntervalSec: 600, StoreGCBatchSize: 50,
		StoreCleanupDryRun: true, StoreIncompleteFileDataGraceSec: 7200, StoreDeletionLogRetentionSec: 3600,
	}
	assert.Equal(t, gcSettings{
		CleanupInterval: time.Minute, VacuumInterval: 10 * time.Minute, BatchSize: 50, DryRun: true,
		IncompleteGrace: 2 * time.Hour, DeletionLogRetention: time.Hour,
	}, gcSettingsFrom(conf))
}

func TestRunEvery_RunsEachTickAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	calls := 0
	done := make(chan struct{})
	go func() {
		runEvery(ctx, 10*time.Millisecond, func(context.Context) { mu.Lock(); calls++; mu.Unlock() })
		close(done)
	}()

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return calls >= 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runEvery did not stop after cancel")
	}
}

func TestRunEvery_ZeroIntervalNeverRuns(t *testing.T) {
	ran := false
	runEvery(context.Background(), 0, func(context.Context) { ran = true })
	assert.False(t, ran)
}

func TestStartStoreGC_RunsBothLoopsAtTheirOwnIntervals(t *testing.T) {
	spy := &spyStore{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startStoreGC(ctx, quietLogger(), spy, gcSettings{
		CleanupInterval: 10 * time.Millisecond, VacuumInterval: 80 * time.Millisecond,
		BatchSize: 25, IncompleteGrace: 3 * time.Hour, DeletionLogRetention: 24 * time.Hour,
	})

	require.Eventually(t, func() bool { c, v, _ := spy.counts(); return v >= 1 && c >= 4 }, 3*time.Second, 5*time.Millisecond)
	c, v, p := spy.counts()
	assert.Greater(t, c, v, "cleanup is the frequent loop, vacuum the rare one")
	assert.Equal(t, c, p, "the deletion log is pruned as part of each cleanup run")
	spy.mu.Lock()
	assert.Equal(t, 25, spy.batch)
	assert.Equal(t, 3*time.Hour, spy.grace)
	assert.WithinDuration(t, time.Now().Add(-24*time.Hour), spy.pruneOlderThan, 5*time.Second)
	spy.mu.Unlock()
}

func TestStartStoreGC_ZeroIntervalDisablesThatLoop(t *testing.T) {
	spy := &spyStore{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startStoreGC(ctx, quietLogger(), spy, gcSettings{CleanupInterval: 10 * time.Millisecond, VacuumInterval: 0, BatchSize: 10})

	require.Eventually(t, func() bool { c, _, _ := spy.counts(); return c >= 2 }, 3*time.Second, 5*time.Millisecond)
	_, v, _ := spy.counts()
	assert.Equal(t, 0, v)
}

func TestCleanupOnce_DryRunNeverPrunesTheLog(t *testing.T) {
	spy := &spyStore{}
	cleanupOnce(context.Background(), quietLogger(), spy, gcSettings{BatchSize: 10, DryRun: true, DeletionLogRetention: time.Hour})

	c, _, p := spy.counts()
	assert.Equal(t, 1, c)
	assert.Equal(t, 0, p)
	assert.True(t, spy.dryRun)
}

func TestCleanupOnce_ZeroRetentionKeepsTheLogForever(t *testing.T) {
	spy := &spyStore{}
	cleanupOnce(context.Background(), quietLogger(), spy, gcSettings{BatchSize: 10, DeletionLogRetention: 0})
	_, _, p := spy.counts()
	assert.Equal(t, 0, p)
}

func TestCleanupLoop_ErrorInOneRunDoesNotStopLaterRuns(t *testing.T) {
	spy := &spyStore{cleanupErr: errors.New("db is locked")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startStoreGC(ctx, quietLogger(), spy, gcSettings{CleanupInterval: 10 * time.Millisecond, BatchSize: 10})

	require.Eventually(t, func() bool { c, _, _ := spy.counts(); return c >= 3 }, 3*time.Second, 5*time.Millisecond)
}

// blockingStream is a BackupService stream whose Send blocks until released,
// standing in for a client that has stopped reading.
type blockingStream struct {
	grpc.ServerStream
	sending chan struct{}
	release chan struct{}
}

func (b *blockingStream) Recv() (*pb.FileRequest, error) { return nil, io.EOF }

func (b *blockingStream) Send(*pb.FileResponse) error {
	close(b.sending)
	<-b.release
	return nil
}

// A handler must not hold the store operation guard while blocked in Send:
// otherwise one stalled client would queue a GC batch behind it, and that
// waiting batch would in turn stall every other backup stream.
func TestHandler_ReleasesStoreGuardBeforeSending(t *testing.T) {
	dir := t.TempDir()
	store, err := wfs.New(filepath.Join(dir, "store"))
	require.NoError(t, err)
	defer store.Close()

	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644))
	files, err := wfsfile.Discover(src, []string{"*"}, nil)
	require.NoError(t, err)
	var file wfsfile.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' {
			file = f
		}
	}
	encoded, err := file.Encode()
	require.NoError(t, err)

	h := newStreamHandler(context.WithValue(context.Background(), config.ContextKey, &config.Config{}), quietLogger(), store, "job-1")
	stream := &blockingStream{sending: make(chan struct{}), release: make(chan struct{})}
	handled := make(chan error, 1)
	go func() {
		handled <- h.handleRequest(context.Background(), stream, &pb.FileRequest{
			RequestType: &pb.FileRequest_FileInfo{FileInfo: &pb.FileInfo{FileId: file.ID(), Attributes: encoded}},
		})
	}()
	<-stream.sending // the handler's store work is done and it is blocked in Send

	gcDone := make(chan error, 1)
	go func() {
		_, err := store.CleanupExpired(context.Background(), time.Now(), 10, false)
		gcDone <- err
	}()
	select {
	case err := <-gcDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("a GC batch was blocked by a handler stuck in Send")
	}

	close(stream.release)
	require.NoError(t, <-handled)
}
