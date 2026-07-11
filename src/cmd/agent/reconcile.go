package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// backoffBase and backoffMax are vars (not consts) so tests can shrink them
// temporarily instead of waiting out real multi-minute backoff windows.
var (
	backoffBase = 30 * time.Second
	backoffMax  = 10 * time.Minute
)

// runner executes a policy's binary under ctx; production code uses
// realExec, tests substitute a fake so they don't actually invoke
// certclient/policyclient/brfs. ctx is honored via exec.CommandContext so
// a cancelled context (agent shutdown) terminates an in-flight process
// rather than orphaning it.
type runner func(ctx context.Context, binary string, args []string) error

// realExec runs binary with args under ctx. If binary is a bare name (no
// path separator), it is first resolved relative to this agent's own
// executable directory — the same "colocated sibling binary" layout used
// elsewhere in this repo (see deploy/control-plane/catalog's
// entrypoint.sh, which execs ./certclient from the same directory as its
// own binary, and common/config.ResolveBaseDir/ResolveVarDir, which
// resolve relative to os.Executable() the same way). This matters because
// Go's os/exec only resolves a bare name via $PATH, never via the working
// or executable directory, and nothing in that deployment layout puts
// certclient/brfs on $PATH. If no colocated file is found, binary is
// passed through unchanged so exec.Command falls back to its normal $PATH
// lookup — this keeps local/dev usage, where these binaries genuinely are
// on $PATH, working exactly as before.
func realExec(ctx context.Context, binary string, args []string) error {
	path := binary
	if !strings.Contains(binary, string(filepath.Separator)) {
		if exePath, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(exePath), binary)
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
			}
		}
	}
	return exec.CommandContext(ctx, path, args...).Run()
}

// isDue reports whether p should run now, given its last recorded state.
// A currently-failing policy (ConsecutiveFailures > 0) is due once
// NextRetryAt has passed, regardless of Due/Interval — decoupled from
// either, so a persistent failure doesn't get retried on every tick, and
// doesn't wait a full Interval/window cycle either. A healthy policy
// defers to p.Due if set, or else to the original
// Interval-since-last-success comparison.
func isDue(p Policy, s PolicyState, now time.Time) bool {
	if s.ConsecutiveFailures == 0 {
		if p.Due != nil {
			return p.Due(s, now)
		}
		if s.LastSuccessAt == nil {
			return true // never succeeded, run immediately
		}
		return !now.Before(s.LastSuccessAt.Add(p.Interval))
	}
	return s.NextRetryAt == nil || !now.Before(*s.NextRetryAt)
}

// backoff returns a jittered retry delay for the given number of
// consecutive failures. It must be called exactly once per failure and the
// result stored (see reconcileState.recordOutcome, PolicyState.NextRetryAt)
// rather than recomputed on every isDue check — recomputing it would
// redraw the jitter each time and make the due-ness threshold unstable.
func backoff(failures int) time.Duration {
	exp := min(max(failures-1, 0), 8)
	d := backoffBase * time.Duration(1<<exp)
	if d > backoffMax {
		d = backoffMax
	}
	// half jitter: never near-zero, still spreads retries across a fleet
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// reconcileState bundles the persisted Cache with the mutex guarding it.
// Before background policies existed, run() only ever touched the cache
// from its own single goroutine; a Policy with Background == true now
// updates it from its own goroutine too, concurrently with the main loop
// and with every other background goroutine, so every read/write goes
// through here.
type reconcileState struct {
	mu        sync.Mutex
	cachePath string
	cache     Cache
	logger    *slog.Logger
}

func (rs *reconcileState) get(id string) PolicyState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.cache[id]
}

// recordOutcome updates and immediately persists id's state given the
// outcome of one attempt at attemptTime. It's the single place both the
// synchronous and background-goroutine paths in run() update PolicyState,
// so their behavior — and what ends up on disk — can never diverge.
func (rs *reconcileState) recordOutcome(id string, attemptErr error, attemptTime time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	state := rs.cache[id]
	state.LastAttemptAt = &attemptTime
	if attemptErr == nil {
		state.LastSuccessAt = &attemptTime
		state.ConsecutiveFailures = 0
		state.NextRetryAt = nil
	} else {
		state.ConsecutiveFailures++
		retryAt := attemptTime.Add(backoff(state.ConsecutiveFailures))
		state.NextRetryAt = &retryAt
		rs.logger.Error("policy execution failed", "policy", id, "error", attemptErr)
	}
	rs.cache[id] = state

	if err := writeCache(rs.cachePath, rs.cache); err != nil {
		rs.logger.Error("failed to persist cache", "error", err)
	}
}

// run polls policiesFunc() every reconcileInterval, executing and
// recording the outcome of any policy isDue reports as due. A due policy
// with Background == false runs synchronously, exactly as before this
// type existed. A due policy with Background == true is launched in its
// own goroutine, bounded by maxConcurrentBackgroundJobs simultaneous
// in-flight jobs — a due background policy that can't acquire a slot this
// tick is simply left due and reconsidered next tick, never queued. run()
// returns once ctx is cancelled, after every in-flight background
// goroutine it launched has finished (each one's execute call receives
// the same ctx, so a context-respecting runner like realExec terminates
// rather than being orphaned).
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() []Policy, maxConcurrentBackgroundJobs int) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}
	rs := &reconcileState{cachePath: cachePath, cache: cache, logger: logger}

	sem := make(chan struct{}, maxConcurrentBackgroundJobs)
	var wg sync.WaitGroup

	for ctx.Err() == nil {
		now := time.Now()
		for _, p := range policiesFunc() {
			state := rs.get(p.ID)
			if !isDue(p, state, now) {
				continue
			}

			if p.Background {
				select {
				case sem <- struct{}{}:
				default:
					continue // no free slot this tick; stays due, retried next tick
				}
				wg.Add(1)
				go func(p Policy) {
					defer wg.Done()
					defer func() { <-sem }()
					attemptErr := execute(ctx, p.Binary, p.Args)
					rs.recordOutcome(p.ID, attemptErr, time.Now())
				}(p)
				continue
			}

			attemptErr := execute(ctx, p.Binary, p.Args)
			rs.recordOutcome(p.ID, attemptErr, now)
		}

		if !sleepOrDone(ctx, reconcileInterval) {
			break
		}
	}

	wg.Wait()
	return nil
}

// sleepOrDone sleeps for d, or returns false immediately if ctx is
// cancelled first.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
