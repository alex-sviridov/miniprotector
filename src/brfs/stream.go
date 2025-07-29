package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/alex-sviridov/miniprotector/common"
)

func (b *BackupProcessor) Process(config *common.Config, ctx context.Context) error {
	defer b.stream.CloseStream()

	b.logger.Info("Stream %d processing %d files", b.streamId, len(b.files))

	batchSize := config.ClientCRSQueryBatchSize

	// Send all files through this persistent connection
	for i := 0; i < len(b.files); i += batchSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Calculate batch end (don't go beyond slice length)
		end := i + batchSize
		if end > len(b.files) {
			end = len(b.files)
		}

		// Get batch of files
		batch := b.files[i:end]
		// Make batch
		filenames := make([]string, len(batch))
		for j, file := range batch {
			filenames[j] = file.Name
		}

		// Create batch message (comma-separated)
		message := fmt.Sprintf("BATCH:%s", strings.Join(filenames, ","))

		response, err := b.stream.SendMessage(message)

		if err != nil {
			return fmt.Errorf("failed to receive ACK for batch: %v", err)
		}

		if response != "BATCH_OK" {
			return fmt.Errorf("unexpected response for batch: %s", response)
		}

		b.logger.Debug("Stream %d sent batch: %v (ACK received)", b.streamId, filenames)
	}

	b.logger.Info("Stream %d completed successfully (%d files)", b.streamId, len(b.files))
	return nil
}
