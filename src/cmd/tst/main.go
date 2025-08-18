package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type ProcessResult struct {
	StreamID int
	Filename file
	Success  bool
	Error    error
}

type file string

func processList(ctx context.Context, list []file, streams int) <-chan ProcessResult {
	resultChan := make(chan ProcessResult)

	if streams <= 0 || len(list) == 0 {
		close(resultChan)
		return resultChan
	}

	workChan := make(chan file)

	var wg sync.WaitGroup

	for i := 0; i < streams; i++ {
		fmt.Printf("Stream started %d\n", i)
		wg.Add(1)
		go stream(ctx, i, workChan, resultChan, &wg)
	}

	go func() {
		// 1. Send all work
		for _, f := range list {
			select {
			case workChan <- f:
			case <-ctx.Done():
				fmt.Printf("Emergency shutdown...\n")
				close(workChan)
				wg.Wait()
				close(resultChan)
				return
			}
		}
		// 2. Signal no more work
		close(workChan)
		// 3. Wait for workers to finish
		wg.Wait()
		// 4. Signal no more results
		close(resultChan)
	}()
	return resultChan
}

func stream(ctx context.Context, streamID int, workChan <-chan file, resultChan chan<- ProcessResult, wg *sync.WaitGroup) {
	defer wg.Done()
	for f := range workChan {
		fmt.Printf("Worker %d processing: %s\n", streamID, f)
		err := processOneFile(ctx, streamID, f)
		resultChan <- ProcessResult{
			StreamID: streamID,
			Filename: f,
			Success:  err == nil,
			Error:    err,
		}
	}

}

func processOneFile(ctx context.Context, streamID int, file file) error {
	delay := time.Duration(rand.Intn(3)+1) * time.Second
	select {
	case <-time.After(delay):
		return nil // Work completed successfully
	case <-ctx.Done():
		fmt.Printf("Worker %d cancelled: %s\n", streamID, file)
		return fmt.Errorf("processing cancelled: %w", ctx.Err())
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	list := []file{"one", "two", "three", "four", "five", "six", "seven", "eight"}

	resultChan := processList(ctx, list, 3)
	for result := range resultChan {
		fmt.Printf("stream %d: result %t: %s\n", result.StreamID, result.Success, result.Filename)
	}
}
