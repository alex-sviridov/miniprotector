package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// backoffBase and backoffMax are vars (not consts) so tests can shrink them
// temporarily instead of waiting out real multi-minute backoff windows.
var (
	backoffBase = 30 * time.Second
	backoffMax  = 10 * time.Minute
)

// runner executes a policy's binary; production code uses realExec, tests
// substitute a fake so they don't actually invoke certclient.
type runner func(binary string, args []string) error

// realExec runs binary with args. If binary is a bare name (no path
// separator), it is first resolved relative to this agent's own executable
// directory — the same "colocated sibling binary" layout used elsewhere in
// this repo (see deploy/control-plane/catalog's entrypoint.sh, which execs
// ./certclient from the same directory as its own binary, and
// common/config.ResolveBaseDir/ResolveVarDir, which resolve relative to
// os.Executable() the same way). This matters because Go's os/exec only
// resolves a bare name via $PATH, never via the working or executable
// directory, and nothing in that deployment layout puts certclient on
// $PATH. If no colocated file is found, binary is passed through unchanged
// so exec.Command falls back to its normal $PATH lookup — this keeps
// local/dev usage, where certclient genuinely is on $PATH, working exactly
// as before.
func realExec(binary string, args []string) error {
	path := binary
	if !strings.Contains(binary, string(filepath.Separator)) {
		if exePath, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(exePath), binary)
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
			}
		}
	}
	return exec.Command(path, args...).Run()
}

// isDue reports whether p should run now, given its last recorded state.
// A healthy policy (no consecutive failures) is due strictly on its own
// Interval. A failing policy is due once NextRetryAt has passed instead —
// decoupled from Interval, so a persistent failure doesn't get retried on
// every tick, and doesn't wait a full Interval either.
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
// result stored (see run, PolicyState.NextRetryAt) rather than recomputed
// on every isDue check — recomputing it would redraw the jitter each time
// and make the due-ness threshold unstable.
func backoff(failures int) time.Duration {
	exp := min(max(failures-1, 0), 8)
	d := backoffBase * time.Duration(1<<exp)
	if d > backoffMax {
		d = backoffMax
	}
	// half jitter: never near-zero, still spreads retries across a fleet
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// run polls the embedded policy list every reconcileInterval, executing
// and recording the outcome of any policy isDue reports as due. It runs
// until ctx is cancelled, at which point it returns nil.
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policies []Policy) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		now := time.Now()
		changed := false
		for _, p := range policies {
			state := cache[p.ID]
			if !isDue(p, state, now) {
				continue
			}

			attemptErr := execute(p.Binary, p.Args)
			attemptTime := now
			state.LastAttemptAt = &attemptTime

			if attemptErr == nil {
				successTime := now
				state.LastSuccessAt = &successTime
				state.ConsecutiveFailures = 0
				state.NextRetryAt = nil
			} else {
				state.ConsecutiveFailures++
				retryAt := now.Add(backoff(state.ConsecutiveFailures))
				state.NextRetryAt = &retryAt
				logger.Error("policy execution failed", "policy", p.ID, "error", attemptErr)
			}

			cache[p.ID] = state
			changed = true
		}

		if changed {
			if err := writeCache(cachePath, cache); err != nil {
				logger.Error("failed to persist cache", "error", err)
			}
		}

		if !sleepOrDone(ctx, reconcileInterval) {
			return nil
		}
	}
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
