package main

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func TestLoggingSender_Send_LogsEveryRecordAndSucceeds(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sender := NewLoggingSender(logger)

	batch := []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
		{Seq: 2, JobID: "job-1", ObjectID: "obj-2"},
	}

	require.NoError(t, sender.Send(batch))

	output := buf.String()
	assert.Contains(t, output, "obj-1")
	assert.Contains(t, output, "obj-2")
}

func TestLoggingSender_Send_EmptyBatchSucceeds(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sender := NewLoggingSender(logger)

	assert.NoError(t, sender.Send(nil))
}

func TestLoggingSender_SendDeletions_LogsEveryRecordAndSucceeds(t *testing.T) {
	var buf bytes.Buffer
	sender := NewLoggingSender(slog.New(slog.NewTextHandler(&buf, nil)))

	require.NoError(t, sender.SendDeletions([]wfs.FileVersionDeletionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "gone-1"},
		{Seq: 2, JobID: "job-1", ObjectID: "gone-2"},
	}))

	assert.Contains(t, buf.String(), "gone-1")
	assert.Contains(t, buf.String(), "gone-2")
	assert.NoError(t, sender.SendDeletions(nil))
}
