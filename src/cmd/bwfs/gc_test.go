package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	cleanups, vacuums, prunes int // cleanups counts every CleanupExpired call, probes included
	dryCalls                  int
	dryRun                    bool
	batch                     int
	grace                     time.Duration
	pruneOlderThan            time.Time
	cleanupErr                error
	vacuumErr                 error
	expired                   int64 // versions CleanupExpired reports (default 3)
	noExpired                 bool  // report 0 expired
}

func (s *spyStore) CleanupExpired(ctx context.Context, now time.Time, batch int, dryRun bool) (*storage.CleanupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanups++
	if dryRun {
		s.dryCalls++
	}
	s.batch, s.dryRun = batch, dryRun
	n := int64(3)
	if s.noExpired {
		n = 0
	} else if s.expired > 0 {
		n = s.expired
	}
	if s.cleanupErr != nil && !dryRun {
		return &storage.CleanupResult{DryRun: dryRun}, s.cleanupErr
	}
	return &storage.CleanupResult{VersionsExpired: n, DryRun: dryRun}, nil
}

func (s *spyStore) VacuumOnline(ctx context.Context, batch int, grace time.Duration) (*storage.VacuumResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vacuums++
	s.batch, s.grace = batch, grace
	if s.vacuumErr != nil {
		return &storage.VacuumResult{}, s.vacuumErr
	}
	return &storage.VacuumResult{OrphanedChunksRemoved: 2, OrphanedFileDataRemoved: 1, BytesReclaimed: 10}, nil
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
	assert.GreaterOrEqual(t, p, 1, "the deletion log is pruned as part of cleanup runs")
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
	assert.Equal(t, 1, c, "a dry run is a single dry-run call, no separate probe")
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

// logLines parses the JSON log output into one map per line.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &m))
		out = append(out, m)
	}
	return out
}

func jsonLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func byEvent(lines []map[string]any, event string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == event {
			out = append(out, l)
		}
	}
	return out
}

func TestCleanupOnce_LogsAJobWithStartAndFinishStatistics(t *testing.T) {
	var buf bytes.Buffer
	spy := &spyStore{expired: 7}

	cleanupOnce(context.Background(), jsonLogger(&buf), spy, gcSettings{BatchSize: 10, DeletionLogRetention: time.Hour})

	lines := logLines(t, &buf)
	starts, finishes := byEvent(lines, "start"), byEvent(lines, "finish")
	require.Len(t, starts, 1)
	require.Len(t, finishes, 1)
	jobID, _ := starts[0]["job_id"].(string)
	assert.Regexp(t, `^cleanup:[^:]+:\d+$`, jobID, "kind prefix, then host, then unix time")
	assert.Equal(t, jobID, finishes[0]["job_id"])
	assert.Equal(t, "success", finishes[0]["status"])
	assert.Equal(t, float64(7), finishes[0]["versions_expired"])
	assert.Equal(t, false, finishes[0]["dry_run"])
	assert.Contains(t, finishes[0], "duration")
}

func TestCleanupOnce_NothingExpiredProducesNoJob(t *testing.T) {
	var buf bytes.Buffer
	spy := &spyStore{noExpired: true}

	cleanupOnce(context.Background(), jsonLogger(&buf), spy, gcSettings{BatchSize: 10, DeletionLogRetention: time.Hour})

	for _, l := range logLines(t, &buf) {
		assert.NotContains(t, l, "job_id", "an hourly no-op run must not clutter the job list")
		assert.NotContains(t, l, "event")
	}
	assert.Equal(t, 1, spy.dryCalls, "only the cheap probe ran, not a real cleanup")
	_, _, prunes := spy.counts()
	assert.Equal(t, 1, prunes, "the deletion log is still pruned")
}

func TestCleanupOnce_FailureFinishesTheJobWithTheError(t *testing.T) {
	var buf bytes.Buffer
	spy := &spyStore{cleanupErr: errors.New("database is locked")}

	cleanupOnce(context.Background(), jsonLogger(&buf), spy, gcSettings{BatchSize: 10})

	lines := logLines(t, &buf)
	finishes := byEvent(lines, "finish")
	require.Len(t, finishes, 1)
	assert.Equal(t, "failure", finishes[0]["status"])
	assert.Equal(t, "ERROR", finishes[0]["level"])
	assert.Contains(t, finishes[0]["error"], "database is locked")
	assert.Len(t, byEvent(lines, "start"), 1)
}

func TestCleanupOnce_DryRunJobSaysSoAndIsCountedOnce(t *testing.T) {
	var buf bytes.Buffer
	spy := &spyStore{expired: 4}

	cleanupOnce(context.Background(), jsonLogger(&buf), spy, gcSettings{BatchSize: 10, DryRun: true})

	finishes := byEvent(logLines(t, &buf), "finish")
	require.Len(t, finishes, 1)
	assert.Equal(t, true, finishes[0]["dry_run"])
	assert.Equal(t, float64(4), finishes[0]["versions_expired"], "the would-be count")
}

func TestVacuumOnce_LogsAJobWithStartAndFinishStatistics(t *testing.T) {
	var buf bytes.Buffer

	vacuumOnce(context.Background(), jsonLogger(&buf), &spyStore{}, gcSettings{BatchSize: 10})

	lines := logLines(t, &buf)
	starts, finishes := byEvent(lines, "start"), byEvent(lines, "finish")
	require.Len(t, starts, 1)
	require.Len(t, finishes, 1)
	assert.Regexp(t, `^vacuum:[^:]+:\d+$`, starts[0]["job_id"])
	assert.Equal(t, starts[0]["job_id"], finishes[0]["job_id"])
	assert.Equal(t, "success", finishes[0]["status"])
	assert.Equal(t, float64(2), finishes[0]["orphaned_chunks_removed"])
	assert.Equal(t, float64(1), finishes[0]["orphaned_file_data_removed"])
	assert.Equal(t, float64(10), finishes[0]["bytes_reclaimed"])
}

func TestVacuumOnce_RunsEvenWhenThereIsNothingToDoAndFailureFinishesTheJob(t *testing.T) {
	var buf bytes.Buffer
	vacuumOnce(context.Background(), jsonLogger(&buf), &spyStore{vacuumErr: errors.New("disk I/O error")}, gcSettings{BatchSize: 10})

	finishes := byEvent(logLines(t, &buf), "finish")
	require.Len(t, finishes, 1)
	assert.Equal(t, "failure", finishes[0]["status"])
	assert.Contains(t, finishes[0]["error"], "disk I/O error")
}
