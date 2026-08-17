package main

import (
	"context"
	"sync"
	"time"
)

// streamIdleTimeout is the idle window withStallWatchdog uses for
// ResolveRestoreFiles, RestoreFile, and ListFiles streaming calls: a
// stream that's actively producing messages never hits this, no matter
// how long the overall call runs; only an actual stall (no message
// received for this long) does. A var rather than a const purely so
// tests can shrink it instead of waiting out the real 60s -- not a
// user-facing setting; there is no flag for it. See
// docs/superpowers/specs/2026-08-16-rwfs-reliability-performance-design.md.
var streamIdleTimeout = 60 * time.Second

// withStallWatchdog returns a context derived from parent that is
// cancelled if touch isn't called within idle of the last call (or of
// ctx's creation, for the first call) -- an idle timeout, not a
// total-duration timeout, so a stream that's genuinely still producing
// output is never penalized for running long.
//
// pause and resume bracket a blocking hand-off that is *not* stream
// activity -- handing a just-received row to a consumer that may legitimately
// take a while to accept it (a busy worker pool, a large file being verified,
// a retry-with-backoff cycle). While paused the idle timer is suspended, so
// consumer backpressure can never be mistaken for a stalled server; resume
// restarts it at the full idle window. Without this bracket a single blocking
// send that outlasts idle would cancel ctx *while the caller is parked in the
// send*, and the touch that follows once the send unblocks would come too
// late to help. Pausing while already paused, or resuming while not paused,
// is a no-op, so callers don't have to track the state themselves; both
// return immediately once stop has been called or ctx is already done. A
// caller that pauses must eventually resume or stop -- a permanently paused
// watchdog protects nothing (this is why the two streaming producers only
// ever pause around a send whose consumer is contractually required to drain).
//
// stop releases the watchdog's goroutine and should be called when the caller
// is done with ctx, whether the underlying call succeeded, failed, or was
// itself cancelled by touch never being called again; it also cancels ctx, so
// a caller doesn't need a separate cancel of its own. It is idempotent --
// calling it more than once, from any goroutine, is safe.
func withStallWatchdog(parent context.Context, idle time.Duration) (ctx context.Context, touch func(), pause func(), resume func(), stop func()) {
	ctx, cancel := context.WithCancel(parent)
	timer := time.NewTimer(idle)
	touchCh := make(chan struct{}, 1)
	// pauseCh is unbuffered, unlike touchCh: a dropped touch is harmless
	// (the pending one already resets the timer) but a dropped pause would
	// leave the timer armed across exactly the blocking hand-off it was
	// meant to cover, which is the bug this whole mechanism exists to avoid.
	pauseCh := make(chan bool)
	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		defer timer.Stop()
		paused := false
		for {
			// While paused, timer.C is masked out of the select entirely, so
			// even a fire that raced the pause can't cancel ctx. Reset (on
			// resume) then re-arms it for a full idle window with no stale
			// value pending -- guaranteed since Go 1.23's timer semantics,
			// and this module is go 1.26.
			var expired <-chan time.Time
			if !paused {
				expired = timer.C
			}
			select {
			case <-expired:
				cancel()
				return
			case wantPaused := <-pauseCh:
				if wantPaused == paused {
					continue // pause while paused / resume while running: no-op
				}
				paused = wantPaused
				if paused {
					timer.Stop()
				} else {
					timer.Reset(idle)
				}
			case <-touchCh:
				if paused {
					continue // the timer is stopped; resume re-arms it
				}
				timer.Stop()
				timer.Reset(idle)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	touch = func() {
		select {
		case touchCh <- struct{}{}:
		default: // a touch is already pending; the pending one already resets the timer
		}
	}
	setPaused := func(wantPaused bool) {
		select {
		case pauseCh <- wantPaused:
		case <-done: // stopped: there is nothing left to pause or resume
		case <-ctx.Done(): // already cancelled: same
		}
	}
	pause = func() { setPaused(true) }
	resume = func() { setPaused(false) }
	stop = func() {
		stopOnce.Do(func() {
			close(done)
			cancel()
		})
	}
	return ctx, touch, pause, resume, stop
}
