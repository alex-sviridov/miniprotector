# Retention Expiry Stamping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stamp every new `file_version` with an `expire_at`, resolved per file from a per-job retention rule matrix built by `agent`.

**Architecture:** A new shared `retention` package owns the matrix (build, JSON, compile, match). `agent` builds the matrix when a backup task is due (a `Prepare` hook on `Policy`), logs it, writes it to a file and passes `--retention-file` to `brfs`. `brfs` compiles it once and sends `expire_at` per file in `FileInfo`. `bwfs` stores it on `file_version_records`; `catalogsync`/`catalog` replicate it.

**Tech Stack:** Go 1.26, gRPC/protobuf (`make proto`), GORM + SQLite, testify.

**Spec:** `docs/superpowers/specs/2026-10-05-retention-expiry-stamping-design.md`

## Global Constraints

- Module path `github.com/alex-sviridov/miniprotector`; code lives under `src/`; run Go commands from `src/`.
- `expire_at` is an absolute unix-seconds `int64` computed by `brfs` (`now + keep`); node clocks are assumed synced.
- `0` on the wire/`NULL` in SQLite means "no expiry recorded or never expires"; nothing treats NULL as expired.
- Glob syntax is the backup `include` semantics: `*`, `?`, `[...]`, no `**`; a pattern without `/` matches the basename at any depth, with `/` the root-relative path.
- Default keep: config key `RetentionDefaultDays`, default `7`.
- A bad `--retention-file` makes `brfs` exit non-zero before opening any stream.
- Docs per `.claude/CLAUDE.md`: protocol docs for proto changes, component docs, `CHANGELOG.md` entry; commit messages end with the two attribution lines from the session reminder.

## File Structure

- Create `src/retention/retention.go` (+ `retention_test.go`): `Rule`, `Row`, `Matrix`, `Build`, `Compile`, `Matcher.ExpireAt`, `Rel`, `WriteFile`, `LoadFile`.
- Modify `src/workload/filesystem/fileslist.go`: export `MatchesAny`.
- Modify `src/common/config/config.go` (+ test): `RetentionDefaultDays`.
- Modify `src/api/backup.proto`, `src/api/catalog.proto` (+ regenerated `*.pb.go`).
- Modify `src/storage/interface.go`, `src/storage/filesystem/{models,fileversion}.go`, `src/cmd/bwfs/handler.go`.
- Modify `src/cmd/brfs/{arguments,main,filesstream,onefile}.go`; create `src/cmd/brfs/stamp.go` (+ test).
- Create `src/cmd/agent/retention.go` (+ test); modify `src/cmd/agent/{policy,reconcile,backup,main}.go`.
- Modify `src/storage/catalog/{models,store}.go`, `src/cmd/catalog/server.go`, `src/cmd/catalogsync/grpcsender.go`.
- Docs listed in Task 8.

---

### Task 1: `retention` package

**Files:**
- Create: `src/retention/retention.go`
- Test: `src/retention/retention_test.go`
- Modify: `src/workload/filesystem/fileslist.go` (export `MatchesAny`)

**Interfaces:**
- Produces:
  - `type Rule struct { Prefix string; Include []string; KeepSeconds int64 }` (absolute prefix, pre-resolution)
  - `type Row struct { Prefix string \`json:"prefix"\`; Include []string \`json:"include,omitempty"\`; KeepSeconds int64 \`json:"keep_seconds"\` }` (root-relative, slash-separated, `""` = whole root)
  - `type Matrix []Row`
  - `func Build(rules []Rule, root string, defaultKeepSeconds int64) Matrix`
  - `func Compile(m Matrix) (*Matcher, error)`
  - `func (m *Matcher) ExpireAt(relPath string, now time.Time) int64`
  - `func Rel(root, path string) string`
  - `func WriteFile(path string, m Matrix) error`, `func LoadFile(path string) (*Matcher, error)`
  - `filesystem.MatchesAny(patterns []string, relPath string) bool`

- [ ] **Step 1: Export `MatchesAny`**

In `src/workload/filesystem/fileslist.go` rename `matchesAny` to `MatchesAny` (definition at line 74 and every caller):

Run: `cd src && sed -i 's/\bmatchesAny\b/MatchesAny/g' workload/filesystem/*.go && go build ./... && go test ./workload/filesystem/`
Expected: PASS.

- [ ] **Step 2: Write the failing tests**

```go
package retention

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const day = int64(86400)

func TestBuild_NoRulesIsJustTheDefaultRow(t *testing.T) {
	m := Build(nil, "/data", 7*day)
	assert.Equal(t, Matrix{{Prefix: "", KeepSeconds: 7 * day}}, m)
}

func TestBuild_DropsRulesThatCannotOverlapTheRoot(t *testing.T) {
	m := Build([]Rule{{Prefix: "/etc", KeepSeconds: 1}}, "/data", 7*day)
	assert.Equal(t, Matrix{{Prefix: "", KeepSeconds: 7 * day}}, m)
}

func TestBuild_SegmentBoundaryNotStringPrefix(t *testing.T) {
	// /data2 must not be treated as under /data
	m := Build([]Rule{{Prefix: "/data2", KeepSeconds: 1}}, "/data", 7*day)
	assert.Equal(t, Matrix{{Prefix: "", KeepSeconds: 7 * day}}, m)
}

func TestBuild_RewritesRulesBelowRootRelativeToRoot(t *testing.T) {
	m := Build([]Rule{{Prefix: "/data/tmp", Include: []string{"*.log"}, KeepSeconds: 3 * day}}, "/data", 7*day)
	assert.Equal(t, Matrix{
		{Prefix: "tmp", Include: []string{"*.log"}, KeepSeconds: 3 * day},
		{Prefix: "", KeepSeconds: 7 * day},
	}, m)
}

func TestBuild_RuleCoveringRootBecomesEmptyPrefix(t *testing.T) {
	m := Build([]Rule{{Prefix: "/", Include: []string{"*.tmp"}, KeepSeconds: day}}, "/data", 7*day)
	assert.Equal(t, Matrix{
		{Prefix: "", Include: []string{"*.tmp"}, KeepSeconds: day},
		{Prefix: "", KeepSeconds: 7 * day},
	}, m)
}

func TestBuild_TruncatesAfterFirstUnconditionalCoveringRow(t *testing.T) {
	m := Build([]Rule{
		{Prefix: "/data", KeepSeconds: 30 * day},
		{Prefix: "/data/x", KeepSeconds: 1},
	}, "/data", 7*day)
	assert.Equal(t, Matrix{{Prefix: "", KeepSeconds: 30 * day}}, m)
}

func TestBuild_RootSlash(t *testing.T) {
	m := Build([]Rule{{Prefix: "/var/log", KeepSeconds: day}}, "/", 7*day)
	assert.Equal(t, Matrix{{Prefix: "var/log", KeepSeconds: day}, {Prefix: "", KeepSeconds: 7 * day}}, m)
}

func TestCompile_RejectsBadGlobAndBadPrefixAndNegativeKeep(t *testing.T) {
	_, err := Compile(Matrix{{Include: []string{"[abc"}}})
	assert.Error(t, err)
	_, err = Compile(Matrix{{Prefix: "/abs"}})
	assert.Error(t, err)
	_, err = Compile(Matrix{{Prefix: "a/../b"}})
	assert.Error(t, err)
	_, err = Compile(Matrix{{KeepSeconds: -1}})
	assert.Error(t, err)
}

func TestExpireAt_FirstMatchWinsAndSegmentBoundary(t *testing.T) {
	m, err := Compile(Matrix{
		{Prefix: "tmp", KeepSeconds: 3 * day},
		{Prefix: "", Include: []string{"*.log"}, KeepSeconds: 30 * day},
		{Prefix: "", KeepSeconds: 7 * day},
	})
	require.NoError(t, err)
	now := time.Unix(1_000_000, 0)

	assert.Equal(t, now.Unix()+3*day, m.ExpireAt("tmp/a.txt", now))
	assert.Equal(t, now.Unix()+3*day, m.ExpireAt("tmp", now))
	assert.Equal(t, now.Unix()+30*day, m.ExpireAt("tmpfile/a.log", now)) // not under tmp/
	assert.Equal(t, now.Unix()+7*day, m.ExpireAt("docs/readme.md", now))
}

func TestExpireAt_ZeroKeepMeansNever(t *testing.T) {
	m, err := Compile(Matrix{{Prefix: "keep", KeepSeconds: 0}, {Prefix: "", KeepSeconds: day}})
	require.NoError(t, err)
	assert.Equal(t, int64(0), m.ExpireAt("keep/x", time.Unix(5, 0)))
}

func TestExpireAt_EmptyMatrixIsUnset(t *testing.T) {
	m, err := Compile(nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), m.ExpireAt("a", time.Unix(5, 0)))
}

func TestRel(t *testing.T) {
	assert.Equal(t, "a/b", Rel("/data", "/data/a/b"))
	assert.Equal(t, "", Rel("/data", "/data"))
}

func TestWriteFileLoadFile_RoundTripAndMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "m.json")
	require.NoError(t, WriteFile(p, Matrix{{Prefix: "a", KeepSeconds: day}, {KeepSeconds: 7 * day}}))
	m, err := LoadFile(p)
	require.NoError(t, err)
	assert.Equal(t, int64(100)+day, m.ExpireAt("a/x", time.Unix(100, 0)))

	_, err = LoadFile(filepath.Join(t.TempDir(), "missing.json"))
	assert.Error(t, err)
}

func BenchmarkExpireAt_TenRows(b *testing.B) {
	rows := Matrix{}
	for i := 0; i < 9; i++ {
		rows = append(rows, Row{Prefix: "dir" + string(rune('a'+i)), Include: []string{"*.log"}, KeepSeconds: day})
	}
	rows = append(rows, Row{KeepSeconds: 7 * day})
	m, _ := Compile(rows)
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ExpireAt("other/deep/path/file.txt", now)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd src && go test ./retention/`
Expected: FAIL (package has no non-test files / undefined `Build`).

- [ ] **Step 4: Implement**

```go
// Package retention resolves per-file retention for one backup job: agent
// builds a Matrix of ordered rules for the job's root, brfs compiles it once
// and evaluates it per file. First matching row wins.
package retention

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// Rule is a retention rule before resolution against a job root: Prefix is
// absolute and slash-separated.
type Rule struct {
	Prefix      string
	Include     []string
	KeepSeconds int64
}

// Row is a resolved rule. Prefix is relative to the job root, slash-separated;
// "" covers the whole root. KeepSeconds 0 means never expire.
type Row struct {
	Prefix      string   `json:"prefix"`
	Include     []string `json:"include,omitempty"`
	KeepSeconds int64    `json:"keep_seconds"`
}

// Matrix is the ordered rows for one job.
type Matrix []Row

func clean(p string) string { return path.Clean(filepath.ToSlash(p)) }

// within reports whether child equals parent or lies below it, on a path
// segment boundary ("/data2" is not within "/data").
func within(child, parent string) bool {
	return child == parent || parent == "/" || strings.HasPrefix(child, parent+"/")
}

// Build resolves rules (already in priority order) against root: rules that
// cannot overlap root are dropped, the rest rewritten root-relative, and the
// matrix ends with a default row unless an earlier row already covers
// everything unconditionally.
func Build(rules []Rule, root string, defaultKeepSeconds int64) Matrix {
	root = clean(root)
	var m Matrix
	for _, r := range rules {
		p := clean(r.Prefix)
		var rel string
		switch {
		case within(root, p):
			rel = ""
		case within(p, root):
			rel = strings.TrimPrefix(p, strings.TrimSuffix(root, "/")+"/")
		default:
			continue
		}
		m = append(m, Row{Prefix: rel, Include: r.Include, KeepSeconds: r.KeepSeconds})
		if rel == "" && len(r.Include) == 0 {
			return m
		}
	}
	return append(m, Row{KeepSeconds: defaultKeepSeconds})
}

type compiledRow struct {
	prefix      string
	prefixSlash string
	include     []string
	keep        int64
}

// Matcher is an immutable, concurrency-safe compiled Matrix.
type Matcher struct{ rows []compiledRow }

// Compile validates m and precomputes what ExpireAt needs per file.
func Compile(m Matrix) (*Matcher, error) {
	out := &Matcher{rows: make([]compiledRow, 0, len(m))}
	for i, r := range m {
		if strings.HasPrefix(r.Prefix, "/") || strings.Contains(r.Prefix, "..") {
			return nil, fmt.Errorf("retention row %d: prefix %q must be relative and free of '..'", i, r.Prefix)
		}
		if r.KeepSeconds < 0 {
			return nil, fmt.Errorf("retention row %d: negative keep_seconds", i)
		}
		for _, pat := range r.Include {
			if _, err := filepath.Match(pat, ""); err != nil {
				return nil, fmt.Errorf("retention row %d: bad include glob %q: %w", i, pat, err)
			}
		}
		out.rows = append(out.rows, compiledRow{
			prefix: r.Prefix, prefixSlash: r.Prefix + "/", include: r.Include, keep: r.KeepSeconds,
		})
	}
	return out, nil
}

// ExpireAt returns the unix-seconds expiry for relPath (root-relative,
// slash-separated, "" for the root itself): now + keep for the first matching
// row, 0 for never-expire or when no row matches.
func (m *Matcher) ExpireAt(relPath string, now time.Time) int64 {
	for i := range m.rows {
		r := &m.rows[i]
		if r.prefix != "" && relPath != r.prefix && !strings.HasPrefix(relPath, r.prefixSlash) {
			continue
		}
		if len(r.include) > 0 && !filesystem.MatchesAny(r.include, relPath) {
			continue
		}
		if r.keep == 0 {
			return 0
		}
		return now.Unix() + r.keep
	}
	return 0
}

// Rel returns p relative to root, slash-separated, "" for root itself.
func Rel(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// WriteFile atomically writes m as JSON to p, creating parent directories.
func WriteFile(p string, m Matrix) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// LoadFile reads and compiles a matrix written by WriteFile.
func LoadFile(p string) (*Matcher, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m Matrix
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return Compile(m)
}
```

- [ ] **Step 5: Run tests**

Run: `cd src && go test ./retention/ ./workload/... && go test ./retention/ -bench ExpireAt -run xxx`
Expected: PASS; benchmark well under 1µs/op.

- [ ] **Step 6: Commit**

```bash
git add src/retention src/workload/filesystem
git commit -m "feat(retention): matrix build/compile/match package shared by agent and brfs"
```

---

### Task 2: `RetentionDefaultDays` config key

**Files:**
- Modify: `src/common/config/config.go` (struct near line 111, defaults near line 169, parser near line 412)
- Test: `src/common/config/config_test.go`

**Produces:** `conf.RetentionDefaultDays int` (default 7).

- [ ] **Step 1: Failing tests** (append to `config_test.go`, following the `BackupWindowGraceSec` pair)

```go
func TestParseConfig_RetentionDefaultDaysDefaultsTo7(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n"), 0o644))
	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 7, conf.RetentionDefaultDays)
}

func TestParseConfig_RetentionDefaultDaysParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlog_dir=/tmp\nRetentionDefaultDays=30\n"), 0o644))
	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 30, conf.RetentionDefaultDays)
}
```

- [ ] **Step 2:** Run `cd src && go test ./common/config/ -run RetentionDefaultDays` — Expected: FAIL (field undefined).

- [ ] **Step 3: Implement.** Add `RetentionDefaultDays int` after `BackupWindowGraceSec int` in the struct; `RetentionDefaultDays: 7,` after `BackupWindowGraceSec: 3600,` in the defaults; and in the parse switch after the `BackupWindowGraceSec` case:

```go
		case "RetentionDefaultDays":
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 {
				return nil, fmt.Errorf("invalid RetentionDefaultDays value at line %d: %s", lineNum, value)
			}
			config.RetentionDefaultDays = number
			foundFields["RetentionDefaultDays"] = true
```

- [ ] **Step 4:** Run `cd src && go test ./common/config/` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/common/config
git commit -m "feat(config): RetentionDefaultDays (default 7)"
```

---

### Task 3: Proto fields

**Files:**
- Modify: `src/api/backup.proto` (`FileInfo`), `src/api/catalog.proto` (`FileVersionEntry`)
- Regenerate: `src/api/backup.pb.go`, `src/api/catalog.pb.go`

**Produces:** `pb.FileInfo.ExpireAt int64` (field 3), `pb.FileVersionEntry.ExpireAt int64` (field 7).

- [ ] **Step 1:** In `backup.proto`, `FileInfo` becomes:

```proto
message FileInfo {
  string file_id = 1; // hostname:fullpath:mtime
  bytes attributes = 2;
  int64 expire_at = 3; // unix seconds; 0 = no expiry recorded / never expires
}
```

In `catalog.proto`, add to `FileVersionEntry` after `created_at = 6`:

```proto
  int64  expire_at  = 7; // unix seconds; 0 = no expiry recorded / never expires
```

- [ ] **Step 2:** Run `make proto && cd src && go build ./...` — Expected: builds; `git diff --stat src/api` shows only the two proto and two pb.go files.

- [ ] **Step 3: Commit**

```bash
git add src/api
git commit -m "feat(api): expire_at on backup FileInfo and catalog FileVersionEntry"
```

---

### Task 4: `bwfs` stores `expire_at`

**Files:**
- Modify: `src/storage/interface.go:39`, `src/storage/filesystem/models.go` (`FileVersionRecord`), `src/storage/filesystem/fileversion.go`, `src/cmd/bwfs/handler.go` (struct, `handleFileInfoRequest`, `fileWritten`)
- Test: `src/storage/filesystem/store_test.go`, existing call sites

**Interfaces:**
- Consumes: `pb.FileInfo.ExpireAt` (Task 3).
- Produces: `EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata []byte, ctime int64, expireAt int64) error`; `FileVersionRecord.ExpireAt *int64`.

- [ ] **Step 1: Failing tests** (append to `store_test.go`)

```go
func TestEnsureFileVersion_StoresExpireAt(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", "hosta", "/p", "f", nil, 1, 1_700_000_000))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-2", "hosta", "/q", "f", nil, 1, 0))

	var withExpiry, without FileVersionRecord
	require.NoError(t, store.RawDB().Where("object_id = ?", "obj-1").First(&withExpiry).Error)
	require.NoError(t, store.RawDB().Where("object_id = ?", "obj-2").First(&without).Error)
	require.NotNil(t, withExpiry.ExpireAt)
	assert.Equal(t, int64(1_700_000_000), *withExpiry.ExpireAt)
	assert.Nil(t, without.ExpireAt, "0 means unset and must be stored as NULL")
}
```

Update every existing call by appending `, 0`:

Run: `cd src && perl -0pi -e 's/(EnsureFileVersion\([^\n]*?, -?\d+)\)/$1, 0)/g' storage/filesystem/*_test.go`

- [ ] **Step 2:** Run `cd src && go test ./storage/filesystem/ -run StoresExpireAt` — Expected: FAIL (compile error: too many arguments / no field `ExpireAt`).

- [ ] **Step 3: Implement.**
`models.go` — add to `FileVersionRecord` after `Ctime`:

```go
	ExpireAt   *int64 // unix seconds; NULL = no expiry recorded / never expires
```

`interface.go` line 39:

```go
	EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata []byte, ctime int64, expireAt int64) error
```

`fileversion.go`:

```go
func (s *Store) EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata []byte, ctime int64, expireAt int64) error {
	var expire *int64
	if expireAt != 0 {
		expire = &expireAt
	}
	record := FileVersionRecord{
		JobID:      jobID,
		ObjectID:   objectID,
		SourceHost: sourceHost,
		Path:       path,
		Type:       objType,
		Metadata:   metadata,
		Ctime:      ctime,
		ExpireAt:   expire,
		CreatedAt:  time.Now(),
	}
	// ... unchanged OnConflict create
```

Also extend its doc comment: `expireAt` is the client-computed expiry, 0 = none.

`handler.go`: add field `currentExpireAt int64` to `streamHandler`; in `handleFileInfoRequest` right after `h.currentFile = fileInfo` add `h.currentExpireAt = fi.GetExpireAt()`; pass `h.currentExpireAt` as the last argument of both `EnsureFileVersion` calls (the skip path at the `else` branch and `fileWritten`).

- [ ] **Step 4:** Run `cd src && go build ./... && go test ./storage/... ./cmd/bwfs/` — Expected: PASS (AutoMigrate adds the column on open; existing rows NULL).

- [ ] **Step 5: Add handler-level tests** in `src/cmd/bwfs/` next to the existing handler/integration tests: send a `FileInfo` with `ExpireAt` set for (a) a new file that completes via `fileWritten`, (b) an already-known file (skip path), and assert the `file_version_records` row has that `ExpireAt` in both cases. Follow the setup used by the existing tests in `cmd/bwfs/integration_test.go`.

Run: `cd src && go test ./cmd/bwfs/` — Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src
git commit -m "feat(bwfs): record per-file expire_at on file versions"
```

---

### Task 5: `brfs` stamps `expire_at`

**Files:**
- Modify: `src/cmd/brfs/arguments.go`, `main.go`, `filesstream.go`, `onefile.go`
- Create: `src/cmd/brfs/stamp.go`
- Test: `src/cmd/brfs/stamp_test.go`, `src/cmd/brfs/arguments_test.go`

**Interfaces:**
- Consumes: `retention.LoadFile`, `Matcher.ExpireAt`, `retention.Rel`; `pb.FileInfo.ExpireAt`.
- Produces: `type stamper struct{ m *retention.Matcher; root string }`; `func (s *stamper) expireAt(file filesystem.FileInfo, now time.Time) int64` (nil-safe); `Arguments.RetentionFile string`.

- [ ] **Step 1: Failing tests**

`stamp_test.go` (build a `filesystem.FileInfo` the way other brfs/filesystem tests do — via `filesystem.Discover` over a `t.TempDir()` tree; the helper below does that):

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/retention"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

func discover(t *testing.T, root string) map[string]filesystem.FileInfo {
	t.Helper()
	list, err := filesystem.Discover(root, []string{"*"}, nil)
	require.NoError(t, err)
	out := map[string]filesystem.FileInfo{}
	for _, f := range list {
		out[f.Path()] = f
	}
	return out
}

func TestStamper_AppliesFirstMatchingRowPerFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tmp"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tmp", "a.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.txt"), []byte("x"), 0o644))

	p := filepath.Join(t.TempDir(), "m.json")
	require.NoError(t, retention.WriteFile(p, retention.Matrix{
		{Prefix: "tmp", KeepSeconds: 100},
		{Prefix: "", KeepSeconds: 1000},
	}))
	m, err := retention.LoadFile(p)
	require.NoError(t, err)
	st := &stamper{m: m, root: root}
	files := discover(t, root)
	now := time.Unix(5000, 0)

	assert.Equal(t, int64(5100), st.expireAt(files[filepath.Join(root, "tmp", "a.txt")], now))
	assert.Equal(t, int64(6000), st.expireAt(files[filepath.Join(root, "b.txt")], now))
}

func TestStamper_NilSendsZero(t *testing.T) {
	var st *stamper
	assert.Equal(t, int64(0), st.expireAt(filesystem.FileInfo{}, time.Now()))
}
```

`arguments_test.go`: add a test that `--retention-file /x.json` parses into `Arguments.RetentionFile` and that omitting it leaves `""` (copy the shape of `TestParseArguments_JobIDFlag_ParsesValue`).

- [ ] **Step 2:** Run `cd src && go test ./cmd/brfs/` — Expected: FAIL (`stamper` undefined).

- [ ] **Step 3: Implement.**

`stamp.go`:

```go
package main

import (
	"time"

	"github.com/alex-sviridov/miniprotector/retention"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// stamper resolves each file's expire_at from the job's compiled retention
// matrix. A nil stamper (no --retention-file) stamps nothing: 0 on the wire.
type stamper struct {
	m    *retention.Matcher
	root string
}

func (s *stamper) expireAt(file filesystem.FileInfo, now time.Time) int64 {
	if s == nil || s.m == nil {
		return 0
	}
	return s.m.ExpireAt(retention.Rel(s.root, file.Path()), now)
}
```

`arguments.go`: add `retentionFile string` to the flag vars, `RetentionFile string` to `Arguments`, flag `cmd.Flags().StringVar(&retentionFile, "retention-file", "", "JSON retention matrix resolved by agent; per-file expire_at is stamped from it (omit to send none)")`, and `RetentionFile: retentionFile` in the returned struct.

`main.go`: after the logger is created and before `filesystem.Discover`:

```go
	var st *stamper
	if arguments.RetentionFile != "" {
		matcher, err := retention.LoadFile(arguments.RetentionFile)
		if err != nil {
			logger.Error("Retention file unusable, refusing to run unprotected", "path", arguments.RetentionFile, "error", err)
			os.Exit(1)
		}
		st = &stamper{m: matcher, root: arguments.SourceFolder}
	}
```

(import `github.com/alex-sviridov/miniprotector/retention`), and pass `st` into `processFilesList(ctx, logger, client, filesList, arguments.Streams, st)`.

`filesstream.go`: `processFilesList(..., streams int, st *stamper)`; `go stream(ctx, logger, client, workChan, resultChan, cancelAllStreams, &wg, st)`; `stream(..., wg *sync.WaitGroup, st *stamper)`; call `processOneFile(ctx, fileLogger, stream, f, st.expireAt(f, time.Now()))` (import `time`).

`onefile.go`: `processOneFile(ctx, logger, stream, file filesystem.FileInfo, expireAt int64)` and `sendFileMetadata(ctx, logger, stream, file, expireAt)` setting `ExpireAt: expireAt` in the `pb.FileInfo` literal.

- [ ] **Step 4:** Run `cd src && go build ./... && go test ./cmd/brfs/` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/brfs
git commit -m "feat(brfs): stamp per-file expire_at from --retention-file matrix"
```

---

### Task 6: `agent` resolves, logs and passes the matrix

**Files:**
- Create: `src/cmd/agent/retention.go`, `src/cmd/agent/retention_test.go`
- Modify: `src/cmd/agent/policy.go` (`Policy` struct), `src/cmd/agent/reconcile.go` (both dispatch paths), `src/cmd/agent/backup.go` (`backupTasks`), `src/cmd/agent/main.go` (two `backupTasks` calls), existing tests that call `backupTasks`

**Interfaces:**
- Consumes: `retention.Build`, `retention.WriteFile`, `conf.RetentionDefaultDays`.
- Produces: `Policy.Prepare func(logger *slog.Logger) ([]string, error)` (extra args computed just before exec; error = failed attempt); `backupTasks(cachedPolicies []cachedPolicy, logger *slog.Logger, conf *config.Config, retentionDir string) []Policy`.

- [ ] **Step 1: Failing tests** (`retention_test.go`)

```go
package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/retention"
)

func TestPrepareRetention_WritesFileLogsMatrixAndReturnsFlag(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	dir := t.TempDir()
	conf := &config.Config{RetentionDefaultDays: 7}

	args, err := prepareRetention(logger, conf, dir, "backup:p:/data:abcd1234", "job-1", "/data", nil)
	require.NoError(t, err)

	require.Len(t, args, 2)
	assert.Equal(t, "--retention-file", args[0])
	assert.Equal(t, dir, filepath.Dir(args[1]))

	m, err := retention.LoadFile(args[1])
	require.NoError(t, err)
	assert.Equal(t, int64(1_000_000+7*86400), m.ExpireAt("x", timeAt(1_000_000)))

	assert.Contains(t, buf.String(), `"event":"retention_matrix"`)
	assert.Contains(t, buf.String(), `"job_id":"job-1"`)
	assert.Contains(t, buf.String(), `"keep_seconds":604800`)
}

func TestPrepareRetention_UnwritableDirIsAnError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	_, err := prepareRetention(slog.Default(), &config.Config{RetentionDefaultDays: 7}, filepath.Join(file, "sub"), "t", "j", "/data", nil)
	assert.Error(t, err)
}

func TestSafeFileName(t *testing.T) {
	assert.Equal(t, "backup_p__data_abcd1234", safeFileName("backup:p:/data:abcd1234"))
}
```

(Add `func timeAt(sec int64) time.Time { return time.Unix(sec, 0) }` to the test file, importing `time`.)

`backup_test.go`: add

```go
func TestBackupTasks_AttachPrepareThatAddsRetentionFile(t *testing.T) {
	// reuse the cached-policy fixture/helpers already used by this file's
	// other backupTasks tests (writeCachedPolicies + mustReadCachedPolicies)
	dir := t.TempDir()
	tasks := backupTasks(fixtureCachedBackupPolicy(t), slog.Default(), &config.Config{BackupWindowGraceSec: 3600, RetentionDefaultDays: 7}, dir)
	require.NotEmpty(t, tasks)
	require.NotNil(t, tasks[0].Prepare)
	before := len(tasks[0].Args)

	extra, err := tasks[0].Prepare(slog.Default())
	require.NoError(t, err)
	assert.Equal(t, "--retention-file", extra[0])
	assert.Len(t, tasks[0].Args, before, "Prepare must not mutate Args; the extra args are returned")
}
```

(`fixtureCachedBackupPolicy` = a tiny helper in the test file calling `mustReadCachedPolicies(t, writeCachedPolicies(t, t.TempDir(), <the same valid backup-policy JSON the neighbouring tests use>))`.)

`reconcile_test.go`: add a test that a task whose `Prepare` returns an error never calls `execute` and is recorded as a failed attempt, and one whose `Prepare` returns extra args has them appended to what `execute` receives (follow the existing fake `runner` pattern in that file).

- [ ] **Step 2:** Run `cd src && go test ./cmd/agent/` — Expected: FAIL (compile errors: `prepareRetention`, `Prepare`, extra `backupTasks` arg).

- [ ] **Step 3: Implement.**

`retention.go`:

```go
package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/retention"
)

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// safeFileName maps a task ID (which contains ':' and '/') to a file name.
func safeFileName(taskID string) string {
	return unsafeFileChars.ReplaceAllString(taskID, "_")
}

// retentionRulesFrom returns the retention rules in effect, in priority
// order. Retention policies don't exist until Part 2 of the retention work
// (see docs/superpowers/specs/2026-10-05-retention-expiry-stamping-design.md),
// so today only the built-in default applies; this is the seam it plugs into.
func retentionRulesFrom(_ []cachedPolicy) []retention.Rule { return nil }

// prepareRetention resolves the retention matrix for one backup job, logs it
// under the job's id, writes it to retentionDir (one file per task, overwritten
// each run) and returns the brfs args that point at it. The logged matrix is
// exactly what brfs applies: agent is the only place that resolves rules.
func prepareRetention(logger *slog.Logger, conf *config.Config, retentionDir, taskID, jobID, root string, rules []retention.Rule) ([]string, error) {
	m := retention.Build(rules, root, int64(conf.RetentionDefaultDays)*86400)
	path := filepath.Join(retentionDir, safeFileName(taskID)+".json")
	if err := retention.WriteFile(path, m); err != nil {
		return nil, fmt.Errorf("write retention matrix: %w", err)
	}
	logger.Info("retention matrix resolved", "job_id", jobID, "task", taskID, "rows", m, "event", "retention_matrix")
	return []string{"--retention-file", path}, nil
}
```

`policy.go` — add to `Policy` after `Stdin`:

```go
	// Prepare, when non-nil, runs immediately before this task's exec (so only
	// when it is actually due) and returns extra args appended to Args. An
	// error fails the attempt without exec'ing, with the usual backoff. Used by
	// backup tasks to resolve and hand brfs its retention matrix at job start.
	Prepare func(logger *slog.Logger) ([]string, error)
```

(import `log/slog` if missing.)

`reconcile.go` — add helper and use it in both dispatch paths (replace the two `execute(ctx, p.Binary, p.Args, p.Stdin)` calls):

```go
// execWithPrepare runs p.Prepare (if any) and then execute with the combined
// args; a Prepare failure is the attempt's error and nothing is exec'd.
func execWithPrepare(ctx context.Context, logger *slog.Logger, execute runner, p Policy) error {
	args := p.Args
	if p.Prepare != nil {
		extra, err := p.Prepare(logger)
		if err != nil {
			return fmt.Errorf("prepare %s: %w", p.ID, err)
		}
		args = append(append([]string(nil), p.Args...), extra...)
	}
	return execute(ctx, p.Binary, args, p.Stdin)
}
```

Background path: `attemptErr := execWithPrepare(ctx, rs.logger, execute, p)`; synchronous path the same.

`backup.go` — new signature `backupTasks(cachedPolicies []cachedPolicy, logger *slog.Logger, conf *config.Config, retentionDir string) []Policy`; compute `rules := retentionRulesFrom(cachedPolicies)` once before the loop; in the `tasks = append(tasks, Policy{...})` literal add:

```go
				Prepare: func(l *slog.Logger) ([]string, error) {
					return prepareRetention(l, conf, retentionDir, backupTaskID(policyName, filter.Path, filter.ID), jobID, filter.Path, rules)
				},
```

(`filter` and `jobID` are loop variables; Go 1.22+ per-iteration semantics make the capture safe.) Update the doc comment to mention it.

`main.go`: both call sites pass `filepath.Join(varDir, "retention")` — `newDerivedFunc` needs the dir: add a `retentionDir string` parameter to `newDerivedFunc` (caller in `serve` has `varDir`), and in `listPolicies` pass `filepath.Join(varDir, "retention")` is unavailable (no varDir) — pass `""` there since `list-policies` never calls `Prepare`.

Then fix the remaining test call sites: `cd src && go vet ./cmd/agent/ 2>&1 | grep -n backupTasks` and append `, t.TempDir()` (or `""` where the result is only inspected).

- [ ] **Step 4:** Run `cd src && go test ./cmd/agent/` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent
git commit -m "feat(agent): resolve and log the retention matrix at backup job start, pass --retention-file to brfs"
```

---

### Task 7: `catalogsync` and `catalog` replicate `expire_at`

**Files:**
- Modify: `src/cmd/catalogsync/grpcsender.go`, `src/cmd/catalog/server.go`, `src/storage/catalog/models.go` (`EntryRecord`), `src/storage/catalog/store.go` (`Entry`, `ensureEntries`)
- Test: `src/cmd/catalogsync/grpcsender_test.go`, `src/storage/catalog/store_test.go`, `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: `wfs.FileVersionRecord.ExpireAt *int64`, `pb.FileVersionEntry.ExpireAt`.
- Produces: `catalogstore.Entry.ExpireAt int64`, `EntryRecord.ExpireAt *int64`.

- [ ] **Step 1: Failing tests**

`grpcsender_test.go` (next to the existing conversion test; reuse `newTestGrpcSender`):

```go
func TestGrpcSender_Send_CarriesExpireAt(t *testing.T) {
	fake := &fakeCatalogServer{}
	sender := newTestGrpcSender(t, fake)
	exp := int64(1_700_000_000)
	require.NoError(t, sender.Send([]wfs.FileVersionRecord{
		{Seq: 1, JobID: "j", ObjectID: "a", ExpireAt: &exp, CreatedAt: time.Now()},
		{Seq: 2, JobID: "j", ObjectID: "b", CreatedAt: time.Now()},
	}))
	require.Len(t, fake.lastReq.Entries, 2)
	assert.Equal(t, exp, fake.lastReq.Entries[0].ExpireAt)
	assert.Equal(t, int64(0), fake.lastReq.Entries[1].ExpireAt)
}
```

`store_test.go`:

```go
func TestEnsureEntries_PersistsExpireAt(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "n", JobID: "j", ObjectID: "a", ExpireAt: 1_700_000_000, StoreCreatedAt: time.Now()},
		{StoreNode: "n", JobID: "j", ObjectID: "b", StoreCreatedAt: time.Now()},
	}))
	recs, _, err := store.ListEntries(t.Context(), ListEntriesFilter{})
	require.NoError(t, err)
	byObj := map[string]EntryRecord{}
	for _, r := range recs {
		byObj[r.ObjectID] = r
	}
	require.NotNil(t, byObj["a"].ExpireAt)
	assert.Equal(t, int64(1_700_000_000), *byObj["a"].ExpireAt)
	assert.Nil(t, byObj["b"].ExpireAt)
}
```

`server_test.go`: extend `TestSyncFileVersions_PersistsBatchUnderPeerHostname`-style test with an entry carrying `ExpireAt` and assert it lands via `store.ListEntries`.

- [ ] **Step 2:** Run `cd src && go test ./cmd/catalogsync/ ./storage/catalog/ ./cmd/catalog/` — Expected: FAIL (compile errors on `ExpireAt`).

- [ ] **Step 3: Implement.**
- `grpcsender.go`: before building `entries[i]` add `var expire int64; if r.ExpireAt != nil { expire = *r.ExpireAt }` and set `ExpireAt: expire,` in the literal.
- `models.go` `EntryRecord`: add `ExpireAt *int64 // unix seconds; NULL = no expiry recorded / never expires` after `Ctime`.
- `store.go`: add `ExpireAt int64` to `Entry`; in `ensureEntries` build `var expire *int64; if e.ExpireAt != 0 { v := e.ExpireAt; expire = &v }` and set `ExpireAt: expire` in the `EntryRecord` literal.
- `cmd/catalog/server.go`: add `ExpireAt: e.GetExpireAt(),` to the `catalogstore.Entry` literal.

- [ ] **Step 4:** Run `cd src && go build ./... && go test ./cmd/catalogsync/ ./storage/catalog/ ./cmd/catalog/` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src
git commit -m "feat(catalog): replicate per-file expire_at from bwfs through catalogsync"
```

---

### Task 8: Docs, changelog, full verification

**Files:**
- Modify: `docs/protocols/backup.md`, `docs/protocols/catalog-sync.md`, `docs/components/{agent,brfs,bwfs,catalogsync,catalog}.md`, `docs/superpowers/specs/2026-10-05-retention-expiry-stamping-design.md`, `CHANGELOG.md`; check `docs/ARCHITECTURE.md`, `README.md`

- [ ] **Step 1: Protocol docs.** `backup.md`: document `FileInfo.expire_at` (field 3, unix seconds, 0 = no expiry recorded/never) in the per-file metadata step and why it travels there (skipped files still record a version). `catalog-sync.md`: add `expire_at = 7` to the `FileVersionEntry` listing with the same semantics. Cross-links already exist from README; keep them.

- [ ] **Step 2: Component docs.** `agent.md`: new "Retention matrix" subsection under policy-driven backup execution (built when the task is due via `Policy.Prepare`, overlap pruning/rewrite/truncation, `retention_matrix` log event, `<varDir>/retention/<task>.json` overwritten per run, `RetentionDefaultDays` in the config table, Part 1 seam `retentionRulesFrom`). `brfs.md`: `--retention-file` flag, fail-fast behaviour, no flag = no `expire_at`. `bwfs.md`: `expire_at` column on file versions, NULL semantics. `catalogsync.md` / `catalog.md`: replicated and stored.

- [ ] **Step 3: Spec sync.** In the spec: change "removed with the task's state" to "one file per task, overwritten on each run" and state that `expire_at = 0` conflates "unset" and "never", both meaning never auto-deleted.

- [ ] **Step 4: CHANGELOG.** Add at the top, dated today:

```markdown
## 2026-10-05 — Retention expiry stamping

Every new file version now carries an `expire_at`, decided per file at backup time. When a backup
task is due, `agent` resolves a retention rule matrix for the job (today just the built-in
default, `RetentionDefaultDays`, 7), logs it as a `retention_matrix` event under the job's id and
hands it to `brfs` via `--retention-file`; `brfs` evaluates it per file (first matching rule wins,
prefix plus optional glob) and sends `expire_at` with the file's metadata, `bwfs` stores it on
`file_versions`, and `catalogsync` replicates it to the catalog. Nothing deletes anything yet:
this lays the data foundation for retention policies and a later cleanup process. Existing
versions keep a NULL `expire_at` (never expired). See
`docs/superpowers/specs/2026-10-05-retention-expiry-stamping-design.md`.
```

- [ ] **Step 5: Architecture/README check.** Run `grep -n "brfs\|agent" docs/ARCHITECTURE.md | head`; update only if the data-flow text or diagram needs the retention hand-off (agent → brfs). README unchanged unless quick-start flags changed (they didn't).

- [ ] **Step 6: Full verification**

Run: `make lint && make test`
Expected: vet clean; all unit and integration tests PASS. (`make test-e2e` needs the demo lab; skip unless it is up.)

- [ ] **Step 7: Commit**

```bash
git add docs CHANGELOG.md
git commit -m "docs: retention expiry stamping — protocols, components, changelog"
```

---

## Self-Review

- **Spec coverage:** matrix build/prune/rewrite/truncate/default → Task 1 + 6; logging + file + flag → Task 6; per-file stamping, fail-fast, no-flag → Task 5; proto fields → Task 3; `bwfs` storage for new and skipped files, NULL for old rows → Task 4; catalog replication → Task 7; docs/CHANGELOG → Task 8; benchmark → Task 1; error handling (bad file, bad glob) → Tasks 1, 5, 6.
- **Placeholders:** none; the two fixture helpers named in Task 6 tests (`fixtureCachedBackupPolicy`, `timeAt`) are defined there.
- **Type consistency:** `retention.Rule/Row/Matrix/Build/Compile/ExpireAt/Rel/WriteFile/LoadFile`, `stamper.expireAt`, `Policy.Prepare`, `prepareRetention`, `retentionRulesFrom`, `EnsureFileVersion(..., expireAt int64)`, `FileVersionRecord.ExpireAt *int64`, `Entry.ExpireAt int64` match across tasks.
