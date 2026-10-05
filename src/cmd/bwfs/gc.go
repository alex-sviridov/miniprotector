package main

import (
	"context"
	"log/slog"
	"time"

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

// cleanupOnce deletes expired versions and prunes the deletion log. A failure
// is logged and the next tick simply tries again -- unlike startup vacuum,
// scheduled maintenance is never fatal to a running server.
func cleanupOnce(ctx context.Context, logger *slog.Logger, store storage.BackupStore, s gcSettings) {
	start := time.Now()
	res, err := store.CleanupExpired(ctx, start, s.BatchSize, s.DryRun)
	if err != nil {
		logger.Error("store cleanup failed", "event", "store_cleanup", "error", err, "dry_run", s.DryRun, "duration", time.Since(start))
		return
	}
	var pruned int64
	if !s.DryRun && s.DeletionLogRetention > 0 {
		pruned, err = store.PruneDeletionLog(ctx, start.Add(-s.DeletionLogRetention))
		if err != nil {
			logger.Error("deletion log prune failed", "event", "store_cleanup", "error", err)
		}
	}
	logger.Info("store cleanup completed",
		"event", "store_cleanup", "dry_run", s.DryRun,
		"versions_expired", res.VersionsExpired, "deletion_log_pruned", pruned,
		"duration", time.Since(start))
}

func vacuumOnce(ctx context.Context, logger *slog.Logger, store storage.BackupStore, s gcSettings) {
	start := time.Now()
	res, err := store.VacuumOnline(ctx, s.BatchSize, s.IncompleteGrace)
	if err != nil {
		logger.Error("store vacuum failed", "event", "store_vacuum", "error", err, "duration", time.Since(start))
		return
	}
	logger.Info("store vacuum completed",
		"event", "store_vacuum",
		"incomplete_file_data_removed", res.IncompleteFileData,
		"orphaned_file_data_removed", res.OrphanedFileDataRemoved,
		"orphaned_chunk_links_removed", res.OrphanedChunkLinksRemoved,
		"orphaned_chunks_removed", res.OrphanedChunksRemoved,
		"bytes_reclaimed", res.BytesReclaimed,
		"duration", time.Since(start))
}
