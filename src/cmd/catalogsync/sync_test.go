package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type fakeReader struct {
	mu        sync.Mutex
	records   []wfs.FileVersionRecord
	deletions []wfs.FileVersionDeletionRecord
	damaged   []string // sorted, like bwfs's file_id order
	// damagedErr fails every DamagedFileIDs call; damagedErrAfterFirstPage
	// fails only the follow-up pages (a read error mid-stream).
	damagedErr               error
	damagedErrAfterFirstPage error
}

func (f *fakeReader) DamagedFileIDs(ctx context.Context, after string, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.damagedErr != nil {
		return nil, f.damagedErr
	}
	if after != "" && f.damagedErrAfterFirstPage != nil {
		return nil, f.damagedErrAfterFirstPage
	}
	out := []string{}
	for _, id := range f.damaged {
		if id > after {
			out = append(out, id)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeReader) setDamaged(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.damaged = ids
}

func (f *fakeReader) addRecord(r wfs.FileVersionRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, r)
}

func (f *fakeReader) addDeletion(r wfs.FileVersionDeletionRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletions = append(f.deletions, r)
}

func (f *fakeReader) FileVersionDeletionsSince(ctx context.Context, cursor int64, limit int) ([]wfs.FileVersionDeletionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []wfs.FileVersionDeletionRecord
	for _, r := range f.deletions {
		if r.Seq > cursor {
			out = append(out, r)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeReader) FileVersionsSince(ctx context.Context, cursor int64, limit int) ([]wfs.FileVersionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []wfs.FileVersionRecord
	for _, r := range f.records {
		if r.Seq > cursor {
			out = append(out, r)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

type fakeSender struct {
	mu         sync.Mutex
	batches    [][]wfs.FileVersionRecord
	delBatches [][]wfs.FileVersionDeletionRecord
	failN      int // number of subsequent Send calls to fail before succeeding
	failDelN   int // same, for SendDeletions

	// damagedSends holds one entry per successful SendDamaged: the pages it
	// streamed, in order.
	damagedSends    [][][]string
	damagedAttempts int
	failDamagedN    int   // same as failN, for SendDamaged
	damagedErr      error // when set, every SendDamaged fails with it
	// calls records the order of successful sends ("versions", "deletions",
	// "damaged") so tests can check the pass order.
	calls []string
}

func (f *fakeSender) SendDamaged(nextPage DamagedPages) error {
	// Drain the pages before taking the lock: nextPage reads the fake reader.
	var pages [][]string
	var readErr error
	for {
		page, err := nextPage()
		if err != nil {
			readErr = err
			break
		}
		if len(page) == 0 {
			break
		}
		pages = append(pages, page)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.damagedAttempts++
	if readErr != nil {
		return readErr
	}
	if f.damagedErr != nil {
		return f.damagedErr
	}
	if f.failDamagedN > 0 {
		f.failDamagedN--
		return errors.New("simulated damaged send failure")
	}
	f.damagedSends = append(f.damagedSends, pages)
	f.calls = append(f.calls, "damaged")
	return nil
}

// damagedSendIDs returns the ids of every successful SendDamaged, one slice
// per send.
func (f *fakeSender) damagedSendIDs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.damagedSends))
	for i, pages := range f.damagedSends {
		out[i] = []string{}
		for _, p := range pages {
			out[i] = append(out[i], p...)
		}
	}
	return out
}

func (f *fakeSender) damagedAttemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.damagedAttempts
}

func (f *fakeSender) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeSender) SendDeletions(batch []wfs.FileVersionDeletionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelN > 0 {
		f.failDelN--
		return errors.New("simulated deletion send failure")
	}
	f.delBatches = append(f.delBatches, batch)
	f.calls = append(f.calls, "deletions")
	return nil
}

func (f *fakeSender) sentDeletions() []wfs.FileVersionDeletionRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []wfs.FileVersionDeletionRecord
	for _, b := range f.delBatches {
		all = append(all, b...)
	}
	return all
}

func (f *fakeSender) Send(batch []wfs.FileVersionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failN > 0 {
		f.failN--
		return errors.New("simulated send failure")
	}
	f.batches = append(f.batches, batch)
	f.calls = append(f.calls, "versions")
	return nil
}

func (f *fakeSender) sentVersions() []wfs.FileVersionRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []wfs.FileVersionRecord
	for _, b := range f.batches {
		all = append(all, b...)
	}
	return all
}

func (f *fakeSender) sentBatchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRun_SendsAllRecordsAndAdvancesCursor(t *testing.T) {
	dir := t.TempDir()
	cursorFile := filepath.Join(dir, "catalogsync.cursor")

	rd := &fakeReader{records: []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
		{Seq: 2, JobID: "job-1", ObjectID: "obj-2"},
	}}
	sender := &fakeSender{}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	cfg := syncConfig{BatchSize: 10, PollInterval: 10 * time.Millisecond, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
	err := run(ctx, testLogger(), rd, sender, cursorFile, filepath.Join(dir, "catalogsync-deletions.cursor"), cfg)
	require.NoError(t, err)

	require.Equal(t, 1, sender.sentBatchCount())
	assert.Len(t, sender.batches[0], 2)

	seq, err := readCursor(cursorFile)
	require.NoError(t, err)
	assert.Equal(t, int64(2), seq)
}

func TestRun_CursorDoesNotAdvanceOnSendFailure(t *testing.T) {
	dir := t.TempDir()
	cursorFile := filepath.Join(dir, "catalogsync.cursor")

	rd := &fakeReader{records: []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
	}}
	sender := &fakeSender{failN: 1000} // fails for the whole test window

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cfg := syncConfig{BatchSize: 10, PollInterval: 10 * time.Millisecond, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
	err := run(ctx, testLogger(), rd, sender, cursorFile, filepath.Join(dir, "catalogsync-deletions.cursor"), cfg)
	require.NoError(t, err)

	seq, err := readCursor(cursorFile)
	require.NoError(t, err)
	assert.Equal(t, int64(0), seq, "cursor must not advance while sends keep failing")
}

func TestRun_RetriesAfterTransientFailureThenAdvances(t *testing.T) {
	dir := t.TempDir()
	cursorFile := filepath.Join(dir, "catalogsync.cursor")

	rd := &fakeReader{records: []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
	}}
	sender := &fakeSender{failN: 2} // fails twice, then succeeds

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cfg := syncConfig{BatchSize: 10, PollInterval: 10 * time.Millisecond, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 30 * time.Millisecond}
	err := run(ctx, testLogger(), rd, sender, cursorFile, filepath.Join(dir, "catalogsync-deletions.cursor"), cfg)
	require.NoError(t, err)

	seq, err := readCursor(cursorFile)
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)
}

func fastCfg(batch int) syncConfig {
	return syncConfig{BatchSize: batch, PollInterval: 10 * time.Millisecond, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
}

func TestRun_SendsDeletionsAndAdvancesTheDeletionCursor(t *testing.T) {
	dir := t.TempDir()
	delCursor := filepath.Join(dir, "catalogsync-deletions.cursor")
	rd := &fakeReader{deletions: []wfs.FileVersionDeletionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
		{Seq: 2, JobID: "job-1", ObjectID: "obj-2"},
	}}
	sender := &fakeSender{}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	require.NoError(t, run(ctx, testLogger(), rd, sender, filepath.Join(dir, "v.cursor"), delCursor, fastCfg(10)))

	assert.Equal(t, rd.deletions, sender.sentDeletions())
	seq, err := readCursor(delCursor)
	require.NoError(t, err)
	assert.Equal(t, int64(2), seq)
	versionSeq, err := readCursor(filepath.Join(dir, "v.cursor"))
	require.NoError(t, err)
	assert.Equal(t, int64(0), versionSeq, "the versions cursor is independent of the deletions cursor")
}

func TestRun_DeletionCursorDoesNotAdvanceOnSendFailureAndRetriesLater(t *testing.T) {
	dir := t.TempDir()
	delCursor := filepath.Join(dir, "catalogsync-deletions.cursor")
	rd := &fakeReader{deletions: []wfs.FileVersionDeletionRecord{{Seq: 1, JobID: "j", ObjectID: "o"}}}

	failing := &fakeSender{failDelN: 1000}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	require.NoError(t, run(ctx, testLogger(), rd, failing, filepath.Join(dir, "v.cursor"), delCursor, fastCfg(10)))
	cancel()
	seq, err := readCursor(delCursor)
	require.NoError(t, err)
	assert.Equal(t, int64(0), seq, "cursor must not advance while deletion sends keep failing")

	flaky := &fakeSender{failDelN: 2}
	ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	require.NoError(t, run(ctx, testLogger(), rd, flaky, filepath.Join(dir, "v.cursor"), delCursor, fastCfg(10)))
	assert.Len(t, flaky.sentDeletions(), 1)
	seq, err = readCursor(delCursor)
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)
}

// Deletions must never overtake versions: a version whose send has not been
// acknowledged yet could otherwise be deleted in the catalog first and then
// re-created by the retried send.
func TestRun_DeletionsWaitUntilTheVersionsBatchIsAcknowledged(t *testing.T) {
	dir := t.TempDir()
	rd := &fakeReader{
		records:   []wfs.FileVersionRecord{{Seq: 1, JobID: "j", ObjectID: "o1"}},
		deletions: []wfs.FileVersionDeletionRecord{{Seq: 1, JobID: "j", ObjectID: "o0"}},
	}
	sender := &fakeSender{failN: 1000}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	require.NoError(t, run(ctx, testLogger(), rd, sender, filepath.Join(dir, "v.cursor"), filepath.Join(dir, "d.cursor"), fastCfg(10)))

	assert.Empty(t, sender.sentDeletions(), "no deletion may be sent while the versions send keeps failing")
}

func TestRun_DrainsADeletionBacklogWithoutWaitingForThePollInterval(t *testing.T) {
	dir := t.TempDir()
	rd := &fakeReader{}
	for i := 1; i <= 5; i++ {
		rd.deletions = append(rd.deletions, wfs.FileVersionDeletionRecord{Seq: int64(i), JobID: "j", ObjectID: "o"})
	}
	sender := &fakeSender{}
	cfg := syncConfig{BatchSize: 2, PollInterval: time.Hour, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	require.NoError(t, run(ctx, testLogger(), rd, sender, filepath.Join(dir, "v.cursor"), filepath.Join(dir, "d.cursor"), cfg))

	assert.Len(t, sender.sentDeletions(), 5)
}

// damageCfg is fastCfg with a damage interval.
func damageCfg(batch int, interval time.Duration) syncConfig {
	cfg := fastCfg(batch)
	cfg.DamageInterval = interval
	return cfg
}

// startRun runs the sync loop in the background until the test ends.
func startRun(t *testing.T, rd reader, sender Sender, cfg syncConfig) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), rd, sender, filepath.Join(dir, "v.cursor"), filepath.Join(dir, "d.cursor"), cfg)
	}()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
}

func TestRun_DamagePassRunsAfterVersionsAndDeletions(t *testing.T) {
	rd := &fakeReader{
		records:   []wfs.FileVersionRecord{{Seq: 1, JobID: "j", ObjectID: "o1"}},
		deletions: []wfs.FileVersionDeletionRecord{{Seq: 1, JobID: "j", ObjectID: "o0"}},
		damaged:   []string{"f1"},
	}
	sender := &fakeSender{}
	startRun(t, rd, sender, damageCfg(10, time.Hour))

	require.Eventually(t, func() bool { return len(sender.damagedSendIDs()) == 1 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"versions", "deletions", "damaged"}, sender.callOrder())
	assert.Equal(t, [][]string{{"f1"}}, sender.damagedSendIDs())
}

func TestRun_DamagePassWaitsForTheInterval(t *testing.T) {
	rd := &fakeReader{damaged: []string{"f1"}}
	sender := &fakeSender{}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	// The poll loop turns over every 10ms, but the damage pass may run only
	// once an hour: just the first pass after start.
	require.NoError(t, run(ctx, testLogger(), rd, sender, filepath.Join(dir, "v"), filepath.Join(dir, "d"), damageCfg(10, time.Hour)))

	assert.Equal(t, 1, sender.damagedAttemptCount())
}

func TestRun_DamagePassRepeatsOnceTheIntervalElapses(t *testing.T) {
	rd := &fakeReader{damaged: []string{"f1"}}
	sender := &fakeSender{}
	startRun(t, rd, sender, damageCfg(10, 20*time.Millisecond))

	require.Eventually(t, func() bool { return len(sender.damagedSendIDs()) >= 3 }, time.Second, 5*time.Millisecond)
}

func TestRun_DamagePassStreamsTheWholeSetInBatchSizePages(t *testing.T) {
	rd := &fakeReader{damaged: []string{"a", "b", "c", "d", "e"}}
	sender := &fakeSender{}
	startRun(t, rd, sender, damageCfg(2, time.Hour))

	require.Eventually(t, func() bool { return len(sender.damagedSendIDs()) == 1 }, time.Second, 5*time.Millisecond)
	sender.mu.Lock()
	defer sender.mu.Unlock()
	assert.Equal(t, [][]string{{"a", "b"}, {"c", "d"}, {"e"}}, sender.damagedSends[0],
		"one SendDamaged call, one page per CatalogSyncBatchSize ids")
}

// After a restart the first send happens even when nothing is damaged, so
// rows a previous run left in the catalog get cleared; after that an empty
// set is not re-sent while it stays empty.
func TestRun_EmptyDamagedSetIsSentOnceThenSkippedWhileItStaysEmpty(t *testing.T) {
	rd := &fakeReader{}
	sender := &fakeSender{}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	require.NoError(t, run(ctx, testLogger(), rd, sender, filepath.Join(dir, "v"), filepath.Join(dir, "d"), damageCfg(10, 10*time.Millisecond)))

	assert.Equal(t, [][]string{{}}, sender.damagedSendIDs())
}

func TestRun_EmptyDamagedSetAfterANonEmptyOneIsSent(t *testing.T) {
	rd := &fakeReader{damaged: []string{"f1"}}
	sender := &fakeSender{}
	startRun(t, rd, sender, damageCfg(10, 10*time.Millisecond))

	require.Eventually(t, func() bool { return len(sender.damagedSendIDs()) >= 1 }, time.Second, 5*time.Millisecond)
	rd.setDamaged() // the file healed

	require.Eventually(t, func() bool {
		sends := sender.damagedSendIDs()
		return len(sends[len(sends)-1]) == 0
	}, time.Second, 5*time.Millisecond, "the now-empty set must be sent so the catalog clears it")
	settled := len(sender.damagedSendIDs())
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, settled, len(sender.damagedSendIDs()), "and then not re-sent while it stays empty")
}

// A failed send must not count as "the catalog has the empty set", or the
// retry would be skipped and stale rows would stay in the catalog.
func TestRun_FailedDamagedSendIsRetried(t *testing.T) {
	rd := &fakeReader{}
	sender := &fakeSender{failDamagedN: 2}
	startRun(t, rd, sender, damageCfg(10, 10*time.Millisecond))

	require.Eventually(t, func() bool { return len(sender.damagedSendIDs()) == 1 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, 3, sender.damagedAttemptCount())
}

func TestRun_FailingDamagedSendDoesNotHoldUpVersionsOrDeletions(t *testing.T) {
	rd := &fakeReader{damaged: []string{"f1"}}
	sender := &fakeSender{failDamagedN: 1 << 30}
	// A loop-wide backoff of an hour would stall everything if the damage
	// failure slept on it; the damage pass must back off on its own.
	cfg := syncConfig{BatchSize: 10, PollInterval: 5 * time.Millisecond, InitialBackoff: time.Hour, MaxBackoff: time.Hour, DamageInterval: 5 * time.Millisecond}
	startRun(t, rd, sender, cfg)

	require.Eventually(t, func() bool { return sender.damagedAttemptCount() >= 1 }, time.Second, 5*time.Millisecond)
	rd.addRecord(wfs.FileVersionRecord{Seq: 1, JobID: "j", ObjectID: "o1"})
	rd.addDeletion(wfs.FileVersionDeletionRecord{Seq: 1, JobID: "j", ObjectID: "o0"})

	require.Eventually(t, func() bool {
		return len(sender.sentVersions()) == 1 && len(sender.sentDeletions()) == 1
	}, time.Second, 5*time.Millisecond)
	assert.Empty(t, sender.damagedSendIDs())
}

func TestRun_DamagedReadErrorDoesNotStopVersionsOrDeletions(t *testing.T) {
	rd := &fakeReader{damagedErr: errors.New("disk I/O error")}
	sender := &fakeSender{}
	cfg := syncConfig{BatchSize: 10, PollInterval: 5 * time.Millisecond, InitialBackoff: time.Hour, MaxBackoff: time.Hour, DamageInterval: 5 * time.Millisecond}
	startRun(t, rd, sender, cfg)

	for i := int64(1); i <= 3; i++ {
		rd.addRecord(wfs.FileVersionRecord{Seq: i, JobID: "j", ObjectID: "o"})
		rd.addDeletion(wfs.FileVersionDeletionRecord{Seq: i, JobID: "j", ObjectID: "o"})
		time.Sleep(15 * time.Millisecond)
	}

	require.Eventually(t, func() bool {
		return len(sender.sentVersions()) == 3 && len(sender.sentDeletions()) == 3
	}, time.Second, 5*time.Millisecond)
	assert.Zero(t, sender.damagedAttemptCount(), "nothing to send when the first page cannot be read")
}

func TestRun_DamagedReadErrorMidStreamIsNotASuccessfulSend(t *testing.T) {
	rd := &fakeReader{damaged: []string{"a", "b", "c"}, damagedErrAfterFirstPage: errors.New("database is locked")}
	sender := &fakeSender{}
	startRun(t, rd, sender, damageCfg(2, 5*time.Millisecond))

	require.Eventually(t, func() bool { return sender.damagedAttemptCount() >= 2 }, time.Second, 5*time.Millisecond)
	assert.Empty(t, sender.damagedSendIDs())
}

// A failed send leaves the catalog's state unknown: the server may have
// replaced its set before the client saw the error. So after a failure an
// empty set must be sent again even if the last successful send was empty,
// or a heal could leave a stale damaged set in the catalog until restart.
func TestDamagePass_FailedSendInvalidatesTheEmptySetBelief(t *testing.T) {
	rd := &fakeReader{}
	sender := &fakeSender{}
	d := newDamagePass(testLogger(), rd, sender, damageCfg(10, time.Minute))
	ctx := context.Background()
	t0 := time.Unix(1_000_000, 0)

	d.runIfDue(ctx, t0) // empty set, sent: the catalog is known empty
	require.Equal(t, [][]string{{}}, sender.damagedSendIDs())

	rd.setDamaged("f1")
	sender.mu.Lock()
	sender.failDamagedN = 1
	sender.mu.Unlock()
	d.runIfDue(ctx, t0.Add(time.Hour)) // fails; the catalog may or may not hold {f1}
	require.Equal(t, 2, sender.damagedAttemptCount())

	rd.setDamaged() // healed
	d.runIfDue(ctx, t0.Add(2*time.Hour))

	assert.Equal(t, [][]string{{}, {}}, sender.damagedSendIDs(), "the empty set must be re-sent after a failed send")
}

// An old catalog without ReportDamagedFiles answers Unimplemented on every
// attempt. That is a steady state, not a fault: log it once at Info and wait
// the longest backoff between attempts, instead of a Warn every minute.
func TestDamagePass_UnimplementedLogsOnceAndWaitsTheMaxBackoff(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sender := &fakeSender{damagedErr: fmt.Errorf("ReportDamagedFiles: %w", status.Error(codes.Unimplemented, "unknown method"))}
	cfg := damageCfg(10, time.Minute)
	d := newDamagePass(logger, &fakeReader{}, sender, cfg)
	ctx := context.Background()
	t0 := time.Unix(1_000_000, 0)

	d.runIfDue(ctx, t0)
	d.runIfDue(ctx, t0.Add(cfg.MaxBackoff))
	d.runIfDue(ctx, t0.Add(2*cfg.MaxBackoff))

	assert.Equal(t, 1, strings.Count(logs.String(), "catalog does not support damage reports yet"))
	assert.NotContains(t, logs.String(), "level=WARN")
	assert.Equal(t, 3, sender.damagedAttemptCount(), "retried once per max backoff, not sooner")

	d.runIfDue(ctx, t0.Add(2*cfg.MaxBackoff+cfg.MaxBackoff/2))
	assert.Equal(t, 3, sender.damagedAttemptCount(), "no attempt before the max backoff has passed")
}
