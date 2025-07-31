package main

import (
	"context"
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/protocol"
)

func (b *BackupProcessor) Process(config *common.Config, ctx context.Context) error {
	defer b.stream.CloseStream()

	b.logger.Info("Stream starts file processing", "files_count", len(b.files))

	//batchSize := config.ClientHashQueryBatchSize

	//iterate one file at a time
	for _, file := range b.files {
		fileNeeded, err := protocol.SendFileMetadata(b.stream, &file)
		if err != nil {
			return fmt.Errorf("error sending file metadata: %w", err)
		}
		if !fileNeeded {
			continue
		}
	}

	b.logger.Info("Stream finished successfully", "files_count", len(b.files))
	return nil
}
