# bwfs Subcommands Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `server` and `list` subcommands to `bwfs`, where `list` queries the store read-only (no flock) and renders stored file data as a table or JSON.

**Architecture:** Root cobra command takes `storage_path`; two subcommands (`server`, `list`) hang off it. A new `NewReadOnly` constructor opens the DB without acquiring the flock, allowing `list` to run concurrently with a live server. A new `list.go` file runs a single JOIN query and renders output; `arguments.go` and `main.go` are updated to dispatch on action.

**Tech Stack:** Go 1.26, `github.com/spf13/cobra`, `gorm.io/gorm`, `text/tabwriter`, `encoding/json`, `modernc.org/sqlite`

## Global Constraints

- Go 1.26.0 — use `go1.26` in all `go` directives
- `CGO_ENABLED=0` must build cleanly throughout
- `modernc.org/sqlite` pure-Go driver only — no `mattn/go-sqlite3`
- `bwfs` is Linux-only; no Windows platform concerns in this plan
- No new dependencies — all needed packages already in `go.mod`
- Run all tests from `src/` directory: `go test ./...`
- Storage tests are in `package filesystem` (internal package)

---

### Task 1: `NewReadOnly` and `RawDB` on `*Store`

**Files:**
- Modify: `src/storage/filesystem/store.go`
- Test: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Produces: `NewReadOnly(basePath string) (*Store, error)` — opens DB, no flock, `lockFile` is nil
- Produces: `(*Store).RawDB() *gorm.DB` — returns `s.db` for CLI query use
- Produces: updated `(*Store).Close() error` — guards `s.lockFile != nil` before closing it

- [ ] **Step 1: Write the failing test**

Add to `src/storage/filesystem/store_test.go`:

```go
func TestNewReadOnly_CanOpenWhileExclusiveLockHeld(t *testing.T) {
	dir := t.TempDir()

	// Simulate a live server holding the exclusive lock
	server, err := New(dir)
	require.NoError(t, err)
	defer server.Close()

	// NewReadOnly must succeed despite the exclusive flock
	ro, err := NewReadOnly(dir)
	require.NoError(t, err)
	defer ro.Close()

	// RawDB must be non-nil and usable
	assert.NotNil(t, ro.RawDB())
	assert.NoError(t, ro.RawDB().Exec("SELECT 1").Error)
}

func TestNewReadOnly_CloseDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	ro, err := NewReadOnly(dir)
	require.NoError(t, err)
	assert.NoError(t, ro.Close()) // must not panic on nil lockFile
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd src && go test ./storage/filesystem/ -run 'TestNewReadOnly' -v
```

Expected: FAIL — `NewReadOnly` undefined.

- [ ] **Step 3: Implement `NewReadOnly`, `RawDB`, and update `Close`**

Replace the full contents of `src/storage/filesystem/store.go` with:

```go
package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

type Store struct {
	basePath string
	db       *gorm.DB
	lockFile *os.File
}

func New(basePath string) (*Store, error) {
	chunksDir := filepath.Join(basePath, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return nil, fmt.Errorf("create chunks dir: %w", err)
	}

	lockFile, err := acquireLock(basePath)
	if err != nil {
		return nil, err
	}

	db, err := openDB(basePath)
	if err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("open db: %w", err)
	}

	return &Store{basePath: basePath, db: db, lockFile: lockFile}, nil
}

// NewReadOnly opens the store for read-only administrative use (e.g. CLI listing).
// It does not acquire the exclusive flock, so it can run alongside a live bwfs server.
// SQLite WAL mode allows concurrent readers with no blocking.
func NewReadOnly(basePath string) (*Store, error) {
	db, err := openDB(basePath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return &Store{basePath: basePath, db: db}, nil
}

func acquireLock(basePath string) (*os.File, error) {
	lockPath := filepath.Join(basePath, "metadata.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("store at %s already in use by another process", basePath)
		}
		return nil, fmt.Errorf("acquire store lock: %w", err)
	}
	return f, nil
}

// RawDB returns the underlying *gorm.DB for read-only administrative queries.
// Not part of BackupStore interface — only for CLI tooling.
func (s *Store) RawDB() *gorm.DB { return s.db }

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

var _ storage.BackupStore = (*Store)(nil)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd src && go test ./storage/filesystem/ -run 'TestNewReadOnly' -v
```

Expected: PASS.

- [ ] **Step 5: Run full storage test suite**

```bash
cd src && go test ./storage/filesystem/ -v
```

Expected: all existing tests still PASS (including `TestNew_ExclusiveLock`).

- [ ] **Step 6: Verify CGO_ENABLED=0 build**

```bash
cd src && CGO_ENABLED=0 go build ./...
```

Expected: clean build.

- [ ] **Step 7: Commit**

```bash
git add src/storage/filesystem/store.go src/storage/filesystem/store_test.go
git commit -m "feat: add NewReadOnly constructor and RawDB accessor to filesystem store"
```

---

### Task 2: `arguments.go` — cobra subcommands

**Files:**
- Modify: `src/cmd/bwfs/arguments.go`

**Interfaces:**
- Produces: `Arguments` struct with fields `StoragePath`, `Action`, `Port`, `Debug`, `Quiet`, `Output`, `Filter`
- Produces: `parseArguments(conf *config.Config) (*Arguments, error)` — returns error if no subcommand given or `--output` is invalid

- [ ] **Step 1: Rewrite `arguments.go`**

Replace the full contents of `src/cmd/bwfs/arguments.go` with:

```go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

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

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "bwfs <storage_path> <command>",
		Short: "Backup writer filesystem tool",
		Args:  cobra.ExactArgs(1),
	}

	// server subcommand
	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Start the backup writer server",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			args.Action = "server"
			args.StoragePath = rootCmd.Flags().Args()[0]
		},
	}
	serverCmd.Flags().IntVar(&args.Port, "port", conf.DefaultPort, "Port to listen on")
	serverCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	serverCmd.Flags().BoolVar(&args.Quiet, "quiet", false, "Enable quiet mode")

	// list subcommand
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List stored file data",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			args.Action = "list"
			args.StoragePath = rootCmd.Flags().Args()[0]
		},
	}
	listCmd.Flags().StringVar(&args.Output, "output", "table", "Output format: table or json")
	listCmd.Flags().StringVar(&args.Filter, "filter", "", "Filter by text in file path")
	listCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	rootCmd.AddCommand(serverCmd, listCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: server or list")
	}

	if args.Action == "server" {
		if err := common.ValidatePort(args.Port); err != nil {
			return nil, fmt.Errorf("port error: %w", err)
		}
	}

	if args.Action == "list" && args.Output != "table" && args.Output != "json" {
		return nil, fmt.Errorf("--output must be 'table' or 'json', got: %q", args.Output)
	}

	return args, nil
}
```

- [ ] **Step 2: Build to verify no compile errors**

```bash
cd src && CGO_ENABLED=0 go build ./cmd/bwfs/
```

Expected: clean build. (main.go will fail to compile if it references the old `arguments.Debug` without `Quiet` — fix is in Task 3.)

- [ ] **Step 3: Commit**

```bash
git add src/cmd/bwfs/arguments.go
git commit -m "feat: rewrite bwfs arguments.go with cobra server/list subcommands"
```

---

### Task 3: `main.go` — dispatch on action

**Files:**
- Modify: `src/cmd/bwfs/main.go`

**Interfaces:**
- Consumes: `Arguments.Action` (`"server"` | `"list"`) from Task 2
- Consumes: `runList(logger *slog.Logger, storagePath, output, filter string) error` from Task 4 (forward reference — build will pass once Task 4 is done)

- [ ] **Step 1: Rewrite `main.go`**

Replace the full contents of `src/cmd/bwfs/main.go` with:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
	const (
		configPath = "../.config/local.conf"
		appName    = "bwfs"
	)

	ctx := context.WithValue(context.Background(), "appName", appName)

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

		if err := connection.StartServer(ctx, logger, arguments.Port, backupServer); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}

	case "list":
		if err := runList(logger, arguments.StoragePath, arguments.Output, arguments.Filter); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add src/cmd/bwfs/main.go
git commit -m "feat: dispatch bwfs main on action (server/list)"
```

---

### Task 4: `list.go` — query, parse, render

**Files:**
- Create: `src/cmd/bwfs/list.go`

**Interfaces:**
- Consumes: `wfs.NewReadOnly(basePath string) (*Store, error)` from Task 1
- Consumes: `(*Store).RawDB() *gorm.DB` from Task 1
- Produces: `runList(logger *slog.Logger, storagePath, output, filter string) error`

- [ ] **Step 1: Create `list.go`**

Create `src/cmd/bwfs/list.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

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

type fileRowJSON struct {
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

func runList(logger *slog.Logger, storagePath, output, filter string) error {
	store, err := wfs.NewReadOnly(storagePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	rows, err := queryFileRows(store, filter)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	switch output {
	case "json":
		return renderJSON(rows)
	default:
		return renderTable(rows)
	}
}

type queryResult struct {
	FileDataID string `gorm:"column:file_data_id"`
	FileID     string `gorm:"column:file_id"`
	Size       int64  `gorm:"column:size"`
	Chunks     int    `gorm:"column:chunks"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	Versions   int64  `gorm:"column:versions"`
}

func queryFileRows(store *wfs.Store, filter string) ([]fileRow, error) {
	query := store.RawDB().
		Table("file_data_records fd").
		Select("fd.id AS file_data_id, fd.file_id, fd.size, fd.chunk_count AS chunks, fd.created_at, COUNT(fv.id) AS versions").
		Joins("LEFT JOIN file_version_records fv ON fv.file_id = fd.file_id").
		Where("fd.checksum IS NOT NULL").
		Group("fd.file_id").
		Order("fd.created_at ASC")

	if filter != "" {
		query = query.Where("fd.file_id LIKE ?", "%"+filter+"%")
	}

	var results []queryResult
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	rows := make([]fileRow, len(results))
	for i, r := range results {
		src, typ, path, ts := parseFileID(r.FileID)
		rows[i] = fileRow{
			FileDataID: r.FileDataID,
			FileID:     r.FileID,
			Source:     src,
			Type:       typ,
			Path:       path,
			Timestamp:  ts,
			Size:       r.Size,
			Chunks:     r.Chunks,
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt,
		}
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
	// Minimum valid: host, type, path, mtime = 4 tokens
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

func formatSize(bytes int64) string {
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

func renderTable(rows []fileRow) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tTYPE\tPATH\tTIMESTAMP\tSIZE\tCHUNKS\tVERSIONS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%d\n",
			r.Source, r.Type, r.Path, r.Timestamp, formatSize(r.Size), r.Chunks, r.Versions)
	}
	return w.Flush()
}

func renderJSON(rows []fileRow) error {
	out := make([]fileRowJSON, len(rows))
	for i, r := range rows {
		out[i] = fileRowJSON{
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
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
```

- [ ] **Step 2: Build to verify no compile errors**

```bash
cd src && CGO_ENABLED=0 go build ./cmd/bwfs/
```

Expected: clean build.

- [ ] **Step 3: Smoke-test with a real store**

```bash
cd src && go test -tags integration ./cmd/bwfs/ -v -run TestIntegration_NewFile_TransferPath
```

Expected: PASS — this confirms the server path still works after the refactor.

- [ ] **Step 4: Run full test suite**

```bash
cd src && go test ./...
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/bwfs/list.go
git commit -m "feat: add bwfs list subcommand with table and JSON output"
```

---

## Self-Review

**Spec coverage:**

| Spec requirement | Task |
|---|---|
| `bwfs <storage_path> server` works as before | Task 3 |
| `bwfs <storage_path> list` subcommand | Task 2, 3, 4 |
| `--output table\|json` with table default | Task 2, 4 |
| `--filter` substring match in SQL | Task 4 |
| `NewReadOnly` — no flock, concurrent with server | Task 1 |
| `Close()` guards nil `lockFile` | Task 1 |
| `RawDB()` accessor for CLI tooling | Task 1 |
| Table columns: SOURCE TYPE PATH TIMESTAMP SIZE CHUNKS VERSIONS | Task 4 |
| JSON fields: file_data_id, source, type, path, timestamp, size, chunks, versions, created_at | Task 4 |
| `file_id` parse handles Windows paths with colons | Task 4 `parseFileID` |
| Empty result: table prints header only; JSON prints `[]` | Task 4 — `renderTable` prints header unconditionally; `json.Encode([])` produces `[]` ✓ |
| `--output` invalid value rejected before store opens | Task 2 — validation in `parseArguments` after `Execute()`, before any store call ✓ |
| `BackupStore` interface unchanged | No interface file touched ✓ |

**Placeholder scan:** No TBDs, all code blocks are complete.

**Type consistency:**
- `wfs.NewReadOnly` defined in Task 1, consumed in Task 4 ✓
- `(*Store).RawDB() *gorm.DB` defined in Task 1, consumed in Task 4 ✓
- `runList(logger *slog.Logger, storagePath, output, filter string) error` defined in Task 4, consumed in Task 3 ✓
- `Arguments.Action`, `.Output`, `.Filter` defined in Task 2, consumed in Task 3 ✓
- `queryResult.Chunks` maps to `fd.chunk_count AS chunks` via GORM column tag ✓
