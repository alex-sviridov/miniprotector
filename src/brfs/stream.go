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
		message, err := protocol.Encode(&file)
		if err != nil {
			return fmt.Errorf("error encoding file metadata: %w", err)
		}
		response, err := b.stream.SendMessage(message)
		if err != nil {
			return err
		}
		if response != "FILE_OK" {
			return fmt.Errorf("unexpected response: %s", response)
		}
	}

	b.logger.Info("Stream finished successfully", "files_count", len(b.files))
	return nil
}
