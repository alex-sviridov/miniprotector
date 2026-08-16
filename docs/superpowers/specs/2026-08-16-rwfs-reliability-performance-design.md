# rwfs: Reliability, Performance, and Reuse — Design

> **Builds on:** `docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md` (the
> current `rwfs restore` phase 1, and the `ResolveRestoreFiles` consumption pipeline `verify
> --rules-stdin` and `restore` both already drive). This design does not add restore behavior —
> file content restore (phase 2) remains unbuilt. It hardens and reorganizes what already exists:
> `list`, `verify`, and `restore`'s directory phase.

## Problem

A review of `rwfs`'s current implementation surfaced three classes of issue, none of them bugs in
the sense of wrong output, but all real gaps against "fast, reliable, and reusable":

1. **`ListFiles` is a unary RPC with an unbounded response.** `bwfs` already filters server-side
   (host/path/substring), but returns the whole filtered result set as one gRPC message. That message
   is capped by gRPC's default ~4MB `MaxRecvMsgSize`; a host with a large listing can hit that ceiling
   outright, and `rwfs verify`'s plain (non-`--rules-stdin`) path also has to wait for the entire
   response before it can start verifying anything. `docs/protocols/list.md` explicitly recorded the
   unary choice as deliberate ("thousands of entries per host, not millions") — this design revisits
   that tradeoff for the wire-transfer shape specifically, not the underlying SQL query.
2. **Two of `rwfs`'s streaming RPCs have no stall protection.** `ResolveRestoreFiles` (used by both
   `verify --rules-stdin` and `restore`) and each per-file `RestoreFile` call (used by `verify`) run on
   contexts with no deadline at all. If `bwfs` stops responding mid-stream, the client hangs forever.
   Separately, `verifyFileWithRetry` retries a failed stream immediately, with no backoff, which can
   hammer a struggling server.
3. **The "stream `ResolveRestoreFiles`, apply `resolver.Feed`, dispatch matched rows" logic is
   written twice**, once in `verify.go` (channel-fed worker pool) and once in `restore.go` (inline
   loop), in two different shapes. `verify.go` also hand-rolls its worker pool
   (`workCh`/`resultCh`/`sync.WaitGroup`) rather than using anything reusable — `brfs`'s
   `filesstream.go` has an almost identical pool, independently written. A future phase 2 (file
   content restore) would need this shape a third time.

## Goals

- Convert `ListFiles` to a server-streaming RPC, so no single-message size ceiling exists on the wire
  and `verify`'s plain path can start work as rows arrive.
- Add stall protection (an idle-timeout watchdog, not a flat total-duration timeout) to
  `ResolveRestoreFiles` and `RestoreFile` streaming calls, since both can legitimately run a long time
  on a large result set — only an actual stall should fail the call.
- Add capped, doubling backoff between `verifyFileWithRetry` attempts.
- Extract two small, independently-understandable, reusable pieces from what's duplicated today:
  - a generic worker pool, replacing `verify.go`'s hand-rolled channel/`WaitGroup` plumbing;
  - a shared resolved-row source wrapping `ResolveRestoreFiles` + `resolver.Feed`'s dispatch/gating,
    used identically by `verify --rules-stdin` and `restore`.
- Keep the CLI surface and all documented behavior (output formats, exit codes, not-found semantics,
  directory-creation contract) unchanged — this is a reliability/performance/structure pass, not a
  feature change.

## Non-Goals (this round)

- **No SQL/cursor rewrite of `queryFileRows`.** `bwfs` still runs one query and materializes the full
  filtered result server-side before streaming it out row by row. This fixes the wire-transfer
  ceiling, not server-side memory scale for a store with millions of rows — that remains a documented,
  deliberate limit, same spirit as the original tradeoff, just narrowed.
- **No new CLI flags.** Idle-timeout and backoff values are fixed constants, not configurable.
- **No change to `verify`/`restore`'s documented semantics** — not-found reporting, exit codes,
  `--overwrite` being logged-only, phase 1's abort-on-first-failure directory creation, and
  `--rules-stdin`'s empty-ruleset rejection are all untouched.
- **No phase 2 (file content restore).** The worker pool and resolved-row source are built to be
  reused by it later, but nothing in this round calls `RestoreFile` from `restore.go`.
- **`brfs`'s own worker pool (`filesstream.go`) is not touched.** The duplication with it is noted as
  a known parallel, not resolved — the new pieces live inside `cmd/rwfs`, not a shared `common`
  package, since the only confirmed future consumer is `rwfs` itself.

## Architecture

### 1. `ListFiles` becomes a streaming RPC

```proto
service ListService {
  rpc ListFiles(ListRequest) returns (stream FileRow);
  rpc ResolveRestoreFiles(ResolveRestoreFilesRequest) returns (stream ResolveRestoreFilesResponse);
}
```

`ListResponse` is removed from `list.proto` — nothing references it once `ListFiles` streams `FileRow`
directly (mirrors `ResolveRestoreFilesResponse`'s per-row shape, minus the `filter_index` field, which
`ListFiles` has no use for — it has exactly one implicit filter, not a list of them).

`src/cmd/bwfs/listserver.go`'s `ListFiles` handler keeps calling `queryFileRows` exactly as today (one
query, one slice, unchanged SQL), then loops:

```go
func (s *listServer) ListFiles(req *pb.ListRequest, stream pb.ListService_ListFilesServer) error {
	rows, err := queryFileRows(s.store, req.GetServerName(), req.GetPath(), req.GetFilter())
	if err != nil {
		s.logger.Error("ListFiles query failed", "error", err)
		return err
	}
	for _, r := range rows {
		if err := stream.Send(rowToProto(r)); err != nil {
			return err
		}
	}
	return nil
}
```

### 2. Client-side streaming consumption

`src/cmd/rwfs/list.go` drains the stream into a slice, same as its current `resp.Rows` — table/JSON
rendering needs the full set for column widths, so behavior and output are unchanged:

```go
stream, err := client.ListFiles(ctx, &pb.ListRequest{...})
var rows []*pb.FileRow
for {
	row, err := stream.Recv()
	if err == io.EOF {
		break
	}
	if err != nil {
		return fmt.Errorf("list files: %w", err) // discard partial rows, same as today's atomic failure
	}
	rows = append(rows, row)
}
```

`verify.go`'s plain-listing branch instead pushes each row straight into the worker pool's input
channel as it arrives (filtering `type=="f" && size>0` inline, same predicate as today), so
verification starts before the listing finishes rather than after.

### 3. Generic worker pool (`src/cmd/rwfs/workerpool.go`, new file)

```go
// runWorkerPool runs work concurrently across streams goroutines, consuming
// in until it closes and emitting one R per T processed. The returned
// channel closes once every worker has drained in. Mirrors the shape
// brfs's filesstream.go already uses for backup streaming, but generic so
// verify and (later) restore's file-content phase can both use it with a
// different work function.
func runWorkerPool[T, R any](ctx context.Context, streams int, in <-chan T, work func(context.Context, T) R) <-chan R {
	out := make(chan R, streams)
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range in {
				out <- work(ctx, item)
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
```

`verify.go`'s dispatch loop becomes:

```go
resultCh := runWorkerPool(callCtx, streams, workCh, func(ctx context.Context, row *pb.FileRow) verifyResult {
	return verifyFileWithRetry(ctx, logger, restoreClient, row, retries)
})
```

### 4. Shared resolved-row source (`src/cmd/rwfs/resolve.go`, extended)

```go
// dispatchedRow is one row streamResolvedRows has already run through
// restoreResolver.Feed's precedence/type gating and confirmed should be
// acted on -- ruleIndex is the winning rule (needed by restore for
// dest_path attribution; verify ignores it).
type dispatchedRow struct {
	Row       *pb.FileRow
	RuleIndex int
}

// streamResolvedRows wraps a watchdog-protected ResolveRestoreFiles call
// and resolver.Feed's existing gating in one reusable stream, used
// identically by verify --rules-stdin and restore -- previously each
// drove this loop independently, in two different shapes. The returned
// resolver's NotFound() must be called only after rows is fully drained.
func streamResolvedRows(ctx context.Context, client pb.ListServiceClient, rules []RestoreRule) (rows <-chan dispatchedRow, resolver *restoreResolver, errCh <-chan error) {
	filters, filterToRuleIndex := buildRestoreFilters(rules)
	resolver = newRestoreResolver(rules, filterToRuleIndex)

	watchdogCtx, touch, stop := withStallWatchdog(ctx, streamIdleTimeout)
	out := make(chan dispatchedRow)
	errs := make(chan error, 1)

	stream, err := client.ResolveRestoreFiles(watchdogCtx, &pb.ResolveRestoreFilesRequest{Filters: filters})
	if err != nil {
		stop()
		close(out)
		errs <- fmt.Errorf("resolve restore files: %w", err)
		return out, resolver, errs
	}

	go func() {
		defer stop()
		defer close(out)
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				errs <- nil
				return
			}
			if err != nil {
				errs <- fmt.Errorf("resolve restore files: %w", err)
				return
			}
			touch()
			if dispatch, ruleIndex := resolver.Feed(resp.GetRow(), resp.GetFilterIndex()); dispatch {
				out <- dispatchedRow{Row: resp.GetRow(), RuleIndex: ruleIndex}
			}
		}
	}()
	return out, resolver, errs
}
```

`verify.go`'s `--rules-stdin` branch and `restore.go`'s loop both replace their current
hand-written stream-consumption with `streamResolvedRows`, then differ only in what they do per
`dispatchedRow`: `verify` gates on `Row.GetType() == "f"` and pushes into the worker pool (as it does
today); `restore` splits on `Row.GetType()` into its `"resolved"` log line or `restoreDirectory`
collection (as it does today). Neither's post-stream logic (`resolver.NotFound()`, summary counts)
changes.

### 5. Stall watchdog (`src/cmd/rwfs/watchdog.go`, new file)

```go
// withStallWatchdog returns a context that's cancelled if touch isn't
// called within idle after the last call (or after ctx was created, for
// the first call) -- an idle timeout, not a total-duration timeout, so a
// stream that's actively producing rows never times out no matter how
// long the overall call runs; only an actual stall does. stop releases
// the watchdog goroutine and must be called once the caller is done with
// ctx, success or failure alike.
func withStallWatchdog(parent context.Context, idle time.Duration) (ctx context.Context, touch func(), stop func()) {
	ctx, cancel := context.WithCancel(parent)
	timer := time.NewTimer(idle)
	touchCh := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				cancel()
				return
			case <-touchCh:
				if !timer.Stop() {
					<-timer.C
				}
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
	stop = func() {
		close(done)
		cancel()
	}
	return ctx, touch, stop
}
```

Used identically for `ResolveRestoreFiles` (touched on each `Recv`, see above) and for each
`RestoreFile` stream in `verifyFile` (touched on each chunk `Recv`). A cancellation from the watchdog
surfaces to the caller as a normal `context.Canceled`-wrapped error from the next `Recv` call — logged
the same way any other stream error is today, so no new error-handling branch is needed at the call
sites; the watchdog is purely about *causing* the right failure sooner, not changing how failures are
reported.

### 6. Backoff in `verifyFileWithRetry`

```go
const (
	retryBackoffInitial = 500 * time.Millisecond
	retryBackoffCap     = 5 * time.Second
)

func verifyFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, row *pb.FileRow, maxRetries int) verifyResult {
	backoff := retryBackoffInitial
	var result verifyResult
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result = verifyFile(ctx, client, row)
		if result.ok || result.reason == "blake3_mismatch" || result.reason == "crc_mismatch" {
			return result
		}
		if attempt < maxRetries {
			logger.Warn("stream error, retrying", "path", row.Path, "file_uuid", row.FileUuid, "attempt", attempt, "reason", result.reason)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return result
			}
			backoff = min(backoff*2, retryBackoffCap)
		}
	}
	return result
}
```

A checksum mismatch (`blake3_mismatch`/`crc_mismatch`) is a real, deterministic failure, not a
transient error, so it still returns immediately without waiting or retrying — unchanged from today.

## Data Flow

**`rwfs list` / `rwfs verify` (plain path):**
```
ListFiles(req) [streaming]
  -> bwfs: queryFileRows (unchanged query) -> stream.Send per row
  -> rwfs list.go: Recv loop -> []FileRow -> render (unchanged output)
  -> rwfs verify.go: Recv loop -> filter type=="f"&&size>0 -> workCh -> runWorkerPool
```

**`rwfs verify --rules-stdin` / `rwfs restore`:**
```
rules -> buildRestoreFilters (unchanged) -> streamResolvedRows
  -> ResolveRestoreFiles [watchdog-wrapped, streaming]
  -> bwfs: resolveRestoreFilter + resolveRestoreDirectoryFilter (both unchanged)
  -> rwfs: resolver.Feed per row (unchanged precedence/gating) -> dispatchedRow channel
verify: dispatchedRow (type f) -> runWorkerPool -> verifyFileWithRetry [watchdog-wrapped RestoreFile, now backed off]
restore: dispatchedRow -> split on Type -> "resolved" log line (f) | restoreDirectory collection (d)
both: channel closes -> resolver.NotFound() (unchanged) -> summary
```

**Phase 2 (future, not built now):** would call `streamResolvedRows` identically, then plug a
`RestoreFile`-fetch-and-write function into `runWorkerPool` in place of `verify`'s checksum-only
worker — the same two reusable pieces, a different `work` function.

## Error Handling

- **Watchdog timeout** (idle > `streamIdleTimeout`, fixed at 60s): cancels the call's context; the
  next `Recv` returns a `context.Canceled`-derived error, handled by each call site's existing
  stream-error path (retried with backoff for `RestoreFile` in `verify`; a hard abort for
  `ResolveRestoreFiles`, same as any other stream error there today — no per-filter retry exists or is
  added).
- **Backoff**: capped doubling from 500ms to 5s, only between attempts under the existing `--retries`
  flag; a deterministic checksum mismatch still fails immediately, never retried.
- **`ListFiles` mid-stream failure** (new failure mode — the call was previously atomic):
  - `list.go`: discards whatever rows were collected so far and returns the error — no partial
    table/JSON output, preserving today's all-or-nothing behavior.
  - `verify.go` plain path: rows already dispatched to the worker pool were legitimately verified and
    their results stand; the mid-stream error is reported alongside the summary, the same pattern
    `--rules-stdin` already uses for its own stream errors.
- Directory-creation error handling (phase 1: abort on first `Mkdir` failure) is unchanged.
- Not-found semantics, exit codes, and `--rules-stdin`'s empty-ruleset rejection are unchanged.

## Testing

- **`workerpool.go`**: work distributed across all `streams` goroutines; all results delivered; empty
  input closes `out` cleanly; context cancellation stops in-flight dispatch.
- **`watchdog.go`**: repeated `touch()` within the idle window never cancels; no `touch()` within the
  window cancels; `stop()` releases the goroutine with no leak (assert via a done-channel or
  goroutine-count check) once the caller finishes normally.
- **`resolve.go`**: `streamResolvedRows` — existing `resolve_test.go` precedence/`NotFound`/directory-
  dispatch cases re-target the new entry point; behavior is unchanged, so assertions carry over. Add a
  case for a `ResolveRestoreFiles` stream that stalls past the idle window.
- **`bwfs` `listserver.go`**: `listserver_test.go` updates from asserting one `ListResponse` to
  draining a stream and asserting the same rows in the same order; add a mid-stream send-error case.
- **`rwfs` `list.go` / `verify.go`**: extend the existing bufconn-based tests (`verify_test.go`'s
  `runVerifyWithDialer` pattern) to cover streamed `ListFiles` across multiple `Recv` calls, a
  mid-stream error (partial rows discarded for `list`, partial results kept for `verify`), backoff
  actually delaying between retries (short test-only constants or a mock clock), and a
  watchdog-triggered failure on a stalled stream.
- `restore_test.go`, `rules_test.go`, `restoredirectory_test.go` need no behavioral changes — only
  updated to route through `streamResolvedRows`, so existing assertions should hold.

## Documentation Impact

Per `.claude/CLAUDE.md`'s protocol-change and feature-change rules:

- **`docs/protocols/list.md`** — `ListFiles`'s protocol definition changes to streaming; the "Why
  unary RPC instead of server streaming?" design-decision note is replaced with the updated rationale
  (wire-transfer ceiling removed; server-side query still materializes fully, scale tradeoff narrowed
  accordingly, not eliminated).
- **`docs/components/rwfs.md`** — note the idle-timeout watchdog on `ResolveRestoreFiles`/`RestoreFile`
  streams, retry backoff, and that `list`/`verify`/`restore` now share a worker pool and resolved-row
  source internally (no CLI-visible change).
- **`CHANGELOG.md`** — entry before merge summarizing the reliability/performance/structure changes.
- **`README.md`** / **`docs/ARCHITECTURE.md`** — no change; CLI surface, quick-start examples, and
  system topology are all unaffected.

## Relationship to Prior Work

The 2026-08-16 restore-directory-structure design built `resolver.Feed`'s type-agnostic gating and
`restore.go`'s two-phase split without touching how the stream driving it was structured or protected.
This design doesn't change what gets resolved or restored — it changes how reliably and efficiently
the underlying `ListFiles`/`ResolveRestoreFiles` streams are consumed, and collapses `verify` and
`restore`'s independently-written consumption loops into one shared, reusable path, so phase 2 (file
content restore) has a proven drop-in point instead of a third bespoke implementation to write.
