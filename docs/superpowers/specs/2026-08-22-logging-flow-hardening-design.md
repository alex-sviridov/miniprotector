# Logging Flow Hardening — Design

> **Amends:** `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md` (Vector buffer
> config, `log-gateway`'s push path) and `docs/superpowers/specs/2026-08-17-live-job-updates-design.md`
> (`jobAggregator`'s reconcile/tail lifecycle). No new components, no schema change — three
> independent, surgical fixes found by auditing the full logging pipeline (client log write → Vector
> → `log-gateway` → Loki → `api-server` → browser) against reliability, performance, and code-size
> priorities.

## Problem

The fleet log aggregation and live-job-updates pipelines work, but three concrete issues survived
those designs:

1. **Vector's disk buffer drops the wrong data under sustained backpressure.** Its `when_full`
   setting is `drop_newest`, but Vector's disk buffer only supports `block` or `drop_newest` — there
   is no "drop oldest" mode. The original design doc's own claim ("oldest entries are dropped") was
   never actually achievable with the chosen tool, and the config as shipped means a `log-gateway`/
   Loki outage that outlasts the 256MB buffer discards the *freshest* incoming log lines while stale
   ones sit queued — backwards for an operator debugging a live incident, who cares most about what's
   happening right now.
2. **`jobAggregator` queries Loki on a fixed schedule regardless of demand.** `reconcileLoop` re-runs
   two full 24h, fleet-wide Loki queries every 60 seconds for as long as `api-server` runs, even when
   zero browsers are subscribed to `/api/v1/jobs/stream`. This is steady-state Loki load that buys
   nothing when nobody's watching.
3. **`log-gateway`'s push route fully buffers each request body before forwarding it.** `ServeHTTP`
   does `io.ReadAll` into a byte slice, then re-wraps it in a `bytes.Reader` to build the outbound
   request — every push sits fully in memory before the outbound POST to Loki even starts, adding
   latency and peak-memory cost per request on the one process every node's logs must pass through.

## Goals

- A prolonged `log-gateway`/Loki outage no longer causes Vector to discard the most recent, most
  operationally relevant log lines; the design doc's actual documented behavior matches what's
  configured.
- `jobAggregator` does no periodic full-fleet Loki querying while no browser has
  `/api/v1/jobs/stream` open, and a browser that connects after an idle stretch never sees a snapshot
  staler than "since the last time anyone was watching."
- `log-gateway`'s push route streams the request body to Loki instead of buffering the whole thing in
  memory first, for the common case of a well-formed request.

## Non-Goals

- **No change to `log-gateway`'s `ServeQuery` (read) path.** Its buffer-then-check-size approach
  gives an atomicity guarantee (a complete valid response or a clean `502`, never a truncated body
  after `200` is already committed) that a streamed response can't — worth keeping on a read path
  that isn't the fleet-wide ingestion bottleneck the push path is.
- **No change to the tail-driven side of `jobAggregator`** (`tailLoop`, its own reconcile-before-
  reattach, or its backoff). Only the ticker-driven periodic reconcile changes.
- **No new configuration.** All three fixes are internal behavior changes; no new `local.conf` key,
  no new flag.
- **No HA, no retry-with-backoff added to `log-gateway`'s forward call, no change to Loki itself.**
  Out of scope for this pass, same as the original design's own Non-Goals.

## Architecture

### 1. Vector buffer: `when_full: block`

`src/cmd/agent/vector.go`'s `vectorConfigTemplate`, sink block:

```yaml
    buffer:
      type: disk
      max_size: 268435488
      when_full: block
```

(was `when_full: drop_newest`.) `block` is the only alternative Vector's disk buffer offers besides
`drop_newest` — there is no third "evict oldest" option. Under `block`, once the 256MB buffer fills,
Vector's `local_logs` file source pauses advancing through the tailed files rather than dropping
anything; it resumes and catches up once `log-gateway`/Loki is reachable again. This backpressure is
contained entirely to Vector's own internal pipeline: it never touches the OS-level file descriptors
`agent`/`brfs`/`bwfs`/etc. hold open for their own log writes, so a stalled Vector sink cannot slow or
block a backup, cert-refresh, or policy-fetch operation — preserving the original design's Goal ("a
brief outage... never blocks or fails an actual backup... operation").

The practical bound on "how much log history survives an outage" becomes local disk headroom for the
buffer plus the pre-existing lumberjack rotation/retention on the source `*.log` files themselves
(50MB × 5 backups, 14 days) — the same bound that already existed for "how far behind can Vector fall
before source data is rotated away," just no longer compounded by the buffer itself discarding fresh
data on top of it.

Update `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`'s Architecture section
(the "oldest entries are dropped" line, in both the Vector bullet and the Data Flow diagram) to
describe the corrected `block` behavior instead.

### 2. `jobAggregator`: no periodic reconcile with zero subscribers

`src/cmd/api-server/jobs_aggregator.go`:

- `reconcileLoop`'s ticker branch gains a guard: skip calling `a.reconcile(ctx)` when there are no
  current subscribers. Needs a way to read subscriber count under `a.mu` — add a small
  `subscriberCount()` helper (`len(a.subs)` under the existing lock) rather than reaching into `a.subs`
  directly from `reconcileLoop`.
- `Subscribe()` triggers one synchronous reconcile when the subscriber count transitions from 0 to 1,
  *before* returning the snapshot to the caller — so the first browser to connect after an idle
  stretch gets fresh data immediately rather than whatever was last computed (possibly a long time
  ago) or waiting up to `jobsAggregatorReconcileEvery` for the next tick. Implementation: under
  `a.mu`, check `len(a.subs) == 0` before registering the new channel; if so, call `a.reconcile(ctx)`
  (needs a `context.Context` — `Subscribe` gains one, threaded from `handleJobsStream`'s
  `r.Context()`) *before* taking the snapshot, so the snapshot reflects the fresh state. This makes
  `Subscribe` briefly synchronous on the 0→1 transition only; every subsequent subscriber (1→2, 2→3,
  ...) is unaffected and returns immediately as today.
- `tailLoop` and its own reconcile-before-reattach are unchanged — they run regardless of subscriber
  count, since a broken tail is a real event that needs to resync state promptly whether or not
  anyone happens to be subscribed at that exact moment, and reconnects are comparatively rare (not a
  fixed 60s drumbeat).

### 3. `log-gateway`: stream the push body

`src/cmd/log-gateway/server.go`'s `ServeHTTP`:

- Before reading anything: if `r.ContentLength > 0 && r.ContentLength > maxPushBodyBytes`, respond
  `413` immediately — no read, no dial to Loki. This preserves today's fast, clean rejection for the
  common case (a well-behaved client that sets `Content-Length`, which Vector's `loki` sink always
  does for a single batched POST).
- Otherwise, keep `r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)` as the hard safety net
  (unset/lying `Content-Length`, or chunked transfer), but stop reading it into a byte slice. Pass
  `r.Body` directly as the outbound request's `io.Reader` when building `lokiReq` via
  `http.NewRequestWithContext`, instead of `bytes.NewReader(body)`. The outbound POST now streams
  from the inbound connection straight through to Loki.
- Consequence, stated plainly: if the `MaxBytesReader` safety net trips *mid-stream* (a caller that
  lies about a smaller `Content-Length`, or omits it while still exceeding the cap), the read error
  surfaces as a failed `s.httpClient.Do(lokiReq)` call, which today's code already turns into a `502`
  — not the clean `413` a pre-buffered read would give. Accepted: this path only exists for a
  malformed or oversized caller on an internal, mTLS-authenticated route where the cap is an OOM
  guard, not a caller-facing validation contract to preserve at all costs.
- `passthroughHeaders` forwarding, the `lokiForwardTimeout` context, and the response-copy tail
  (`io.Copy(w, resp.Body)`) are unchanged.

## Data Flow (Vector buffer, before/after)

```
before: log-gateway/Loki unreachable, buffer fills
  -> new log lines arriving at the disk buffer are dropped (when_full: drop_newest)
  -> stale, already-buffered lines remain queued; nothing shipped for either

after: log-gateway/Loki unreachable, buffer fills
  -> local_logs file source pauses (when_full: block) -- no new lines read from disk
  -> local *.log files keep growing/rotating under lumberjack exactly as before (agent/brfs/bwfs
     writes are never blocked by this)
  -> once log-gateway/Loki is reachable again, the buffer drains and the file source resumes from
     its checkpoint -- no gap in what's shipped, only a delay bounded by how long the outage plus
     drain takes
```

## Testing

- `cmd/agent/vector_test.go`: update the rendered-config assertion to expect `when_full: block`.
- `cmd/api-server/jobs_aggregator_test.go`:
  - Ticker-driven reconcile is skipped when `Subscribe` has never been called (0 subscribers) —
    advance a fake clock/ticker past `jobsAggregatorReconcileEvery` and assert the fake `lokiQuerier`
    saw no additional `QueryRange` calls beyond the initial `Start`-time reconcile.
  - Ticker-driven reconcile fires normally once at least one subscriber is registered.
  - `Subscribe()` on the 0→1 transition triggers a synchronous reconcile — assert the returned
    snapshot reflects a `QueryRange` result set *after* `Subscribe` was called, not before.
  - A second concurrent `Subscribe()` (1→2 transition) does *not* trigger an extra reconcile.
- `cmd/log-gateway/server_test.go`:
  - A push whose `Content-Length` exceeds `maxPushBodyBytes` gets `413` without the fake Loki target
    ever being dialed.
  - A push within the cap round-trips unchanged (status/body forwarded correctly) with the new
    streaming path.
  - A push with no/understated `Content-Length` whose actual body exceeds the cap gets `502` (via the
    `MaxBytesReader` safety net), documenting the accepted behavior change from `413`.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule (behavior change, no new component/flag/proto):

- **`docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`** — correct the Vector buffer
  bullet and Data Flow diagram to describe `when_full: block`, not "oldest entries are dropped."
- **`docs/components/log-gateway.md`** — note the push route's `Content-Length`-based fast-`413` and
  that a body exceeding the cap via a lying/absent `Content-Length` now surfaces as `502`, not `413`.
- **`docs/protocols/log-gateway.md`** — same behavior change, in the push route's Response section.
- No change needed to `docs/components/api-server.md` or `docs/ARCHITECTURE.md` — `jobAggregator`'s
  subscriber-gated reconcile is an internal efficiency change with no observable API/behavior
  difference for a connected client.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.
