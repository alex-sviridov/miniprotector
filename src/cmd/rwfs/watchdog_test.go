package main

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestWithStallWatchdog_CancelsAfterIdleWindowWithNoTouch(t *testing.T) {
	ctx, _, _, _, stop := withStallWatchdog(context.Background(), 30*time.Millisecond)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected the watchdog to cancel ctx after the idle window elapsed with no touch")
	}
}

func TestWithStallWatchdog_RepeatedTouchesPreventCancellation(t *testing.T) {
	ctx, touch, _, _, stop := withStallWatchdog(context.Background(), 40*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		touch()
		time.Sleep(10 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatalf("expected ctx to remain live under repeated touches, got %v", ctx.Err())
	}
}

func TestWithStallWatchdog_StopReleasesGoroutineWithoutLeaking(t *testing.T) {
	before := runtime.NumGoroutine()
	_, _, _, _, stop := withStallWatchdog(context.Background(), time.Hour)
	stop()

	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Fatalf("expected watchdog goroutine to exit after stop(), goroutine count still %d (baseline %d)", got, before)
	}
}

func TestWithStallWatchdog_StopAlsoCancelsCtx(t *testing.T) {
	ctx, _, _, _, stop := withStallWatchdog(context.Background(), time.Hour)
	stop()
	if ctx.Err() == nil {
		t.Fatal("expected stop() to cancel ctx -- a caller done with the stream must not leak a live context")
	}
}

// The core of the finding this pair exists for: a blocking hand-off that
// outlasts the idle window is consumer backpressure, not a stalled stream,
// and must not cancel ctx -- not even when the caller is parked inside the
// hand-off the whole time and only touches afterwards.
func TestWithStallWatchdog_PauseSuspendsTheIdleTimer(t *testing.T) {
	ctx, touch, pause, resume, stop := withStallWatchdog(context.Background(), 30*time.Millisecond)
	defer stop()

	pause()
	time.Sleep(200 * time.Millisecond) // 6+ idle windows spent "handing off"
	if ctx.Err() != nil {
		t.Fatalf("expected a paused watchdog to leave ctx live, got %v", ctx.Err())
	}
	resume()
	touch()
	if ctx.Err() != nil {
		t.Fatalf("expected ctx to still be live right after resume, got %v", ctx.Err())
	}
}

func TestWithStallWatchdog_ResumeRearmsTheIdleTimer(t *testing.T) {
	ctx, _, pause, resume, stop := withStallWatchdog(context.Background(), 30*time.Millisecond)
	defer stop()

	pause()
	time.Sleep(100 * time.Millisecond)
	resume()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("expected the watchdog to cancel ctx after resume re-armed the idle timer and no touch followed")
	}
}

// Pausing twice or resuming without a pause must be a no-op, so a caller
// never has to track the state itself -- and in particular a stray resume
// must not leave the timer double-armed or the goroutine wedged.
func TestWithStallWatchdog_RedundantPauseAndResumeAreNoOps(t *testing.T) {
	ctx, touch, pause, resume, stop := withStallWatchdog(context.Background(), 50*time.Millisecond)
	defer stop()

	resume() // never paused
	pause()
	pause() // already paused
	time.Sleep(150 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("expected a (doubly) paused watchdog to leave ctx live, got %v", ctx.Err())
	}
	resume()
	resume() // already running
	touch()
	if ctx.Err() != nil {
		t.Fatalf("expected ctx to still be live after redundant resumes, got %v", ctx.Err())
	}
}

// pause/resume must not deadlock once the watchdog is gone -- a producer can
// legitimately reach its hand-off after the stream was already cancelled.
func TestWithStallWatchdog_PauseAndResumeReturnAfterStopOrCancel(t *testing.T) {
	_, _, pause, resume, stop := withStallWatchdog(context.Background(), time.Hour)
	stop()

	done := make(chan struct{})
	go func() {
		pause()
		resume()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pause/resume must return once the watchdog has been stopped, not block forever")
	}

	ctx, _, pause2, resume2, stop2 := withStallWatchdog(context.Background(), 20*time.Millisecond)
	defer stop2()
	<-ctx.Done() // let the idle timer fire and the goroutine exit

	done2 := make(chan struct{})
	go func() {
		pause2()
		resume2()
		close(done2)
	}()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("pause/resume must return once ctx is already cancelled, not block forever")
	}
}

func TestWithStallWatchdog_StopIsIdempotent(t *testing.T) {
	_, _, _, _, stop := withStallWatchdog(context.Background(), time.Hour)
	stop()
	stop() // must not panic on a double close
}
