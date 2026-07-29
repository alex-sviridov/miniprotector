package main

import (
	"context"
	"errors"
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

// resolveExecPath resolves binary to a colocated sibling of this agent's
// own executable when one exists there (bare name, no path separator),
// falling back to binary unchanged otherwise so exec.Command's normal
// $PATH lookup applies -- the same "colocated sibling binary" layout used
// elsewhere in this repo (see deploy/control-plane/catalog's
// entrypoint.sh, and common/config.ResolveBaseDir/ResolveVarDir). Shared by
// realExec (one-shot policy execs) and main.go's bwfs resolution for
// storage.go's storageManager (which resolves "bwfs" once at construction
// rather than per-spawn).
func resolveExecPath(binary string) string {
	if strings.Contains(binary, string(filepath.Separator)) {
		return binary
	}
	exePath, err := os.Executable()
	if err != nil {
		return binary
	}
	candidate := filepath.Join(filepath.Dir(exePath), binary)
	if _, err := os.Stat(candidate); err != nil {
		return binary
	}
	return candidate
}

// realExec runs binary with args under ctx, resolving binary via
// resolveExecPath first.
func realExec(ctx context.Context, binary string, args []string) error {
	return exec.CommandContext(ctx, resolveExecPath(binary), args...).Run()
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
	inFlight  map[string]bool
}

// tryMarkInFlight marks id as in-flight and returns true if it wasn't
// already -- a background policy that's still running from a previous
// tick must not be dispatched again, even though its persisted
// PolicyState won't reflect that until the in-flight run completes.
func (rs *reconcileState) tryMarkInFlight(id string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.inFlight == nil {
		rs.inFlight = make(map[string]bool)
	}
	if rs.inFlight[id] {
		return false
	}
	rs.inFlight[id] = true
	return true
}

func (rs *reconcileState) clearInFlight(id string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	delete(rs.inFlight, id)
}

func (rs *reconcileState) get(id string) PolicyState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.cache[id]
}

// isBackupPolicy reports whether p is a scheduled backup dispatch (see
// backup.go's backupTaskID) rather than one of agent's three static
// policies. Backup jobs' event/status lifecycle markers come solely from
// brfs (start) and bwfs (finish) -- see logExecStart/logExecCompletion.
func isBackupPolicy(p Policy) bool {
	return strings.HasPrefix(p.ID, "backup:")
}

// logExecStart logs that agent is about to dispatch p's exec. Called
// immediately before execute for both the synchronous and background
// dispatch paths in run(), so agent's own log always shows an exec
// starting even if it never finishes (e.g. agent is killed mid-exec).
// event=start is added for every policy except scheduled backups --
// brfs's own "Backup reader started" line is that job kind's sole
// event=start source, so this line staying untagged for backups is
// deliberate, not an oversight (see isBackupPolicy).
func logExecStart(logger *slog.Logger, p Policy) {
	if isBackupPolicy(p) {
		logger.Info("policy execution started", "policy", p.ID, "binary", p.Binary, "job_id", p.JobID)
		return
	}
	logger.Info("policy execution started", "policy", p.ID, "binary", p.Binary, "job_id", p.JobID, "event", "start")
}

// logExecCompletion logs the outcome of one exec attempt at Info level, on
// both success and failure -- unlike recordOutcome's existing Error-level
// line (which only fires on failure, for operators grepping specifically
// for errors), this gives agent's own log a complete start/end timeline
// for every dispatched exec. exit_code is included only when attemptErr is
// a real *exec.ExitError -- fabricating one for any other error type would
// be misleading. event/status are omitted for scheduled backups, same
// reasoning as logExecStart.
func logExecCompletion(logger *slog.Logger, p Policy, attemptErr error, duration time.Duration) {
	backup := isBackupPolicy(p)
	status := "success"
	if attemptErr != nil {
		status = "failure"
	}

	if attemptErr == nil {
		if backup {
			logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration)
			return
		}
		logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "event", "finish", "status", status)
		return
	}

	var exitErr *exec.ExitError
	if errors.As(attemptErr, &exitErr) {
		if backup {
			logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "exit_code", exitErr.ExitCode(), "error", attemptErr)
			return
		}
		logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "exit_code", exitErr.ExitCode(), "error", attemptErr, "event", "finish", "status", status)
		return
	}
	if backup {
		logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "error", attemptErr)
		return
	}
	logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "error", attemptErr, "event", "finish", "status", status)
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
		state.LastError = ""
	} else {
		state.ConsecutiveFailures++
		retryAt := attemptTime.Add(backoff(state.ConsecutiveFailures))
		state.NextRetryAt = &retryAt
		state.LastError = attemptErr.Error()
		rs.logger.Error("policy execution failed", "policy", id, "error", attemptErr)
	}
	rs.cache[id] = state

	if err := writeCache(rs.cachePath, rs.cache); err != nil {
		rs.logger.Error("failed to persist cache", "error", err)
	}
}

// prune removes any cache entry whose ID isn't present in currentIDs --
// called once per reconcile tick, only when that tick's policy list came
// from a confirmed-good read (run passes ok from policiesFunc), so a
// transient unreadable policies-cache.json can never be mistaken for
// "every backup task was removed" and wipe live backoff/RPO history for
// tasks that are still current.
func (rs *reconcileState) prune(currentIDs map[string]struct{}) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	changed := false
	for id := range rs.cache {
		if _, ok := currentIDs[id]; !ok {
			delete(rs.cache, id)
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := writeCache(rs.cachePath, rs.cache); err != nil {
		rs.logger.Error("failed to persist cache after prune", "error", err)
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
// storageTasksFunc/storageMgr add ensure-running bwfs supervision alongside
// the due/execute policy loop below -- either nil disables it entirely,
// preserving prior behavior exactly (see storage.go).
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int, onSuccess func(policyID string), storageTasksFunc func() ([]storageTask, bool), storageMgr *storageManager) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}
	rs := &reconcileState{cachePath: cachePath, cache: cache, logger: logger}

	sem := make(chan struct{}, maxConcurrentBackgroundJobs)
	var wg sync.WaitGroup

	for ctx.Err() == nil {
		now := time.Now()
		policyList, ok := policiesFunc()

		var storageTaskList []storageTask
		storageOk := true
		if storageTasksFunc != nil {
			storageTaskList, storageOk = storageTasksFunc()
		}

		if ok && storageOk {
			currentIDs := make(map[string]struct{}, len(policyList)+len(storageTaskList))
			for _, p := range policyList {
				currentIDs[p.ID] = struct{}{}
			}
			for _, t := range storageTaskList {
				currentIDs[t.ID] = struct{}{}
			}
			rs.prune(currentIDs)
		}

		if storageMgr != nil && storageOk {
			storageMgr.reconcile(ctx, rs, storageTaskList)
		}

		for _, p := range policyList {
			state := rs.get(p.ID)
			if !isDue(p, state, now) {
				continue
			}

			if p.Background {
				if !rs.tryMarkInFlight(p.ID) {
					continue // still running from a previous tick; stays due, skip this tick
				}
				select {
				case sem <- struct{}{}:
				default:
					rs.clearInFlight(p.ID)
					continue // no free slot this tick; stays due, retried next tick
				}
				wg.Add(1)
				go func(p Policy) {
					defer wg.Done()
					defer func() { <-sem }()
					defer rs.clearInFlight(p.ID)
					logExecStart(rs.logger, p)
					start := time.Now()
					attemptErr := execute(ctx, p.Binary, p.Args)
					logExecCompletion(rs.logger, p, attemptErr, time.Since(start))
					rs.recordOutcome(p.ID, attemptErr, time.Now())
					if attemptErr == nil && onSuccess != nil {
						onSuccess(p.ID)
					}
				}(p)
				continue
			}

			logExecStart(rs.logger, p)
			start := time.Now()
			attemptErr := execute(ctx, p.Binary, p.Args)
			logExecCompletion(rs.logger, p, attemptErr, time.Since(start))
			rs.recordOutcome(p.ID, attemptErr, now)
			if attemptErr == nil && onSuccess != nil {
				onSuccess(p.ID)
			}
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
