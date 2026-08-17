package main

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestRunWorkerPool_ProcessesAllItems(t *testing.T) {
	in := make(chan int)
	go func() {
		for i := 0; i < 5; i++ {
			in <- i
		}
		close(in)
	}()

	out := runWorkerPool(context.Background(), 3, in, func(_ context.Context, item int) int {
		return item * 2
	})

	var got []int
	for r := range out {
		got = append(got, r)
	}
	sort.Ints(got)

	want := []int{0, 2, 4, 6, 8}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestRunWorkerPool_EmptyInputClosesCleanly(t *testing.T) {
	in := make(chan int)
	close(in)

	out := runWorkerPool(context.Background(), 3, in, func(_ context.Context, item int) int { return item })

	count := 0
	for range out {
		count++
	}
	if count != 0 {
		t.Fatalf("expected zero results from empty input, got %d", count)
	}
}

// TestRunWorkerPool_RunsWorkConcurrentlyAcrossAllStreams proves streams
// goroutines really run at once, not sequentially: every worker blocks on
// a shared gate until all `streams` of them have started, so the test
// cannot complete unless true concurrency of that width actually happened
// -- no arbitrary sleep, no flakiness window.
func TestRunWorkerPool_RunsWorkConcurrentlyAcrossAllStreams(t *testing.T) {
	const streams = 4
	in := make(chan int)
	go func() {
		for i := 0; i < streams; i++ {
			in <- i
		}
		close(in)
	}()

	var mu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	release := make(chan struct{})
	var releaseOnce sync.Once

	out := runWorkerPool(context.Background(), streams, in, func(_ context.Context, item int) int {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		reached := inFlight == streams
		mu.Unlock()
		if reached {
			releaseOnce.Do(func() { close(release) })
		}
		<-release
		return item
	})

	done := make(chan struct{})
	go func() {
		for range out {
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workers never reached full concurrency -- release was never closed")
	}

	if maxInFlight != streams {
		t.Fatalf("expected all %d workers to run concurrently, max in flight was %d", streams, maxInFlight)
	}
}

// TestRunWorkerPool_PassesContextToWorkFunction proves the pool's ctx
// reaches the work function unmodified -- the mechanism verify.go relies
// on for per-call cancellation (e.g. via a watchdog-derived context).
func TestRunWorkerPool_PassesContextToWorkFunction(t *testing.T) {
	type ctxKey string
	const key ctxKey = "k"
	ctx := context.WithValue(context.Background(), key, "v")

	in := make(chan int, 1)
	in <- 1
	close(in)

	var gotValue any
	out := runWorkerPool(ctx, 1, in, func(c context.Context, item int) int {
		gotValue = c.Value(key)
		return item
	})
	for range out {
	}

	if gotValue != "v" {
		t.Fatalf("expected the pool's ctx to be passed through to the work function, got %v", gotValue)
	}
}
