# List Subprotocol + rwfs Design

## Overview

`rrfs` (Restore Reader for File System) was scaffolded as a stub but never
grew real functionality — and on reflection it's the wrong shape. Instead of
a separate restore-read service, `bwfs` (Backup Writer for File System)
already owns the storage and SQLite metadata; it should expose a **list
subprotocol** over gRPC so remote clients can query what's stored. `rwfs`
(Restore Writer for File System) becomes that remote client, starting with a
`list` subcommand. `rrfs` is deleted as obsolete.

This is the first step toward restore. A future "restore subprotocol" will
let `rwfs` actually pull chunks and reconstruct files; this design only
covers listing.

## Goals

- Add a `ListService` gRPC subprotocol to `bwfs`, separate from the existing
  `BackupService`, so storage contents can be queried remotely.
- Create `rwfs` with a `list` subcommand that queries a remote `bwfs` over
  gRPC and renders results identically to the existing local `bwfs list`.
- Update `bwfs list`'s CLI syntax to add source-host and path-prefix
  filtering, shared by both the local and remote list paths.
- Remove `rrfs` entirely.

## Non-goals

- Restore subprotocol (reading/streaming chunk data back) — future work.
- `rwfs restore` or any write-to-destination logic — future work.
- Authentication/authorization on the new RPC — out of scope, matches
  existing `BackupService` (insecure credentials, no auth).

## Architecture

```
                    ListService (new)         BackupService (existing)
                          ▲                          ▲
                          │ gRPC                      │ gRPC
                          │                          │
   rwfs list ─────────────┘                          └───────────── brfs
   (remote query)                  bwfs server                  (backup write)
                          │
                          ▼
                   SQLite metadata
                   (same store as today)
```

`bwfs server` registers both `BackupService` and `ListService` on the same
`grpc.Server`, using the existing `register func(*grpc.Server)` callback
already wired through `connection.StartServer`. No change needed to
`StartServer` itself — it already accepts an arbitrary registrar.

`bwfs list` (local) and `rwfs list` (remote) both end up calling the same
underlying query logic and the same rendering code; only the data-access
path differs (direct SQLite read vs. gRPC round-trip).

## Components

### 1. Remove `rrfs`

Delete:
- `src/cmd/rrfs/arguments.go`
- `src/cmd/rrfs/server.go`
- `src/cmd/rrfs/main.go`

Remove the `RRFS_CMD` variable, `rrfs` build target, and `rrfs` from the
`.PHONY` line in `src/Makefile`.

Update `docs/ARCHITECTURE.md`: drop the `rrfs` row from the components
table. `rwfs` replaces it as "Not yet implemented" (list is implemented,
restore is not) — update the table and the data-flow diagram to show `rwfs`
connecting to `bwfs` for both list and (future) restore, rather than `rrfs`
connecting to a separate restore-read service.

### 2. `src/api/list.proto` (new)

```proto
syntax = "proto3";

package listservice;

option go_package = "./proto";

service ListService {
  rpc ListFiles(ListRequest) returns (ListResponse);
}

message ListRequest {
  string server_name = 1; // source hostname filter; empty = all sources
  string path        = 2; // prefix filter on file path; empty = no filter
  string filter      = 3; // free-text substring filter; empty = no filter
}

message ListResponse {
  repeated FileRow rows = 1;
}

message FileRow {
  string file_data_id = 1;
  string source        = 2;
  string type          = 3;
  string path          = 4;
  int64  timestamp      = 5;
  int64  size           = 6;
  int32  chunks         = 7;
  int64  versions       = 8;
}
```

Generated into the same `src/api` proto output directory as `backup.proto`
(separate generated file, e.g. `list.pb.go` / `list_grpc.pb.go`), following
whatever proto-gen Makefile target already produces `backup.pb.go`.

### 3. Shared query logic

`queryFileRows` (currently in `src/cmd/bwfs/list.go`) is extended to accept
`serverName` and `pathPrefix` filters in addition to the existing `filter`
substring:

```go
func queryFileRows(store *wfs.Store, serverName, pathPrefix, filter string) ([]fileRow, error)
```

- `serverName` filters on the hostname component of `file_id`
  (`fs://hostname:type:path:mtime`) — exact match, empty = no filter.
- `pathPrefix` filters on the path component as a **prefix** match, empty =
  no filter.
- `filter` remains a substring match anywhere in the path, composing with
  the above (existing behavior, unchanged).

This function moves to (or is called from) a location both `bwfs` and the
new server-side list handler can reach — it already lives alongside
`wfs.Store`, so no package move is required; the gRPC handler in `bwfs`
calls it directly since it runs in-process.

### 4. Shared rendering code

`fileRow`, `renderTable`, `renderJSON`, and `formatSize` (currently in
`src/cmd/bwfs/list.go`) move to a new shared package, e.g.
`src/common/listformat`, so `rwfs` can render gRPC `FileRow` results
identically to how `bwfs list` renders local query results. Both `bwfs` and
`rwfs` convert their respective row types (`queryResult` / proto `FileRow`)
into the shared `listformat.Row` and call `listformat.RenderTable` /
`listformat.RenderJSON`.

### 5. `bwfs` server-side: `ListService` handler

New file `src/cmd/bwfs/listserver.go`:

```go
type listServer struct {
    listpb.UnimplementedListServiceServer
    store  storage.BackupStore // or *wfs.Store, matching queryFileRows' signature
    logger *slog.Logger
}

func (s *listServer) ListFiles(ctx context.Context, req *listpb.ListRequest) (*listpb.ListResponse, error) {
    rows, err := queryFileRows(s.store, req.ServerName, req.Path, req.Filter)
    ...
    return &listpb.ListResponse{Rows: ...}, nil
}
```

`bwfs server` (in `main.go`) registers it alongside `BackupService`:

```go
if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {
    pb.RegisterBackupServiceServer(s, backupServer)
    listpb.RegisterListServiceServer(s, listServer)
}); err != nil { ... }
```

### 6. `bwfs list` CLI syntax change

New syntax:

```
bwfs <storage_path> list [[server_name:]path] --filter <text> --output table|json
```

- The `[[server_name:]path]` positional is itself optional. Omitted = no
  source/path filter (current behavior, preserved).
- When given, split on the **first colon only**: text before the colon is
  `server_name` (optional — a leading `:path` means no server filter), text
  after is `path` (required once the positional is present at all, matches
  as a **prefix**). This allows paths containing colons (e.g. Windows
  `C:/foo`) to pass through as `myhost:C:/foo`.
- `--filter` is unchanged — a separate free-text substring filter that
  composes with the positional filters.
- `--output` is unchanged.

### 7. `rwfs` (new)

```
src/cmd/rwfs/
  arguments.go
  list.go
  main.go
```

Syntax:

```
rwfs list [[server_name:]path] <bwfs_host:port> --filter <text> --output table|json
```

- Same `[[server_name:]path]` parsing rules as `bwfs list`.
- When `server_name` is omitted, it defaults via `common.GetHostname()` —
  the exact mechanism `brfs` uses today
  (`common.HostnameContextKey`/`common.GetHostname()`), so "the current
  one" means the hostname of the machine running `rwfs`.
- `<bwfs_host:port>` is a required positional — the gRPC connection target.
- `list.go`'s `runList` connects via `connection.Connect`, calls
  `ListService.ListFiles`, converts the response into `listformat.Row`s, and
  renders via the shared `listformat` package (table/json, same as `bwfs
  list`).
- No `restore` subcommand in this design — `list` only.

### 8. `connection.Connect` generalization

Current signature returns a hardcoded `pb.BackupServiceClient`. Change to:

```go
func Connect(host string, port, timeout int) (*grpc.ClientConn, error)
```

Callers wrap the returned `*grpc.ClientConn` with whichever generated client
they need:

```go
// brfs (update call site in src/cmd/brfs/main.go)
conn, err := connection.Connect(arguments.WriterHost, arguments.WriterPort, 5)
client := pb.NewBackupServiceClient(conn)

// rwfs
conn, err := connection.Connect(host, port, 5)
client := listpb.NewListServiceClient(conn)
```

`checkConnection` and the connection-readiness logic are unchanged — they
operate on `*grpc.ClientConn` already.

## Error handling

- `ListFiles` returns a standard gRPC error (e.g. via `status.Errorf`) if
  the underlying SQLite query fails — `rwfs list` surfaces this as a CLI
  error to stderr and a non-zero exit, matching `bwfs list`'s existing error
  handling pattern.
- Connection failures in `rwfs list` (bwfs unreachable, timeout) use the
  same `connection.Connect` error path `brfs` already relies on.
- Malformed `[server_name:]path` positional (e.g. empty path after a colon)
  is a CLI argument validation error caught in `parseArguments`, consistent
  with existing `bwfs`/`brfs` argument validation.

## Testing

- Unit tests for the first-colon-split parsing helper (shared between `bwfs`
  and `rwfs` argument parsing) covering: no positional, path-only, path with
  colon, `server:path`, leading-colon (`:path`), empty path after colon
  (error).
- Unit tests for `queryFileRows` with the new `serverName`/`pathPrefix`
  params (extends existing `list_test.go` coverage).
- Integration-level test (mirroring `bwfs/integration_test.go` patterns) for
  `ListService.ListFiles` against a real SQLite-backed store.
- New `rwfs` e2e or integration test: start a `bwfs server`, run `rwfs list`
  against it, assert rendered output matches what local `bwfs list` would
  produce against the same storage path.

## Migration / cleanup

- `rrfs` scaffold code, its Makefile target, and its mention in
  `ARCHITECTURE.md` are removed in this change — no deprecation period
  needed since `rrfs` never shipped real functionality.
