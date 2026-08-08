# Design: Storage Connection Foundation

## Problem

Four SQLite-backed stores (`storage/catalog`, `storage/policyserver`, `storage/clientmanager`,
`storage/filesystem`'s `ReplicaReader`) each hand-roll the same ~40-line `openDB`: create the dir,
open via `database/sql` with `_busy_timeout=5000`, `SetMaxOpenConns(1)`, set `PRAGMA
journal_mode=WAL`, wrap in GORM with a silent logger, `AutoMigrate`. The duplication makes the
pattern easy to get subtly wrong in one place and not the others, and obscures the one place that
actually needs different behavior: `catalog`.

Separately, no `Store` method on any of these three packages accepts a `context.Context`, so a
gRPC handler's cancellation/deadline never reaches the database call. Combined with the shared
single-connection pool, one slow or stuck query can queue up every other request behind it with no
way to abort it.

`catalog` in particular now serves two concurrent workloads on the same connection: continuous
`SyncFileVersions` writes from every `bwfs` node in the fleet, and read-heavy queries
(`ListEntries`, three facet endpoints, `ListDirectoryChildren`) from the web catalog view. SQLite's
WAL mode is designed to let readers run concurrently with a single writer, but `SetMaxOpenConns(1)`
forecloses that — reads queue behind writes and behind each other.

This is scoped to `catalog`, `policyserver`, and `clientmanager`, plus `filesystem.ReplicaReader`
(small, and the thing `catalogsync` — catalog's write-side counterpart — reads from). It explicitly
excludes `filesystem.Store` (`bwfs`/`brfs`'s backing store): 25 methods on the hot chunk
read/write backup/restore path, a materially bigger and riskier surface than the three
control-plane stores combined, left for a separate future pass.

## Goals

- One shared implementation of the SQLite-open boilerplate, used by all four packages.
- Every `Store`/`ReplicaReader` method threads `context.Context` through to the underlying query,
  so a caller's cancellation/timeout actually takes effect.
- `catalog` gets a connection topology that lets reads proceed concurrently with writes.

## Non-goals

- Changing `policyserver`/`clientmanager` to a multi-connection pool — their traffic is low-QPS
  admin usage; a second pool would be complexity without a measured payoff, and cuts against this
  project's stated preference (`storage/CLAUDE.md`) for simplicity over premature optimization.
- Touching `filesystem.Store` (see Problem, above).
- Any behavior change to what a query returns — this is purely about how the connection is opened
  and how cancellation propagates.

## Design

### Shared `Open` helper

New package `storage/sqlitedb`, one exported function:

```go
package sqlitedb

type Options struct {
    Path     string // full path to the db file, e.g. filepath.Join(basePath, "catalog.db")
    ReadOnly bool   // opens with mode=ro; skips MkdirAll and AutoMigrate
    MaxConns int    // connection pool size; 0 defaults to 1
    Models   []any  // AutoMigrate targets; ignored when ReadOnly or empty
}

func Open(opts Options) (*gorm.DB, error)
```

`Open` absorbs: `MkdirAll` on the containing directory (skipped when `ReadOnly` — a read-only
opener must never be the thing that creates a store), building the DSN (`_busy_timeout=5000`, plus
`mode=ro` when `ReadOnly`), `sql.Open("sqlite", ...)`, `SetMaxOpenConns` (1 if `MaxConns` is 0),
`PRAGMA journal_mode=WAL` (skipped when `ReadOnly` — see ordering note below), `gorm.Open` with a
silent logger, and `AutoMigrate(opts.Models...)` when non-empty.

Each package's own constructor becomes a thin wrapper:

```go
// storage/catalog
func New(basePath string) (*Store, error) {
    db, err := sqlitedb.Open(sqlitedb.Options{
        Path:   filepath.Join(basePath, "catalog.db"),
        Models: []any{&EntryRecord{}, &DirectoryRecord{}},
    })
    ...
}
```

`filesystem.OpenReplicaReader` switches to the same helper with `ReadOnly: true`. Today it doesn't
set `PRAGMA journal_mode=WAL` at all (it just opens `mode=ro`); the shared helper standardizes this
— see the ordering note below for why a read-only opener skips the pragma rather than setting it
redundantly.

### `catalog`'s dual pool

`catalog.Store` gains a second `*gorm.DB`, opened read-only with `MaxConns: 4`
(`sqlitedb.Open(sqlitedb.Options{Path: ..., ReadOnly: true, MaxConns: 4})`) — a small fixed
constant rather than a new config knob or a `NumCPU()`-derived value: SQLite's read concurrency
benefit under WAL plateaus quickly, there's no measured read-QPS to size against yet, and a
hardcoded default matches this codebase's existing convention of not exposing tunables nothing
calls for (e.g. `_busy_timeout=5000` is already a hardcoded constant, not a config field). Read
methods
(`ListEntries`, `ListClientFacets`, `ListJobFacets`, `ListDirectoryFacets`, `ListDirectoryChildren`,
`Count`) use it; write methods (`EnsureEntries`, `EnsureDirectories`) keep using the single writer
connection. `Store.Close()` closes both.

**Ordering constraint:** WAL is a property of the database file itself, not a per-connection
setting — once set, every subsequent connection that opens the file sees WAL automatically. That
means the writer pool (which sets the pragma and runs `AutoMigrate`) must open, and the schema must
exist, before the reader pool's first connection touches the file. In practice this just means
`catalog.New` opens the writer first, then the reader pool, in that order, inside the same
constructor call — no separate startup-sequencing concern across processes, since both pools live
inside the one `catalog` process.

`policyserver`/`clientmanager` call `sqlitedb.Open` once, `MaxConns` omitted (defaults to 1) — no
change in connection topology, only in where the code that opens them lives.

### Context propagation

Every method on `catalog.Store`, `policyserver.Store`, `clientmanager.Store`, and
`filesystem.ReplicaReader` gains `ctx context.Context` as its first parameter, and calls
`.WithContext(ctx)` on the relevant `*gorm.DB` (writer or reader, for catalog) before issuing the
query. Every call site identified so far already has a `ctx` in scope — gRPC handlers receive one
per-RPC, and `catalogsync`'s `run()` loop already threads one through, it just needs to reach
`FileVersionsSince`. No call site needs a `context.Background()` fallback.

Call sites to update: `cmd/catalog`, `cmd/catalogsync`, `cmd/policy-server`, `cmd/clientmanager-api`,
`cmd/clientmanager-admin-api`, and the `clientmanager` CLI (`add.go`/`list.go`/`label.go`/`san.go`).
All are mechanical signature-threading with no logic change.

## Testing

Existing `*_test.go` files construct a `Store` directly and call its methods; the constructor
signature (`New(basePath)`) is unchanged, so only the per-method calls need a `ctx` argument —
`t.Context()` (stdlib since Go 1.24; this repo is on Go 1.26) is the natural choice in tests,
consistent with not needing any custom cancellation behavior in the test suite itself.

## Risks

- Mechanical but wide: touches every `Store` method's signature and every call site across five
  `cmd/*` packages. Low logic risk, moderate diff size.
- The dual-pool ordering constraint (writer must fully open, migrate, and set WAL before the reader
  pool opens) is a real invariant, not just documentation — worth a code comment at the call site
  in `catalog.New`, and worth double-checking `modernc.org/sqlite` doesn't need the WAL pragma
  re-asserted per-connection (standard SQLite behavior says no; flagging in case the pure-Go driver
  differs).
