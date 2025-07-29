package main

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/alex-sviridov/miniprotector/common"
)

// BackupMessageHandler implements backup-specific logic
type BackupMessageHandler struct {
	logger      *common.Logger
	storagePath string
	streams     map[uint32]*StreamState
	streamsMu   sync.RWMutex
	jobs        map[string]*JobState
	jobsMu      sync.RWMutex
}

type StreamState struct {
	jobID    string
	streamID int
	logger   *common.Logger
}

type JobState struct {
	JobID          string
	ActiveStreams  int
	FilesProcessed int
}

func NewBackupMessageHandler(config common.Config, ctx context.Context, storagePath string) *BackupMessageHandler {
	return &BackupMessageHandler{
		logger:      ctx.Value("logger").(*common.Logger),
		storagePath: storagePath,
		streams:     make(map[uint32]*StreamState),
		jobs:        make(map[string]*JobState),
	}
}

// Implement network.MessageHandler interface
func (h *BackupMessageHandler) OnConnectionStart(config *common.Config, ctx context.Context, connectionID uint32, scanner *bufio.Scanner, writer *bufio.Writer) error {
	h.logger.Info("Backup connection established: %d, waiting for START_STREAM", connectionID)

	// Wait for the first message (should be START_STREAM)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("failed to read START_STREAM message: %v", err)
		}
		return fmt.Errorf("connection closed before START_STREAM received")
	}

	message := strings.TrimSpace(scanner.Text())

	// Validate that it's a START_STREAM message
	if !strings.HasPrefix(message, "START_STREAM:") {
		h.logger.Error("Connection %d: expected START_STREAM, got: %s", connectionID, message)
		// Send error response
		writer.WriteString("ERROR: START_STREAM required\n")
		writer.Flush()
		return fmt.Errorf("first message must be START_STREAM, got: %s", message)
	}

	// Parse START_STREAM message: START_STREAM:jobId:streamId
	parts := strings.Split(message, ":")
	if len(parts) != 3 {
		h.logger.Error("Connection %d: invalid START_STREAM format: %s", connectionID, message)
		writer.WriteString("ERROR: invalid START_STREAM format\n")
		writer.Flush()
		return fmt.Errorf("invalid START_STREAM format, expected START_STREAM:jobId:streamId")
	}

	jobID := parts[1]
	streamID, err := strconv.Atoi(parts[2])
	if jobID == "" || err != nil {
		h.logger.Error("Connection %d: empty jobId or streamId", connectionID)
		writer.WriteString("ERROR: jobId and streamId cannot be empty\n")
		writer.Flush()
		return fmt.Errorf("jobId and streamId cannot be empty")
	}
	ctx = context.WithValue(ctx, "streamId", streamID)
	ctx = context.WithValue(ctx, "jobId", jobID)

	streamLogger, err := common.NewLogger(config, ctx)
	if err != nil {
		streamLogger = h.logger
	}

	// Store stream state with its own logger
	h.streamsMu.Lock()
	h.streams[connectionID] = &StreamState{
		jobID:    jobID,
		streamID: streamID,
		logger:   streamLogger, // Each stream gets its own logger
	}
	h.streamsMu.Unlock()

	h.jobsMu.Lock()
	if _, exists := h.jobs[jobID]; !exists {
		h.jobs[jobID] = &JobState{
			JobID:          jobID,
			ActiveStreams:  0,
			FilesProcessed: 0,
		}
	}
	h.jobs[jobID].ActiveStreams++
	h.jobsMu.Unlock()

	h.streams[connectionID].logger.Info("Stream started - Connection: %d, JobID: %s, StreamID: %d", connectionID, jobID, streamID)

	// Send acknowledgment
	writer.WriteString("START_STREAM_OK\n")
	writer.Flush()

	return nil
}

func (h *BackupMessageHandler) OnMessage(connectionID uint32, message string) (string, error) {
	// Parse backup-specific message format
	s := *h.streams[connectionID]
	if strings.HasPrefix(message, "BATCH:") {
		// Parse batch message
		fileList := message[6:] // Remove "BATCH:" prefix
		filenames := strings.Split(fileList, ",")

		// Process each file in the batch
		for _, filename := range filenames {
			// Print as requested: connectionid:filename
			s.logger.Debug("%d:%s", connectionID, filename)
		}

		s.logger.Debug("Stream %d received batch with %d files", s.streamID, len(filenames))

		// Send acknowledgment back to client
		return "BATCH_OK", nil

	} else {
		s.logger.Debug("Stream %d received unknown message: %s", s.streamID, message)
	}

	return "", nil
}

func (h *BackupMessageHandler) OnConnectionEnd(connectionID uint32) error {
	s := *h.streams[connectionID]
	s.logger.Info("Backup stream ended: %d", s.streamID)

	// Here you can add backup-specific cleanup:
	// - Finalize backup for this stream
	// - Update statistics
	// - etc.

	return nil
}
