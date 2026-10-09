package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/storage"
)

// gcSettings is the scheduled store maintenance configuration (see
// docs/superpowers/specs/2026-10-05-store-cleanup-vacuum-design.md). A zero
// interval disables that loop.
type gcSettings struct {
	CleanupInterval      time.Duration // how often expired versions are deleted
	VacuumInterval       time.Duration // how often unreferenced data/chunks are reclaimed
	BatchSize            int           // rows per batch: bounds how long backups can be paused
	DryRun               bool          // cleanup only logs what it would delete
	IncompleteGrace      time.Duration // age after which incomplete file data is treated as abandoned
	DeletionLogRetention time.Duration // how long catalogsync has to consume deletions; 0 = never prune
}

func gcSettingsFrom(conf *config.Config) gcSettings {
	return gcSettings{
		CleanupInterval:      time.Duration(conf.StoreCleanupIntervalSec) * time.Second,
		VacuumInterval:       time.Duration(conf.StoreVacuumIntervalSec) * time.Second,
		BatchSize:            conf.StoreGCBatchSize,
		DryRun:               conf.StoreCleanupDryRun,
		IncompleteGrace:      time.Duration(conf.StoreIncompleteFileDataGraceSec) * time.Second,
		DeletionLogRetention: time.Duration(conf.StoreDeletionLogRetentionSec) * time.Second,
	}
}

// startStoreGC launches the cleanup and vacuum loops, each at its own
// interval, and returns immediately; both stop when ctx is cancelled. The
// first run of each is one interval from now -- startup vacuum already ran.
// Cleanup is the frequent loop (it only deletes version rows, cheap and
// bounded), vacuum the rare one (it reclaims the data those versions
// referenced and scans more).
func startStoreGC(ctx context.Context, logger *slog.Logger, store storage.BackupStore, s gcSettings) {
	logger.Info("store maintenance scheduled",
		"cleanup_interval", s.CleanupInterval, "vacuum_interval", s.VacuumInterval,
		"batch_size", s.BatchSize, "cleanup_dry_run", s.DryRun)
	go runEvery(ctx, s.CleanupInterval, func(ctx context.Context) { cleanupOnce(ctx, logger, store, s) })
	go runEvery(ctx, s.VacuumInterval, func(ctx context.Context) { vacuumOnce(ctx, logger, store, s) })
}

// runEvery calls fn every interval until ctx is cancelled. Runs never
// overlap: the next tick waits for the current fn to return. interval <= 0
// returns immediately (the loop is disabled).
func runEvery(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

// maintenanceJob is one cleanup or vacuum run, reported the way every other
// job in the system is: a job_id of "<kind>:<host>:<unix>" and one
// event=start line and one event=finish line (status success/failure, the
// run's statistics or its error on the finish line), which is all the
// Jobs view needs to list it and show its outcome. The host is part of the
// id because the Jobs view keys on job_id alone, and several stores' loops
// started together would otherwise collide on the same second. Anything
// more detailed (per-batch progress) is deliberately not logged.
type maintenanceJob struct {
	log   *slog.Logger
	name  string // "store cleanup" / "store vacuum"
	start time.Time
}

func startMaintenanceJob(logger *slog.Logger, kind string, attrs ...any) *maintenanceJob {
	now := time.Now()
	j := &maintenanceJob{
		log:   logger.With("job_id", fmt.Sprintf("%s:%s:%d", kind, common.GetHostname(), now.Unix())),
		name:  "store " + kind,
		start: now,
	}
	j.log.Info(j.name+" started", append([]any{"event", "start"}, attrs...)...)
	return j
}

// finish ends the job: status success with stats, or -- when err is non-nil --
// an Error-level line with status failure and the error text.
func (j *maintenanceJob) finish(err error, stats ...any) {
	attrs := append([]any{"event", "finish", "duration", time.Since(j.start).Round(time.Millisecond)}, stats...)
	if err != nil {
		j.log.Error(j.name+" failed", append(attrs, "status", "failure", "error", err.Error())...)
		return
	}
	j.log.Info(j.name+" completed", append(attrs, "status", "success")...)
}

// cleanupOnce deletes expired versions and prunes the deletion log. It is a
// job in the Jobs view only when it has something to report: hourly runs
// that find nothing expired would just bury the interesting jobs, so a cheap
// dry-run probe (one indexed count) decides first, and a no-op run is only a
// Debug line. A failure is reported and the next tick simply tries again --
// unlike startup vacuum, scheduled maintenance is never fatal to a running
// server.
func cleanupOnce(ctx context.Context, logger *slog.Logger, store storage.BackupStore, s gcSettings) {
	now := time.Now()
	probeErr := error(nil)
	if !s.DryRun {
		probe, err := store.CleanupExpired(ctx, now, s.BatchSize, true)
		switch {
		case err != nil:
			probeErr = err // let the real run below report it as a failed job
		case probe.VersionsExpired == 0:
			logger.Debug("store cleanup: nothing expired")
			pruneDeletionLog(ctx, logger, store, s, now)
			return
		}
	}

	job := startMaintenanceJob(logger, "cleanup", "dry_run", s.DryRun)
	var res *storage.CleanupResult
	err := probeErr
	if err == nil {
		res, err = store.CleanupExpired(ctx, now, s.BatchSize, s.DryRun)
	}
	var pruned int64
	if err == nil {
		pruned, err = pruneDeletionLogCounted(ctx, store, s, now)
	}
	if err != nil {
		var expired int64
		if res != nil {
			expired = res.VersionsExpired
		}
		job.finish(err, "dry_run", s.DryRun, "versions_expired", expired)
		return
	}
	job.finish(nil, "dry_run", s.DryRun, "versions_expired", res.VersionsExpired, "deletion_log_pruned", pruned)
}

// pruneDeletionLogCounted drops deletion-log rows older than the configured
// retention (never in a dry run, and never when retention is 0 = keep
// forever) and returns how many.
func pruneDeletionLogCounted(ctx context.Context, store storage.BackupStore, s gcSettings, now time.Time) (int64, error) {
	if s.DryRun || s.DeletionLogRetention <= 0 {
		return 0, nil
	}
	return store.PruneDeletionLog(ctx, now.Add(-s.DeletionLogRetention))
}

// pruneDeletionLog is the quiet form used by runs that aren't a job: a
// failure is a plain Error line, success silent.
func pruneDeletionLog(ctx context.Context, logger *slog.Logger, store storage.BackupStore, s gcSettings, now time.Time) {
	if _, err := pruneDeletionLogCounted(ctx, store, s, now); err != nil {
		logger.Error("deletion log prune failed", "error", err)
	}
}

// vacuumOnce runs one vacuum. Unlike cleanup it is always a job: it runs
// daily, and "it ran and reclaimed nothing" is itself worth seeing.
func vacuumOnce(ctx context.Context, logger *slog.Logger, store storage.BackupStore, s gcSettings) {
	job := startMaintenanceJob(logger, "vacuum")
	res, err := store.VacuumOnline(ctx, s.BatchSize, s.IncompleteGrace)
	if err != nil {
		job.finish(err)
		return
	}
	job.finish(nil,
		"incomplete_file_data_removed", res.IncompleteFileData,
		"orphaned_file_data_removed", res.OrphanedFileDataRemoved,
		"orphaned_chunk_links_removed", res.OrphanedChunkLinksRemoved,
		"orphaned_chunks_removed", res.OrphanedChunksRemoved,
		"segments_removed", res.SegmentsRemoved,
		"segments_compacted", res.SegmentsCompacted,
		"bytes_reclaimed", res.BytesReclaimed)
}
