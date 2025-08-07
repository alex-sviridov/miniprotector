package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/alex-sviridov/miniprotector/common/config"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type BackupServer struct {
	pb.UnimplementedBackupServiceServer
	storagePath string
	config      *config.Config
	logger      *slog.Logger
}

func NewBackupServer(conf *config.Config, logger *slog.Logger, storagePath string) (*BackupServer, error) {

	// Check if folder is available, create if needed
	if _, err := os.Stat(storagePath); os.IsNotExist(err) {
		logger.Info("Storage directory doesn't exist, creating", "path", storagePath)
		if err := os.MkdirAll(storagePath, 0700); err != nil {
			return nil, fmt.Errorf("failed to create storage directory %s: %w", storagePath, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to check storage directory %s: %w", storagePath, err)
	}

	return &BackupServer{
		logger:      logger,
		config:      conf,
		storagePath: storagePath,
	}, nil
}
