package main

import (
	"strings"
	"github.com/alex-sviridov/miniprotector/common/protocol"
)

func (h *BackupMessageHandler) OnMessage(connectionID uint32, message string) (string, error) {
	// Parse backup-specific message format
	s := *h.streams[connectionID]
	if strings.HasPrefix(message, "FILE:") {
		file, err := protocol.ParseFileMetadata(message)
		if err != nil {
			return "", err
		}
		s.logger.Debug("Received file metadata", "file", file)
	} else {
		s.logger.Debug("Received unknown message", "message", message)
	}

	return "", nil
}
