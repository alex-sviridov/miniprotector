package main

import (
	"context"
	"log/slog"
	"time"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

// syncConfig bundles the tunables run needs, decoupled from config.Config
// so tests don't need a parsed config file.
type syncConfig struct {
	BatchSize      int
	PollInterval   time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// reader is the subset of *filesystem.ReplicaReader that run depends on.
type reader interface {
	FileVersionsSince(ctx context.Context, cursor int64, limit int) ([]wfs.FileVersionRecord, error)
}

// run polls rd for new file_versions rows and hands each batch to sender,
// persisting the cursor via cursorFile only after a batch is successfully
// sent. It runs until ctx is cancelled, at which point it returns nil.
func run(ctx context.Context, logger *slog.Logger, rd reader, sender Sender, cursorFile string, cfg syncConfig) error {
	cursor, err := readCursor(cursorFile)
	if err != nil {
		return err
	}

	backoff := cfg.InitialBackoff

	for {
		if ctx.Err() != nil {
			return nil
		}

		batch, err := rd.FileVersionsSince(ctx, cursor, cfg.BatchSize)
		if err != nil {
			logger.Error("read file versions failed", "error", err)
			if !sleepOrDone(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}

		if len(batch) == 0 {
			if !sleepOrDone(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}

		if err := sender.Send(batch); err != nil {
			logger.Warn("send batch failed, retrying", "error", err, "backoff", backoff)
			if !sleepOrDone(ctx, backoff) {
				return nil
			}
			backoff *= 2
			if backoff > cfg.MaxBackoff {
				backoff = cfg.MaxBackoff
			}
			continue
		}

		backoff = cfg.InitialBackoff
		cursor = batch[len(batch)-1].Seq
		if err := writeCursor(cursorFile, cursor); err != nil {
			return err
		}

		if len(batch) == cfg.BatchSize {
			continue // there may be more backlog — drain it without sleeping
		}
		if !sleepOrDone(ctx, cfg.PollInterval) {
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
