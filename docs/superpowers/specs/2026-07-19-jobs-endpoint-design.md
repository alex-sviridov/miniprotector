# Design: `/jobs` REST Endpoint

> Builds on [Design: api-server](2026-07-14-api-server-design.md) (REST conventions, thin-translation
> principle) and [Design: Fleet Log Aggregation](2026-07-11-fleet-log-aggregation-design.md) (Loki,
> `log-gateway`, Vector, uniform `job_id` propagation) — this spec adds no new credential type and no
> new central database, reusing both pipelines end to end.

## Problem

An operator has no fleet-wide way to answer "what jobs ran, when, from where, and did they
succeed?" — not just backups, but every `agent`-dispatched job (`bootstrap-refresh`,
`operating-refresh`, `policy-update`) too. Job outcome exists today in two disconnected places:
`bwfs`'s own local SQLite (`BackupJobRecord` — start/end/source/state, but backup-only and local to
one node, never centralized) and scattered structured log lines (all job types, already tagged with
a uniform `job_id` per the fleet-log-aggregation design, already shipped fleet-wide to Loki — but
with no consistent status field, and not efficiently queryable by job).

## Goals

- `GET /api/v1/jobs` — list jobs fleet-wide (all job kinds, not just backups) with start time, end
  time, source host, and state, filterable and reasonably efficient to query.
- `GET /api/v1/jobs/{job_id}/logs` — fetch (and, via repeated polling, near-real-time tail) every log
  line associated with one job, across whichever hosts and binaries touched it.
- No new central database or replication RPC — both endpoints read through the fleet-log-aggregation
  pipeline that already exists.
- Querying by `job_id` must not reintroduce the label-cardinality problem the original fleet-log
  design deliberately avoided by keeping `job_id` out of Loki's indexed labels.

## Non-Goals

- No coverage for `issuer`/`client-manager` jobs — same exclusion the fleet-log-aggregation design
  already made, for the same reason (no mTLS identity / not `agent`-managed).
- No websocket/SSE push for "online" log viewing — polling (`since` cursor) only, consistent with
  `api-server`'s existing plain request/response style everywhere else.
- No UI work — this spec is the REST surface only.
- No guarantee of correctness once Loki's own retention (720h) has expired a job's lines, or once a
  query's `truncated: true` flag fires (see Performance) — an operator narrows the range and retries,
  the same way any Loki consumer already has to.

## Architecture

### Job identity and kind, for free

`job_id`'s existing prefix convention (`agent/policy.go`, `agent/backup.go`) already encodes job
kind — `backup:...`, `bootstrap-refresh:...`, `operating-refresh:...`, `policy-update:...`.
`api-server` derives `kind` by parsing that prefix; no new field is added anywhere for this.

### Two new structured-metadata fields, not a label

The original fleet-log design kept `job_id` out of Loki's labels specifically because it's
effectively unique per invocation — as a label it would open a brand-new, permanent, near-empty
stream per job, which is Loki's own documented worst-case cardinality pattern (index growth for the
life of retention, tiny never-filling chunks, ingester memory pressure from ever-growing open-stream
count). That reasoning still holds and isn't revisited here.

Instead, this design uses **Loki structured metadata** (supported by the deployed Loki 3.7.3 +
schema v13/TSDB, `demo/loki/loki-config.yaml` / `deploy/control-plane/loki/loki-config.yaml`) — Loki's
purpose-built mechanism for exactly this case: per-line key/value pairs that are filterable
efficiently without becoming part of a stream's label identity, so cardinality never grows with job
count. `job_id` already reaches every job-scoped log line for free (`common/logging` already attaches
it to every logger built from a job-scoped context — no code change). Two more fields are added, at
exactly the two log call sites that mark a job's lifecycle boundaries, and lifted into structured
metadata by Vector:

- `event`: `"start"` on the one line that marks a job beginning, `"finish"` on the one line that
  marks it ending. Added to:
  - `agent/reconcile.go`'s `logExecStart` (`event=start`) and `logExecCompletion`
    (`event=finish`) — but **only for its three static policies**
    (`bootstrap-refresh`/`operating-refresh`/`policy-update`), gated on `p.ID` not having the
    `backup:` prefix. Scheduled backup dispatches deliberately do *not* get `event`/`status` here —
    see below.
  - `brfs/main.go`'s "Backup reader started" line (`event=start`) — the sole `event=start` source
    for every backup job, whether `agent`-dispatched (scheduled, via a policy) or run ad hoc (per the
    Quick Start's manual `brfs` example). Keeping backups' lifecycle markers on `brfs`/`bwfs` only,
    never duplicated onto `agent`'s own wrapper log, is what keeps the `event=start`/`event=finish`
    pairing in `GET /api/v1/jobs` unambiguous — `agent`'s wrapper log line for a scheduled backup
    still logs as it does today (dispatch/completion, useful for debugging `agent` itself), it's just
    not part of this structured-metadata contract.
  - `bwfs/commit.go`'s "Backup job committed" and "BackupCommit for already-finalized job" lines
    (`event=finish`) — the authoritative backup outcome, since `bwfs` is what actually finalizes a
    commit.
- `status`: `"success"` / `"failure"`, added only to `event=finish` lines. `agent`'s
  `logExecCompletion` today infers success only by the absence of an `error` field — made explicit.
  `bwfs`'s "Backup job committed" line today logs `matched` (bool) but no status string — added,
  reusing the same `storage.JobStatusSuccess`/`JobStatusFailure` enum the adjacent
  "already-finalized" line already logs.

One unrelated cleanup, noticed while touching this code: `brfs/main.go`'s start line logs a stray
duplicate `"jobId"` (camelCase) attribute alongside the `job_id` (snake_case) `common/logging`
already auto-attaches — the one inconsistent key-spelling in the system. Removed.

### Vector: lift `job_id`/`event`/`status` into structured metadata

`agent`'s generated Vector config (`cmd/agent/vector.go`'s `vectorConfigTemplate`) gains a decode
step in its existing `add_binary_label` `remap` transform — parse each line's JSON body (already
`common/logging`'s wire format) far enough to pull out `job_id`, `event`, `status` as event fields —
and the `loki_gateway` sink gains a `structured_metadata` block mapping them through, alongside the
existing `labels` block (`binary`, `hostname`, unchanged). The full raw line is still shipped and
stored as before; structured metadata is additive, not a replacement.

### `log-gateway`: read-path proxy

`log-gateway`'s `main.go` already builds an `http.ServeMux` (currently one route,
`POST /loki/api/v1/push`) — a second route, `GET /loki/api/v1/query_range`, is added, gated by the
same operating-tier mTLS check the listener already enforces on every connection. It forwards query
parameters to Loki's real `query_range` endpoint unmodified, same unexamined-passthrough philosophy
as the push path, with a response-size cap mirroring the existing 10MB inbound push cap
(`maxPushBodyBytes`) so a very broad query can't OOM the gateway. Reachable by any operating-tier
mesh node, same "any operating-tier cert may call any RPC it can reach" convention already accepted
for `clientmanager-api`/`catalog`/`policy-server`.

### `api-server`: two new endpoints

Both are a deliberate, documented exception to `api-server`'s otherwise-universal "one REST call maps
to exactly one backend gRPC call" rule (see [Design: api-server](2026-07-14-api-server-design.md)) —
there's no gRPC service to call here, and `GET /api/v1/jobs` specifically requires grouping two Loki
query results (`event=start` lines, `event=finish` lines) into one job summary per `job_id`.

## `GET /api/v1/jobs`

Query params (all optional):

| Param | Type | Description |
|-------|------|--------------|
| `kind` | string | `backup` \| `bootstrap-refresh` \| `operating-refresh` \| `policy-update` |
| `source_host` | string | Exact match; also narrows the underlying Loki label selector (see Performance) |
| `state` | string | `in_progress` \| `success` \| `failure` |
| `since` | unix seconds | Start of query window. **Default: 24h before `until`.** |
| `until` | unix seconds | End of query window. Default: now. |
| `limit` | int, 1–500 | Cap on returned jobs, default 100 |

`until - since` is capped at 7 days (168h) — `400` if exceeded. (Loki's own retention is 720h/30
days; the cap is about query cost, not data availability — see Performance.)

`api-server` runs one Loki query for `event=start` lines and one for `event=finish` lines (both using
the structured-metadata filter, narrowed by `binary`/`hostname` labels whenever `kind`/`source_host`
was given — see Performance), then pairs them by `job_id`:

- `kind` — parsed from the `job_id` prefix.
- `source_host` — the `hostname` label on the `event=start` line (for backups, this is `brfs`'s own
  host — the real source; for agent-dispatched jobs, the one node involved).
- `store_host` — for `kind=backup` only, the `hostname` label on the `event=finish` line (`bwfs`'s
  host); `null` for every other kind.
- `started_at` / `finished_at` — each line's own timestamp. If no `event=finish` line was found in
  the window, `state: "in_progress"` and `finished_at: null`. If a `event=finish` line was found but
  no matching `event=start` line was in the window (job began before it), `started_at: null` — not
  guessed.
- `state` — from the `event=finish` line's `status`, or `"in_progress"` per above.

```json
{"data": [
  {"job_id": "backup:nightly-db-backup:...:1752400000", "kind": "backup",
   "source_host": "database", "store_host": "bwfs-east",
   "started_at": 1752400000, "finished_at": 1752400010, "state": "success"},
  {"job_id": "operating-refresh:1752400500", "kind": "operating-refresh",
   "source_host": "webserver", "store_host": null,
   "started_at": 1752400500, "finished_at": 1752400501, "state": "success"}
], "truncated": false}
```

No cursor pagination (unlike `/catalog`) — this result set is recomputed per query, not a stable
sequence. `truncated: true` if either underlying Loki query hit its own line cap, meaning the window
may be missing jobs; the fix is a narrower `since`/`until`, not a next page.

## `GET /api/v1/jobs/{job_id}/logs`

| Param | Type | Description |
|-------|------|--------------|
| `since` | unix seconds | Only lines after this timestamp — a UI polls with an advancing cursor for a live-tail feel. Default: 24h before now. |
| `source_host` / `store_host` | string | Optional hints (a caller that already has a `/jobs` entry for this `job_id` has both) — narrows the Loki label selector instead of scanning every stream. |

`job_id` (URL path segment) is validated against `^[a-zA-Z0-9:_-]+$` before being embedded in the
Loki query — `400` on anything else, since it's otherwise unvalidated caller input going into a query
string.

```json
{"data": [
  {"timestamp": 1752400000123456789, "hostname": "database", "binary": "brfs", "line": "{...raw json log line...}"}
]}
```

## Performance

- **Structured metadata, not a content scan**: both endpoints filter on `job_id`/`event`/`status` via
  Loki's structured-metadata mechanism (Architecture, above) rather than parsing every line's JSON
  body — the reason this is fast enough to be worth building at all.
- **Label narrowing**: `kind` narrows the `binary` label selector — `kind=backup` →
  `{binary=~"brfs|bwfs"}` (never `agent`, per Architecture's pairing note above); any of
  `bootstrap-refresh`/`operating-refresh`/`policy-update` → `{binary="agent"}`. With no `kind` filter,
  the query spans `{binary=~"agent|brfs|bwfs"}`, which is exactly why `agent` must never emit
  `event`/`status` for backup dispatches — it would otherwise appear in that unfiltered query
  alongside `brfs`/`bwfs`'s lines for the same `job_id` and break 1:1 pairing.
  `source_host`/`store_host` narrow the `hostname` label selector. Either turns a fleet-wide stream
  scan into a single- or few-stream lookup, actually using Loki's index rather than just its
  structured-metadata filter.
- **Default window stays 24h** (per product decision) rather than a tighter default — acceptable
  because structured metadata keeps the per-query cost low even at that window size; the 7-day cap
  exists as a backstop against unbounded requests, not because 24h itself is expensive.
- **Short in-memory TTL cache in `api-server`** (10s), keyed by the exact query params — a live-tail
  UI polling `/jobs` or `/jobs/{id}/logs` every few seconds would otherwise re-run a near-identical
  query on every poll.
- **Loki-side bounds**, added to both `loki-config.yaml`s: `limits_config.max_query_length` (matches
  the 7-day cap), `max_entries_limit_per_query`, and a query timeout — defense-in-depth so a broad or
  misbehaving query can't peg the single Loki instance, the same "generous but bounded" philosophy
  `log-gateway` already applies to push size/timeout. Exact values tuned during implementation.
- **Stated ceiling**: this is still a windowed query, not an indexed point lookup — cheaper than the
  original content-regex design by roughly the difference between "scan every line" and "check a
  per-line metadata filter," but not free. If fleet size or job volume grows enough for this to
  become a real bottleneck, that's a signal to revisit, not something this design solves preemptively
  (consistent with the original fleet-log design's own stated scale — single-instance Loki, no HA).

## Security Evaluation

- No new credential type — `api-server` calls `log-gateway`'s new query route with the same
  operating-tier mTLS credential it already holds for `clientmanager-api`/`catalog`.
- `log-gateway`'s query route is reachable by any operating-tier mesh node, not just `api-server` —
  an existing, already-accepted trust boundary in this project (see api-server's own doc), not a new
  gap introduced here.
- `job_id` charset validation on `/jobs/{job_id}/logs` prevents Loki query-string injection through
  that path parameter (the one genuinely new piece of unvalidated external input this design
  introduces into a query string).
- Log line *content* remains unverified against the sending node's real state, same accepted trust
  level the fleet-log-aggregation design already establishes — this spec doesn't change that.

## Configuration

No new `local.conf` keys — `log_gateway_host`/`log_gateway_port` already exist and are reused for the
new query route on the same listener.

## Testing

- Unit: `bwfs`/`agent`/`brfs` new `event`/`status` fields present on the right lines, stray `jobId`
  key removed.
- Unit: `log-gateway`'s new query route — auth gate, param passthrough, response-size cap — mirroring
  the existing push-handler tests.
- Unit: `api-server`'s start/finish pairing logic (`kind` derivation, `in_progress` with no finish
  line, `null started_at` with no start line, `truncated` flag, `job_id` charset validation) —
  fabricated Loki responses, no real Loki needed.
- Unit: Vector config template — confirms the rendered config includes the `structured_metadata`
  mapping and the JSON-decode step in `add_binary_label`.
- Integration/e2e: extend the existing throwaway-Loki e2e (from the fleet-log-aggregation design) to
  push known `event=start`/`event=finish` lines with structured metadata and confirm `/jobs` and
  `/jobs/{id}/logs` return them correctly end-to-end through `log-gateway`.

## Documentation Impact

- New `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` sections in `docs/api/rest-v1.md`.
- Update `docs/components/api-server.md` (new endpoints, the documented exception to its
  one-RPC-per-call rule) and `docs/components/log-gateway.md` (new query route).
- Update `docs/protocols/log-gateway.md` (the new `GET /loki/api/v1/query_range` proxy shape).
- Update `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`'s Vector config section
  to note the `structured_metadata` addition, or cross-link forward to this spec.
- `CHANGELOG.md` entry before merge, per this repo's standing convention.

## See Also

- [Design: api-server](2026-07-14-api-server-design.md)
- [Design: Fleet Log Aggregation](2026-07-11-fleet-log-aggregation-design.md)
- [REST API v1](../../api/rest-v1.md)
