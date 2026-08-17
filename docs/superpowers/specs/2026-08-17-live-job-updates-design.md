# Design: Live Job & Log Updates (WebSocket)

> Builds on [Design: `/jobs` REST Endpoint](2026-07-19-jobs-endpoint-design.md) (Loki structured
> metadata, `job_id`/`event`/`status` lifecycle markers, start/finish pairing) and
> [Design: Fleet Log Aggregation](2026-07-11-fleet-log-aggregation-design.md) (Loki, `log-gateway`,
> Vector) — this spec adds a push path on top of both, replacing neither.

## Problem

A real job finishing and that fact becoming visible in `web` are separated by two distinct gaps:

1. A bounded, few-seconds pipeline delay (Vector batching, Loki ingest, `api-server`'s 10s query
   cache) — already small, and Vector's `loki` sink already defaults to a 1s batch timeout, so
   there's little to tune here in isolation.
2. An **unbounded** frontend gap: `web/src/views/JobsListView.vue` and `JobDetailView.vue` both
   fetch exactly once, in `onMounted`, and never again. A user watching `/jobs/:job_id` while a
   restore runs sees nothing change until they manually reload — regardless of how fast the backend
   pipeline is.

The `/jobs`-endpoint design already anticipated a live-tail UX ("a UI polls with an advancing cursor
for a live-tail feel") but explicitly scoped out push transport ("No websocket/SSE push... polling
only"). This design revisits that non-goal: Loki natively supports a WebSocket tail endpoint
(`GET /loki/api/v1/tail`), which fits this use case better than either manual reload or polling.

## Goals

- `/jobs/:job_id` shows new log lines — including the line that marks a job's completion — without
  a manual reload.
- `/jobs` shows new jobs and `in_progress` → `success`/`failure` transitions without a manual
  reload.
- Reliability first: no failure mode is silent. A dropped connection is visible in the UI and
  self-heals; a rare missed line is caught by a periodic correctness backstop, not trusted to the
  push path alone.
- Performance second: one shared upstream tail serves the `/jobs` list regardless of how many
  browser tabs are watching it, not one per tab.
- Simplicity third, once the above are satisfied: reuse existing patterns (`backoff()`,
  `log-gateway`'s passthrough-proxy style, the existing start/finish pairing logic) rather than
  inventing new ones.

## Non-Goals

- No coverage for `issuer`/`client-manager` jobs — same exclusion the original fleet-log and
  `/jobs`-endpoint designs already made (no mTLS identity / not `agent`-managed).
- No multi-replica `api-server` fan-out consistency — consistent with this system's existing
  single-instance-Loki, no-HA stance ("if fleet size or job volume grows enough for this to become a
  real bottleneck, that's a signal to revisit").
- No removal of the existing REST `/jobs` and `/jobs/{id}/logs` endpoints — they remain the initial
  fetch, the reconciliation backstop, and the surface for any non-browser caller.
- No change to Loki retention, query-cost caps, or existing REST response shapes — purely additive.
- No change to which component owns `event`/`status` emission for which job kind (see
  [Backup vs. everything else](#backup-vs-everything-else) below for why that split is correct as-is
  and is a load-bearing assumption of this design, not incidental).

## Architecture

```
Browser --WS---> api-server --WS---> log-gateway --WS---> Loki (native tail)
   |                  |
   | REST (history)   | REST (cold-start priming / reconciliation backstop)
   v                  v
api-server -----------------------> log-gateway --REST--> Loki (query_range)
```

Two independent push paths, sharing one new proxy hop:

### `log-gateway`: WebSocket tail proxy

A third route, `GET /loki/api/v1/tail`, alongside the existing `POST /loki/api/v1/push` and
`GET /loki/api/v1/query_range` — same operating-tier mTLS gate, same unexamined-passthrough
philosophy: the WebSocket upgrade is proxied to Loki's real tail endpoint with query parameters
forwarded unmodified. No verified peer certificate → the upgrade is rejected before the handshake
completes, same as the other two routes reject an unauthenticated request today.

### `api-server`: WebSocket auth via short-lived ticket

Browsers cannot set an `Authorization` header on a WebSocket handshake — the only way to
authenticate the upgrade is a query parameter or subprotocol. Putting the existing long-lived shared
bearer token directly into a WS URL would regress its exposure (URLs land in access logs, proxy
logs, browser history) for a credential that already carries meaningful access
(`docs/components/api-server.md`).

Instead: `POST /api/v1/ws-tickets` (existing bearer-auth REST call) returns a random single-use
ticket valid for 30s. The browser opens the WS with `?ticket=...`; `api-server` validates and
immediately invalidates it. Every reconnect requests a fresh ticket — tickets are single-use by
design, so this falls out for free rather than needing special-casing.

### `api-server`: per-job log tail (`GET /api/v1/jobs/{job_id}/logs/stream`)

Stateless per-connection proxy. On connect, `api-server` dials `log-gateway`'s tail with
`{binary=~"agent|brfs|bwfs"} | job_id="<id>"` and pumps each line to the browser in the same shape
the existing REST `/jobs/{id}/logs` already returns (`timestamp`, `hostname`, `binary`, `line`) — one
parser on the frontend, not two. Either side disconnecting closes the pair.

### `api-server`: fleet-wide job aggregator (`GET /api/v1/jobs/stream`)

Unlike the per-job tail, one shared upstream tail serves every connected browser — a fleet-wide,
`job_id`-unfiltered tail (`{binary=~"agent|brfs|bwfs"}`) would otherwise be opened once per open
`/jobs` tab, multiplying Loki-side query cost with no benefit.

- **State**: an in-memory `job_id → summary` map (`kind`, `source_host`, `store_host`,
  `started_at`, `finished_at`, `state`), updated one line at a time by a shared
  `ingestLine(line) → (job_id, summary)` function. This is the *same* start/finish pairing logic
  `GET /api/v1/jobs` already runs per-query, refactored so both the REST handler (batch, per-query)
  and the aggregator (incremental, per-line) call one implementation — not two independently
  maintained pairing logics that could drift apart.
- **Cold start / reconnect resync**: before attaching the tail, prime the map via the existing
  `query_range`-based `/jobs` logic over the default 24h window — Loki's tail is explicitly
  best-effort on initial lookback, not a backfill guarantee, so it cannot be the sole source for
  a fresh map. On an upstream disconnect, reconnect and re-prime for the gap window
  (last-processed timestamp → now) before resuming the tail, using the same jittered `backoff()`
  helper `agent`'s `vectorSupervisor` already uses for its own unexpected-exit reconnects
  (`cmd/agent/vector.go`) — same idiom, new instance, not shared code (different package).
- **Fan-out**: a newly-connected browser receives the current map as one `snapshot` message
  (pruned to the 24h window), then an `upsert` per job whose summary changes afterward — never a
  full re-send per tick.
- **Reconciliation backstop**: independent of tail health, the aggregator re-runs the `query_range`
  priming query every 60s and reconciles the in-memory map against it (replace-by-`job_id`) — a
  correctness net against Loki tail's own documented best-effort guarantee, not just a
  disconnect-recovery mechanism.

## Frontend

**Connection lifecycle (both pages)**: on mount, run the existing one-shot REST fetch first
(unchanged), record the request's wall-clock time as `T0`, then open the WS with
`start = T0 - 2s` (overlap margin) and dedup incoming lines/rows by `(timestamp_ns, hostname,
binary)` against what's already rendered. A shared `ConnectionStatus` component shows `Live` /
`Reconnecting…` / `Live updates unavailable — refreshing every 10s` — reconnects use the same
`backoff()` idiom as the aggregator's upstream reconnect. After 5 consecutive failed reconnects,
fall back permanently (for the rest of that page load) to REST polling at a fixed 10s interval
rather than leaving the page silently stuck. Every reconnect (including the fallback-to-polling
transition) re-derives its cursor from the last successfully-rendered timestamp, never from
scratch — a mid-session drop can neither re-show history nor silently skip a gap.

**`/jobs/:job_id`**: WS tail filtered to this `job_id`. A periodic (60s) REST re-fetch of
`/jobs/{id}/logs` runs alongside it as the reconciliation backstop, merged in via the same dedup
key. On observing this job's `event=finish` line, close the WS (per the earlier
stop-on-terminal-state decision) but leave the status indicator showing `Finished`, not blank.

**`/jobs`**: WS gives `snapshot` + `upsert`. A periodic (60s) REST re-fetch of `/jobs` reconciles
the whole table (replace-by-`job_id`) as the backstop against a missed aggregator update.

## Backup vs. everything else

This design's aggregator relies on an invariant that already holds today, unchanged by this work:
every `job_id` produces exactly one `event=start` line and exactly one `event=finish` line, from
exactly one authoritative source — never two, since the pairing (in both the existing REST handler
and this design's incremental aggregator) is a 1:1 join by `job_id`.

- **Backups** are two-sided: `agent` dispatches `brfs` locally, but the real outcome depends on a
  *separate host*, `bwfs`, actually finalizing the commit. `agent`'s own exec exit code only proves
  the local `brfs` process returned 0, not that `bwfs` committed the data — so `brfs`'s "Backup
  reader started" line and `bwfs`'s "Backup job committed" line are the sole `event=start`/
  `event=finish` sources for `kind=backup`; `agent`'s own wrapper log for a backup dispatch
  deliberately carries no `event`/`status` fields, to avoid a second, competing pair on the same
  `job_id`.
- **Everything else** (`restore`, `verify`, `bootstrap-refresh`, `operating-refresh`,
  `policy-update`) is single-sided from `agent`'s vantage point: one subprocess, exec'd and
  synchronously waited on, with no second independently-authoritative host in the loop. `agent`'s
  own wrapper line (`logExecStart`/`logExecCompletion`, `cmd/agent/reconcile.go`) is therefore the
  correct and only source for these kinds.

No change needed here — this section documents why the existing split is correct, since both the
REST handler and this design's new aggregator depend on it holding.

## Error Handling

- `log-gateway`: unverified caller → `403` before the WS handshake completes. Upstream Loki
  unreachable → close the WS with a distinguishable close-frame reason so `api-server` knows to
  reconnect rather than treat it as a normal client-initiated close.
- `api-server` ticket endpoint: expired or unknown ticket on WS upgrade → `401`, mirroring a bad
  bearer token today.
- Aggregator upstream tail: supervised with jittered backoff (mirrors `vectorSupervisor`); every
  reconnect re-primes the gap window before resuming.
- Browser: connection-state indicator is never silent about a stale/disconnected state; permanent
  fallback to REST polling after repeated reconnect failures, rather than an indefinitely stuck
  "Reconnecting…".

## Testing

- Unit, `log-gateway`: WS upgrade auth gate (mirrors existing push/query_range tests), unreachable
  upstream close-frame behavior.
- Unit, `api-server`: the shared pairing function gets one test suite, exercised by both the REST
  handler's batch path and the aggregator's incremental path; aggregator ingest-one-line/emit-upsert
  logic against fabricated lines, no real Loki; ticket issuance/single-use/expiry; reconnect-and
  -reprime gap-window logic against a fake Loki.
- Unit, frontend: dedup-by-key logic, backoff/fallback-to-polling state machine, stop-on-terminal
  -state — covered directly, not only through e2e.
- Integration/e2e: extend the existing real-Loki e2e pattern (`web/e2e/restore-verify.spec.js`) with
  a case that starts a real restore job through the UI and asserts `/jobs/:job_id` flips to
  `Finished`/`success` via the WS path with no manual reload — the scenario this feature exists for.

## Documentation Impact

- `docs/protocols/log-gateway.md` — new `GET /loki/api/v1/tail` proxy shape.
- `docs/components/log-gateway.md` — new WS route, See Also cross-link to this spec.
- `docs/components/api-server.md` — new `GET /api/v1/jobs/stream`,
  `GET /api/v1/jobs/{job_id}/logs/stream`, `POST /api/v1/ws-tickets` endpoints; note as a second
  documented exception to the one-RPC-per-call rule (the aggregator holds state across calls).
- `docs/components/web.md` — `/jobs` and `/jobs/:job_id` bullets updated to describe live updates
  and the connection-status indicator, replacing the current "fetched once on page load (no
  live-tail/polling)" language.
- `docs/api/rest-v1.md` — new endpoint sections.
- `README.md` — no change expected (no quick-start example touches jobs/logs today).
