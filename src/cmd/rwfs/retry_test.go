package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type retryTestResult struct {
	ok  bool
	err error
}

func TestWithRetry_RecoversAfterRetryableFailures(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	attempts := 0
	result := withRetry(context.Background(), logger, 3,
		func(ctx context.Context) retryTestResult {
			attempts++
			if attempts < 3 {
				return retryTestResult{err: errors.New("transient")}
			}
			return retryTestResult{ok: true}
		},
		func(r retryTestResult) bool { return !r.ok },
		func(r retryTestResult) string { return r.err.Error() },
	)
	require.True(t, result.ok)
	assert.Equal(t, 3, attempts)
}

func TestWithRetry_StopsAtMaxRetriesAndReturnsFinalResult(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	attempts := 0
	result := withRetry(context.Background(), logger, 3,
		func(ctx context.Context) retryTestResult {
			attempts++
			return retryTestResult{err: errors.New("always fails")}
		},
		func(r retryTestResult) bool { return !r.ok },
		func(r retryTestResult) string { return r.err.Error() },
	)
	assert.False(t, result.ok)
	assert.Equal(t, 3, attempts, "must attempt exactly maxRetries times, no more")
}

func TestWithRetry_NonRetryableStopsImmediately(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	attempts := 0
	start := time.Now()
	result := withRetry(context.Background(), logger, 3,
		func(ctx context.Context) retryTestResult {
			attempts++
			return retryTestResult{err: errors.New("terminal")}
		},
		func(r retryTestResult) bool { return false }, // never retryable
		func(r retryTestResult) string { return r.err.Error() },
	)
	elapsed := time.Since(start)
	assert.False(t, result.ok)
	assert.Equal(t, 1, attempts, "a non-retryable failure must not be retried")
	assert.Less(t, elapsed, 100*time.Millisecond, "no backoff wait should occur for a non-retryable failure")
}

func TestWithRetry_BacksOffBetweenAttempts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := time.Now()
	withRetry(context.Background(), logger, 3,
		func(ctx context.Context) retryTestResult { return retryTestResult{err: errors.New("fail")} },
		func(r retryTestResult) bool { return true },
		func(r retryTestResult) string { return r.err.Error() },
	)
	elapsed := time.Since(start)
	// 3 attempts -> 2 waits; backoff starts at 500ms and doubles, so the
	// floor is 500ms + 1s = 1.5s (well under the 5s cap). Assert a
	// slightly relaxed floor to absorb scheduler jitter -- mirrors
	// verify_test.go's TestVerifyFileWithRetry_BacksOffBetweenAttempts.
	if elapsed < 1300*time.Millisecond {
		t.Fatalf("expected at least ~1.5s of backoff across 2 waits, took %v", elapsed)
	}
}

func TestWithRetry_RespectsContextCancellationDuringBackoffWait(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	result := withRetry(ctx, logger, 5,
		func(ctx context.Context) retryTestResult {
			attempts++
			return retryTestResult{err: errors.New("fail")}
		},
		func(r retryTestResult) bool { return true },
		func(r retryTestResult) string { return r.err.Error() },
	)
	elapsed := time.Since(start)
	assert.False(t, result.ok)
	assert.Less(t, elapsed, 500*time.Millisecond, "cancellation during a backoff wait must return promptly, not wait out the full backoff")
	assert.Equal(t, 1, attempts, "cancellation during the first backoff wait must prevent a second attempt")
}
