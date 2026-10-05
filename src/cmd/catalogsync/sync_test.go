package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type fakeReader struct {
	mu        sync.Mutex
	records   []wfs.FileVersionRecord
	deletions []wfs.FileVersionDeletionRecord
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
}

func (f *fakeSender) SendDeletions(batch []wfs.FileVersionDeletionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelN > 0 {
		f.failDelN--
		return errors.New("simulated deletion send failure")
	}
	f.delBatches = append(f.delBatches, batch)
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
	return nil
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
