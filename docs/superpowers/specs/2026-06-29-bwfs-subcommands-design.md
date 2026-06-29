# bwfs Subcommands: server + list

**Date:** 2026-06-29
**Status:** Approved

## Problem

`bwfs` currently does one thing: start the backup server. As the tool grows it needs a CLI structure that can host multiple actions against the same storage path. The first new action is `list` — inspect stored file data in table or JSON form.

## Scope

Four files change:

- `src/cmd/bwfs/arguments.go` — rewritten to use cobra subcommands
- `src/cmd/bwfs/main.go` — add `switch` on `Action`; server path unchanged
- `src/cmd/bwfs/list.go` — new file, all list logic
- `src/storage/filesystem/store.go` — add `NewReadOnly` constructor

No changes to storage interface, models, handler, server, or any other file.

## CLI Shape

```
bwfs <storage_path> server [--port 15722] [--debug] [--quiet]
bwfs <storage_path> list   [--output table|json] [--filter <text>] [--debug]
```

`storage_path` is a positional arg on the root command, shared by all subcommands.

## Design

### `arguments.go`

Root cobra command: `bwfs <storage_path>`, `Args: cobra.ExactArgs(1)`. Two subcommands registered on the root:

- `server` — flags: `--port` (default from config), `--debug`, `--quiet`
- `list` — flags: `--output` (default `"table"`), `--filter` (default `""`), `--debug`

Each subcommand's `Run` sets `arguments.Action` to its name. `parseArguments` returns after `rootCmd.Execute()`. If no subcommand is given, cobra prints usage and `Execute()` returns an error.

`Arguments` struct:

```go
type Arguments struct {
    StoragePath string
    Action      string  // "server" | "list"
    // server flags
    Port        int
    Debug       bool
    Quiet       bool
    // list flags
    Output      string  // "table" | "json"
    Filter      string
}
```

`--output` validation (must be `"table"` or `"json"`) happens inside `parseArguments` after `Execute()`.

### `main.go`

Logger init, config parse, and context setup are unchanged and run before the switch. `NewBackupServer` (which opens the store) is only called on the server path.

```go
switch arguments.Action {
case "server":
    backupServer, err := NewBackupServer(ctx, logger, arguments.StoragePath)
    // ... existing server startup
case "list":
    if err := runList(logger, arguments.StoragePath, arguments.Output, arguments.Filter); err != nil {
        logger.Error("List failed", "error", err)
        os.Exit(1)
    }
}
```

### `store.go` — `NewReadOnly`

```go
func NewReadOnly(basePath string) (*Store, error) {
    db, err := openDB(basePath)
    if err != nil {
        return nil, fmt.Errorf("open db: %w", err)
    }
    return &Store{basePath: basePath, db: db}, nil
}
```

No flock is acquired. The flock's purpose is to prevent two *server* processes from corrupting the store simultaneously — a read-only `list` operation poses no such risk. SQLite WAL mode allows concurrent readers alongside an active writer with no blocking. The existing `_busy_timeout=5000` in `openDB` handles any transient write contention at the DB layer.

`Close()` on a `NewReadOnly` store closes the DB connection only (`lockFile` is nil — `Close` must guard against this):

```go
func (s *Store) Close() error {
    sqlDB, err := s.db.DB()
    if err != nil {
        return err
    }
    if err := sqlDB.Close(); err != nil {
        return err
    }
    if s.lockFile != nil {
        return s.lockFile.Close()
    }
    return nil
}
```

A new single-purpose accessor is also added — not on the `BackupStore` interface, only for CLI tooling:

```go
// RawDB returns the underlying *gorm.DB for read-only administrative queries.
func (s *Store) RawDB() *gorm.DB { return s.db }
```

### `list.go`

Single entry point: `runList(logger *slog.Logger, storagePath, output, filter string) error`.

#### Store access

Opens via `wfs.NewReadOnly(storagePath)`, defers `Close()`. Can run concurrently with a live `bwfs server` process on the same storage path.

#### Query

One GORM query with a LEFT JOIN and GROUP BY — no N+1:

```sql
SELECT
    fd.id          AS file_data_id,
    fd.file_id,
    fd.size,
    fd.chunk_count AS chunks,
    fd.created_at,
    COUNT(fv.id)   AS versions
FROM file_data_records fd
LEFT JOIN file_version_records fv ON fv.file_id = fd.file_id
WHERE fd.checksum IS NOT NULL
  [AND fd.file_id LIKE '%<filter>%']
GROUP BY fd.file_id
ORDER BY fd.created_at ASC
```

Result mapped into an internal slice of `fileRow`:

```go
type fileRow struct {
    FileDataID string
    FileID     string
    Source     string
    Type       string
    Path       string
    Timestamp  int64
    Size       int64
    Chunks     int
    Versions   int64
    CreatedAt  time.Time
}
```

#### `file_id` parsing

Format: `fs://host:type:path:mtime` where type is one character and path may contain colons (Windows `C:/foo`).

Parse steps:
1. Strip `fs://` prefix
2. Split on `:` — tokens = `[host, type+path+mtime...]`
3. `source` = tokens[0]
4. `fileType` = tokens[1] (single char — the whole token is always one char)
5. `timestamp` = last token (parse as int64)
6. `path` = tokens[2 : len-1] rejoined with `:`

Invalid `file_id` strings (wrong prefix, too few tokens) produce `source="?"`, `type="?"`, `path=file_id`, `timestamp=0` rather than an error — a malformed ID should not abort the list.

#### Table output

`text/tabwriter`, written to `os.Stdout`. Header row in uppercase.

Columns: `SOURCE`, `TYPE`, `PATH`, `TIMESTAMP`, `SIZE`, `CHUNKS`, `VERSIONS`

`SIZE` formatted as human-readable: `< 1024` → `N B`, `< 1 MiB` → `N KB`, `< 1 GiB` → `N MB`, else `N GB`. Uses integer arithmetic only, no `math` import.

`TIMESTAMP` printed as unix epoch integer (matches the raw value in the file_id — user can correlate).

#### JSON output

`encoding/json` with `json.MarshalIndent`, written to `os.Stdout`.

Output is a JSON array. Each element:

```json
{
  "file_data_id": "3e4a...",
  "source":       "workstation",
  "type":         "f",
  "path":         "/var/log/spooler",
  "timestamp":    1782605538,
  "size":         4096,
  "chunks":       3,
  "versions":     2,
  "created_at":   "2026-06-29T08:10:42Z"
}
```

`created_at` formatted as RFC3339 UTC. `timestamp` is the raw unix mtime integer (not formatted as time — it's part of the ID, not a wall-clock display value).

Empty result set: table prints header only; JSON prints `[]`.

## Invariants

- `--filter` is a substring match on the raw `file_id`, applied in SQL (not in Go after fetch)
- `--output` values other than `table`/`json` are rejected in `parseArguments` before the store is opened
- The `BackupStore` interface gains no new methods
- `NewReadOnly` acquires no flock — safe to run alongside a live server; SQLite WAL handles concurrency
- `Close()` on a read-only store skips `lockFile.Close()` (lockFile is nil)
- `RawDB()` and `NewReadOnly` are the only additions to `*Store`; neither is on the interface
- Server startup path in `main.go` is byte-for-byte equivalent to today's behavior
