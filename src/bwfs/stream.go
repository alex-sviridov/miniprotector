package main

import (
	"strings"
)

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
			s.logger.Debug("procrssing file", "filename", filename)
		}

		s.logger.Debug("Received batch", "files_count", len(filenames))

		// Send acknowledgment back to client
		return "BATCH_OK", nil

	} else {
		s.logger.Debug("Received unknown message", "message", message)
	}

	return "", nil
}
