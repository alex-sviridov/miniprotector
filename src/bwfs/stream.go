package main

import (
	"strings"

	"github.com/alex-sviridov/miniprotector/common/protocol"
)

func (h *BackupMessageHandler) OnMessage(connectionID uint32, message string) (string, error) {
	// Parse backup-specific message format
	s := *h.streams[connectionID]
	if strings.HasPrefix(message, "FILE:") {
		file, err := protocol.DecodeFileInfo(message)
		if err != nil {
			return "", err
		}
		s.logger.Debug("Received file metadata", "fileinfo", file.Print())
		// respond FILE_OK
		return "FILE_OK", nil
	} else {
		s.logger.Debug("Received unknown message", "message", message)
	}

	return "", nil
}
