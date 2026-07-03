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
	mu      sync.Mutex
	records []wfs.FileVersionRecord
}

func (f *fakeReader) FileVersionsSince(cursor int64, limit int) ([]wfs.FileVersionRecord, error) {
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
	mu      sync.Mutex
	batches [][]wfs.FileVersionRecord
	failN   int // number of subsequent Send calls to fail before succeeding
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
	err := run(ctx, testLogger(), rd, sender, cursorFile, cfg)
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
	err := run(ctx, testLogger(), rd, sender, cursorFile, cfg)
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
	err := run(ctx, testLogger(), rd, sender, cursorFile, cfg)
	require.NoError(t, err)

	seq, err := readCursor(cursorFile)
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)
}
