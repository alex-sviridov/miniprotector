package wfs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

type Writer struct {
	config *config.Config
	logger *slog.Logger
	db     *fileDB
}

func NewWriter(ctx context.Context, storagePath string) (*Writer, error) {
	// storagePath should be a directory or nonexisting
	if _, err := os.Stat(storagePath); os.IsNotExist(err) {
		if err := os.MkdirAll(storagePath, 0700); err != nil {
			return nil, fmt.Errorf("failed to create storage directory %s: %w", storagePath, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to check storage directory %s: %w", storagePath, err)
	}
	dbPath := filepath.Join(storagePath, "wfs.db")
	db, err := newDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	return &Writer{
		config: config.GetConfigFromContext(ctx),
		logger: logging.GetLoggerFromContext(ctx),
		db:     db,
	}, nil
}

func (w *Writer) Close() error {
	return w.db.close()
}

func (w *Writer) FileExists(fileInfo *files.FileInfo) (bool, error) {
	return w.db.fileExists(fileInfo)
}

func (w *Writer) AddFile(fileInfo *files.FileInfo, checksum string) error {
	return w.db.addFile(fileInfo, checksum)
}
