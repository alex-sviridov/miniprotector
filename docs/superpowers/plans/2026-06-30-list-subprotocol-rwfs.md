# List Subprotocol + rwfs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `ListService` gRPC subprotocol to `bwfs`, create `rwfs` as a remote client with a `list` subcommand, update `bwfs list`'s CLI syntax to add source/path filtering, and delete the obsolete `rrfs` stub.

**Architecture:** `bwfs server` registers a new `ListService` alongside the existing `BackupService` on the same `grpc.Server`. Both `bwfs list` (local SQLite read) and `rwfs list` (gRPC call to a remote `bwfs`) share query logic (`queryFileRows`, extended with `serverName`/`pathPrefix` filters) and rendering logic (extracted into `common/listformat`). `connection.Connect` is generalized to return a raw `*grpc.ClientConn` so callers can wrap it with any generated client.

**Tech Stack:** Go 1.26, Cobra (CLI), gRPC (`google.golang.org/grpc`), protobuf (`google.golang.org/protobuf`), GORM/SQLite (`storage/filesystem`), testify.

## Global Constraints

- Module path: `github.com/alex-sviridov/miniprotector`
- Proto files live in `src/api/*.proto`; generated Go code lands in `src/api/` with Go package name `proto` (per existing `option go_package = "./proto"` convention), imported as `pb "github.com/alex-sviridov/miniprotector/api"` — same import alias and package as `backup.proto` already uses since both generate into the same Go package.
- `protoc-gen-go` and `protoc-gen-go-grpc` binaries already exist at `/home/alex/go/bin` — ensure that directory is on `PATH` before running `make proto`.
- Port range: 1024–65535 (enforced by `common.ValidatePort`)
- `bwfs list`'s existing local SQLite read path (`wfs.NewReadOnly`) is preserved — only its argument parsing changes.
- The new positional `[[server_name:]path]` splits on the **first colon only**.
- `path` (when the positional is present) matches as a **prefix** against the file's path component.
- `server_name` (when present) matches **exactly** against the file's hostname component.
- `--filter` remains a separate free-text substring filter, unchanged, composes with the above.
- `rwfs`'s default `server_name` (when omitted) is `common.GetHostname()` — the same mechanism `brfs` already uses.
- No `restore` subcommand or chunk-streaming logic in this plan — `list` only.
- Existing e2e tests (`src/e2e/e2e_test.go`, `validate.go`) parse `bwfs list --output json` output — the JSON field names (`file_data_id`, `source`, `type`, `path`, `timestamp`, `size`, `chunks`, `versions`, `created_at`) must not change.

---

### Task 1: Remove `rrfs`

**Files:**
- Delete: `src/cmd/rrfs/arguments.go`
- Delete: `src/cmd/rrfs/server.go`
- Delete: `src/cmd/rrfs/main.go`
- Modify: `Makefile`
- Modify: `docs/ARCHITECTURE.md`

**Interfaces:** None — pure removal, no other task depends on rrfs code.

- [ ] **Step 1: Delete the rrfs source files**

```bash
cd /home/alex/miniprotector && rm -rf src/cmd/rrfs
```

- [ ] **Step 2: Remove rrfs from the Makefile**

Read `Makefile` and find the `RRFS_CMD` variable, the `rrfs` build target, and `rrfs` in the `.PHONY` line. Remove all three. For example, if the file contains:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rrfs test test-e2e lint
```

change it to:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs test test-e2e lint
```

And remove the `RRFS_CMD := cmd/rrfs` line and the `rrfs:` build target block entirely (the one that runs `$(GO) build ... -o $(BINARY_DIR)/rrfs ./$(RRFS_CMD)`). Also remove `rrfs` from the `BINARIES` list/variable if one exists (check for a line like `BINARIES := brfs bwfs rrfs` or similar aggregation).

- [ ] **Step 3: Verify the project still builds without rrfs**

```bash
cd /home/alex/miniprotector/src && go build ./...
```

Expected: no output, exit 0 (no references to `cmd/rrfs` remain anywhere).

```bash
cd /home/alex/miniprotector && make build
```

Expected: builds `brfs` and `bwfs` only, no `rrfs` target invoked, no error.

- [ ] **Step 4: Update ARCHITECTURE.md**

Read `docs/ARCHITECTURE.md`. In the components table, replace the `rrfs` row:

```markdown
| rrfs | Restore Reader for File System — reads from storage, sends via gRPC | Not yet implemented |
```

with:

```markdown
| rwfs | Restore Writer for File System — queries bwfs (list, future restore) and writes to destination | list implemented; restore not yet implemented |
```

Remove the `rwfs` row that's currently below it (it will be replaced by this merged row), so the table has one `rwfs` entry, not two. Update the "Restore Process _(planned)_" section text and the mermaid diagram: replace `rrfs` nodes/edges with `rwfs` connecting to `bwfs` (not a separate restore-read service). Specifically, in the mermaid diagram, remove:

```
rrfs[rrfs<br/>Restore Reader]
```

and the `DB -->|queries metadata| rrfs` / `BackupFS -->|reads chunks| rrfs` edges, replacing them with `bwfs` already shown, and add `bwfs -->|list/restore protocol<br/>network/unix socket| rwfs`. Keep `rwfs` writing to `DstFS` as before.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector && \
git add -A src/cmd Makefile docs/ARCHITECTURE.md && \
git commit -m "chore: remove obsolete rrfs stub, update architecture docs"
```

---

### Task 2: Generalize `connection.Connect`

**Files:**
- Modify: `src/common/connection/client.go`
- Modify: `src/cmd/brfs/main.go`

**Interfaces:**
- Produces: `func Connect(host string, port, timeout int) (*grpc.ClientConn, error)`
- Consumes (by brfs): wraps the returned `*grpc.ClientConn` with `pb.NewBackupServiceClient(conn)`

- [ ] **Step 1: Change `Connect`'s return type in `src/common/connection/client.go`**

Find:

```go
func Connect(host string, port, timeout int) (pb.BackupServiceClient, error) {
```

Replace with:

```go
func Connect(host string, port, timeout int) (*grpc.ClientConn, error) {
```

Find the end of the function:

```go
	if err := checkConnection(conn, timeout); err != nil {
		conn.Close() // Close only on connection failure
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	// Create protobuf client - connection will remain open
	return pb.NewBackupServiceClient(conn), nil
}
```

Replace with:

```go
	if err := checkConnection(conn, timeout); err != nil {
		conn.Close() // Close only on connection failure
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	// Connection remains open; caller wraps it with the generated client it needs.
	return conn, nil
}
```

The `pb` import is still used elsewhere in this file (`ResponseMatcher`, `WaitForResponse`, etc.) — do not remove it.

- [ ] **Step 2: Update the call site in `src/cmd/brfs/main.go`**

Find:

```go
	// Create gRPC connection
	client, err := connection.Connect(arguments.WriterHost, arguments.WriterPort, 5)
	if err != nil {
		logger.Error("Error connecting to server", "error", err)
		return
	}
	logger.Info("Connected to server")
```

Replace with:

```go
	// Create gRPC connection
	conn, err := connection.Connect(arguments.WriterHost, arguments.WriterPort, 5)
	if err != nil {
		logger.Error("Error connecting to server", "error", err)
		return
	}
	client := pb.NewBackupServiceClient(conn)
	logger.Info("Connected to server")
```

Add `pb "github.com/alex-sviridov/miniprotector/api"` to the import block in `src/cmd/brfs/main.go` if not already present (check the existing imports first — it currently imports `common`, `common/config`, `common/connection`, `common/logging`, `workload/filesystem`).

- [ ] **Step 3: Verify it builds**

```bash
cd /home/alex/miniprotector/src && go build ./...
```

Expected: no output, exit 0.

- [ ] **Step 4: Run existing brfs/bwfs unit and integration tests to confirm no regression**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/... ./common/...
```

Expected: all PASS (integration-tagged tests are skipped by default since they require `-tags integration`; that's fine, this step just confirms no compile break and unit tests pass).

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add common/connection/client.go cmd/brfs/main.go && \
git commit -m "refactor: generalize connection.Connect to return *grpc.ClientConn"
```

---

### Task 3: Add `list.proto` and generate code

**Files:**
- Create: `src/api/list.proto`
- Generated: `src/api/list.pb.go`, `src/api/list_grpc.pb.go`

**Interfaces:**
- Produces (generated): `pb.ListServiceServer`, `pb.ListServiceClient`, `pb.UnimplementedListServiceServer`, `pb.RegisterListServiceServer(*grpc.Server, pb.ListServiceServer)`, `pb.NewListServiceClient(*grpc.ClientConn) pb.ListServiceClient`
- Produces (generated): `pb.ListRequest{ServerName, Path, Filter string}`, `pb.ListResponse{Rows []*pb.FileRow}`, `pb.FileRow{FileDataId, Source, Type, Path string; Timestamp, Size int64; Chunks int32; Versions int64}`

- [ ] **Step 1: Create `src/api/list.proto`**

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
  string created_at     = 9; // RFC3339 UTC, matches listformat's JSON rendering
}
```

- [ ] **Step 2: Generate the Go code**

```bash
export PATH="$PATH:/home/alex/go/bin"
cd /home/alex/miniprotector && make proto
```

Expected output includes `Protobuf code generated in src/api/`, and `src/api/list.pb.go` + `src/api/list_grpc.pb.go` now exist. This regenerates `backup.pb.go`/`backup_grpc.pb.go` too (the `proto` target globs `api/*.proto`) — verify with `git diff src/api/backup.pb.go src/api/backup_grpc.pb.go` that they are unchanged (only timestamps/versions in the header comment may differ; if so that's fine).

- [ ] **Step 3: Verify the generated code compiles**

```bash
cd /home/alex/miniprotector/src && go build ./api/...
```

Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add api/list.proto api/list.pb.go api/list_grpc.pb.go api/backup.pb.go api/backup_grpc.pb.go && \
git commit -m "feat(api): add ListService proto and generated code"
```

---

### Task 4: Extract shared rendering into `common/listformat`

**Files:**
- Create: `src/common/listformat/listformat.go`
- Create: `src/common/listformat/listformat_test.go`
- Modify: `src/cmd/bwfs/list.go`
- Delete (content moves out): rendering functions from `src/cmd/bwfs/list.go`
- Modify: `src/cmd/bwfs/list_test.go` (remove tests now covered by `listformat_test.go`, or keep thin wrappers — see Step 5)

**Interfaces:**
- Produces: `type listformat.Row struct { FileDataID, Source, Type, Path string; Timestamp, Size int64; Chunks int; Versions int64; CreatedAt time.Time }`
- Produces: `func listformat.RenderTable(rows []Row) error`
- Produces: `func listformat.RenderJSON(rows []Row) error`
- Produces: `func listformat.FormatSize(bytes int64) string`
- Consumes (by bwfs): `queryFileRows` results converted to `[]listformat.Row`

- [ ] **Step 1: Write the failing test for the new package**

Create `src/common/listformat/listformat_test.go`:

```go
package listformat

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatSize_Bytes(t *testing.T) {
	assert.Equal(t, "0 B", FormatSize(0))
	assert.Equal(t, "1 B", FormatSize(1))
	assert.Equal(t, "1023 B", FormatSize(1023))
}

func TestFormatSize_Kilobytes(t *testing.T) {
	assert.Equal(t, "1 KB", FormatSize(1024))
	assert.Equal(t, "1023 KB", FormatSize(1024*1024-1))
}

func TestFormatSize_Megabytes(t *testing.T) {
	assert.Equal(t, "1 MB", FormatSize(1024*1024))
	assert.Equal(t, "1023 MB", FormatSize(1024*1024*1024-1))
}

func TestFormatSize_Gigabytes(t *testing.T) {
	assert.Equal(t, "1 GB", FormatSize(1024*1024*1024))
	assert.Equal(t, "10 GB", FormatSize(10*1024*1024*1024))
}

func TestRenderTable_EmptyDoesNotError(t *testing.T) {
	err := RenderTable(nil)
	assert.NoError(t, err)

	err = RenderTable([]Row{})
	assert.NoError(t, err)
}

func TestRenderJSON_EmptyProducesArray(t *testing.T) {
	err := RenderJSON([]Row{})
	assert.NoError(t, err)
}

func TestRenderJSON_CreatedAtIsRFC3339UTC(t *testing.T) {
	ts, err := time.Parse(time.RFC3339, "2026-06-29T08:10:42Z")
	require.NoError(t, err)

	rows := []Row{{
		FileDataID: "abc-123",
		Source:     "workstation",
		Type:       "f",
		Path:       "/var/log/test",
		Timestamp:  1782605538,
		Size:       4096,
		Chunks:     3,
		Versions:   2,
		CreatedAt:  ts,
	}}

	out := toJSONRows(rows)
	data, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	s := string(data)
	assert.Contains(t, s, `"file_data_id": "abc-123"`)
	assert.Contains(t, s, `"source": "workstation"`)
	assert.Contains(t, s, `"created_at": "2026-06-29T08:10:42Z"`)
	assert.Contains(t, s, `"timestamp": 1782605538`)
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/alex/miniprotector/src && go test ./common/listformat/... -v
```

Expected: FAIL — package `listformat` does not exist yet (build error: no such file).

- [ ] **Step 3: Create `src/common/listformat/listformat.go`**

```go
// Package listformat renders file-listing rows as a table or JSON.
// Used by both bwfs's local SQLite-backed list and rwfs's gRPC-backed
// list, so the two commands produce identical output for identical data.
package listformat

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"
)

// Row is a rendering-ready file listing entry, independent of where the
// underlying data came from (local SQLite query or a gRPC ListResponse).
type Row struct {
	FileDataID string
	Source     string
	Type       string
	Path       string
	Timestamp  int64
	Size       int64
	Chunks     int
	Versions   int64
	CreatedAt  time.Time
}

type jsonRow struct {
	FileDataID string `json:"file_data_id"`
	Source     string `json:"source"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	Timestamp  int64  `json:"timestamp"`
	Size       int64  `json:"size"`
	Chunks     int    `json:"chunks"`
	Versions   int64  `json:"versions"`
	CreatedAt  string `json:"created_at"`
}

func toJSONRows(rows []Row) []jsonRow {
	out := make([]jsonRow, len(rows))
	for i, r := range rows {
		out[i] = jsonRow{
			FileDataID: r.FileDataID,
			Source:     r.Source,
			Type:       r.Type,
			Path:       r.Path,
			Timestamp:  r.Timestamp,
			Size:       r.Size,
			Chunks:     r.Chunks,
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return out
}

// FormatSize renders a byte count as a human-readable B/KB/MB/GB string.
func FormatSize(bytes int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case bytes < kb:
		return fmt.Sprintf("%d B", bytes)
	case bytes < mb:
		return fmt.Sprintf("%d KB", bytes/kb)
	case bytes < gb:
		return fmt.Sprintf("%d MB", bytes/mb)
	default:
		return fmt.Sprintf("%d GB", bytes/gb)
	}
}

// RenderTable writes rows to stdout as a tab-aligned table.
func RenderTable(rows []Row) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tTYPE\tPATH\tTIMESTAMP\tSIZE\tCHUNKS\tVERSIONS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%d\n",
			r.Source, r.Type, r.Path, r.Timestamp, FormatSize(r.Size), r.Chunks, r.Versions)
	}
	return w.Flush()
}

// RenderJSON writes rows to stdout as indented JSON.
func RenderJSON(rows []Row) error {
	out := toJSONRows(rows)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/alex/miniprotector/src && go test ./common/listformat/... -v
```

Expected: all PASS.

- [ ] **Step 5: Update `src/cmd/bwfs/list.go` to use `listformat`**

Read the current `src/cmd/bwfs/list.go`. Replace the `fileRow`/`fileRowJSON` types, `formatSize`, `renderTable`, `renderJSON` functions with calls into `listformat`. The file becomes:

```go
package main

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/common/listformat"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func runList(logger *slog.Logger, storagePath, serverName, pathPrefix, output, filter string) error {
	store, err := wfs.NewReadOnly(storagePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	rows, err := queryFileRows(store, serverName, pathPrefix, filter)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	switch output {
	case "json":
		return listformat.RenderJSON(rows)
	default:
		return listformat.RenderTable(rows)
	}
}

type queryResult struct {
	FileDataID string    `gorm:"column:file_data_id"`
	FileID     string    `gorm:"column:file_id"`
	Size       int64     `gorm:"column:size"`
	Chunks     int       `gorm:"column:chunks"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	Versions   int64     `gorm:"column:versions"`
}

// queryFileRows returns the latest finalized FileDataRecord per file_id,
// optionally narrowed by source hostname (exact match), path (prefix
// match), and a free-text substring filter on the path.
func queryFileRows(store *wfs.Store, serverName, pathPrefix, filter string) ([]listformat.Row, error) {
	// Subquery picks the single latest finalized FileDataRecord per file_id,
	// so non-aggregated columns (id, size, chunk_count, created_at) are
	// unambiguous even if multiple records share the same file_id.
	// COUNT(DISTINCT fv.id) avoids inflation from the cross-join when multiple
	// FileDataRecords exist.
	query := store.RawDB().
		Table("file_data_records fd").
		Select("fd.id AS file_data_id, fd.file_id, fd.size, fd.chunk_count AS chunks, fd.created_at, COUNT(DISTINCT fv.id) AS versions").
		Joins("LEFT JOIN file_version_records fv ON fv.file_id = fd.file_id").
		Where("fd.checksum IS NOT NULL").
		Where("fd.created_at = (SELECT MAX(fd2.created_at) FROM file_data_records fd2 WHERE fd2.file_id = fd.file_id AND fd2.checksum IS NOT NULL)").
		Group("fd.file_id").
		Order("fd.created_at ASC")

	if serverName != "" {
		query = query.Where("fd.file_id LIKE ?", "fs://"+serverName+":%")
	}
	if pathPrefix != "" {
		// file_id format: fs://hostname:type:path:mtime — path is not a
		// fixed-offset column, so prefix-match via LIKE on the decoded
		// path is done in Go after the query (see filtering below).
	}
	if filter != "" {
		query = query.Where("fd.file_id LIKE ?", "%"+filter+"%")
	}

	var results []queryResult
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	rows := make([]listformat.Row, 0, len(results))
	for _, r := range results {
		src, typ, path, ts := parseFileID(r.FileID)
		if pathPrefix != "" && !strings.HasPrefix(path, pathPrefix) {
			continue
		}
		rows = append(rows, listformat.Row{
			FileDataID: r.FileDataID,
			Source:     src,
			Type:       typ,
			Path:       path,
			Timestamp:  ts,
			Size:       r.Size,
			Chunks:     r.Chunks,
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt,
		})
	}
	return rows, nil
}

// parseFileID splits "fs://host:type:path:mtime" into its four parts.
// type is always a single character. path may contain colons (Windows C:/foo).
// Returns ("?","?",fileID,0) for malformed IDs — never errors.
func parseFileID(fileID string) (source, fileType, path string, timestamp int64) {
	const prefix = "fs://"
	if !strings.HasPrefix(fileID, prefix) {
		return "?", "?", fileID, 0
	}
	rest := fileID[len(prefix):]
	tokens := strings.Split(rest, ":")
	if len(tokens) < 4 {
		return "?", "?", fileID, 0
	}
	source = tokens[0]
	fileType = tokens[1]
	ts, err := strconv.ParseInt(tokens[len(tokens)-1], 10, 64)
	if err != nil {
		return "?", "?", fileID, 0
	}
	path = strings.Join(tokens[2:len(tokens)-1], ":")
	return source, fileType, path, ts
}
```

Note: `serverName` filtering is done via SQL `LIKE 'fs://servername:%'` (matches the file_id encoding directly, exact hostname token), while `pathPrefix` filtering is done in Go after decoding (since the path is embedded inside a colon-delimited string, not a separate column) — this is simpler and avoids fragile SQL string slicing, and result sets are small (per-host backup catalogs), so post-filtering in Go is not a performance concern.

- [ ] **Step 6: Update `src/cmd/bwfs/list_test.go`**

Remove `TestFormatSize_*`, `TestRenderJSON_*`, `TestRenderTable_*` (now covered by `listformat_test.go`). Keep `TestParseFileID_*` tests as-is (the `parseFileID` function stays in `bwfs/list.go`). The file becomes just the `parseFileID` test block (lines testing `TestParseFileID_ValidLinuxPath` through `TestParseFileID_EmptyString` from the original file) — remove the `encoding/json`, `time` imports if no longer used, keep `testing`, `github.com/stretchr/testify/assert`.

- [ ] **Step 7: Run bwfs tests to verify everything still passes**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/bwfs/... ./common/listformat/... -v
```

Expected: all PASS. If `runList`'s new signature (`serverName, pathPrefix` params) breaks a caller, that's expected — Task 5 updates `main.go`'s call site next.

- [ ] **Step 8: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add common/listformat cmd/bwfs/list.go cmd/bwfs/list_test.go && \
git commit -m "refactor: extract list rendering into common/listformat, add server/path filters to queryFileRows"
```

---

### Task 5: Update `bwfs list` CLI syntax (positional `[server_name:]path` + colon-split parsing)

**Files:**
- Create: `src/common/listfilter.go`
- Create: `src/common/listfilter_test.go`
- Modify: `src/cmd/bwfs/arguments.go`
- Modify: `src/cmd/bwfs/main.go`

**Interfaces:**
- Produces: `func common.ParseServerPath(positional string) (serverName, path string, err error)`
- Consumes (by bwfs arguments.go): `ParseServerPath` to split the new positional
- Consumes (by bwfs main.go): `runList(logger, storagePath, serverName, pathPrefix, output, filter string) error` (signature from Task 4 Step 5)

- [ ] **Step 1: Write the failing test for the shared colon-split parser**

Create `src/common/listfilter_test.go`:

```go
package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseServerPath_Empty(t *testing.T) {
	server, path, err := ParseServerPath("")
	require.NoError(t, err)
	assert.Equal(t, "", server)
	assert.Equal(t, "", path)
}

func TestParseServerPath_PathOnly(t *testing.T) {
	server, path, err := ParseServerPath("/home/user")
	require.NoError(t, err)
	assert.Equal(t, "", server)
	assert.Equal(t, "/home/user", path)
}

func TestParseServerPath_ServerAndPath(t *testing.T) {
	server, path, err := ParseServerPath("myhost:/home/user")
	require.NoError(t, err)
	assert.Equal(t, "myhost", server)
	assert.Equal(t, "/home/user", path)
}

func TestParseServerPath_PathWithColon(t *testing.T) {
	// First colon only splits server from path — remaining colons stay in path.
	server, path, err := ParseServerPath("myhost:C:/Users/foo")
	require.NoError(t, err)
	assert.Equal(t, "myhost", server)
	assert.Equal(t, "C:/Users/foo", path)
}

func TestParseServerPath_LeadingColonMeansNoServer(t *testing.T) {
	server, path, err := ParseServerPath(":/home/user")
	require.NoError(t, err)
	assert.Equal(t, "", server)
	assert.Equal(t, "/home/user", path)
}

func TestParseServerPath_EmptyPathAfterColonIsError(t *testing.T) {
	_, _, err := ParseServerPath("myhost:")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/alex/miniprotector/src && go test ./common/... -run TestParseServerPath -v
```

Expected: FAIL — `ParseServerPath` undefined.

- [ ] **Step 3: Create `src/common/listfilter.go`**

```go
package common

import (
	"fmt"
	"strings"
)

// ParseServerPath splits the "[server_name:]path" CLI positional used by
// both `bwfs list` and `rwfs list` on the first colon only, so paths that
// themselves contain colons (e.g. Windows "C:/Users/foo") pass through
// intact. An empty positional returns ("", "", nil) — no filter. A leading
// colon (":path") means no server filter, path-only. A trailing colon with
// nothing after it is a user error (empty path is not a valid filter once
// the positional is given at all).
func ParseServerPath(positional string) (serverName, path string, err error) {
	if positional == "" {
		return "", "", nil
	}

	idx := strings.Index(positional, ":")
	if idx == -1 {
		return "", positional, nil
	}

	serverName = positional[:idx]
	path = positional[idx+1:]
	if path == "" {
		return "", "", fmt.Errorf("path filter cannot be empty after ':' in %q", positional)
	}
	return serverName, path, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/alex/miniprotector/src && go test ./common/... -run TestParseServerPath -v
```

Expected: all PASS.

- [ ] **Step 5: Update `src/cmd/bwfs/arguments.go`**

Read the current file. Find the `Arguments` struct:

```go
type Arguments struct {
	StoragePath string
	Action      string // "server" | "list"
	// server flags
	Port  int
	Debug bool
	Quiet bool
	// list flags
	Output string // "table" | "json"
	Filter string
}
```

Replace with (adds `ServerName`/`PathFilter`, the parsed positional, plus
`listPositional`, an unexported staging field holding the raw
`[server_name:]path` string before `common.ParseServerPath` splits it):

```go
type Arguments struct {
	StoragePath string
	Action      string // "server" | "list"
	// server flags
	Port  int
	Debug bool
	Quiet bool
	// list flags
	ServerName     string // source hostname filter, from positional, may be empty
	PathFilter     string // path prefix filter, from positional, may be empty
	listPositional string // raw "[server_name:]path" before ParseServerPath
	Output         string // "table" | "json"
	Filter         string
}
```

Find the `listCmd` block:

```go
	// list subcommand
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List stored file data",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "list" },
	}
	listCmd.Flags().StringVar(&args.Output, "output", "table", "Output format: table or json")
	listCmd.Flags().StringVar(&args.Filter, "filter", "", "Filter by text in file path")
	listCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
```

Replace with:

```go
	// list subcommand
	listCmd := &cobra.Command{
		Use:   "list [[server_name:]path]",
		Short: "List stored file data",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, cliArgs []string) {
			args.Action = "list"
			if len(cliArgs) == 1 {
				args.listPositional = cliArgs[0]
			}
		},
	}
	listCmd.Flags().StringVar(&args.Output, "output", "table", "Output format: table or json")
	listCmd.Flags().StringVar(&args.Filter, "filter", "", "Filter by text in file path")
	listCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
```

Find the validation block at the end of `parseArguments`:

```go
	if args.Action == "list" && args.Output != "table" && args.Output != "json" {
		return nil, fmt.Errorf("--output must be 'table' or 'json', got: %q", args.Output)
	}

	return args, nil
}
```

Replace with:

```go
	if args.Action == "list" {
		if args.Output != "table" && args.Output != "json" {
			return nil, fmt.Errorf("--output must be 'table' or 'json', got: %q", args.Output)
		}
		serverName, path, err := common.ParseServerPath(args.listPositional)
		if err != nil {
			return nil, fmt.Errorf("list positional error: %w", err)
		}
		args.ServerName = serverName
		args.PathFilter = path
	}

	return args, nil
}
```

`common` is already imported in this file (used by `ValidatePort`/`ValidatePath`).

- [ ] **Step 6: Update the call site in `src/cmd/bwfs/main.go`**

Find:

```go
	case "list":
		if err := runList(logger, arguments.StoragePath, arguments.Output, arguments.Filter); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
```

Replace with:

```go
	case "list":
		if err := runList(logger, arguments.StoragePath, arguments.ServerName, arguments.PathFilter, arguments.Output, arguments.Filter); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
```

- [ ] **Step 7: Verify it builds and existing tests pass**

```bash
cd /home/alex/miniprotector/src && go build ./... && go test ./cmd/bwfs/... ./common/... -v
```

Expected: build succeeds, all tests PASS.

- [ ] **Step 8: Manual smoke test**

```bash
cd /home/alex/miniprotector && make build
mkdir -p /tmp/bwfs-smoke && /home/alex/miniprotector/bin/bwfs /tmp/bwfs-smoke list
/home/alex/miniprotector/bin/bwfs /tmp/bwfs-smoke list myhost:/some/path --output json
```

Expected: both commands run without argument-parsing errors (empty result sets are fine since `/tmp/bwfs-smoke` has no data); no panic, no "unknown flag" or "invalid argument" error.

- [ ] **Step 9: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add common/listfilter.go common/listfilter_test.go cmd/bwfs/arguments.go cmd/bwfs/main.go && \
git commit -m "feat(bwfs): add [server_name:]path positional filter to list subcommand"
```

---

### Task 6: `ListService` server-side handler in `bwfs`

**Files:**
- Create: `src/cmd/bwfs/listserver.go`
- Create: `src/cmd/bwfs/listserver_test.go`
- Modify: `src/cmd/bwfs/main.go`

**Interfaces:**
- Consumes: `queryFileRows(store *wfs.Store, serverName, pathPrefix, filter string) ([]listformat.Row, error)` (Task 4)
- Produces: `type listServer struct { pb.UnimplementedListServiceServer; store *wfs.Store; logger *slog.Logger }`
- Produces: `func NewListServer(store *wfs.Store, logger *slog.Logger) *listServer`
- Produces: `func (s *listServer) ListFiles(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error)`

- [ ] **Step 1: Write the failing integration-style test**

Create `src/cmd/bwfs/listserver_test.go`. This is a plain unit test (no `//go:build integration` tag needed since it exercises `listServer` directly without a network listener):

```go
package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func newTestListServer(t *testing.T) (*listServer, *wfs.Store) {
	t.Helper()
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewListServer(store, logger), store
}

func TestListFiles_EmptyStoreReturnsEmptyRows(t *testing.T) {
	srv, _ := newTestListServer(t)

	resp, err := srv.ListFiles(context.Background(), &pb.ListRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Rows)
}

func TestListFiles_FiltersByServerName(t *testing.T) {
	srv, store := newTestListServer(t)

	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hostb:f:/data/b.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hostb:f:/data/b.txt:1000", []byte{5, 6, 7, 8}))

	resp, err := srv.ListFiles(context.Background(), &pb.ListRequest{ServerName: "hosta"})
	require.NoError(t, err)
	require.Len(t, resp.Rows, 1)
	assert.Equal(t, "hosta", resp.Rows[0].Source)
	assert.Equal(t, "/data/a.txt", resp.Rows[0].Path)
}

func TestListFiles_FiltersByPathPrefix(t *testing.T) {
	srv, store := newTestListServer(t)

	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hosta:f:/other/c.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/other/c.txt:1000", []byte{5, 6, 7, 8}))

	resp, err := srv.ListFiles(context.Background(), &pb.ListRequest{Path: "/data"})
	require.NoError(t, err)
	require.Len(t, resp.Rows, 1)
	assert.Equal(t, "/data/a.txt", resp.Rows[0].Path)
}
```

`CreateFileData` + `FinalizeFileData` alone is sufficient to make a row visible to `queryFileRows` (it only requires `checksum IS NOT NULL`, per `src/storage/filesystem/filedata.go`) — no chunk data needs to be written for these listing tests.

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/bwfs/... -run TestListFiles -v
```

Expected: FAIL — `listServer`/`NewListServer` undefined.

- [ ] **Step 3: Create `src/cmd/bwfs/listserver.go`**

```go
package main

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/listformat"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type listServer struct {
	pb.UnimplementedListServiceServer
	store  *wfs.Store
	logger *slog.Logger
}

func NewListServer(store *wfs.Store, logger *slog.Logger) *listServer {
	return &listServer{store: store, logger: logger}
}

func (s *listServer) ListFiles(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	rows, err := queryFileRows(s.store, req.GetServerName(), req.GetPath(), req.GetFilter())
	if err != nil {
		s.logger.Error("ListFiles query failed", "error", err)
		return nil, err
	}

	pbRows := make([]*pb.FileRow, len(rows))
	for i, r := range rows {
		pbRows[i] = &pb.FileRow{
			FileDataId: r.FileDataID,
			Source:     r.Source,
			Type:       r.Type,
			Path:       r.Path,
			Timestamp:  r.Timestamp,
			Size:       r.Size,
			Chunks:     int32(r.Chunks),
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return &pb.ListResponse{Rows: pbRows}, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/bwfs/... -run TestListFiles -v
```

Expected: all PASS. If the store setup in Step 1 needs adjustment (per the note), fix it now and re-run until green.

- [ ] **Step 5: Wire `ListService` registration into `bwfs server` in `src/cmd/bwfs/main.go`**

`backupServer.store` is typed as `storage.BackupStore` (the interface), but
`queryFileRows` requires the concrete `*wfs.Store` (for its `RawDB()`
method). Rather than threading the concrete type out through
`backupServer`, open a second read-only store handle for the list server —
SQLite WAL mode allows concurrent readers alongside the writer-held lock
(see `wfs.NewReadOnly`'s doc comment in `src/storage/filesystem/store.go`).

Find:

```go
	case "server":
		logger.Info("Backup writer started",
			"StoragePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)
		backupServer, err := NewBackupServer(ctx, logger, arguments.StoragePath)
		if err != nil {
			logger.Error("Server initialization failed", "error", err)
			os.Exit(1)
		}
		defer backupServer.store.Close()

		if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {
			pb.RegisterBackupServiceServer(s, backupServer)
		}); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
```

Replace with:

```go
	case "server":
		logger.Info("Backup writer started",
			"StoragePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)
		backupServer, err := NewBackupServer(ctx, logger, arguments.StoragePath)
		if err != nil {
			logger.Error("Server initialization failed", "error", err)
			os.Exit(1)
		}
		defer backupServer.store.Close()

		listStore, err := wfs.NewReadOnly(arguments.StoragePath)
		if err != nil {
			logger.Error("List store initialization failed", "error", err)
			os.Exit(1)
		}
		defer listStore.Close()
		listServer := NewListServer(listStore, logger)

		if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {
			pb.RegisterBackupServiceServer(s, backupServer)
			pb.RegisterListServiceServer(s, listServer)
		}); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
```

Add `wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"` to the import block in `src/cmd/bwfs/main.go` if not already present.

- [ ] **Step 6: Verify it builds**

```bash
cd /home/alex/miniprotector/src && go build ./...
```

Expected: no output, exit 0.

- [ ] **Step 7: Run bwfs tests**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/bwfs/... -v
```

Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add cmd/bwfs/listserver.go cmd/bwfs/listserver_test.go cmd/bwfs/main.go && \
git commit -m "feat(bwfs): add ListService gRPC handler, register alongside BackupService"
```

---

### Task 7: Create `rwfs` with `list` subcommand

**Files:**
- Create: `src/cmd/rwfs/arguments.go`
- Create: `src/cmd/rwfs/arguments_test.go`
- Create: `src/cmd/rwfs/list.go`
- Create: `src/cmd/rwfs/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `common.ParseServerPath(string) (string, string, error)` (Task 5)
- Consumes: `connection.Connect(host string, port, timeout int) (*grpc.ClientConn, error)` (Task 2)
- Consumes: `pb.NewListServiceClient(*grpc.ClientConn) pb.ListServiceClient`, `pb.ListRequest`, `pb.ListResponse` (Task 3)
- Consumes: `listformat.Row`, `listformat.RenderTable`, `listformat.RenderJSON` (Task 4)
- Consumes: `common.GetHostname() string`, `common.ParseDestination(dest, defaultHost string, defaultPort int) (string, int, error)`
- Produces: `type Arguments struct { ServerName, PathFilter, BwfsHost string; BwfsPort int; Output, Filter string; Debug, Quiet bool }`
- Produces: `func parseArguments(conf *config.Config) (*Arguments, error)`
- Produces: `func runList(host string, port int, serverName, pathFilter, filter, output string) error`

- [ ] **Step 1: Write the failing test for `rwfs` argument parsing**

Create `src/cmd/rwfs/arguments_test.go`:

```go
package main

import (
	"os"
	"testing"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

func testConfig() *config.Config {
	return &config.Config{DefaultPort: 8080}
}

func TestParseArguments_ListWithBwfsTargetOnly(t *testing.T) {
	withArgs(t, []string{"rwfs", "list", "localhost:8080"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "list", args.Action)
		assert.Equal(t, "localhost", args.BwfsHost)
		assert.Equal(t, 8080, args.BwfsPort)
		assert.Equal(t, "", args.ServerName)
		assert.Equal(t, "", args.PathFilter)
	})
}

func TestParseArguments_ListWithServerAndPath(t *testing.T) {
	withArgs(t, []string{"rwfs", "list", "myhost:/home/user", "localhost:8080"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "myhost", args.ServerName)
		assert.Equal(t, "/home/user", args.PathFilter)
		assert.Equal(t, "localhost", args.BwfsHost)
		assert.Equal(t, 8080, args.BwfsPort)
	})
}

func TestParseArguments_MissingBwfsTargetErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "list"}, func() {
		_, err := parseArguments(testConfig())
		assert.Error(t, err)
	})
}

func TestParseArguments_InvalidOutputErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "list", "localhost:8080", "--output", "xml"}, func() {
		_, err := parseArguments(testConfig())
		assert.Error(t, err)
	})
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/rwfs/... -v
```

Expected: FAIL — package doesn't exist / `parseArguments` undefined.

- [ ] **Step 3: Create `src/cmd/rwfs/arguments.go`**

The CLI shape is `rwfs list [[server_name:]path] <bwfs_host:port> [flags]`. Since the bwfs target positional always comes last and the filter positional is optional, count positionals after `list` to disambiguate (1 positional = bwfs target only; 2 positionals = filter + bwfs target):

```go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Action     string // "list"
	ServerName string // source hostname filter, may be empty
	PathFilter string // path prefix filter, may be empty
	BwfsHost   string
	BwfsPort   int
	Output     string // "table" | "json"
	Filter     string
	Debug      bool
	Quiet      bool

	listPositional string // raw "[server_name:]path", staged before ParseServerPath
	bwfsTarget     string // raw "host:port", staged before ParseDestination
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "rwfs <command>",
		Short: "Restore writer filesystem tool",
	}

	listCmd := &cobra.Command{
		Use:   "list [[server_name:]path] <bwfs_host:port>",
		Short: "List files available on a remote bwfs server",
		Args:  cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, cliArgs []string) {
			args.Action = "list"
			if len(cliArgs) == 1 {
				args.bwfsTarget = cliArgs[0]
			} else {
				args.listPositional = cliArgs[0]
				args.bwfsTarget = cliArgs[1]
			}
		},
	}
	listCmd.Flags().StringVar(&args.Output, "output", "table", "Output format: table or json")
	listCmd.Flags().StringVar(&args.Filter, "filter", "", "Filter by text in file path")
	listCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	listCmd.Flags().BoolVar(&args.Quiet, "quiet", false, "Suppress console logging")

	rootCmd.AddCommand(listCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: list")
	}

	if args.Output != "table" && args.Output != "json" {
		return nil, fmt.Errorf("--output must be 'table' or 'json', got: %q", args.Output)
	}

	serverName, path, err := common.ParseServerPath(args.listPositional)
	if err != nil {
		return nil, fmt.Errorf("list positional error: %w", err)
	}
	if serverName == "" {
		serverName = common.GetHostname()
	}
	args.ServerName = serverName
	args.PathFilter = path

	host, port, err := common.ParseDestination(args.bwfsTarget, "localhost", conf.DefaultPort)
	if err != nil {
		return nil, fmt.Errorf("invalid bwfs target: %w", err)
	}
	args.BwfsHost = host
	args.BwfsPort = port

	return args, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/rwfs/... -v
```

Expected: all PASS. Fix any unused-import or field-mismatch errors that surface.

- [ ] **Step 5: Create `src/cmd/rwfs/list.go`**

```go
package main

import (
	"context"
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/listformat"
)

func runList(host string, port int, serverName, pathFilter, filter, output string) error {
	conn, err := connection.Connect(host, port, 5)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	client := pb.NewListServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.ListFiles(ctx, &pb.ListRequest{
		ServerName: serverName,
		Path:       pathFilter,
		Filter:     filter,
	})
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	rows := make([]listformat.Row, len(resp.Rows))
	for i, r := range resp.Rows {
		createdAt, _ := time.Parse(time.RFC3339, r.CreatedAt) // zero value on parse failure is acceptable for display
		rows[i] = listformat.Row{
			FileDataID: r.FileDataId,
			Source:     r.Source,
			Type:       r.Type,
			Path:       r.Path,
			Timestamp:  r.Timestamp,
			Size:       r.Size,
			Chunks:     int(r.Chunks),
			Versions:   r.Versions,
			CreatedAt:  createdAt,
		}
	}

	switch output {
	case "json":
		return listformat.RenderJSON(rows)
	default:
		return listformat.RenderTable(rows)
	}
}
```

`pb.FileRow.CreatedAt` is an RFC3339 UTC string (set server-side in
`listserver.go`, Task 6) — parsed back into a `time.Time` here so
`listformat.RenderJSON` produces the same `created_at` field shape as local
`bwfs list`.

- [ ] **Step 6: Create `src/cmd/rwfs/main.go`**

Follow the same inline `context.WithValue` pattern `bwfs/main.go` and
`brfs/main.go` already use (no wrapper helpers):

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
	const appName = "rwfs"

	ctx := context.WithValue(context.Background(), "appName", appName)

	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, config.ContextKey, conf)

	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", arguments.Quiet)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	switch arguments.Action {
	case "list":
		if err := runList(arguments.BwfsHost, arguments.BwfsPort, arguments.ServerName, arguments.PathFilter, arguments.Filter, arguments.Output); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
	}
}
```

- [ ] **Step 7: Add `rwfs` to the Makefile**

Find the `BWFS_CMD` variable and `bwfs` build target in `Makefile`. Add a parallel `RWFS_CMD` variable and `rwfs` target following the exact same pattern (refer to the existing `bwfs:` target block for the precise format — same `CGO_ENABLED`/`GOOS`/`GOARCH`/`$(GO) build` invocation, output to `$(BINARY_DIR)/rwfs`). Add `rwfs` to the `.PHONY` line and to the `BINARIES` aggregation variable if one exists (alongside `brfs bwfs`).

- [ ] **Step 8: Build and verify**

```bash
cd /home/alex/miniprotector && make build
```

Expected output includes a line confirming `rwfs` built successfully to `bin/rwfs`, alongside `brfs` and `bwfs`.

```bash
/home/alex/miniprotector/bin/rwfs --help
/home/alex/miniprotector/bin/rwfs list --help
```

Expected: cobra usage output, no panic.

- [ ] **Step 9: Run all rwfs and bwfs tests**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/rwfs/... ./cmd/bwfs/... ./common/... -v
```

Expected: all PASS.

- [ ] **Step 10: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add cmd/rwfs cmd/bwfs/listserver.go api/list.proto api/list.pb.go api/list_grpc.pb.go && \
cd /home/alex/miniprotector && git add Makefile && \
git commit -m "feat(rwfs): add rwfs binary with list subcommand calling bwfs ListService"
```

---

### Task 8: gRPC round-trip test for the list subprotocol

**Files:**
- Modify: `src/cmd/bwfs/listserver_test.go`

**Interfaces:**
- Consumes: `NewListServer` (Task 6), `pb.RegisterListServiceServer`/`pb.NewListServiceClient` (Task 3)

`cmd/rwfs` cannot import `cmd/bwfs` (both are `package main`), so the gRPC
wire-contract test belongs in `cmd/bwfs`, the package that owns both the
server implementation and the generated `RegisterListServiceServer`. This
test drives `listServer` through a real in-memory gRPC connection
(`bufconn`) instead of calling `ListFiles` directly, verifying the proto
marshaling/unmarshaling round-trip that Task 6's direct-call tests don't
exercise.

- [ ] **Step 1: Add the bufconn round-trip test to `src/cmd/bwfs/listserver_test.go`**

Append to `src/cmd/bwfs/listserver_test.go` (the file created in Task 6):

```go
func TestListFiles_GRPCRoundTrip(t *testing.T) {
	srv, store := newTestListServer(t)
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewListServiceClient(conn)
	resp, err := client.ListFiles(context.Background(), &pb.ListRequest{ServerName: "hosta"})
	require.NoError(t, err)
	require.Len(t, resp.Rows, 1)
	assert.Equal(t, "/data/a.txt", resp.Rows[0].Path)
}
```

Add the necessary imports to the top of `src/cmd/bwfs/listserver_test.go`: `net`, `google.golang.org/grpc`, `google.golang.org/grpc/credentials/insecure`, `google.golang.org/grpc/test/bufconn`.

- [ ] **Step 2: Run the test to verify it passes**

```bash
cd /home/alex/miniprotector/src && go test ./cmd/bwfs/... -run TestListFiles -v
```

Expected: all PASS, including `TestListFiles_GRPCRoundTrip`.

- [ ] **Step 3: Full test suite run**

```bash
cd /home/alex/miniprotector/src && go build ./... && go test ./...
```

Expected: build succeeds, all non-integration/non-e2e tests PASS (integration- and e2e-tagged tests are excluded by default build tags).

```bash
cd /home/alex/miniprotector && make test
```

Expected: PASS (this Makefile target should run both unit and `-tags integration` tests per existing convention — check `make test`'s definition; if it doesn't already include integration tests, also run `cd src && go test -tags integration ./...` directly and confirm PASS).

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add cmd/bwfs/listserver_test.go && \
git commit -m "test(bwfs): add gRPC round-trip test for ListService"
```

---

## Self-Review

**Spec coverage:**
- [x] Remove rrfs entirely — Task 1
- [x] New ListService gRPC subprotocol (separate list.proto) — Task 3
- [x] bwfs registers both BackupService and ListService — Task 6 Step 5
- [x] queryFileRows extended with serverName/pathPrefix — Task 4 Step 5
- [x] Shared rendering code (listformat) — Task 4
- [x] bwfs list CLI syntax change with first-colon-split parsing — Task 5
- [x] rwfs created with list subcommand — Task 7
- [x] rwfs server_name defaults to common.GetHostname() — Task 7 Step 3
- [x] connection.Connect generalized to *grpc.ClientConn — Task 2
- [x] ARCHITECTURE.md updated — Task 1 Step 4
- [x] Makefile updated (rrfs removed, rwfs added) — Task 1 Step 2, Task 7 Step 7
- [x] Testing: colon-split parser unit tests, queryFileRows filter tests, ListFiles handler tests, gRPC round-trip test, rwfs argument parsing tests — Tasks 4, 5, 6, 7, 8
- [x] created_at field added to FileRow proto so rwfs list --output json matches bwfs list --output json — Task 7 Step 5

**Placeholder scan:** No TBD/TODO/"implement later" found. Every code block shown is the final, directly-usable version — no intermediate or corrected-in-place snippets remain.

**Type consistency:**
- `listformat.Row` (Task 4) fields match between bwfs's `queryFileRows` (Task 4 Step 5), `listserver.go`'s conversion (Task 6 Step 3), and `rwfs/list.go`'s conversion (Task 7 Step 5) — `FileDataID, Source, Type, Path string; Timestamp, Size int64; Chunks int; Versions int64; CreatedAt time.Time` used consistently.
- `pb.FileRow` (Task 3, amended in Task 7 Step 5) fields (`FileDataId, Source, Type, Path, Timestamp, Size, Chunks, Versions, CreatedAt`) match what `listserver.go` populates and what `rwfs/list.go` reads.
- `common.ParseServerPath(string) (string, string, error)` (Task 5) signature matches both call sites: `bwfs/arguments.go` (Task 5 Step 5) and `rwfs/arguments.go` (Task 7 Step 3).
- `connection.Connect(host string, port, timeout int) (*grpc.ClientConn, error)` (Task 2) signature matches both call sites: `brfs/main.go` (Task 2 Step 2) and `rwfs/list.go` (Task 7 Step 5).
- `runList` in bwfs (Task 4 Step 5: `(logger, storagePath, serverName, pathPrefix, output, filter string)`) vs `runList` in rwfs (Task 7 Step 5: `(host string, port int, serverName, pathFilter, filter, output string)`) — these are two distinct functions in two distinct `package main`s (no collision), but note the **parameter order differs** (bwfs: `..., output, filter`; rwfs: `..., filter, output`) — call sites in each package's own `main.go` match their own `runList` signature correctly (Task 5 Step 6 for bwfs, Task 7 Step 6 for rwfs), so this is not a bug, just worth the implementer's awareness since the two functions look similar but aren't interchangeable.
