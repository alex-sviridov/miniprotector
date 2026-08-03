# Design: Policy Check-in Tracking

## Problem

`policy-server` currently has no record of which hosts have actually received a
given policy, or when. An operator can see which policies *would* match a
host (`client_filters`), but not which hosts have *actually* polled and been
handed a policy, or how recently. This makes it hard to answer "is this
policy actually reaching anyone?" or "has host X stopped checking in?".

## Goals

- Every time `GetPolicies` hands a policy to a host, record that fact:
  policy, hostname, timestamp.
- Expose, per policy, the set of hosts that have checked in and when each
  last did so.
- Bound the data's growth: a background routine periodically purges
  check-ins from hosts that haven't re-polled in a configurable window.

## Non-goals

- Full check-in history (every individual poll). Only the most recent
  check-in per `(policy, host)` pair is kept — see "Data model" below.
- Any change to `GetPolicies`'s matching/authorization behavior, or to how
  `agent`/`policyclient` consume it.
- Cross-service aggregation — this is `policy-server`-local state, the same
  way its policy cache is.

## Architecture

`policy-server` gains its first piece of persistent state: a local SQLite
database, following the same pattern already used by `catalog`
(`src/storage/catalog`) and `clientmanager-api`
(`src/storage/clientmanager`) — `gorm` + `modernc.org/sqlite`, WAL journal
mode, a single connection (`SetMaxOpenConns(1)`), `AutoMigrate` at startup.
The file lives at `<var-dir>/policy-server.sqlite`, where `<var-dir>` is
`config.ResolveVarDir` — the same directory `issuer`, `clientmanager-api`,
`clientmanager-admin-api`, and `policyclient` already use for their own
runtime state.

This supersedes the current claim (in `main.go`'s package comment and
`docs/components/policy-server.md`) that "`policy-server` holds no database
and calls no other service" — that line is updated as part of this change.
`policy-server` still calls no other *service*; it now owns local state, the
same way `catalog` and `clientmanager-api` do.

New package: `src/storage/policyserver` (package name `policyserver`),
mirroring the `db.go` / `models.go` / `store.go` (+ `store_test.go`) layout
of the existing storage packages.

## Data model

One table, one row per `(policy_id, hostname)` pair, **upserted** on every
check-in rather than appended as a growing log:

```go
type CheckinRecord struct {
    PolicyID   string `gorm:"primaryKey"`
    Hostname   string `gorm:"primaryKey"`
    LastSeenAt time.Time
}
```

Each row already holds a host's *latest* check-in for a policy, so "list of
hosts and timestamps for a policy" is a direct `WHERE policy_id = ?` scan —
no `GROUP BY`/`MAX` aggregation needed. It also gives the cleanup routine a
direct meaning: a row surviving past the retention window means that host
hasn't re-polled this policy in that long (decommissioned, re-pointed to a
different policy set, etc.) — deleting it removes a host that has
effectively stopped checking in, not just old log lines. An append-only log
of every poll was considered and rejected: this project only needs the
deduplicated, per-host view, so the extra rows and read-time aggregation
would be pure overhead.

## Write path: check-in on `GetPolicies`

In `policyServerServer.GetPolicies` (`src/cmd/policy-server/server.go`), for
every policy matched and returned to the calling host, upsert
`(policy.ID, hostname, now)`. This covers every policy type `GetPolicies`
can return today (`"backup"` and `"storage"`) — the check-in mechanism is
generic over policy type, the same way `GetPolicies` itself is.

The upsert uses GORM's `clause.OnConflict` (`Save` alone does not insert new
rows once given a already-set composite primary key, so plain `Save` isn't
enough here):

```go
db.Clauses(clause.OnConflict{
    Columns:   []clause.Column{{Name: "policy_id"}, {Name: "hostname"}},
    DoUpdates: clause.AssignmentColumns([]string{"last_seen_at"}),
}).Create(&CheckinRecord{PolicyID: id, Hostname: hostname, LastSeenAt: now})
```

**Failure handling: fail closed.** If the check-in write errors, `GetPolicies`
logs the error and returns it, the same as every other failure already
handled in that method (`mtls.PeerHostname`, `jobid.FromIncoming`,
`mtls.PeerAttributes`) — no policies are returned to the caller. This keeps
`GetPolicies`'s error handling uniform: any failure partway through the
method aborts the whole call rather than returning a partial/best-effort
result.

## Cleanup routine

A background goroutine, started from `main.go` the same way the existing
`watchForReload` fsnotify watcher is started, ticks on a **fixed 1-minute
interval** (not configurable) and deletes every `CheckinRecord` whose
`LastSeenAt` is older than `now - CheckinRetentionSec`. Lives in a new
`src/cmd/policy-server/checkin.go`, stopped via the same `signalCtx` every
other background loop in this binary already respects.

New `Config` field / `local.conf` key: `CheckinRetentionSec`, default
`86400` (24h), following the naming and validation convention of
`AdhocPolicyTimeoutSec` (rejects zero/negative). This is a `local.conf` key
parsed into the `Config` struct — like every other tunable in this project —
not a raw OS environment variable.

## API changes

### Proto

`Policy` gains a `checkins` field, populated **only by `ListPolicies`** —
`GetPolicies`, `CreatePolicy`, and `UpdatePolicy` leave it empty, the same
way `GetPolicies`'s response never echoes back `client_filters`:

```proto
message PolicyCheckin {
  string hostname = 1;
  google.protobuf.Timestamp last_seen_at = 2;
}

message Policy {
  // ...existing fields 1-15 unchanged...
  repeated PolicyCheckin checkins = 16;
}
```

`ListPolicies`'s handler (`src/cmd/policy-server/server.go`) queries
`Store.CheckinsForPolicy(id)` once per policy after building each
`pb.Policy`, the same per-policy-lookup shape `attachDestination` already
uses. One query per policy — acceptable at this project's scale
(operator-maintained policy counts); no batching/`JOIN` needed.

### REST (`api-server`)

`policies.go`'s `toPolicyDTO` gains:

```go
type checkinDTO struct {
    Hostname   string `json:"hostname"`
    LastSeenAt int64  `json:"last_seen_at"`
}
```

`policyDTO.Checkins []checkinDTO`. Because `GET /api/v1/policies` and
`GET /api/v1/policies/{id}` both already call the `ListPolicies` RPC
internally (`handleListPolicies`, `handleGetPolicy`), both endpoints pick up
`checkins` automatically — no additional wiring in `api-server` beyond the
DTO field and its mapping in `toPolicyDTO`.

## Testing

- `src/storage/policyserver/store_test.go` (mirroring
  `clientmanager/store_test.go`'s style): upsert overwrites `LastSeenAt`
  rather than duplicating a row; `CheckinsForPolicy` scopes correctly by
  `policy_id`; `DeleteOlderThan` boundary behavior (older-than vs.
  exactly-at-cutoff).
- `src/cmd/policy-server` server tests: a check-in row exists after
  `GetPolicies`; `ListPolicies` surfaces it in `checkins`; a simulated store
  failure fails `GetPolicies` (fail-closed behavior above).
- Cleanup routine: tested by calling the delete function directly with
  controlled timestamps, not by waiting on the real 1-minute ticker.

## Documentation updates

Per this repo's `CLAUDE.md` rules (feature change + new persistent state):

- `docs/components/policy-server.md` — replace the "holds no database"
  claim; document the SQLite store, the cleanup routine, and
  `CheckinRetentionSec`.
- `docs/protocols/policy-server.md` — document `PolicyCheckin`/
  `Policy.checkins`, and that only `ListPolicies` populates it.
- `docs/api/rest-v1.md` — document `checkins` in the policy DTO.
- `docs/ARCHITECTURE.md` — update if the topology diagram/text currently
  depicts `policy-server` as stateless.
- `CHANGELOG.md` — entry before merging to `main`.
