package main

import (
	"context"
	"sync"
)

// runWorkerPool runs work concurrently across streams goroutines, each
// consuming items from in until it closes, and emits one result per item
// processed on the returned channel. The returned channel closes once
// every worker has drained in -- callers range over it to know when all
// work is done. streams must be >= 1.
//
// Replaces verify.go's previous hand-rolled channel/sync.WaitGroup
// plumbing (which mirrored brfs's filesstream.go almost exactly), made
// generic so a future phase-2 file-content restore can reuse it with a
// different work function -- see
// docs/superpowers/specs/2026-08-16-rwfs-reliability-performance-design.md.
func runWorkerPool[T, R any](ctx context.Context, streams int, in <-chan T, work func(context.Context, T) R) <-chan R {
	out := make(chan R, streams)
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range in {
				out <- work(ctx, item)
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
