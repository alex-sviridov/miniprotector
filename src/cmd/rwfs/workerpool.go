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
// ctx is passed through to work and is not otherwise consulted: cancelling it
// does not stop the pool from draining in, it only lets each in-flight work
// call bail out early. Closing in is what ends the pool, so a caller that
// wants cancellation to actually stop the work must have whatever produces in
// react to ctx as well.
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
