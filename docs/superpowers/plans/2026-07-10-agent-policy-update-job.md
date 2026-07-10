# Agent Policy-Update Job Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a third standard `agent` job, `policy-update`, that fetches this node's applicable
backup policies from `policy-server` via a new `policyclient` binary and caches them locally —
fetching and caching only, nothing acts on the cache yet.

**Architecture:** `policyclient fetch` dials `policy-server` using the node's existing operating
credential (`client.crt`/`client.key`, the default `connection.Connect` identity), calls
`GetPolicies`, and atomically writes the result to `<var_dir>/policies-cache.json` via a new
shared `common/atomicfile` helper. On any failure it leaves the existing cache file completely
untouched. `agent`'s own pre-existing `agent-state.json` writer is refactored onto the same shared
helper, rather than leaving two independent copies of the same temp-file-then-rename logic. `agent`
gains one new entry in its compiled-in `policies()` list that execs `policyclient fetch` on a
configurable interval — no changes to `agent`'s generic reconcile/backoff machinery.

**Tech Stack:** Go, cobra (CLI), gRPC (generated `pb.PolicyServiceClient`, already exists in
`src/api/policyserver_grpc.pb.go`), testify (`assert`/`require`).

## Global Constraints

- Full design: `docs/superpowers/specs/2026-07-10-agent-policy-update-job-design.md`. Every task
  below implements a specific section of that spec — do not deviate from it. (The shared
  `common/atomicfile` extraction below is a controller-approved refinement made during planning,
  not in the original design doc; treat it as equally binding.)
- No proto changes. `GetPolicies`/`Policy`/`ObjectFilter` already exist in
  `src/api/policyserver.proto` and their generated Go code already exists in
  `src/api/policyserver.pb.go` / `src/api/policyserver_grpc.pb.go`. Do not regenerate or edit these.
- Nothing in this plan reads or consumes `policies-cache.json` — it is written and left inert.
  Do not add a scheduler, a policy interpreter, or any code that acts on cached policies.
- All new Go code lives under `src/` (the Go module root is `src/go.mod`, module path
  `github.com/alex-sviridov/miniprotector`). Run all `go build`/`go test`/`go vet` commands from
  inside `src/`.
- Atomic file writes (temp file in the target's directory, then rename over the target) go through
  the shared `common/atomicfile.Write(path string, data []byte) error` helper introduced in Task 2
  — both `policyclient`'s cache write and `agent`'s pre-existing `agent-state.json` cache write use
  it; do not write a third, separate copy of this logic anywhere in this plan.
  `policy-server/cache.go` is unaffected — it never writes files, only reads, so it's out of scope
  for this extraction.
- Follow existing repo conventions exactly otherwise: `--debug` + `common/logging.NewLogger` wiring
  (see `src/cmd/certclient/main.go`), a small `*Client` interface for the RPC client so tests can
  fake it (see `src/cmd/certclient/operatingrefresh.go`'s `issuerClient`), and a real gRPC+mTLS
  `//go:build integration` test using `common/testdata/certs` (see
  `src/cmd/bwfs/integration_test.go`) for the one integration-level check.
- Per `.claude/CLAUDE.md`'s feature-change rule: `docs/components/agent.md` and a new
  `docs/components/policyclient.md` must be updated/created before this is committed to `main`,
  `README.md` and `docs/ARCHITECTURE.md` must be updated since the topology and component list
  change, and a `CHANGELOG.md` entry is required before merging to `main`.

---

### Task 1: Add `PolicyFetchIntervalSec` config key

**Files:**
- Modify: `src/common/config/config.go`
- Test: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.PolicyFetchIntervalSec int` (default `900`), parsed from a
  `PolicyFetchIntervalSec` key in `local.conf`. Task 6 reads this field.

- [ ] **Step 1: Write the failing tests**

Append to the end of `src/common/config/config_test.go`:

```go
func TestParseConfig_PolicyFetchIntervalSecDefaultsTo900(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 900, conf.PolicyFetchIntervalSec)
}

func TestParseConfig_PolicyFetchIntervalSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nPolicyFetchIntervalSec=300\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 300, conf.PolicyFetchIntervalSec)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./common/config/... -run TestParseConfig_PolicyFetchIntervalSec -v`
Expected: FAIL — compile error, `conf.PolicyFetchIntervalSec undefined (type *Config has no field or method PolicyFetchIntervalSec)`

- [ ] **Step 3: Add the field, default, and parser case**

In `src/common/config/config.go`, add the field to the `Config` struct right after
`PolicyServerPort int` (currently the last field, line 108):

```go
	PolicyServerHost                 string
	PolicyServerPort                 int
	PolicyFetchIntervalSec           int
}
```

Add the default in `ParseConfig`'s initial `config := &Config{...}` literal, right after
`PolicyServerPort: 9300,`:

```go
		PolicyServerPort:                 9300,
		PolicyFetchIntervalSec:           900,
	}
```

Add the parser case right after the existing `case "policy_server_port":` block (around line
319-325), immediately before the `default:` case:

```go
		case "policy_server_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid policy_server_port value at line %d: %s", lineNum, value)
			}
			config.PolicyServerPort = port
			foundFields["policy_server_port"] = true
		case "PolicyFetchIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid PolicyFetchIntervalSec value at line %d: %s", lineNum, value)
			}
			config.PolicyFetchIntervalSec = number
			foundFields["PolicyFetchIntervalSec"] = true
		default:
			return nil, fmt.Errorf("unknown configuration key at line %d: %s", lineNum, key)
		}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests in the package, not just the two new ones)

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add PolicyFetchIntervalSec"
```

---

### Task 2: Shared atomic-file-write helper (`common/atomicfile`)

`src/cmd/agent/cache.go`'s `writeCache` already implements "write a temp file in the target's
directory, then rename over the target" for `agent-state.json`. Task 3 needs the identical pattern
for `policies-cache.json`. Rather than writing it a second time, this task extracts it once into
`common/atomicfile` and refactors `agent` to use it too — so there's exactly one copy of this
logic in the repo, not two.

**Files:**
- Create: `src/common/atomicfile/atomicfile.go`
- Test: `src/common/atomicfile/atomicfile_test.go`
- Modify: `src/cmd/agent/cache.go`

**Interfaces:**
- Produces: `func atomicfile.Write(path string, data []byte) error`. Creates `path`'s parent
  directory if missing (`os.MkdirAll(..., 0o755)`), writes `data` to `path+".tmp"` (`0o644`), then
  renames over `path`. Task 3's `policyclient` cache write consumes this directly.

- [ ] **Step 1: Write the failing tests**

Create `src/common/atomicfile/atomicfile_test.go`:

```go
package atomicfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrite_CreatesParentDirectoryIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.json")

	require.NoError(t, Write(path, []byte(`{"a":1}`)))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(data))
}

func TestWrite_OverwritesPreviousValueAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.json")

	require.NoError(t, Write(path, []byte("first")))
	require.NoError(t, Write(path, []byte("second")))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second", string(data))

	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err), "no leftover temp file after a successful write")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./common/atomicfile/... -v`
Expected: FAIL — `no Go files in .../common/atomicfile` (the package doesn't exist yet)

- [ ] **Step 3: Write the implementation**

Create `src/common/atomicfile/atomicfile.go`:

```go
// Package atomicfile provides one small helper for durably persisting a
// file: write to a temp file in the target's own directory, then rename
// over the target, so a crash mid-write never leaves a torn file in place.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write persists data to path atomically, creating path's parent directory
// first if it doesn't already exist.
func Write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename temp file into place for %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./common/atomicfile/... -v`
Expected: PASS (both tests)

- [ ] **Step 5: Refactor `agent`'s `writeCache` to delegate to `atomicfile.Write`**

In `src/cmd/agent/cache.go`, replace the `writeCache` function:

```go
// writeCache persists c atomically via common/atomicfile, so a crash
// mid-write never leaves a torn cache file.
func writeCache(path string, c Cache) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	if err := atomicfile.Write(path, data); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return nil
}
```

Update the import block at the top of `src/cmd/agent/cache.go`: remove `"path/filepath"` (no
longer used anywhere in this file once `writeCache` no longer calls `filepath.Dir` directly) and
add `"github.com/alex-sviridov/miniprotector/common/atomicfile"`. The file's imports should read:

```go
import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common/atomicfile"
)
```

- [ ] **Step 6: Run `agent`'s existing cache tests to confirm no regression**

Run: `cd src && go test ./cmd/agent/... -run TestWriteCache -v`
Expected: PASS — `TestWriteCache_CreatesParentDirectoryIfMissing` and
`TestWriteCache_OverwritesPreviousValueAndLeavesNoTempFile` (already existing in
`cmd/agent/cache_test.go`, unmodified by this task) both still pass, since `writeCache`'s observable
behavior is unchanged.

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS (the full `agent` package test suite)

- [ ] **Step 7: Commit**

```bash
git add src/common/atomicfile/atomicfile.go src/common/atomicfile/atomicfile_test.go src/cmd/agent/cache.go
git commit -m "refactor: extract common/atomicfile, adopt it in agent's cache writer"
```

---

### Task 3: `policyclient` fetch-and-cache logic

**Files:**
- Create: `src/cmd/policyclient/fetch.go`
- Test: `src/cmd/policyclient/fetch_test.go`

**Interfaces:**
- Consumes: `pb.PolicyServiceClient` (`src/api/policyserver_grpc.pb.go`, already generated);
  `pb.GetPoliciesRequest{}`, `pb.GetPoliciesResponse.GetPolicies() []*pb.Policy`; `pb.Policy`'s
  getters `GetName() string`, `GetCreatedAt() *timestamppb.Timestamp`,
  `GetUpdatedAt() *timestamppb.Timestamp`, `GetObjectFilters() []*pb.ObjectFilter`,
  `GetRpo() string`, `GetBackupWindow() []string`; `pb.ObjectFilter.GetPath() string`;
  `atomicfile.Write(path string, data []byte) error` (Task 2).
- Produces: `type CachedPolicy struct{...}` (JSON-tagged, this task's on-disk cache schema);
  `type policyServiceClient interface{ GetPolicies(...) }` (the fake-able subset of the real
  client); `func runFetch(ctx context.Context, client policyServiceClient, cachePath string, logger *slog.Logger) error`
  (the testable core, no network dialing); `func fetchAndCache(certsDir, host string, port, timeoutSec int, cachePath string, logger *slog.Logger) error`
  (the real, network-dialing entry point) — Task 4's integration test and Task 5's `main.go` both
  call `fetchAndCache` directly.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/policyclient/fetch_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func fetchTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakePolicyServiceClient struct {
	resp *pb.GetPoliciesResponse
	err  error
}

func (f *fakePolicyServiceClient) GetPolicies(_ context.Context, _ *pb.GetPoliciesRequest, _ ...grpc.CallOption) (*pb.GetPoliciesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestRunFetch_Success_WritesCacheFile(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "nested", "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Name:          "daily-db-backup",
				CreatedAt:     created,
				UpdatedAt:     updated,
				ObjectFilters: []*pb.ObjectFilter{{Path: "/var/lib/postgres"}, {Path: "/etc/postgres"}},
				Rpo:           "24h",
				BackupWindow:  []string{"0 2 * * *"},
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
	assert.Equal(t, []string{"/var/lib/postgres", "/etc/postgres"}, got[0].ObjectFilters)
	assert.Equal(t, "24h", got[0].RPO)
	assert.Equal(t, []string{"0 2 * * *"}, got[0].BackupWindow)
}

func TestRunFetch_EmptyPoliciesWritesEmptyArrayNotNull(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{}}

	require.NoError(t, runFetch(context.Background(), fake, cachePath, fetchTestLogger()))

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	assert.JSONEq(t, "[]", string(data))
}

func TestRunFetch_ErrorPropagates_ExistingCacheUntouched(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	require.NoError(t, os.WriteFile(cachePath, []byte("previous-good-cache"), 0o644))

	fake := &fakePolicyServiceClient{err: assert.AnError}
	err := runFetch(context.Background(), fake, cachePath, fetchTestLogger())
	assert.Error(t, err)

	data, readErr := os.ReadFile(cachePath)
	require.NoError(t, readErr)
	assert.Equal(t, "previous-good-cache", string(data))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policyclient/... -v`
Expected: FAIL — build fails, package `main` in `cmd/policyclient` has no `CachedPolicy` or
`runFetch` yet (this is the first file in this new directory).

- [ ] **Step 3: Write the implementation**

Create `src/cmd/policyclient/fetch.go`:

```go
// fetch.go implements policyclient fetch: pulling the current policy list
// from policy-server and atomically caching it locally via
// common/atomicfile. On any failure the existing cache file is left
// completely untouched -- policyclient never clears or partially
// overwrites a previously-good cache.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/atomicfile"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"google.golang.org/grpc"
)

// CachedPolicy is the on-disk representation of one policy-server Policy --
// the same fields the GetPolicies RPC response already defines, converted
// directly from the protobuf message. ObjectFilters flattens
// []*pb.ObjectFilter (a list of single-field {path} messages) to a plain
// []string: a lossless simplification since Path is ObjectFilter's only
// field.
type CachedPolicy struct {
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ObjectFilters []string  `json:"object_filters"`
	RPO           string    `json:"rpo"`
	BackupWindow  []string  `json:"backup_window"`
}

// policyServiceClient is the subset of pb.PolicyServiceClient runFetch
// needs -- satisfied directly by the real generated client, and by a fake
// in tests, mirroring certclient's issuerClient pattern.
type policyServiceClient interface {
	GetPolicies(ctx context.Context, in *pb.GetPoliciesRequest, opts ...grpc.CallOption) (*pb.GetPoliciesResponse, error)
}

// fetchAndCache is the real, network-dialing entry point main.go calls: it
// authenticates to policy-server with this node's operating credential
// (the default connection.Connect identity -- required, since policy-server
// matches policies against attribute labels embedded only in the operating
// certificate) and delegates to runFetch.
func fetchAndCache(certsDir, host string, port, timeoutSec int, cachePath string, logger *slog.Logger) error {
	conn, err := connection.Connect(host, port, timeoutSec, certsDir)
	if err != nil {
		return fmt.Errorf("connect to policy-server: %w", err)
	}
	defer conn.Close()

	client := pb.NewPolicyServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	return runFetch(ctx, client, cachePath, logger)
}

// runFetch is the testable core: given an already-connected
// policyServiceClient, fetch the current policy list and atomically write
// it to cachePath via common/atomicfile. On any failure, cachePath is left
// completely untouched.
func runFetch(ctx context.Context, client policyServiceClient, cachePath string, logger *slog.Logger) error {
	logger.Debug("fetching policies")
	resp, err := client.GetPolicies(ctx, &pb.GetPoliciesRequest{})
	if err != nil {
		return fmt.Errorf("get policies: %w", err)
	}

	cached := toCachedPolicies(resp.GetPolicies())
	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal policies: %w", err)
	}

	if err := atomicfile.Write(cachePath, data); err != nil {
		return fmt.Errorf("write policy cache: %w", err)
	}
	logger.Info("policy cache updated", "count", len(cached))
	return nil
}

// toCachedPolicies converts the RPC response's policies to their on-disk
// representation. Always returns a non-nil slice (even when policies is
// empty) so the cache file holds a JSON array, never null.
func toCachedPolicies(policies []*pb.Policy) []CachedPolicy {
	out := make([]CachedPolicy, 0, len(policies))
	for _, p := range policies {
		filters := make([]string, 0, len(p.GetObjectFilters()))
		for _, of := range p.GetObjectFilters() {
			filters = append(filters, of.GetPath())
		}
		out = append(out, CachedPolicy{
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
		})
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policyclient/... -v`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go
git commit -m "feat(policyclient): add fetch-and-cache logic"
```

---

### Task 4: Integration test — `policyclient` against a real gRPC+mTLS server

The fakes in Task 3 prove `runFetch`'s logic but never exercise `fetchAndCache`'s real network
dial (`connection.Connect`, real TLS handshake, real protobuf wire encoding). This task adds one
`//go:build integration` test that spins up a genuine `grpc.Server` with real mTLS credentials
(from `common/testdata/certs`, the same fixtures `cmd/bwfs/integration_test.go` already uses) and
drives `fetchAndCache` against it over a real TCP loopback connection — no Docker, no CA needed,
since `policy-server`'s own RPC surface needs neither. This is a minimal stub implementing
`pb.PolicyServiceServer` directly, not the real `policy-server` binary — `policy-server`'s own
matching logic already has its own package tests; this test's job is only to prove
`policyclient`'s wire path is real, not faked away.

Note: no Makefile target currently runs `-tags=integration` tests anywhere in this repo (the
existing `cmd/bwfs` integration tests are also only ever run manually) — this task follows that
same existing convention rather than introducing a new CI wiring concern.

**Files:**
- Create: `src/cmd/policyclient/integration_test.go`

**Interfaces:**
- Consumes: `fetchAndCache` (Task 3), `mtls.LoadServerCredentials(certsDir string) (credentials.TransportCredentials, error)` (`common/mtls`, already exists).

- [ ] **Step 1: Write the test**

Create `src/cmd/policyclient/integration_test.go`:

```go
//go:build integration

package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
)

const testCertsDir = "../../common/testdata/certs"

// stubPolicyServer is a minimal, literal pb.PolicyServiceServer -- not
// policy-server's own matching logic (already covered by
// cmd/policy-server's own package tests). It exists only to give this test
// a genuine gRPC+mTLS peer to dial, so fetchAndCache's real network path
// (connection.Connect, a real TLS handshake, real protobuf encoding) is
// exercised at least once, not just its fake-backed unit tests.
type stubPolicyServer struct {
	pb.UnimplementedPolicyServiceServer
	resp *pb.GetPoliciesResponse
}

func (s *stubPolicyServer) GetPolicies(context.Context, *pb.GetPoliciesRequest) (*pb.GetPoliciesResponse, error) {
	return s.resp, nil
}

func TestFetchAndCache_Integration_RealServerRealMTLS(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer lis.Close()

	serverCreds, err := mtls.LoadServerCredentials(testCertsDir)
	require.NoError(t, err)

	grpcSrv := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterPolicyServiceServer(grpcSrv, &stubPolicyServer{
		resp: &pb.GetPoliciesResponse{
			Policies: []*pb.Policy{{Name: "real-wire-policy", Rpo: "12h"}},
		},
	})
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	_, portStr, err := net.SplitHostPort(lis.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	cachePath := t.TempDir() + "/policies-cache.json"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	err = fetchAndCache(testCertsDir, "127.0.0.1", port, 5, cachePath, logger)
	require.NoError(t, err)

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	require.Contains(t, string(data), "real-wire-policy")
}
```

- [ ] **Step 2: Run the test**

Run: `cd src && go test -tags=integration ./cmd/policyclient/... -run TestFetchAndCache_Integration -v`
Expected: PASS. (Without `-tags=integration`, `go test ./cmd/policyclient/...` must still pass and
must not even compile this file — the build tag excludes it by default, matching
`cmd/bwfs/integration_test.go`'s existing behavior.)

- [ ] **Step 3: Commit**

```bash
git add src/cmd/policyclient/integration_test.go
git commit -m "test(policyclient): prove fetchAndCache against a real gRPC+mTLS server"
```

---

### Task 5: `policyclient` CLI wiring (`arguments.go`, `main.go`) and Makefile target

**Files:**
- Create: `src/cmd/policyclient/arguments.go`
- Create: `src/cmd/policyclient/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `fetchAndCache(certsDir, host string, port, timeoutSec int, cachePath string, logger *slog.Logger) error` (Task 3).
- Produces: the `policyclient` binary, `policyclient fetch` subcommand, `make policyclient` build
  target. Task 7's Dockerfiles reference this binary and this build target by name.

- [ ] **Step 1: Write `arguments.go`**

Create `src/cmd/policyclient/arguments.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "fetch"
	Debug  bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "policyclient <command>",
		Short: "Fetch backup policies from policy-server into a local cache",
	}
	rootCmd.PersistentFlags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	fetchCmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch current policies from policy-server and update the local cache",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "fetch" },
	}

	rootCmd.AddCommand(fetchCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: fetch")
	}
	return args, nil
}
```

- [ ] **Step 2: Write `main.go`**

Create `src/cmd/policyclient/main.go`:

```go
// policyclient fetches this node's applicable backup policies from
// policy-server and caches them locally as policies-cache.json. It does not
// act on the cached policies -- scheduling or running backups from the
// cache is separate, later work.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

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

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Var directory resolution failed: %v\n", err)
		os.Exit(1)
	}
	cachePath := filepath.Join(varDir, "policies-cache.json")

	ctx := context.WithValue(context.Background(), "appName", "policyclient")
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)
	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	switch args.Action {
	case "fetch":
		if conf.PolicyServerHost == "" {
			fmt.Fprintln(os.Stderr, "Configuration error: policy_server_host not set in local.conf")
			os.Exit(1)
		}
		if err := fetchAndCache(certsDir, conf.PolicyServerHost, conf.PolicyServerPort, conf.ConnectionTimeOutSec, cachePath, logger); err != nil {
			logger.Error("fetch failed", "error", err)
			fmt.Fprintf(os.Stderr, "Fetch failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Policy cache updated at", cachePath)
	}
}
```

- [ ] **Step 3: Build and smoke-test**

Run: `cd src && go build ./cmd/policyclient/...`
Expected: builds with no errors.

Run: `cd src && go run ./cmd/policyclient --help`
Expected: cobra help output listing `fetch` as an available command.

Run: `cd src && go run ./cmd/policyclient`
Expected: exits non-zero, prints `Arguments error: a subcommand is required: fetch` (config
resolution isn't even reached — argument parsing fails first, matching `certclient`'s and
`agent`'s existing behavior when invoked with no subcommand).

- [ ] **Step 4: Add the Makefile build target**

In `Makefile`, add a `POLICYCLIENT_CMD` variable right after `POLICY_SERVER_CMD` (line 27):

```makefile
POLICY_SERVER_CMD := cmd/policy-server
POLICYCLIENT_CMD := cmd/policyclient
```

Add `policyclient` to the `.PHONY` line (line 39):

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certclient catalogsync catalog agent clientmanager issuer policy-server policyclient test test-e2e lint control-plane-up demo-up demo-down
```

Add a build target right after the `policy-server` target (after line 131):

```makefile
policyclient: $(BINARY_DIR) ## Build policyclient binary
	@printf "$(BLUE)Building policyclient...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/policyclient ./$(POLICYCLIENT_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/policyclient"
```

- [ ] **Step 5: Verify the Makefile target builds**

Run: `make policyclient`
Expected: `Built successfully:bin/policyclient`, and `bin/policyclient` exists.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policyclient/arguments.go src/cmd/policyclient/main.go Makefile
git commit -m "feat(policyclient): add CLI, main wiring, and build target"
```

---

### Task 6: Wire `agent`'s third policy, `policy-update`

**Files:**
- Modify: `src/cmd/agent/policy.go`
- Test: Create `src/cmd/agent/policy_test.go`

**Interfaces:**
- Consumes: `config.Config.PolicyFetchIntervalSec` (Task 1).
- Produces: `policies(conf)` now includes a third `Policy{ID: "policy-update", ...}` entry. No
  change to `Policy`, `PolicyState`, `Cache`, `isDue`, `backoff`, or `run` — this task only adds a
  list entry and its test.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/agent/policy_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicies_IncludesPolicyUpdateWithConfiguredInterval(t *testing.T) {
	conf := &config.Config{PolicyFetchIntervalSec: 1234}
	pols := policies(conf)

	var found *Policy
	for i := range pols {
		if pols[i].ID == "policy-update" {
			found = &pols[i]
		}
	}
	require.NotNil(t, found, "policies() must include a policy-update entry")
	assert.Equal(t, "policyclient", found.Binary)
	assert.Equal(t, []string{"fetch"}, found.Args)
	assert.Equal(t, 1234*time.Second, found.Interval)
}

func TestPolicies_StillIncludesExistingCertPolicies(t *testing.T) {
	conf := &config.Config{BootstrapCertRefreshIntervalSec: 86400, OperatingCertFetchIntervalSec: 900}
	pols := policies(conf)

	ids := make([]string, len(pols))
	for i, p := range pols {
		ids[i] = p.ID
	}
	assert.Contains(t, ids, "bootstrap-refresh")
	assert.Contains(t, ids, "operating-refresh")
	assert.Len(t, pols, 3)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestPolicies_ -v`
Expected: FAIL — `TestPolicies_IncludesPolicyUpdateWithConfiguredInterval` fails with
`policies() must include a policy-update entry` (found is nil); `TestPolicies_StillIncludesExistingCertPolicies` fails on `assert.Len(t, pols, 3)` (currently 2).

- [ ] **Step 3: Add the third policy entry**

In `src/cmd/agent/policy.go`, replace the `policies` function:

```go
// policies returns agent's three embedded policies, their intervals read
// from conf rather than compiled in -- bootstrap-refresh (long-lived
// credential, infrequent), operating-refresh (short-lived credential,
// frequent), and policy-update (fetches this node's applicable backup
// policies from policy-server into a local cache; nothing yet acts on that
// cache -- see docs/superpowers/specs/2026-07-10-agent-policy-update-job-design.md).
func policies(conf *config.Config) []Policy {
	return []Policy{
		{ID: "bootstrap-refresh", Binary: "certclient", Args: []string{"renew"},
			Interval: time.Duration(conf.BootstrapCertRefreshIntervalSec) * time.Second},
		{ID: "operating-refresh", Binary: "certclient", Args: []string{"operating-refresh"},
			Interval: time.Duration(conf.OperatingCertFetchIntervalSec) * time.Second},
		{ID: "policy-update", Binary: "policyclient", Args: []string{"fetch"},
			Interval: time.Duration(conf.PolicyFetchIntervalSec) * time.Second},
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS (the full `agent` package test suite, not just the new tests — this confirms
`list_test.go`/`reconcile_test.go` aren't affected)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/policy.go src/cmd/agent/policy_test.go
git commit -m "feat(agent): add policy-update as a third standard policy"
```

---

### Task 7: Bundle `policyclient` into deployment images that run `agent`

`agent`'s `policy-update` policy execs `policyclient` as a sibling binary in the same directory
(see `cmd/agent/reconcile.go`'s `realExec`, unchanged by this plan). Every Docker image that
bundles `agent` must also bundle `policyclient`, or the new policy fails every cycle with an
"executable file not found" error on every node using that image, forever (harmless per `agent`'s
existing backoff, but pointless noise in every deployment). Three images currently bundle `agent`:
`demo/backup-host/Dockerfile`, `deploy/control-plane/catalog/Dockerfile`,
`deploy/control-plane/policy-server/Dockerfile`.

**Files:**
- Modify: `demo/backup-host/Dockerfile`
- Modify: `deploy/control-plane/catalog/Dockerfile`
- Modify: `deploy/control-plane/policy-server/Dockerfile`

**Interfaces:**
- Consumes: `make policyclient` (Task 5), the `bin/policyclient` binary it produces.

- [ ] **Step 1: Update `demo/backup-host/Dockerfile`**

Change:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs catalogsync certclient agent
```
to:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs catalogsync certclient agent policyclient
```

Change:
```dockerfile
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs /build/bin/catalogsync /build/bin/certclient /build/bin/agent ./
```
to:
```dockerfile
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs /build/bin/catalogsync /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
```

- [ ] **Step 2: Update `deploy/control-plane/catalog/Dockerfile`**

Change:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make catalog certclient agent
```
to:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make catalog certclient agent policyclient
```

Change:
```dockerfile
COPY --from=builder /build/bin/catalog /build/bin/certclient /build/bin/agent ./
```
to:
```dockerfile
COPY --from=builder /build/bin/catalog /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
```

- [ ] **Step 3: Update `deploy/control-plane/policy-server/Dockerfile`**

Change:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make policy-server certclient agent
```
to:
```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make policy-server certclient agent policyclient
```

Change:
```dockerfile
COPY --from=builder /build/bin/policy-server /build/bin/certclient /build/bin/agent ./
```
to:
```dockerfile
COPY --from=builder /build/bin/policy-server /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
```

- [ ] **Step 4: Verify each image still builds**

Run: `docker build -f demo/backup-host/Dockerfile -t mp-backup-host-test .`
Run: `docker build -f deploy/control-plane/catalog/Dockerfile -t mp-catalog-test .`
Run: `docker build -f deploy/control-plane/policy-server/Dockerfile -t mp-policy-server-test .`
Expected: all three builds complete successfully (this also transitively exercises `make
policyclient` inside each container, on top of Task 5's own local build verification).

- [ ] **Step 5: Commit**

```bash
git add demo/backup-host/Dockerfile deploy/control-plane/catalog/Dockerfile deploy/control-plane/policy-server/Dockerfile
git commit -m "feat(deploy): bundle policyclient into images that run agent"
```

---

### Task 8: Documentation

Per `.claude/CLAUDE.md`'s feature-change rule.

**Files:**
- Create: `docs/components/policyclient.md`
- Modify: `docs/components/agent.md`
- Modify: `docs/components/policy-server.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`

- [ ] **Step 1: Create `docs/components/policyclient.md`**

```markdown
# policyclient

Fetches this node's applicable backup policies from `policy-server` and caches them locally as
`policies-cache.json`. Does not act on the cached policies — no scheduling, no interpretation of
`rpo`/`backup_window`; that remains separate, later work. See
[Design: Agent Policy-Update Job](../superpowers/specs/2026-07-10-agent-policy-update-job-design.md).
**Agent tool** — bundled onto every node also running `agent`.

## Usage

```bash
policyclient fetch
policyclient --debug fetch
```

| Flag | Subcommand | Default | Description |
|------|------------|---------|-------------|
| `--debug` | root (applies to all subcommands) | `false` | Enable debug logging |

Requires `policy_server_host` set in `local.conf` (`policy_server_port` defaults to `9300`).

## Behavior

`fetch` dials `policy-server` authenticated with this node's existing **operating credential**
(`client.crt`/`client.key`, the same default identity `bwfs`/`brfs`/`rwfs`/`catalogsync`/`catalog`
already use) — required, not a choice: `policy-server` matches policies against the attribute
labels embedded specifically in the operating certificate, so the bootstrap credential wouldn't
authenticate as anything meaningful to it. Calls `GetPolicies` and writes the returned policy list
to `<var_dir>/policies-cache.json`, atomically (via `common/atomicfile`: temp file + rename).

On any failure (unreachable `policy-server`, RPC error, marshal error), the existing cache file is
left completely untouched — no special-casing between failure kinds; `agent`'s existing backoff
handles all of them identically. Always fetches when invoked; there's no staleness check — run it
on a schedule (`agent`'s `policy-update` policy, or a bare cron/systemd timer) if periodic
refreshing is wanted.

The cache file is a plain JSON array, one object per policy, mirroring the RPC response's fields
directly:

```json
[
  {
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": ["/var/lib/postgres", "/etc/postgres"],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"]
  }
]
```

## Building

```bash
make policyclient
```

## See Also

- [policy-server](./policy-server.md) — what `fetch` dials
- [Policy Server Protocol](../protocols/policy-server.md) — `GetPolicies` RPC details
- [agent](./agent.md) — runs `policy-update` as a scheduled policy
- [certclient](./certclient.md) — the sibling binary `agent` execs for credential refresh
- [Design: Agent Policy-Update Job](../superpowers/specs/2026-07-10-agent-policy-update-job-design.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 2: Update `docs/components/agent.md`**

Replace the intro paragraph's last two sentences (currently: "`policy-server` ... now exists as a
standalone component serving backup policies, but `agent` does not yet fetch from it — no
policy-driven scheduling is wired into `agent`'s reconcile loop. That integration remains separate,
later work.") with:

```markdown
It runs three embedded policies — `bootstrap-refresh`, `operating-refresh`, and `policy-update` —
the first two keep this node's two-tier mTLS credential (see [Security Model](../SECURITY.md))
fresh via `certclient`; the third fetches this node's applicable backup policies from
`policy-server` (see [policy-server](./policy-server.md)) into a local cache via `policyclient`.
Nothing yet acts on that cache — no policy-driven scheduling is wired into `agent`'s reconcile
loop. That integration remains separate, later work.
```

Add a row to the policy table:

```markdown
| `policy-update` | `policyclient fetch` | `PolicyFetchIntervalSec` | The local backup-policy cache (`policies-cache.json`) via `policy-server` |
```

Add a row to the Configuration Keys table:

```markdown
| `PolicyFetchIntervalSec` | 900 (15 minutes) | How often the `policy-update` policy runs `policyclient fetch` |
```

Add to the See Also list:

```markdown
- [policyclient](./policyclient.md) — the binary `agent`'s `policy-update` policy execs
- [policy-server](./policy-server.md) — what `policyclient fetch` ultimately talks to
```

- [ ] **Step 3: Update `docs/components/policy-server.md`**'s See Also

Add one line:

```markdown
- [policyclient](./policyclient.md) — fetches `GetPolicies` on `agent`'s `policy-update` schedule
```

- [ ] **Step 4: Update `README.md`**

Add a new component bullet right after the existing `agent` line:

```markdown
- **[policyclient](docs/components/policyclient.md)** - Fetches backup policies from `policy-server` into a local cache (nothing acts on the cache yet)
```

Update the existing `agent` line's parenthetical from "(bootstrap credential renewal and
operating-certificate refresh, both via `certclient`)" to:

```markdown
- **[agent](docs/components/agent.md)** - Node agent — reconciles local state against embedded policies (credential renewal via `certclient`, policy fetch via `policyclient`)
```

- [ ] **Step 5: Update `docs/ARCHITECTURE.md`**

In the Components table (line 13), change the `agent` row's Status cell from "Implemented (two
policies: bootstrap credential renewal and operating-certificate refresh via `issuer`)" to:

```markdown
| agent | Node Agent — reconciles local state against embedded policies | Implemented (three policies: bootstrap credential renewal, operating-certificate refresh via `issuer`, and policy fetch via `policyclient`) |
```

In the same table (line 16), change the `policy-server` row's Status cell from "Implemented (no
client-side consumer yet — agent integration is separate, later work)" to:

```markdown
| policy-server | Serves backup policies filtered by a requesting client's hostname and attribute labels; no database, reads labels from the peer cert | Implemented (`agent` now fetches and caches its policies via `policyclient`; nothing yet acts on the cache — that remains separate, later work) |
```

In the Control Plane vs. Agents table's "Runs where" row (line 26), append to the Agents cell:

```markdown
and `policy_server_host:9300` outbound for policy fetching
```

so it reads "...outbound for operating-certificate refresh, and `policy_server_host:9300`
outbound for policy fetching; otherwise mesh with each other...".

In the same table's "Network role" row (line 27), change "`policy-server` serves `GetPolicies` on
`:9300` (mTLS, no client-side consumer wired yet)" to "`policy-server` serves `GetPolicies` on
`:9300` (mTLS, fetched by `agent` via `policyclient`)".

Update the `policy-server` prose paragraph (lines 43-49): replace "It listens on its own port
(`policy_server_port`, default 9300); nothing dials it yet, since no client-side consumer of
`GetPolicies` exists in this codebase — wiring one (`agent` or `brfs` fetching and acting on
policies) is separate, later work, the same way `issuer`'s own phase 2b deliberately left `agent`
integration for a follow-up phase." with:

```markdown
It listens on its own port (`policy_server_port`, default 9300); `agent` now dials it on a
schedule (`policy-update`, via `policyclient fetch`) and caches the result locally, though nothing
yet acts on that cache — turning it into anything that actually runs a backup (`agent` or `brfs`
consuming it) is separate, later work.
```

Update the `agent` prose paragraph (lines 59-64): replace "`agent serve` runs a reconcile loop
with two config-driven policies, `bootstrap-refresh` (`certclient renew`, daily) and
`operating-refresh` (`certclient operating-refresh`, every 15 minutes by default), tracking each
policy's outcome in a local cache (`agent list-policies` inspects it). It has no network role of
its own; all network behavior is `certclient`'s, unchanged." with:

```markdown
`agent serve` runs a reconcile loop with three config-driven policies: `bootstrap-refresh`
(`certclient renew`, daily) and `operating-refresh` (`certclient operating-refresh`, every 15
minutes by default) keep this node's mTLS credentials fresh; `policy-update` (`policyclient
fetch`, every 15 minutes by default) fetches this node's applicable backup policies from
`policy-server` into a local cache. Each policy's outcome is tracked in the same local cache
(`agent list-policies` inspects it). It has no network role of its own; all network behavior is
`certclient`'s and `policyclient`'s, unchanged.
```

- [ ] **Step 6: Commit**

```bash
git add docs/components/policyclient.md docs/components/agent.md docs/components/policy-server.md README.md docs/ARCHITECTURE.md
git commit -m "docs: document policyclient and agent's policy-update job"
```

---

### Task 9: Changelog entry

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add the entry**

Add a new section at the very top of `CHANGELOG.md`, right after the `# Changelog` header and its
introductory line, before the existing most-recent entry:

```markdown
## 2026-07-10 — Agent policy-update job

`agent` gains a third standard job, `policy-update`, alongside its existing credential-refresh
policies: a new `policyclient fetch` binary pulls this node's applicable backup policies from
`policy-server` and atomically caches them as `policies-cache.json`, authenticated with the node's
existing operating credential. A new shared `common/atomicfile` helper backs the atomic write
(temp file + rename), replacing what had been a copy of the same logic private to `agent`. A
failed fetch leaves the previous cache untouched, the same fail-safe direction used everywhere
else in this codebase. Deliberately stops at fetching and caching — nothing yet reads the cache to
schedule or run a backup; that remains separate, later work.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog entry for agent policy-update job"
```

---

## Final Verification

- [ ] Run the full test suite: `cd src && go test ./...` — expect all packages PASS.
- [ ] Run the integration-tagged test explicitly: `cd src && go test -tags=integration ./cmd/policyclient/... -v` — expect PASS.
- [ ] Run `cd src && go vet ./...` (same as `make lint`) — expect no issues.
- [ ] Run `make build` from the repo root — expect every binary, including the new
      `bin/policyclient`, to build successfully.
