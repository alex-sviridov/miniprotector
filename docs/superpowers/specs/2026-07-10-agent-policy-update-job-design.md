# Agent Policy-Update Job — Design

> Builds on [`policy-server`](../components/policy-server.md) (serves backup policies, filtered by
> client identity) and `agent`'s existing embedded-policy reconcile loop (`agent.md`, phase 2c's
> `bootstrap-refresh`/`operating-refresh`). This adds the third standard job: fetching policies
> from `policy-server` into a local cache. It deliberately stops there — nothing in this phase acts
> on the fetched policies.

## Problem

`docs/components/agent.md` currently states: "`policy-server` now exists as a standalone
component serving backup policies, but `agent` does not yet fetch from it — no policy-driven
scheduling is wired into `agent`'s reconcile loop." Nothing in the codebase calls `GetPolicies`.
This phase closes the fetching gap only: `agent` gains a standard job that periodically pulls the
current policy list from `policy-server` and persists it locally, on the same reconcile machinery
that already keeps this node's mTLS credentials fresh. Actually scheduling backups off that cache
is explicitly deferred — see Non-Goals.

## Goals

- `agent` runs a third standard job, `policy-update`, on the same reconcile/backoff/cache
  machinery as `bootstrap-refresh`/`operating-refresh` — no new scheduling concept introduced.
- The fetched policy list lands in a local, atomically-written cache file that survives restarts
  and reflects a fetch failure the same fail-safe way this codebase already handles it elsewhere:
  the previous good cache is left untouched, never cleared or partially overwritten.
- Authentication reuses this node's existing operating credential (`client.crt`/`client.key`) —
  the same certificate `policy-server` already reads attribute labels from — no new credential
  tier introduced.

## Non-Goals (this pass)

- **No consumption of the fetched policies.** Nothing reads `policies-cache.json` to schedule,
  run, or otherwise act on a backup job. The cache is written and refreshed; it sits inert until a
  future phase wires a scheduler to it. This is an explicit, deliberate stopping point, not an
  oversight.
- **No change to `policy-server` itself.** `GetPolicies`'s handler, matching semantics, and hot
  reload are all untouched; this phase only builds a caller.
- **No new credential tier or connection helper.** `policyclient` uses the same default
  `connection.Connect`/operating-credential path every other mesh component
  (`bwfs`/`brfs`/`rwfs`/`catalogsync`/`catalog`) already uses — nothing like `certclient`'s
  explicit-filename bootstrap-credential path is needed here.

## Architecture

### `policyclient` — a new binary, mirroring `certclient`'s shape

A new binary, built and deployed the same way as every other colocated binary `agent` execs
(`make policyclient`, sibling to `agent` in the same directory). Single subcommand:

- **`policyclient fetch`** — connects to `policy-server` at `PolicyServerHost`:`PolicyServerPort`
  using this node's existing operating credential (`connection.Connect(host, port, timeout,
  certsDir)`, the same default-identity path `bwfs`/`catalog`/etc. already use — deliberately
  *not* `certclient`'s explicit-filename bootstrap path, since `policy-server`'s matching logic
  reads attribute labels off the operating certificate specifically, not the bootstrap one). Calls
  `GetPolicies(ctx, &pb.GetPoliciesRequest{})`, then writes the response's policy list to
  `<var_dir>/policies-cache.json`.
- **Cache shape:** a plain JSON array, one object per returned `Policy`, using the same fields the
  RPC response already defines — `name`, `created_at`, `updated_at`, `object_filters`, `rpo`,
  `backup_window` — converted directly from the protobuf message (`Timestamp.AsTime()` for the two
  timestamp fields). No new schema, no wrapper object, nothing invented.
- **Write path:** temp file in the same directory, then rename over the target — identical to
  `agent`'s own `writeCache` convention in `cache.go`, for the same reason (a crash mid-write never
  leaves a torn cache file).
- **On any failure** (unreachable `policy-server`, RPC error, marshal error): non-zero exit, the
  existing cache file is left completely untouched — mirrors both `agent`'s "leave `client.crt`
  untouched on failure" convention (`certclient operating-refresh`) and `policy-server`'s own
  "keep the previous good in-memory cache if a reload fails" behavior. No special-casing between
  failure kinds; `agent`'s existing backoff handles all of them identically, same direction as
  `operating-refresh`'s error handling.

### `agent`: third policy, no changes to reconcile machinery

One new entry in `policies()` (`src/cmd/agent/policy.go`):

```go
{ID: "policy-update", Binary: "policyclient", Args: []string{"fetch"},
 Interval: time.Duration(conf.PolicyFetchIntervalSec) * time.Second},
```

`reconcile.go`'s generic due-check/backoff/cache-record loop needs no changes — this is exactly
the "adding a third policy is a small diff" case the existing design already anticipated for a
second policy in phase 2c.

## Data Flow

**Ongoing reconcile** (`agent serve`, alongside the existing two policies):
```
policy-update (every PolicyFetchIntervalSec, default 900s):
  exec policyclient fetch
    -> connect to policy-server (operating credential, connection.Connect default identity)
    -> call GetPolicies() -> current policy list for this node's hostname + attribute labels
    -> success: atomically write <var_dir>/policies-cache.json
    -> failure: non-zero exit, policies-cache.json untouched; agent's existing backoff takes over
```

The cache sits there, refreshed on this cadence, until a future phase reads it.

## Configuration

One new `local.conf` key, following the existing `*IntervalSec` convention:

| Key | Default | Used by |
|---|---|---|
| `PolicyFetchIntervalSec` | `900` (15 min, matching `operating-refresh`'s existing cadence) | `agent`'s `policy-update` policy interval |

`PolicyServerHost`/`PolicyServerPort` already exist (added alongside `policy-server` itself,
default port `9300`) — `policyclient` is simply their first consumer.

## Testing

- Unit: `policyclient`'s fetch-and-write logic against a fake `PolicyServiceClient` (mirrors
  `certclient operating-refresh`'s `issuerClient`-interface fake pattern) — a success response
  writes the cache file; an RPC error leaves a pre-existing cache file byte-for-byte untouched.
- Unit: cache read/write round-trip and atomic-write-leaves-no-temp-file (mirrors `agent`'s
  existing `cache_test.go` style, applied to `policies-cache.json`).
- Unit: `agent`'s `policies()` includes `policy-update` with its interval read from
  `conf.PolicyFetchIntervalSec` (mirrors the existing test coverage for the other two policies).
- Integration (build-tag gated, mirrors `cmd/issuer/e2e_test.go`'s real-server pattern): a real
  `policy-server` instance serving a known policy file, a genuine `policyclient fetch` against it,
  confirming the on-disk cache matches the server's configured policy exactly.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule:

- New `docs/components/policyclient.md`.
- Update `docs/components/agent.md` — add the `policy-update` row to the policy table, the new
  config key, and correct the now-stale "`agent` does not yet fetch from it" line (it fetches now;
  it still doesn't *act* on what it fetches).
- Update `docs/components/policy-server.md`'s See Also to cross-link `policyclient`.
- Update `README.md` if the component list or documentation index needs it.
- `CHANGELOG.md` entry before merge, per the standing rule.

## Future Work: config-driven policy list

`agent`'s policy list (`policies()`) is, today, three compiled-in Go literals — a deliberate choice
carried forward unchanged from agent v1 and phase 2c, and one this phase does not disturb. Once a
third hardcoded entry exists, the shape of a plausible future step becomes clearer: moving the
policy list (`ID`/`Binary`/`Args`/`Interval`) out of Go source and into `local.conf` or a small
dedicated file, so operators can add, remove, or retune standard jobs without a rebuild. Nothing
about this phase requires that move — three literals is still a small, readable list — but it is
worth flagging now, before a fourth or fifth job makes the case for it more pressing. Deliberately
left undesigned here: what such a config format would look like, how it would validate
`Binary`/`Args` against a safe allowlist (a config-driven exec list is a materially different trust
boundary than a compiled-in one), and whether it belongs to `agent` alone or to a broader
config-driven-jobs mechanism shared with other components.

## Relationship to Prior Work

This is the fetching half of the gap `docs/components/agent.md` and
`docs/superpowers/specs/2026-07-10-policy-server-design.md` both flagged as deferred: `policy-server`
is real, tested, and reachable; after this phase, `agent` reaches it on a standard schedule and
keeps a local, fail-safe cache of the result. What remains deferred, unchanged by this phase, is
turning that cache into anything that actually runs a backup — a separate, later design.
