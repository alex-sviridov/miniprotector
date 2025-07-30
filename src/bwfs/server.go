package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/alex-sviridov/miniprotector/common"
)

// BackupMessageHandler implements backup-specific logic
type BackupMessageHandler struct {
	logger      *slog.Logger
	storagePath string
	streams     map[uint32]*StreamState
	streamsMu   sync.RWMutex
	jobs        map[string]*JobState
	jobsMu      sync.RWMutex
}

type StreamState struct {
	iobId    string
	streamId int
	logger   *slog.Logger
}

type JobState struct {
	JobID          string
	ActiveStreams  int
	FilesProcessed int
}

func NewBackupMessageHandler(config common.Config, ctx context.Context, storagePath string) *BackupMessageHandler {
	return &BackupMessageHandler{
		logger:      ctx.Value("logger").(*slog.Logger),
		storagePath: storagePath,
		streams:     make(map[uint32]*StreamState),
		jobs:        make(map[string]*JobState),
	}
}

// Implement network.MessageHandler interface
func (h *BackupMessageHandler) OnConnectionStart(config *common.Config, ctx context.Context, connectionID uint32, scanner *bufio.Scanner, writer *bufio.Writer) error {

	streamLogger := h.logger.With(
		slog.Int("connectionID", int(connectionID)),
	)
	streamLogger.Info("Backup connection established, waiting for START_STREAM")

	// Wait for the first message (should be START_STREAM)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("failed to read START_STREAM message", "error", err)
		}
		return fmt.Errorf("connection closed before START_STREAM received")
	}

	message := strings.TrimSpace(scanner.Text())

	// Validate that it's a START_STREAM message
	if !strings.HasPrefix(message, "START_STREAM:") {
		streamLogger.Error("Wrong message, expected START_STREAM", "message")
		// Send error response
		writer.WriteString("ERROR:NEED_START_STREAM\n")
		writer.Flush()
		return fmt.Errorf("first message must be START_STREAM, got: %s", message, "connectionID", connectionID)
	}

	// Parse START_STREAM message: START_STREAM:jobId:streamId
	parts := strings.Split(message, ":")
	if len(parts) != 3 {
		streamLogger.Error("Invalid START_STREAM format", "message", message)
		writer.WriteString("ERROR: invalid START_STREAM format\n")
		writer.Flush()
		return fmt.Errorf("invalid START_STREAM format, expected START_STREAM:jobId:streamId")
	}

	iobId := parts[1]
	streamId, err := strconv.Atoi(parts[2])
	if iobId == "" || err != nil {
		streamLogger.Error("Empty jobId or streamId")
		writer.WriteString("ERROR: jobId and streamId cannot be empty\n")
		writer.Flush()
		return fmt.Errorf("jobId and streamId cannot be empty")
	}
	ctx = context.WithValue(ctx, "streamId", streamId)
	ctx = context.WithValue(ctx, "jobId", iobId)
	streamLogger = streamLogger.With(
		slog.String("jobId", iobId),
		slog.Int("streamId", streamId),
	)

	// Store stream state with its own logger
	h.streamsMu.Lock()
	h.streams[connectionID] = &StreamState{
		iobId:    iobId,
		streamId: streamId,
		logger:   streamLogger, // Each stream gets its own logger
	}
	h.streamsMu.Unlock()

	h.jobsMu.Lock()
	if _, exists := h.jobs[iobId]; !exists {
		h.jobs[iobId] = &JobState{
			JobID:          iobId,
			ActiveStreams:  0,
			FilesProcessed: 0,
		}
	}
	h.jobs[iobId].ActiveStreams++
	h.jobsMu.Unlock()

	streamLogger.Info("Stream started")

	// Send acknowledgment
	writer.WriteString("START_STREAM_OK\n")
	writer.Flush()

	return nil
}

func (h *BackupMessageHandler) OnConnectionEnd(connectionID uint32) error {
	s := *h.streams[connectionID]
	s.logger.Info("Backup stream ended")

	// To add backup-specific cleanup:
	// - Finalize backup for this stream
	// - Update statistics
	// - etc.

	h.streamsMu.Lock()
	delete(h.streams, connectionID)
	h.streamsMu.Unlock()

	return nil
}
