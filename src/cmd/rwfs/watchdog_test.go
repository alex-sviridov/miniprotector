package main

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestWithStallWatchdog_CancelsAfterIdleWindowWithNoTouch(t *testing.T) {
	ctx, _, stop := withStallWatchdog(context.Background(), 30*time.Millisecond)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected the watchdog to cancel ctx after the idle window elapsed with no touch")
	}
}

func TestWithStallWatchdog_RepeatedTouchesPreventCancellation(t *testing.T) {
	ctx, touch, stop := withStallWatchdog(context.Background(), 40*time.Millisecond)
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
	_, _, stop := withStallWatchdog(context.Background(), time.Hour)
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
	ctx, _, stop := withStallWatchdog(context.Background(), time.Hour)
	stop()
	if ctx.Err() == nil {
		t.Fatal("expected stop() to cancel ctx -- a caller done with the stream must not leak a live context")
	}
}
