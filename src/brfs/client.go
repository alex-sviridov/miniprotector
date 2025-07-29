package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/network"
)

type BackupProcessor struct {
	streamId int
	stream   *network.Stream
	files    []files.FileInfo
	logger   *common.Logger
}

func NewBackupProcessor(config *common.Config, ctx context.Context, client *network.Client, filelist []files.FileInfo) (p *BackupProcessor, err error) {
	stream, err := network.NewStream(config, ctx, client)
	if err != nil {
		return nil, err
	}
	streamId := ctx.Value("streamId").(int)

	p = &BackupProcessor{
		streamId: streamId,
		stream:   stream,
		files:    filelist,
		logger:   ctx.Value("logger").(*common.Logger),
	}
	return p, nil
}

// processStreams creates one connection per stream
func processStreams(config *common.Config, ctx context.Context, client *network.Client, streams [][]files.FileInfo) error {
	var wg sync.WaitGroup
	errors := make(chan error, len(streams))

	// Process each stream with its own persistent connection
	for i, filelist := range streams {
		wg.Add(1)
		go func(streamIndex int, filelist []files.FileInfo) {
			defer wg.Done()

			// Create a stream context
			streamCtx := context.WithValue(ctx, "streamId", streamIndex)
			streamLogger, err := common.NewLogger(config, streamCtx)
			if err == nil {
				streamCtx = context.WithValue(streamCtx, "logger", streamLogger)
			}
			// Create stream and get ack
			processor, err := NewBackupProcessor(config, streamCtx, client, filelist)
			if err != nil {
				errors <- fmt.Errorf("stream %d start failed: %v", streamIndex, err)
				return
			}

			// Process stream
			if err := processor.Process(config, streamCtx); err != nil {
				errors <- fmt.Errorf("stream %d processing failed: %v", streamIndex, err)
			}
		}(i, filelist)
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
