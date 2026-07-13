# Object Filter Include/Exclude Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a backup `ObjectFilter` (path) carry optional `include`/`exclude` glob-pattern lists, honored end-to-end from `policy-server`'s on-disk policy files through `policyclient`'s cache, `agent`'s derived backup tasks, and into `brfs`'s own directory walk.

**Architecture:** `brfs` gets two new comma-separated flags (`--include`, `--exclude`) and applies them itself while walking the source folder — excludes prune whole subtrees via `filepath.SkipDir`, includes act as a files-only whitelist. `ObjectFilter` gains matching `Include []string`/`Exclude []string` fields at every layer above `brfs` (policy-server's JSON schema, the gRPC proto, `policyclient`'s cache, `agent`'s mirrored cache struct), and `agent` passes them through to `brfs`'s exec line only when non-empty.

**Tech Stack:** Go, `filepath.WalkDir`/`filepath.Match` (stdlib), Cobra (CLI flags), protobuf/gRPC (policy-server↔policyclient), `testify` (tests).

## Global Constraints

- Patterns are `filepath.Match`/`path.Match` globs only — no `**`, no regex.
- A pattern with no `/` matches an entry's **basename at any depth**; a pattern containing `/` matches the entry's **path relative to the object filter's root**.
- An exclude match on a directory prunes it and everything beneath it (`filepath.SkipDir`); an exclude match on a file omits just that file. Exclude is checked before include.
- Include only filters files, never directories, and never affects traversal.
- The only place that materializes a default is `brfs`'s own `--include` flag default (`"*"`). No other layer (policy-server, `policyclient`, `agent`) fills in a default when `include`/`exclude` is omitted — they stay empty/absent, and `agent` only emits `--include`/`--exclude` on `brfs`'s exec line when the corresponding list is non-empty.
- No migration path for old `policies-cache.json` files — the format change is breaking and that's accepted (agent-internal, fully repopulated every fetch, pre-release project).

---

## Task 1: `brfs` include/exclude filtering (filesystem walk + CLI)

**Files:**
- Create: `src/workload/filesystem/fileslist_test.go`
- Modify: `src/workload/filesystem/fileslist.go`
- Modify: `src/workload/filesystem/fileinfo.go`
- Modify: `src/workload/interface.go`
- Modify: `src/cmd/brfs/arguments.go`
- Modify: `src/cmd/brfs/arguments_test.go`
- Modify: `src/cmd/brfs/main.go`

**Interfaces:**
- Produces: `filesystem.Discover(root string, include, exclude []string) (FilesList, error)` — replaces the current single-argument `Discover(path string)`.
- Produces: `Arguments.Include []string` and `Arguments.Exclude []string` on `cmd/brfs`'s `Arguments` struct (built from the new `--include`/`--exclude` comma-separated flags; empty flag value → `nil` slice, not `[""]`).

- [ ] **Step 1: Write the failing tests for `Discover`**

Create `src/workload/filesystem/fileslist_test.go`:

```go
package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pathPresent checks whether any discovered FileInfo's ID (format
// fs://host:type:path:mtime) carries absPath as its path field. The path
// field is bounded by ":" on both sides, so this substring check can't
// false-positive against ":"+"<longer path with absPath as prefix>".
func pathPresent(files FilesList, absPath string) bool {
	needle := ":" + absPath + ":"
	for _, f := range files {
		if strings.Contains(f.ID(), needle) {
			return true
		}
	}
	return false
}

func TestDiscover_NoExcludePatterns_IncludesEverything(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "nested.txt"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*"}, nil)
	require.NoError(t, err)

	assert.Len(t, files, 4) // root, keep.txt, sub, sub/nested.txt
	assert.True(t, pathPresent(files, root))
	assert.True(t, pathPresent(files, filepath.Join(root, "keep.txt")))
	assert.True(t, pathPresent(files, filepath.Join(root, "sub")))
	assert.True(t, pathPresent(files, filepath.Join(root, "sub", "nested.txt")))
}

func TestDiscover_ExcludeBasenamePattern_MatchesAtAnyDepth(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "skip.tmp"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "nested.tmp"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*"}, []string{"*.tmp"})
	require.NoError(t, err)

	assert.True(t, pathPresent(files, filepath.Join(root, "keep.txt")))
	assert.True(t, pathPresent(files, filepath.Join(root, "sub")), "directory itself must survive; only the .tmp file inside is excluded")
	assert.False(t, pathPresent(files, filepath.Join(root, "skip.tmp")))
	assert.False(t, pathPresent(files, filepath.Join(root, "sub", "nested.tmp")))
}

func TestDiscover_ExcludeDirectory_PrunesEntireSubtree(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.txt"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "excluded_dir", "deeper"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "excluded_dir", "inner.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "excluded_dir", "deeper", "deepfile.txt"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*"}, []string{"excluded_dir"})
	require.NoError(t, err)

	assert.Len(t, files, 2) // root, keep.txt
	assert.True(t, pathPresent(files, root))
	assert.True(t, pathPresent(files, filepath.Join(root, "keep.txt")))
	assert.False(t, pathPresent(files, filepath.Join(root, "excluded_dir")))
	assert.False(t, pathPresent(files, filepath.Join(root, "excluded_dir", "inner.txt")))
	assert.False(t, pathPresent(files, filepath.Join(root, "excluded_dir", "deeper", "deepfile.txt")))
}

func TestDiscover_ExcludeRelativePathPattern_MatchesOnlyThatDepth(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "skip.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b", "skip.txt"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*"}, []string{"a/skip.txt"})
	require.NoError(t, err)

	assert.False(t, pathPresent(files, filepath.Join(root, "a", "skip.txt")))
	assert.True(t, pathPresent(files, filepath.Join(root, "b", "skip.txt")))
	assert.True(t, pathPresent(files, filepath.Join(root, "a")))
	assert.True(t, pathPresent(files, filepath.Join(root, "b")))
}

func TestDiscover_IncludeFiltersFilesButNotDirectories(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.log"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "deep.log"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*.log"}, nil)
	require.NoError(t, err)

	assert.True(t, pathPresent(files, root), "root directory is never filtered by include")
	assert.True(t, pathPresent(files, filepath.Join(root, "sub")), "sub directory is never filtered by include, even though its name doesn't match *.log")
	assert.True(t, pathPresent(files, filepath.Join(root, "app.log")))
	assert.True(t, pathPresent(files, filepath.Join(root, "sub", "deep.log")))
	assert.False(t, pathPresent(files, filepath.Join(root, "notes.txt")))
}

func TestDiscover_ExcludeTakesPrecedenceOverInclude(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.log"), []byte("x"), 0o644))

	files, err := Discover(root, []string{"*.log"}, []string{"keep.log"})
	require.NoError(t, err)

	assert.Len(t, files, 1) // root only
	assert.True(t, pathPresent(files, root))
	assert.False(t, pathPresent(files, filepath.Join(root, "keep.log")))
}

func TestDiscover_NonexistentRootReturnsError(t *testing.T) {
	_, err := Discover(filepath.Join(t.TempDir(), "does-not-exist"), []string{"*"}, nil)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run the new tests, confirm they fail to build**

Run: `cd src && go test ./workload/filesystem/... -run TestDiscover -v`
Expected: FAIL — compile error, `Discover` still takes one argument (`too many arguments in call to Discover`).

- [ ] **Step 3: Implement the filtering walk in `fileslist.go`**

Replace the full contents of `src/workload/filesystem/fileslist.go` with:

```go
package filesystem

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/alex-sviridov/miniprotector/common"
)

type FilesList []FileInfo

// Discover walks root, returning one FileInfo per surviving file and
// directory. exclude is checked first: a directory matching any exclude
// pattern is pruned entirely (skipped, along with everything beneath it);
// a file matching any exclude pattern is omitted. include is then checked
// for files only -- a file is kept only if it matches at least one include
// pattern; directories are never filtered by include, so traversal always
// continues into non-excluded directories, and a directory entry that
// survives the exclude check is always emitted.
//
// A pattern with no "/" matches an entry's basename at any depth; a
// pattern containing "/" matches the entry's path relative to root. An
// empty include list matches no files -- callers that want "match
// everything" must pass a pattern (e.g. []string{"*"}) explicitly; this
// function applies no default of its own.
func Discover(root string, include, exclude []string) (FilesList, error) {
	result := FilesList{}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, fmt.Errorf("source path does not exist: %s", root)
	}

	hostname := common.GetHostname()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("failed to walk dir %s: %w", path, err)
		}

		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, relErr)
		}

		if matchesAny(exclude, relPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.IsDir() && !matchesAny(include, relPath) {
			return nil
		}

		fileInfo, err := getFileInfo(path)
		fileInfo.host = hostname
		if err != nil {
			return fmt.Errorf("failed to get file info %s: %w", path, err)
		}

		result = append(result, fileInfo)
		return nil
	})

	return result, err
}

// matchesAny reports whether relPath matches any pattern: a pattern with
// no "/" is matched against relPath's basename (so it matches at any
// depth); a pattern containing "/" is matched against relPath itself.
func matchesAny(patterns []string, relPath string) bool {
	base := filepath.Base(relPath)
	for _, pattern := range patterns {
		target := base
		if strings.Contains(pattern, "/") {
			target = relPath
		}
		if matched, _ := filepath.Match(pattern, target); matched {
			return true
		}
	}
	return false
}
```

Then remove the now-dead `match` method from `src/workload/filesystem/fileinfo.go` — delete this block (and the blank line above it):

```go
func (fi FileInfo) match(pattern string) bool {
	matched, _ := filepath.Match(pattern, fi.path)
	return matched
}
```

and remove the now-unused `"path/filepath"` import from that same file's import block (it was only used by `match`).

Then remove the now-unused `BackupObjectsList` interface from `src/workload/interface.go` — delete:

```go
// BackupObjectsList represents a filterable array of Backup Objects
type BackupObjectsList interface {
	WithIncludes(patterns ...string) BackupObjectsList
	WithExcludes(patterns ...string) BackupObjectsList
}
```

(Nothing else in the codebase references `BackupObjectsList`, `WithIncludes`, or `WithExcludes` — they were dead code before this change too; the old flat, non-pruning filter shape can't express the subtree-pruning behavior `Discover` now implements directly.)

- [ ] **Step 4: Run the filesystem tests, confirm they pass**

Run: `cd src && go test ./workload/filesystem/... -v`
Expected: PASS — all `TestDiscover_*` tests plus the existing `TestChunkIterator_*` tests green.

- [ ] **Step 5: Write the failing tests for `brfs`'s new flags**

Append to `src/cmd/brfs/arguments_test.go`:

```go
func TestParseArguments_IncludeFlag_DefaultsToAsterisk(t *testing.T) {
	dir := t.TempDir()
	withArgs(t, []string{"brfs", dir}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, []string{"*"}, args.Include)
	})
}

func TestParseArguments_ExcludeFlag_DefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	withArgs(t, []string{"brfs", dir}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Empty(t, args.Exclude)
	})
}

func TestParseArguments_IncludeFlag_SplitsOnComma(t *testing.T) {
	dir := t.TempDir()
	withArgs(t, []string{"brfs", dir, "--include", "*.log,*.txt"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, []string{"*.log", "*.txt"}, args.Include)
	})
}

func TestParseArguments_ExcludeFlag_SplitsOnComma(t *testing.T) {
	dir := t.TempDir()
	withArgs(t, []string{"brfs", dir, "--exclude", "node_modules,*.tmp"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, []string{"node_modules", "*.tmp"}, args.Exclude)
	})
}
```

- [ ] **Step 6: Run the brfs tests, confirm they fail to build**

Run: `cd src && go test ./cmd/brfs/... -run TestParseArguments -v`
Expected: FAIL — compile error, `args.Include`/`args.Exclude` undefined (`Arguments` has no such fields yet).

- [ ] **Step 7: Implement the `--include`/`--exclude` flags in `arguments.go`**

In `src/cmd/brfs/arguments.go`, add `"strings"` to the import block, then change the flags var block:

```go
// Command line flags
var (
	destination string
	streams     int
	debug       bool
	quiet       bool
	jobIDFlag   string
	includeFlag string
	excludeFlag string
)
```

Add two fields to the `Arguments` struct:

```go
// Arguments holds parsed command line arguments
type Arguments struct {
	SourceFolder string
	WriterHost   string
	WriterPort   int
	Streams      int
	Debug        bool
	Quiet        bool
	JobID        string
	Include      []string
	Exclude      []string
}
```

Register the two new flags right after the `job-id` flag:

```go
	cmd.Flags().StringVar(&jobIDFlag, "job-id", "", "Backup job ID (auto-generated if omitted)")
	cmd.Flags().StringVar(&includeFlag, "include", "*", "Comma-separated glob patterns; only matching files are backed up")
	cmd.Flags().StringVar(&excludeFlag, "exclude", "", "Comma-separated glob patterns; matching files/directories are skipped")
```

Add a helper function anywhere in the file (e.g. just above `parseArguments`):

```go
// splitPatterns splits a comma-separated flag value into a pattern list;
// an empty string produces a nil (empty) slice rather than []string{""}.
func splitPatterns(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}
```

Populate the two new fields in the returned `Arguments`:

```go
	return &Arguments{
		SourceFolder: validatedSourceFolder,
		WriterHost:   host,
		WriterPort:   port,
		Streams:      streams,
		Debug:        debug,
		Quiet:        quiet,
		JobID:        jobIDFlag,
		Include:      splitPatterns(includeFlag),
		Exclude:      splitPatterns(excludeFlag),
	}, nil
}
```

- [ ] **Step 8: Wire the parsed flags into `Discover` in `main.go`**

In `src/cmd/brfs/main.go`, change:

```go
	filesList, err := filesystem.Discover(arguments.SourceFolder)
```

to:

```go
	filesList, err := filesystem.Discover(arguments.SourceFolder, arguments.Include, arguments.Exclude)
```

- [ ] **Step 9: Run the full test suite and build, confirm everything passes**

Run: `cd src && go test ./workload/filesystem/... ./cmd/brfs/... -v && go build ./...`
Expected: PASS — all tests green, `go build` succeeds with no errors.

- [ ] **Step 10: Commit**

```bash
git add src/workload/filesystem/fileslist.go src/workload/filesystem/fileslist_test.go \
        src/workload/filesystem/fileinfo.go src/workload/interface.go \
        src/cmd/brfs/arguments.go src/cmd/brfs/arguments_test.go src/cmd/brfs/main.go
git commit -m "feat(brfs): add --include/--exclude directory-walk filtering"
```

---

## Task 2: `policy-server` `ObjectFilter.Include`/`Exclude`

**Files:**
- Modify: `src/api/policyserver.proto`
- Regenerate: `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go` (via `make proto`)
- Modify: `src/cmd/policy-server/policy.go`
- Modify: `src/cmd/policy-server/policy_test.go`
- Modify: `src/cmd/policy-server/cache.go`
- Modify: `src/cmd/policy-server/cache_test.go`
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/server_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `ObjectFilter{Path, Include []string, Exclude []string}` in `cmd/policy-server`'s on-disk/in-memory `Policy` type, and `pb.ObjectFilter.GetInclude()/GetExclude() []string` on the wire — both consumed by Task 3 (`policyclient`).

- [ ] **Step 1: Add `include`/`exclude` to the proto and regenerate**

In `src/api/policyserver.proto`, change:

```proto
message ObjectFilter {
  string path = 1;
}
```

to:

```proto
message ObjectFilter {
  string path = 1;
  repeated string include = 2;
  repeated string exclude = 3;
}
```

Run: `make proto`
Expected: `Protobuf code generated in src/api/` printed, no errors. Confirm with `grep -n "GetInclude\|GetExclude" src/api/policyserver.pb.go` — both methods should now exist on `*ObjectFilter`.

- [ ] **Step 2: Write the failing tests for `policy.go`**

In `src/cmd/policy-server/policy_test.go`, change the existing `TestParsePolicyFile_ValidPolicyParsesAllFields` test's policy JSON and assertion:

```go
func TestParsePolicyFile_ValidPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-10T00:00:00Z"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	assert.Equal(t, "nightly-web-backup", p.Metadata.Name)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.Metadata.CreatedAt)
	assert.Equal(t, []string{"web-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, p.ClientFilters.Labels)
	assert.Equal(t, []ObjectFilter{{Path: "/var/www", Include: []string{"*.html", "*.css"}, Exclude: []string{"*.tmp"}}}, p.ObjectFilters)
	assert.Equal(t, "24h", p.RPO)
	assert.Equal(t, []string{"0 2 * * *", "0 20 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
}
```

Add three new tests, right after it:

```go
func TestParsePolicyFile_ObjectFilterOmitsIncludeExclude(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "minimal.json", `{
		"metadata": {"name": "minimal"},
		"object_filters": [{"path": "/data"}]
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	require.Len(t, p.ObjectFilters, 1)
	assert.Equal(t, "/data", p.ObjectFilters[0].Path)
	assert.Empty(t, p.ObjectFilters[0].Include)
	assert.Empty(t, p.ObjectFilters[0].Exclude)
}

func TestParsePolicyFile_InvalidIncludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "include": ["["]}]
	}`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidExcludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "exclude": ["["]}]
	}`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}
```

- [ ] **Step 3: Write the failing test for `cache.go`'s deep copy**

In `src/cmd/policy-server/cache_test.go`, update `TestCache_PoliciesReturnsSnapshotCopy`'s policy JSON and add mutation assertions for `Include`/`Exclude`:

```go
func TestCache_PoliciesReturnsSnapshotCopy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{
		"metadata": {"name": "policy-a"},
		"client_filters": {
			"hostnames": ["host1", "host2"],
			"labels": {"env": "prod", "team": "platform"}
		},
		"object_filters": [{"path": "/data/*", "include": ["*.sql"], "exclude": ["*.tmp"]}],
		"rpo": "1h",
		"backup_window": ["08:00", "12:00"]
	}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	// Test mutation of plain value field (should not affect cache)
	got := c.Policies()
	got[0].Metadata.Name = "mutated-name"

	// Test mutation of nested slice field
	got[0].ClientFilters.Hostnames[0] = "mutated-host"

	// Test mutation of nested map field
	got[0].ClientFilters.Labels["env"] = "dev"

	// Test mutation of ObjectFilters slice
	got[0].ObjectFilters[0].Path = "/mutated/*"
	got[0].ObjectFilters[0].Include[0] = "mutated"
	got[0].ObjectFilters[0].Exclude[0] = "mutated"

	// Test mutation of BackupWindow slice
	got[0].BackupWindow[0] = "23:00"

	// Verify that a fresh call to Policies() returns the original values
	got2 := c.Policies()
	assert.Equal(t, "policy-a", got2[0].Metadata.Name, "mutating Metadata.Name in returned snapshot must not affect cache")
	assert.Equal(t, "host1", got2[0].ClientFilters.Hostnames[0], "mutating Hostnames in returned snapshot must not affect cache")
	assert.Equal(t, "prod", got2[0].ClientFilters.Labels["env"], "mutating Labels in returned snapshot must not affect cache")
	assert.Equal(t, "/data/*", got2[0].ObjectFilters[0].Path, "mutating ObjectFilters in returned snapshot must not affect cache")
	assert.Equal(t, "*.sql", got2[0].ObjectFilters[0].Include[0], "mutating ObjectFilters[].Include in returned snapshot must not affect cache")
	assert.Equal(t, "*.tmp", got2[0].ObjectFilters[0].Exclude[0], "mutating ObjectFilters[].Exclude in returned snapshot must not affect cache")
	assert.Equal(t, "08:00", got2[0].BackupWindow[0], "mutating BackupWindow in returned snapshot must not affect cache")
}
```

- [ ] **Step 4: Write the failing test for `server.go`'s proto conversion**

In `src/cmd/policy-server/server_test.go`, update `TestGetPolicies_ResponseFieldsRoundTrip`:

```go
func TestGetPolicies_ResponseFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "full.json", `{
		"metadata": {"name": "full-policy", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
		"object_filters": [{"path": "/var/www", "include": ["*.html"], "exclude": ["*.tmp"]}, {"path": "/etc"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, "full-policy", p.Name)
	assert.Equal(t, "24h", p.Rpo)
	assert.Equal(t, []string{"0 2 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
	require.Len(t, p.ObjectFilters, 2)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, []string{"*.html"}, p.ObjectFilters[0].Include)
	assert.Equal(t, []string{"*.tmp"}, p.ObjectFilters[0].Exclude)
	assert.Empty(t, p.ObjectFilters[1].Include)
	assert.Empty(t, p.ObjectFilters[1].Exclude)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.CreatedAt.AsTime())
	assert.Equal(t, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), p.UpdatedAt.AsTime())
}
```

- [ ] **Step 5: Run the policy-server tests, confirm they fail to build**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: FAIL — compile errors referencing `ObjectFilter{... Include: ...}` (unknown field) in the test files, since `policy.go`'s `ObjectFilter` doesn't have those fields yet.

- [ ] **Step 6: Implement `Include`/`Exclude` in `policy.go`**

In `src/cmd/policy-server/policy.go`, change:

```go
type ObjectFilter struct {
	Path string `json:"path"`
}
```

to:

```go
type ObjectFilter struct {
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}
```

Then add pattern validation in `parsePolicyFile`, right after the existing hostname-pattern loop and before `return p, nil`:

```go
	for _, of := range p.ObjectFilters {
		for _, pattern := range of.Include {
			if _, err := path.Match(pattern, ""); err != nil {
				return Policy{}, fmt.Errorf("%s: invalid include pattern %q: %w", filePath, pattern, err)
			}
		}
		for _, pattern := range of.Exclude {
			if _, err := path.Match(pattern, ""); err != nil {
				return Policy{}, fmt.Errorf("%s: invalid exclude pattern %q: %w", filePath, pattern, err)
			}
		}
	}
```

- [ ] **Step 7: Implement the deep copy in `cache.go`**

In `src/cmd/policy-server/cache.go`, replace this line:

```go
		copy(out[i].ObjectFilters, p.ObjectFilters)
```

with:

```go
		for j, f := range p.ObjectFilters {
			out[i].ObjectFilters[j] = ObjectFilter{
				Path:    f.Path,
				Include: append([]string(nil), f.Include...),
				Exclude: append([]string(nil), f.Exclude...),
			}
		}
```

- [ ] **Step 8: Implement the proto conversion in `server.go`**

In `src/cmd/policy-server/server.go`, change:

```go
	objectFilters[i] = &pb.ObjectFilter{Path: f.Path}
```

to:

```go
	objectFilters[i] = &pb.ObjectFilter{Path: f.Path, Include: f.Include, Exclude: f.Exclude}
```

- [ ] **Step 9: Run the policy-server tests, confirm they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS — all tests green.

- [ ] **Step 10: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go \
        src/cmd/policy-server/policy.go src/cmd/policy-server/policy_test.go \
        src/cmd/policy-server/cache.go src/cmd/policy-server/cache_test.go \
        src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go
git commit -m "feat(policy-server): add include/exclude to ObjectFilter"
```

---

## Task 3: `policyclient` cache carries `Include`/`Exclude`

**Files:**
- Modify: `src/cmd/policyclient/fetch.go`
- Modify: `src/cmd/policyclient/fetch_test.go`

**Interfaces:**
- Consumes: `pb.ObjectFilter.GetPath()/GetInclude()/GetExclude() string/[]string` (Task 2).
- Produces: `ObjectFilter{Path, Include []string, Exclude []string}` and `CachedPolicy.ObjectFilters []ObjectFilter` — the on-disk `policies-cache.json` shape Task 4 (`agent`) mirrors.

- [ ] **Step 1: Write the failing test**

In `src/cmd/policyclient/fetch_test.go`, update `TestRunFetch_Success_WritesCacheFile`:

```go
func TestRunFetch_Success_WritesCacheFile(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "nested", "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Name:      "daily-db-backup",
				CreatedAt: created,
				UpdatedAt: updated,
				ObjectFilters: []*pb.ObjectFilter{
					{Path: "/var/lib/postgres", Include: []string{"*.sql"}},
					{Path: "/etc/postgres", Exclude: []string{"*.bak"}},
				},
				Rpo:          "24h",
				BackupWindow: []string{"0 2 * * *"},
				Destination:  "bwfs-east.internal:8080",
			},
		},
	}}

	err := runFetch(context.Background(), fake, cachePath, fetchTestLogger())
	require.NoError(t, err)

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)

	var got []CachedPolicy
	require.NoError(t, json.Unmarshal(data, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "daily-db-backup", got[0].Name)
	assert.True(t, created.AsTime().Equal(got[0].CreatedAt))
	assert.True(t, updated.AsTime().Equal(got[0].UpdatedAt))
	assert.Equal(t, []ObjectFilter{
		{Path: "/var/lib/postgres", Include: []string{"*.sql"}},
		{Path: "/etc/postgres", Exclude: []string{"*.bak"}},
	}, got[0].ObjectFilters)
	assert.Equal(t, "24h", got[0].RPO)
	assert.Equal(t, []string{"0 2 * * *"}, got[0].BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", got[0].Destination)
}
```

- [ ] **Step 2: Run the test, confirm it fails to build**

Run: `cd src && go test ./cmd/policyclient/... -run TestRunFetch_Success -v`
Expected: FAIL — compile error, `ObjectFilter` type undefined in package `main` (policyclient), and `got[0].ObjectFilters` type mismatch (`[]string` vs. the literal above).

- [ ] **Step 3: Implement the new type and conversion in `fetch.go`**

In `src/cmd/policyclient/fetch.go`, replace the `CachedPolicy` doc comment and struct:

```go
// ObjectFilter is the on-disk representation of one policy-server
// ObjectFilter: a backup root path plus its optional include/exclude glob
// patterns, carried through verbatim from the RPC response.
type ObjectFilter struct {
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// CachedPolicy is the on-disk representation of one policy-server Policy --
// the same fields the GetPolicies RPC response already defines, converted
// directly from the protobuf message.
type CachedPolicy struct {
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}
```

Then update `toCachedPolicies`:

```go
func toCachedPolicies(policies []*pb.Policy) []CachedPolicy {
	out := make([]CachedPolicy, 0, len(policies))
	for _, p := range policies {
		filters := make([]ObjectFilter, 0, len(p.GetObjectFilters()))
		for _, of := range p.GetObjectFilters() {
			filters = append(filters, ObjectFilter{
				Path:    of.GetPath(),
				Include: of.GetInclude(),
				Exclude: of.GetExclude(),
			})
		}
		out = append(out, CachedPolicy{
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
		})
	}
	return out
}
```

- [ ] **Step 4: Run the policyclient tests, confirm they pass**

Run: `cd src && go test ./cmd/policyclient/... -v`
Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go
git commit -m "feat(policyclient): carry include/exclude through the policy cache"
```

---

## Task 4: `agent` derives `--include`/`--exclude` on `brfs`'s exec line

**Files:**
- Modify: `src/cmd/agent/backup.go`
- Modify: `src/cmd/agent/backup_test.go`
- Modify: `src/cmd/agent/integration_test.go`

**Interfaces:**
- Consumes: `--include`/`--exclude` flag names and comma-separated shape from Task 1 (`brfs`'s CLI).
- Produces: `Policy.Args` on backup tasks now conditionally includes `--include <comma-joined>` and/or `--exclude <comma-joined>` when the originating `ObjectFilter` carries them.

- [ ] **Step 1: Update the JSON fixtures across `backup_test.go` and `integration_test.go` to the new object-filter shape**

In `src/cmd/agent/backup_test.go`, change every occurrence of a flat-string `object_filters` array to the object form (path only, since none of these tests care about include/exclude):

- `TestBackupTasks_OnePolicyWithTwoPathsYieldsTwoTasksWithStableDistinctIDs`: `"object_filters": ["/var/lib/postgres", "/etc/postgres"],` → `"object_filters": [{"path": "/var/lib/postgres"}, {"path": "/etc/postgres"}],`
- `TestBackupTasks_TaskArgsMatchBrfsShape`: `"object_filters": ["/var/lib/postgres"],` → `"object_filters": [{"path": "/var/lib/postgres"}],`
- `TestBackupTasks_DueRequiresBothWindowOpenAndRpoElapsed`: `"object_filters": ["/data"],` → `"object_filters": [{"path": "/data"}],`
- `TestBackupTasks_PerPathIndependence`: `"object_filters": ["/a", "/b"],` → `"object_filters": [{"path": "/a"}, {"path": "/b"}],`
- `TestBackupTasks_UnparseableRpoSkipsPolicyEntirely`: `"object_filters": ["/data"],` → `"object_filters": [{"path": "/data"}],`
- `TestBackupTasks_NoValidBackupWindowSkipsPolicyEntirely`: `"object_filters": ["/data"],` → `"object_filters": [{"path": "/data"}],`
- `TestBackupTasks_RemovedPolicyStopsBeingDerived`: `"object_filters": ["/data"], "rpo": "1h",` → `"object_filters": [{"path": "/data"}], "rpo": "1h",`

In `TestBackupTasks_JobIDFieldMatchesArgsFlag`, change the Go struct literal:

```go
	cached := []cachedPolicy{{
		Name:          "web-policy",
		ObjectFilters: []string{"/srv/web"},
		RPO:           "1h",
		BackupWindow:  []string{"* * * * *"},
		Destination:   "bwfs:9000",
	}}
```

to:

```go
	cached := []cachedPolicy{{
		Name:          "web-policy",
		ObjectFilters: []ObjectFilter{{Path: "/srv/web"}},
		RPO:           "1h",
		BackupWindow:  []string{"* * * * *"},
		Destination:   "bwfs:9000",
	}}
```

In `src/cmd/agent/integration_test.go`, change:

```go
		"object_filters": ["/var/lib/postgres"],
```

to:

```go
		"object_filters": [{"path": "/var/lib/postgres"}],
```

Then append a new test to `backup_test.go` covering the conditional flags:

```go
func TestBackupTasks_TaskArgsIncludeIncludeExcludeFlagsWhenPresent(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "web-policy",
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.Len(t, task.Args, 9)
	assert.Equal(t, "/var/www", task.Args[0])
	assert.Equal(t, "--destination", task.Args[1])
	assert.Equal(t, "bwfs:8080", task.Args[2])
	assert.Equal(t, "--job-id", task.Args[3])
	assert.Equal(t, "--include", task.Args[5])
	assert.Equal(t, "*.html,*.css", task.Args[6])
	assert.Equal(t, "--exclude", task.Args[7])
	assert.Equal(t, "*.tmp", task.Args[8])
}
```

- [ ] **Step 2: Run the agent tests, confirm they fail to build**

Run: `cd src && go test ./cmd/agent/... -run TestBackupTasks -v`
Expected: FAIL — compile error, `cachedPolicy.ObjectFilters` is still `[]string`, so the `[]ObjectFilter{...}` literal and the JSON-object fixtures don't match the current type.

- [ ] **Step 3: Implement the schema and `Args` changes in `backup.go`**

In `src/cmd/agent/backup.go`, replace the `cachedPolicy` struct and its doc comment:

```go
// ObjectFilter mirrors the subset of policyclient's on-disk ObjectFilter
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type ObjectFilter struct {
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// cachedPolicy mirrors the subset of policyclient's on-disk CachedPolicy
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type cachedPolicy struct {
	Name          string         `json:"name"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}
```

Then replace the inner loop of `backupTasks` (the `for _, path := range p.ObjectFilters` block) with:

```go
		policyName, destination := p.Name, p.Destination
		for _, filter := range p.ObjectFilters {
			jobID := backupJobID(policyName, filter.Path, time.Now())
			args := []string{filter.Path, "--destination", destination, "--job-id", jobID}
			if len(filter.Include) > 0 {
				args = append(args, "--include", strings.Join(filter.Include, ","))
			}
			if len(filter.Exclude) > 0 {
				args = append(args, "--exclude", strings.Join(filter.Exclude, ","))
			}
			tasks = append(tasks, Policy{
				ID:         backupTaskID(policyName, filter.Path),
				Binary:     "brfs",
				JobID:      jobID,
				Args:       args,
				Background: true,
				Due: func(s PolicyState, now time.Time) bool {
					return windowOpen(schedules, now, grace) && rpoElapsed(s, now, rpo)
				},
				NextRun: func(s PolicyState, now time.Time) time.Time {
					return nextWindow(schedules, now)
				},
			})
		}
```

(`"strings"` is already imported in this file for `slug`.)

- [ ] **Step 4: Run the agent tests, confirm they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/backup.go src/cmd/agent/backup_test.go src/cmd/agent/integration_test.go
git commit -m "feat(agent): pass include/exclude through to brfs's exec line"
```

---

## Task 5: Documentation, changelog, and demo

**Files:**
- Create: `docs/process/filesystem-backup.md`
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/policyclient.md`
- Modify: `docs/components/agent.md`
- Modify: `docs/components/brfs.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `README.md`
- Modify: `CHANGELOG.md`
- Modify: `demo/policy-server/policies/webserver-backup.json`
- Create: `demo/sample-data/web/access.log`

**Interfaces:** none — documentation and fixture content only, no code.

- [ ] **Step 1: Write `docs/process/filesystem-backup.md`**

Create the file with this content:

```markdown
# Filesystem Backup Flow

A short walk-through of how a file ends up backed up, end to end, and where `include`/`exclude`
fit in. For wire-level and per-component detail, see the linked docs below — this page is just the
narrative connecting them.

## The flow

1. An operator writes a policy JSON file under `policy-server`'s `$MP_CONFIG_PATH/policies/`. Each
   `object_filters` entry is a backup root (`path`) plus optional `include`/`exclude` glob-pattern
   lists. Neither is required — omit both to back up everything under `path`.
2. Every enrolled node's `agent` runs `policyclient fetch` on a schedule, which calls
   `policy-server`'s `GetPolicies` RPC and caches the matching policies (including each object
   filter's `include`/`exclude`) to `policies-cache.json`.
3. `agent` derives one backup task per cached `(policy, object filter)` pair. When a task is due
   (its `backup_window` is open and its `rpo` has elapsed), `agent` execs `brfs <path>
   --destination <destination> --job-id <id>`, adding `--include <patterns>` and/or `--exclude
   <patterns>` only when the object filter actually carries them.
4. `brfs` walks `path`, applying `--exclude` first (pruning a matched directory's entire subtree,
   omitting a matched file) and `--include` second (a files-only whitelist — directories are never
   filtered by it). Surviving files stream to `bwfs`.

## Matching semantics

Patterns are plain glob patterns (`*`, `?`, `[...]`) — no regex, no `**`. A pattern with no `/`
matches a file's basename at any depth (`*.tmp` matches `cache/x.tmp` and `a/b/x.tmp` alike); a
pattern containing `/` matches the path relative to the object filter's root exactly.

## See Also

- [policy-server](../components/policy-server.md) - policy authoring and on-disk schema
- [policyclient](../components/policyclient.md) - fetch/cache behavior
- [agent](../components/agent.md#policy-driven-backup-execution) - backup task derivation and scheduling
- [brfs](../components/brfs.md) - the actual directory walk and filtering
- [Policy Server Protocol](../protocols/policy-server.md) - wire-level `ObjectFilter` definition
```

- [ ] **Step 2: Update `docs/protocols/policy-server.md`**

Change the `ObjectFilter` message block:

```proto
message ObjectFilter {
  string path = 1;
}
```

to:

```proto
message ObjectFilter {
  string path = 1;
  repeated string include = 2;
  repeated string exclude = 3;
}
```

Add a sentence to the `## Behavior` section, right after the `client_filters` bullet:

```markdown
- Each `object_filters` entry's `include`/`exclude` are opaque, pass-through glob-pattern lists —
  `policy-server` validates their syntax at load time but never evaluates them; `brfs` is what
  applies them, during its own directory walk.
```

- [ ] **Step 3: Update `docs/components/policy-server.md`**

Change:

```markdown
`object_filters`
(a list of `{"path": "..."}` entries), `rpo` (a duration string, e.g. `"24h"`), `backup_window`
```

to:

```markdown
`object_filters`
(a list of `{"path": "...", "include": [...], "exclude": [...]}` entries — `include`/`exclude` are
optional glob-pattern lists, validated as syntactically-valid patterns at load time but otherwise
opaque to `policy-server`; see [Filesystem Backup Flow](../process/filesystem-backup.md) for how
`brfs` applies them), `rpo` (a duration string, e.g. `"24h"`), `backup_window`
```

- [ ] **Step 4: Update `docs/components/policyclient.md`**

Change the cache JSON example's `object_filters` line:

```json
    "object_filters": ["/var/lib/postgres", "/etc/postgres"],
```

to:

```json
    "object_filters": [
      {"path": "/var/lib/postgres", "include": ["*.sql"]},
      {"path": "/etc/postgres"}
    ],
```

- [ ] **Step 5: Update `docs/components/agent.md`**

Change:

```markdown
When due, `agent` execs `brfs <path> --destination <destination> --job-id
backup:<policy>:<slug(path)>:<timestamp>` — the explicit job-id lets an operator correlate a
`bwfs` job record back to the policy and path that produced it.
```

to:

```markdown
When due, `agent` execs `brfs <path> --destination <destination> --job-id
backup:<policy>:<slug(path)>:<timestamp>`, appending `--include <patterns>` and/or `--exclude
<patterns>` (comma-joined) only when the object filter that produced this task actually carries
them — the explicit job-id lets an operator correlate a `bwfs` job record back to the policy and
path that produced it.
```

- [ ] **Step 6: Update `docs/components/brfs.md`**

Add two flags to the `## Arguments and Flags` list, right after `--job-id`:

```markdown
- `--include <patterns>` - Comma-separated glob patterns; only matching files are backed up *(default: `*`)*
- `--exclude <patterns>` - Comma-separated glob patterns; matching files and directories are skipped *(default: none)*
```

Add a new section right after `## Examples`:

```markdown
## Filtering

A pattern with no `/` matches a file's basename at any depth (`*.tmp` excludes every `.tmp` file
anywhere under the source folder); a pattern containing `/` matches the path relative to the
source folder exactly. `--exclude` is checked first: a directory that matches is pruned along with
everything beneath it; a file that matches is skipped. `--include` is then checked for files only
— directories are never filtered by it, so traversal always continues into non-excluded
directories.

```bash
# Back up only .sql files, skipping anything under a "tmp" directory
brfs /var/lib/postgres --destination localhost:8080 --include "*.sql" --exclude "tmp"
```
```

- [ ] **Step 7: Update `docs/ARCHITECTURE.md`**

Change:

```markdown
`agent` also derives a dynamic
backup task per cached policy's object path and executes `brfs` for each one on a schedule gated by
that policy's `backup_window` and `rpo`
```

to:

```markdown
`agent` also derives a dynamic
backup task per cached policy's object filter (a path plus optional include/exclude glob patterns,
passed straight through to `brfs`) and executes `brfs` for each one on a schedule gated by
that policy's `backup_window` and `rpo`
```

- [ ] **Step 8: Update `README.md`**

Add a line to the `## Documentation` list, right after the Architecture line:

```markdown
- **[Filesystem Backup Flow](docs/process/filesystem-backup.md)** - End-to-end walk-through of policy → agent → brfs → bwfs, including include/exclude filtering
```

- [ ] **Step 9: Add a `CHANGELOG.md` entry**

Add this heading and paragraph at the top of the file, right after the `most recent first` line:

```markdown
## 2026-07-13 — Include/exclude glob patterns on object filters

Backup policies' `object_filters` entries (and `brfs` itself) can now carry `include`/`exclude`
glob-pattern lists alongside `path`, letting a policy narrow what gets backed up instead of always
sweeping a path recursively. `brfs` gained `--include`/`--exclude` flags and applies them during
its own directory walk — excludes prune whole matched subtrees, includes act as a files-only
whitelist. `policy-server`, `policyclient`, and `agent` all carry the new fields through
end-to-end; see `docs/process/filesystem-backup.md` for the full flow.
```

- [ ] **Step 10: Update the demo fixtures**

Add a sample log file so the demo's new `exclude` example visibly does something:

Create `demo/sample-data/web/access.log`:

```
127.0.0.1 - - [11/Jul/2026:00:00:00 +0000] "GET / HTTP/1.1" 200 512
```

Change `demo/policy-server/policies/webserver-backup.json`'s `object_filters`:

```json
  "object_filters": [
    {"path": "/var/www/html"}
  ],
```

to:

```json
  "object_filters": [
    {"path": "/var/www/html", "exclude": ["*.log"]}
  ],
```

- [ ] **Step 11: Verify nothing broke**

Run: `make build && go vet ./src/...`
Expected: both succeed with no errors.

- [ ] **Step 12: Commit**

```bash
git add docs/process/filesystem-backup.md docs/protocols/policy-server.md \
        docs/components/policy-server.md docs/components/policyclient.md \
        docs/components/agent.md docs/components/brfs.md docs/ARCHITECTURE.md \
        README.md CHANGELOG.md demo/policy-server/policies/webserver-backup.json \
        demo/sample-data/web/access.log
git commit -m "docs: document include/exclude object filters end-to-end"
```
