package main

import (
	"context"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type restoreServer struct {
	store  storage.BackupStore
	logger *slog.Logger
}

func NewRestoreServer(ctx context.Context, logger *slog.Logger, storagePath string) (*restoreServer, error) {
	store, err := wfs.NewReadOnly(storagePath)
	if err != nil {
		return nil, err
	}
	return &restoreServer{
		store:  store,
		logger: logger,
	}, nil
}
