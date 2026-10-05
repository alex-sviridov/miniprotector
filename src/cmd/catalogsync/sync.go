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
	FileVersionDeletionsSince(ctx context.Context, cursor int64, limit int) ([]wfs.FileVersionDeletionRecord, error)
}

// run replicates bwfs's file_versions to the catalog and, after them, its
// file_version_deletions log -- two independent streams, each with its own
// persisted cursor (cursorFile, deletionCursorFile) that is advanced only
// after the batch was successfully sent, so delivery is at-least-once and
// both operations are idempotent at the catalog. Within one pass versions go
// first and deletions only follow a successful versions send: a deletion
// must never overtake an unacknowledged version, or a retried send could
// re-create an entry the catalog had just dropped. It runs until ctx is
// cancelled, at which point it returns nil.
func run(ctx context.Context, logger *slog.Logger, rd reader, sender Sender, cursorFile, deletionCursorFile string, cfg syncConfig) error {
	versionCursor, err := readCursor(cursorFile)
	if err != nil {
		return err
	}
	deletionCursor, err := readCursor(deletionCursorFile)
	if err != nil {
		return err
	}

	backoff := cfg.InitialBackoff
	sendFailed := func(what string, err error) bool {
		logger.Warn(what+" failed, retrying", "error", err, "backoff", backoff)
		ok := sleepOrDone(ctx, backoff)
		backoff *= 2
		if backoff > cfg.MaxBackoff {
			backoff = cfg.MaxBackoff
		}
		return ok
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		moreWaiting := false

		versions, err := rd.FileVersionsSince(ctx, versionCursor, cfg.BatchSize)
		if err != nil {
			logger.Error("read file versions failed", "error", err)
			if !sleepOrDone(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}
		if len(versions) > 0 {
			if err := sender.Send(versions); err != nil {
				if !sendFailed("send batch", err) {
					return nil
				}
				continue
			}
			backoff = cfg.InitialBackoff
			versionCursor = versions[len(versions)-1].Seq
			if err := writeCursor(cursorFile, versionCursor); err != nil {
				return err
			}
			moreWaiting = len(versions) == cfg.BatchSize // a backlog: drain it without sleeping
		}

		deletions, err := rd.FileVersionDeletionsSince(ctx, deletionCursor, cfg.BatchSize)
		if err != nil {
			logger.Error("read file version deletions failed", "error", err)
			if !sleepOrDone(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}
		if len(deletions) > 0 {
			if err := sender.SendDeletions(deletions); err != nil {
				if !sendFailed("send deletions", err) {
					return nil
				}
				continue
			}
			backoff = cfg.InitialBackoff
			deletionCursor = deletions[len(deletions)-1].Seq
			if err := writeCursor(deletionCursorFile, deletionCursor); err != nil {
				return err
			}
			moreWaiting = moreWaiting || len(deletions) == cfg.BatchSize
		}

		if moreWaiting {
			continue
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
