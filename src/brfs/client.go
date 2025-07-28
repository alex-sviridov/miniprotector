package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/network"
)

// BackupStream represents one stream of files with persistent connection
type BackupStream struct {
	id         int
	files      []FileInfo
	connection *network.Connection
	logger     *common.Logger
}

func NewBackupStream(id int, files []FileInfo, connection *network.Connection, logger *common.Logger) *BackupStream {
	return &BackupStream{
		id:         id,
		files:      files,
		connection: connection,
		logger:     logger,
	}
}

func (s *BackupStream) Process(ctx context.Context) error {
	defer s.connection.Close()

	s.logger.Info("Stream %d processing %d files via connection %d",
		s.id, len(s.files), s.connection.GetID())

	// Send all files through this persistent connection
	for _, file := range s.files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Here you can add file-specific logic:
		// - Read file metadata
		// - Compress file
		// - Calculate checksum
		// - etc.

		// Send file message
		message := fmt.Sprintf("FILE:%s", file.Name)
		if err := s.connection.SendMessage(message); err != nil {
			return fmt.Errorf("failed to send file %s: %v", file.Name, err)
		}

		s.logger.Debug("Stream %d sent file: %s", s.id, file.Name)
	}

	s.logger.Info("Stream %d completed successfully (%d files)", s.id, len(s.files))
	return nil
}

// processStreamsWithPersistentConnections creates one connection per stream
func processStreamsWithPersistentConnections(ctx context.Context, client *network.Client, streams [][]FileInfo, logger *common.Logger) error {
	var wg sync.WaitGroup
	errors := make(chan error, len(streams))

	// Process each stream with its own persistent connection
	for i, files := range streams {
		wg.Add(1)
		go func(streamIndex int, fileList []FileInfo) {
			defer wg.Done()

			// Create persistent connection for this stream
			connection, err := client.CreateConnection(ctx)
			if err != nil {
				errors <- fmt.Errorf("stream %d connection failed: %v", streamIndex, err)
				return
			}

			// Create and process stream
			stream := NewBackupStream(streamIndex, fileList, connection, logger)
			if err := stream.Process(ctx); err != nil {
				errors <- fmt.Errorf("stream %d processing failed: %v", streamIndex, err)
			}
		}(i, files)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		if err != nil {
			return err
		}
	}

	return nil
}
