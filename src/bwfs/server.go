package main

import (
	"strings"

	"github.com/alex-sviridov/miniprotector/common"
)

// BackupMessageHandler implements backup-specific logic
type BackupMessageHandler struct {
	logger      *common.Logger
	storagePath string
}

func NewBackupMessageHandler(logger *common.Logger, storagePath string) *BackupMessageHandler {
	return &BackupMessageHandler{
		logger:      logger,
		storagePath: storagePath,
	}
}

// Implement network.MessageHandler interface
func (h *BackupMessageHandler) OnConnectionStart(connectionID uint32) error {
	h.logger.Info("Backup stream started: %d", connectionID)
	return nil
}

func (h *BackupMessageHandler) OnMessage(connectionID uint32, message string) error {
	// Parse backup-specific message format
	if strings.HasPrefix(message, "FILE:") {
		filename := message[5:] // Remove "FILE:" prefix

		// Print as requested: connectionid:filename
		h.logger.Debug("Stream %d received file: %s", connectionID, filename)

		// Here you can add backup-specific logic:
		// - Validate filename
		// - Create directory structure
		// - Track received files
		// - etc.

	} else {
		h.logger.Debug("Stream %d received unknown message: %s", connectionID, message)
	}

	return nil
}

func (h *BackupMessageHandler) OnConnectionEnd(connectionID uint32) error {
	h.logger.Info("Backup stream ended: %d", connectionID)

	// Here you can add backup-specific cleanup:
	// - Finalize backup for this stream
	// - Update statistics
	// - etc.

	return nil
}
