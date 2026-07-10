package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// watchForReload watches dir for writes to dir/.changed -- the sentinel
// file an operator touches after finishing a (possibly multi-file) policy
// edit -- and triggers a full Cache.Reload on each write. Watching this one
// sentinel, rather than every *.json file individually, means a batch edit
// across several policy files produces exactly one atomic reload instead of
// one reload per file mid-edit. Blocks until ctx is cancelled.
func watchForReload(ctx context.Context, dir string, cache *Cache, logger *slog.Logger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(dir); err != nil {
		return err
	}
	changedPath := filepath.Join(dir, ".changed")

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Name != changedPath {
				continue
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}
			if err := cache.Reload(dir, logger); err != nil {
				logger.Error("policy reload failed", "error", err)
				continue
			}
			logger.Info("policies reloaded")
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logger.Error("policy watcher error", "error", err)
		}
	}
}
