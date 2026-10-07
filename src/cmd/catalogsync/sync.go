package main

import (
	"context"
	"fmt"
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
	// DamageInterval is the minimum time between two damage passes; zero or
	// negative runs it on every loop pass.
	DamageInterval time.Duration
}

// reader is the subset of *filesystem.ReplicaReader that run depends on.
type reader interface {
	FileVersionsSince(ctx context.Context, cursor int64, limit int) ([]wfs.FileVersionRecord, error)
	FileVersionDeletionsSince(ctx context.Context, cursor int64, limit int) ([]wfs.FileVersionDeletionRecord, error)
	DamagedFileIDs(ctx context.Context, after string, limit int) ([]string, error)
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
//
// A third pass, at most once per cfg.DamageInterval, sends the snapshot of
// currently damaged file ids (see damagePass). It never blocks or reorders
// the two streams above.
func run(ctx context.Context, logger *slog.Logger, rd reader, sender Sender, cursorFile, deletionCursorFile string, cfg syncConfig) error {
	versionCursor, err := readCursor(cursorFile)
	if err != nil {
		return err
	}
	deletionCursor, err := readCursor(deletionCursorFile)
	if err != nil {
		return err
	}

	damage := newDamagePass(logger, rd, sender, cfg)

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

		// Runs only after both streams above got through this pass; it
		// handles its own errors, so it can't stall them.
		damage.runIfDue(ctx, time.Now())

		if moreWaiting {
			continue
		}
		if !sleepOrDone(ctx, cfg.PollInterval) {
			return nil
		}
	}
}

// damagePass keeps the catalog's copy of this node's damaged file set
// current. The set is replicated as a snapshot rather than a log because
// damage is an UPDATE on old file_data_records rows (which no seq cursor
// sees) and it also goes away (a healthy re-upload heals it, vacuum removes
// the row); sending "what is damaged now" covers all of that with no cursor
// file, so the pass is stateless across restarts.
type damagePass struct {
	logger         *slog.Logger
	rd             reader
	sender         Sender
	batchSize      int
	interval       time.Duration
	initialBackoff time.Duration
	maxBackoff     time.Duration

	nextRun time.Time // zero: due on the first loop pass
	backoff time.Duration
	// catalogHasEmptySet is true once a send of an empty set succeeded in
	// this process, so an unchanged empty set need not be sent again. It
	// starts false so the first pass after a restart always sends, clearing
	// rows a previous run may have left behind.
	catalogHasEmptySet bool
}

func newDamagePass(logger *slog.Logger, rd reader, sender Sender, cfg syncConfig) *damagePass {
	return &damagePass{
		logger:         logger,
		rd:             rd,
		sender:         sender,
		batchSize:      cfg.BatchSize,
		interval:       cfg.DamageInterval,
		initialBackoff: cfg.InitialBackoff,
		maxBackoff:     cfg.MaxBackoff,
		backoff:        cfg.InitialBackoff,
	}
}

// runIfDue sends the damaged set if the pass is due at now. Failures are
// logged and only push this pass's own next run back with exponential
// backoff -- never the loop's sleep -- so a broken damage read or send can't
// starve version and deletion replication.
func (d *damagePass) runIfDue(ctx context.Context, now time.Time) {
	if now.Before(d.nextRun) {
		return
	}
	if err := d.send(ctx); err != nil {
		if ctx.Err() != nil {
			return // shutting down; the failure is just the cancellation
		}
		d.logger.Warn("send damaged file set failed, retrying later", "error", err, "backoff", d.backoff)
		d.nextRun = now.Add(d.backoff)
		d.backoff = min(d.backoff*2, d.maxBackoff)
		return
	}
	d.backoff = d.initialBackoff
	d.nextRun = now.Add(d.interval)
}

func (d *damagePass) send(ctx context.Context) error {
	// The first page is read before the stream opens, both to apply the
	// empty-set skip rule and so a failing store read never reaches the
	// catalog at all.
	first, err := d.rd.DamagedFileIDs(ctx, "", d.batchSize)
	if err != nil {
		return fmt.Errorf("read damaged file ids: %w", err)
	}
	if len(first) == 0 && d.catalogHasEmptySet {
		return nil
	}

	pages := &damagedPager{ctx: ctx, rd: d.rd, batchSize: d.batchSize, pending: first}
	if err := d.sender.SendDamaged(pages.next); err != nil {
		return err
	}
	d.catalogHasEmptySet = pages.total == 0
	return nil
}

// damagedPager walks DamagedFileIDs page by page for SendDamaged, starting
// with an already-read first page.
type damagedPager struct {
	ctx       context.Context
	rd        reader
	batchSize int
	pending   []string // the first page, handed out by the first next call
	started   bool
	after     string // keyset cursor: the ids come sorted, so the last one sent
	done      bool
	total     int
}

func (p *damagedPager) next() ([]string, error) {
	if p.done {
		return nil, nil
	}
	page := p.pending
	if p.started {
		var err error
		page, err = p.rd.DamagedFileIDs(p.ctx, p.after, p.batchSize)
		if err != nil {
			return nil, fmt.Errorf("read damaged file ids: %w", err)
		}
	}
	p.started = true
	// A short page ends the set, which saves the query that would only
	// confirm it.
	p.done = len(page) < p.batchSize
	if len(page) > 0 {
		p.after = page[len(page)-1]
	}
	p.total += len(page)
	return page, nil
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
