# Agent Storage-Policy Supervision (Activating bwfs) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `agent` supervise a `bwfs server` process (ensure-running, not scheduled) for every storage policy targeting this node, removing the now-redundant `StoragePolicy.Hostname` field in favor of `client_filters` for targeting.

**Architecture:** `policy-server` drops `StoragePolicy.Hostname` (targeting moves to `client_filters`, the mechanism backup policies already use). `policyclient` carries a storage policy's `port`/`config` into `policies-cache.json` (previously dropped). `agent` gains a new `storage.go`: `storageTasks` derives one ensure-running task per cached storage policy (parsing `config` for a `filesystem` backend's root path), and a `storageSupervisor`/`storageManager` pair (modeled on the existing `vectorSupervisor`) starts, crash-restarts, and prunes `bwfs server` processes — sharing `agent-state.json` and `agent list-policies` with everything else agent already tracks. `bwfs` gets a small fix (missing `signal.NotifyContext` wiring) so agent's `SIGTERM` actually drains it gracefully instead of killing in-flight streams.

**Tech Stack:** Go 1.26, gRPC/protobuf (`protoc` + `protoc-gen-go`/`protoc-gen-go-grpc`, already installed at `~/.local/bin`), `testify` (`assert`/`require`), Vue/Pinia + Vitest (`web/`, for Task 3 only).

## Global Constraints

- Every source file in this repo lives under `src/`; the Go module root is `src/go.mod` (`module github.com/alex-sviridov/miniprotector`, `go 1.26.0`). Run all `go build`/`go test` commands from `src/` (or with `-C src`).
- This repo's `.claude/CLAUDE.md` requires, before any commit that changes a `.proto` file or feature behavior: updating the matching `docs/protocols/*.md` and `docs/components/*.md`, cross-linking from `README.md` if the component list/quick-start is affected, and adding a `CHANGELOG.md` entry before merging to `main`. Each task below folds in the specific doc updates it triggers; Task 11 does the remaining repo-wide ones (`ARCHITECTURE.md`, `CHANGELOG.md`).
- Never use `git commit --amend`, `--no-verify`, or force-push. Create a new commit per task.
- **Revision note:** an earlier draft of this plan assumed `api-server` and the web `Storage` section were unimplemented (per `docs/superpowers/specs/2026-07-28-storage-policy-web-ui-design.md`, which describes them as a separate, not-yet-built body of work). That assumption turned out to be false — a complete, working implementation existed on an unmerged branch (`storage-policy-web-ui`) and has since been merged into `main`. It implements the *original* design: a raw `Hostname` field throughout `api-server`'s `policyDTO`/`storagePolicyInput` and the web `StorageEditModal`/`StorageView`. Two new tasks are inserted after Task 1 to bring that real code in line with the `client_filters`-based targeting decision Task 1 makes in `policy-server` — without them, Task 1's proto/Go changes would break `api-server`'s build: Task 2 (`api-server`) and Task 3 (`web`). Every task from the old Task 2 onward is renumbered +2 (old Task 2 → Task 4, ..., old Task 9 → Task 11).
- Design reference: `docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md`. One deliberate implementation simplification versus that doc's literal wording: instead of refactoring `run()` in `reconcile.go` to *accept* a pre-built `*reconcileState` from `main.go`, `run()` keeps building its own `reconcileState` internally exactly as today, and the new storage-supervision call happens *inside* `run()`'s existing loop (see Task 8) — so `main.go`'s call to `run()` still only passes a `cachePath` string, not a `reconcileState` object. This achieves the design's actual goal (one shared, mutex-safe cache, no two-writer race) with a much smaller diff: no existing test call site needs its first few arguments changed, only two new trailing arguments appended.

---

## Task 1: `policy-server` — remove `StoragePolicy.Hostname`

**Files:**
- Modify: `src/cmd/policy-server/storage_policy.go`
- Modify: `src/cmd/policy-server/write.go`
- Modify: `src/api/policyserver.proto`
- Regenerate: `src/api/policyserver.pb.go` (via `make proto`, not hand-edited)
- Modify: `src/cmd/policy-server/storage_policy_test.go`
- Modify: `src/cmd/policy-server/write_test.go`
- Modify: `src/cmd/policy-server/server_test.go`
- Modify: `src/cmd/policy-server/cache_test.go`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/protocols/policy-server.md`

**Interfaces:**
- Produces: `StoragePolicy{PolicyBase, Port int, Config json.RawMessage}` (no `Hostname` field). `pb.Policy`/`pb.CreatePolicyRequest`/`pb.UpdatePolicyRequest` no longer have a `Hostname` field or `GetHostname()` method. `policyFieldsGetter` interface (`write.go`) no longer requires `GetHostname()`.
- Consumed by: Task 4 (`policyclient`'s `toCachedPolicies` already never read `Hostname`, so no change needed there beyond what Task 4 adds).

This is one atomic change — the Go struct, the proto, and every test fixture referencing `Hostname` must all move together or the package won't build. Do it in this order so you never have an uncompilable intermediate commit.

- [ ] **Step 1: Edit the proto — retire the `hostname` fields, don't renumber**

In `src/api/policyserver.proto`, in the `Policy` message, replace:

```proto
  // storage policy only -- unset/empty for a backup policy.
  string hostname = 11;
  // storage policy only.
  int32 port = 12;
  // storage policy only -- opaque JSON text, verbatim passthrough. Never
  // parsed or interpreted by policy-server beyond checking well-formedness.
  string config = 13;
```

with:

```proto
  reserved 11; // formerly "hostname" -- removed, targeting a storage policy
               // at a node is client_filters only now, same as a backup
               // policy; see
               // docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md
  // storage policy only.
  int32 port = 12;
  // storage policy only -- opaque JSON text, verbatim passthrough. Never
  // parsed or interpreted by policy-server beyond checking well-formedness.
  string config = 13;
```

In `CreatePolicyRequest`, replace:

```proto
  string type = 7;
  string hostname = 8;
  int32 port = 9;
  string config = 10;
```

with:

```proto
  string type = 7;
  reserved 8; // formerly "hostname" -- removed, see Policy.reserved 11 above
  int32 port = 9;
  string config = 10;
```

In `UpdatePolicyRequest`, replace:

```proto
  string hostname = 8;
  int32 port = 9;
  string config = 10;
```

with:

```proto
  reserved 8; // formerly "hostname" -- removed, see Policy.reserved 11 above
  int32 port = 9;
  string config = 10;
```

Field numbers are retired, not reused (standard proto3 practice) — `port`/`config` keep their existing numbers unchanged.

- [ ] **Step 2: Regenerate the Go proto code**

Run: `make proto` from the repo root (or, if that fails for an unrelated reason, run the equivalent command directly: `cd src && protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/*.proto`).

Expected: `src/api/policyserver.pb.go` is rewritten; `git diff src/api/policyserver.pb.go` shows `Hostname`/`GetHostname()` removed from `Policy`, `CreatePolicyRequest`, `UpdatePolicyRequest`, and nothing else changes (no other `.proto` file in `src/api/` was touched).

- [ ] **Step 3: Update `storage_policy.go`**

Replace the file's top doc comment:

```go
// StoragePolicy is the "storage" policy type: where a future storage server
// should run (hostname, port) and how it should be configured (config).
// policy-server never interprets config beyond checking it's well-formed
// JSON -- it's opaque pass-through data for whatever future component reads
// it.
```

with:

```go
// StoragePolicy is the "storage" policy type: how a future storage server
// should be configured (port, config). There is no Hostname field --
// targeting which node runs it is PolicyBase's ClientFilters, the same
// mechanism a BackupPolicy already uses, not a field specific to this type.
// policy-server never interprets config beyond checking it's well-formed
// JSON -- it's opaque pass-through data for whatever future component reads
// it. See docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md.
```

Replace the struct:

```go
type StoragePolicy struct {
	PolicyBase
	Hostname string          `json:"hostname"`
	Port     int             `json:"port"`
	Config   json.RawMessage `json:"config"`
}
```

with:

```go
type StoragePolicy struct {
	PolicyBase
	Port   int             `json:"port"`
	Config json.RawMessage `json:"config"`
}
```

Replace `Validate()`:

```go
// Validate checks the fields an operator can set on a storage policy,
// independent of where it came from (a file on disk or a Create/UpdatePolicy
// RPC request): the fields validateCommon checks, plus hostname must be
// non-empty, port must be a valid TCP port (1-65535), and config must be
// non-empty, well-formed JSON -- its contents are never interpreted
// further.
func (p *StoragePolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if p.Hostname == "" {
		return fmt.Errorf("hostname is required")
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", p.Port)
	}
	if len(p.Config) == 0 {
		return fmt.Errorf("config is required")
	}
	if !json.Valid(p.Config) {
		return fmt.Errorf("config must be well-formed JSON")
	}
	return nil
}
```

with:

```go
// Validate checks the fields an operator can set on a storage policy,
// independent of where it came from (a file on disk or a Create/UpdatePolicy
// RPC request): the fields validateCommon checks (including client_filters,
// which is how a storage policy targets a node), plus port must be a valid
// TCP port (1-65535), and config must be non-empty, well-formed JSON -- its
// contents are never interpreted further.
func (p *StoragePolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", p.Port)
	}
	if len(p.Config) == 0 {
		return fmt.Errorf("config is required")
	}
	if !json.Valid(p.Config) {
		return fmt.Errorf("config must be well-formed JSON")
	}
	return nil
}
```

Replace `Clone()`:

```go
func (p *StoragePolicy) Clone() Policy {
	config := make(json.RawMessage, len(p.Config))
	copy(config, p.Config)
	return &StoragePolicy{
		PolicyBase: p.PolicyBase.clone(),
		Hostname:   p.Hostname,
		Port:       p.Port,
		Config:     config,
	}
}
```

with:

```go
func (p *StoragePolicy) Clone() Policy {
	config := make(json.RawMessage, len(p.Config))
	copy(config, p.Config)
	return &StoragePolicy{
		PolicyBase: p.PolicyBase.clone(),
		Port:       p.Port,
		Config:     config,
	}
}
```

Replace `ToProto()`:

```go
func (p *StoragePolicy) ToProto(includeClientFilters bool) *pb.Policy {
	pp := &pb.Policy{
		Id:        p.Metadata.ID,
		Name:      p.Metadata.Name,
		CreatedAt: timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt: timestamppb.New(p.Metadata.UpdatedAt),
		Type:      p.Type,
		Hostname:  p.Hostname,
		Port:      int32(p.Port),
		Config:    string(p.Config),
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
```

with:

```go
func (p *StoragePolicy) ToProto(includeClientFilters bool) *pb.Policy {
	pp := &pb.Policy{
		Id:        p.Metadata.ID,
		Name:      p.Metadata.Name,
		CreatedAt: timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt: timestamppb.New(p.Metadata.UpdatedAt),
		Type:      p.Type,
		Port:      int32(p.Port),
		Config:    string(p.Config),
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
```

- [ ] **Step 4: Update `write.go`**

Replace:

```go
// storageFieldsSet reports whether any storage-only field is non-default --
// used to reject a request mixing storage fields into a backup policy.
func storageFieldsSet(hostname string, port int32, config string) bool {
	return hostname != "" || port != 0 || config != ""
}
```

with:

```go
// storageFieldsSet reports whether any storage-only field is non-default --
// used to reject a request mixing storage fields into a backup policy.
func storageFieldsSet(port int32, config string) bool {
	return port != 0 || config != ""
}
```

Replace the `policyFieldsGetter` interface:

```go
type policyFieldsGetter interface {
	GetObjectFilters() []*pb.ObjectFilter
	GetRpo() string
	GetBackupWindow() []string
	GetDestination() string
	GetHostname() string
	GetPort() int32
	GetConfig() string
}
```

with:

```go
type policyFieldsGetter interface {
	GetObjectFilters() []*pb.ObjectFilter
	GetRpo() string
	GetBackupWindow() []string
	GetDestination() string
	GetPort() int32
	GetConfig() string
}
```

Replace `buildPolicy`:

```go
func buildPolicy(kind string, base PolicyBase, req policyFieldsGetter) (Policy, error) {
	switch kind {
	case "backup":
		if storageFieldsSet(req.GetHostname(), req.GetPort(), req.GetConfig()) {
			return nil, fmt.Errorf("a backup policy must not set hostname/port/config")
		}
		return &BackupPolicy{
			PolicyBase:    base,
			ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
			RPO:           req.GetRpo(),
			BackupWindow:  req.GetBackupWindow(),
			Destination:   req.GetDestination(),
		}, nil
	case "storage":
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetDestination()) {
			return nil, fmt.Errorf("a storage policy must not set object_filters/rpo/backup_window/destination")
		}
		return &StoragePolicy{
			PolicyBase: base,
			Hostname:   req.GetHostname(),
			Port:       int(req.GetPort()),
			Config:     json.RawMessage(req.GetConfig()),
		}, nil
	default:
		return nil, fmt.Errorf("unknown policy type %q", kind)
	}
}
```

with:

```go
func buildPolicy(kind string, base PolicyBase, req policyFieldsGetter) (Policy, error) {
	switch kind {
	case "backup":
		if storageFieldsSet(req.GetPort(), req.GetConfig()) {
			return nil, fmt.Errorf("a backup policy must not set port/config")
		}
		return &BackupPolicy{
			PolicyBase:    base,
			ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
			RPO:           req.GetRpo(),
			BackupWindow:  req.GetBackupWindow(),
			Destination:   req.GetDestination(),
		}, nil
	case "storage":
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetDestination()) {
			return nil, fmt.Errorf("a storage policy must not set object_filters/rpo/backup_window/destination")
		}
		return &StoragePolicy{
			PolicyBase: base,
			Port:       int(req.GetPort()),
			Config:     json.RawMessage(req.GetConfig()),
		}, nil
	default:
		return nil, fmt.Errorf("unknown policy type %q", kind)
	}
}
```

- [ ] **Step 5: Update the test fixtures**

In `src/cmd/policy-server/storage_policy_test.go`:
- `TestParsePolicyFile_StoragePolicyParsesAllFields`: remove `"hostname": "storage-east-1.internal",` from the JSON fixture and remove the line `assert.Equal(t, "storage-east-1.internal", p.Hostname)`.
- `TestParsePolicyFile_SameBasenameInDifferentTypeSubfoldersYieldsDifferentIDs`: change `{"metadata": {"name": "nightly"}, "hostname": "h", "port": 1, "config": {}}` to `{"metadata": {"name": "nightly"}, "port": 1, "config": {}}`.
- `TestStoragePolicy_ValidateValidPolicyReturnsNil`: remove `Hostname: "storage-1.internal",`.
- Delete `TestStoragePolicy_ValidateMissingHostnameFails` entirely (there is no longer a hostname to be missing).
- `TestStoragePolicy_ValidatePortZeroFails`, `TestStoragePolicy_ValidatePortAbove65535Fails`, `TestStoragePolicy_ValidateEmptyConfigFails`, `TestStoragePolicy_ValidateMalformedConfigJSONFails`, `TestStoragePolicy_CloneDeepCopiesConfig`: remove the `Hostname: "h",` line from each struct literal.

In `src/cmd/policy-server/write_test.go`:
- `TestCreatePolicy_StoragePolicyWritesIntoStorageDir`: remove `Hostname: "storage-east-1.internal",` from the request and `assert.Equal(t, "storage-east-1.internal", resp.Hostname)`.
- `TestCreatePolicy_StorageTypeWithBackupFieldsRejected`: remove `Hostname: "h",`.
- `TestCreatePolicy_BackupTypeWithStorageFieldsRejected`: this test's whole point was "a backup-typed request must not set a storage-only field" using `Hostname: "h"` as that field. Since `Hostname` no longer exists, change it to use `Port: 9400` instead: the request becomes `&pb.CreatePolicyRequest{Name: "bad", Type: "backup", Port: 9400}`.
- `TestUpdatePolicy_StoragePolicyRoundTripsAndTypeStaysImmutable`: remove `"hostname": "old-host",` from the fixture JSON and `Hostname: "new-host",` from the request, and `assert.Equal(t, "new-host", resp.Hostname)`.
- `TestUpdatePolicy_StorageTypeWithBackupFieldsRejected`: remove `"hostname": "h", ` from the fixture JSON and `Hostname:    "h",` from the request.

In `src/cmd/policy-server/server_test.go`:
- `TestGetPolicies_StoragePolicyStillOmitsClientFilters`: remove `"hostname": "storage-east-1.internal",` from the fixture JSON and `assert.Equal(t, "storage-east-1.internal", p.Hostname)`.

In `src/cmd/policy-server/cache_test.go`:
- `TestCache_ReloadLoadsBackupAndStoragePoliciesTogether`: change `{"metadata": {"name": "policy-b"}, "hostname": "h", "port": 9400, "config": {}}` to `{"metadata": {"name": "policy-b"}, "port": 9400, "config": {}}`.

- [ ] **Step 6: Run policy-server's tests**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS, no references to `Hostname` remain anywhere under `src/cmd/policy-server/`. Confirm with `grep -rn "Hostname" src/cmd/policy-server/` — the only hits should be unrelated `ClientFilters.Hostnames` (plural).

- [ ] **Step 7: Update `docs/components/policy-server.md`**

Replace:

```
A `"backup"` policy describes what to back up and where: `object_filters`, `rpo`, `backup_window`,
`destination`. A `"storage"` policy describes where a future storage server should run and how it
should be configured: `hostname`, `port`, and an opaque `config` JSON blob `policy-server` validates
is well-formed but never interprets — nothing in `policy-server` yet runs anything based on it.
```

with:

```
A `"backup"` policy describes what to back up and where: `object_filters`, `rpo`, `backup_window`,
`destination`. A `"storage"` policy describes how a future storage server should be configured:
`port` and an opaque `config` JSON blob `policy-server` validates is well-formed but never
interprets. Targeting which node runs it is `client_filters` — the same mechanism a backup policy
already uses — not a field specific to this type; see
[Design: agent storage-policy supervision](../superpowers/specs/2026-07-28-agent-storage-supervision-design.md),
which is the first actual consumer of `storage`-typed policies.
```

Replace:

```
`"backup"` policy additionally has `object_filters` (a list of `{"path": "...", "include": [...],
"exclude": [...]}` entries — `include`/`exclude` are optional glob-pattern lists, validated as
syntactically-valid patterns at load time but otherwise opaque to `policy-server`; see
[Filesystem Backup Flow](../process/filesystem-backup.md) for how `brfs` applies them), `rpo` (a
duration string, e.g. `"24h"`), `backup_window` (a list of cron expressions, e.g.
`["0 2 * * *", "0 20 * * *"]`), and `destination` (a `host:port` string, the target `bwfs` for this
policy's backups). A `"storage"` policy instead has `hostname`, `port`, and `config` (an opaque JSON
object, validated as well-formed at load time but never interpreted).
```

with:

```
`"backup"` policy additionally has `object_filters` (a list of `{"path": "...", "include": [...],
"exclude": [...]}` entries — `include`/`exclude` are optional glob-pattern lists, validated as
syntactically-valid patterns at load time but otherwise opaque to `policy-server`; see
[Filesystem Backup Flow](../process/filesystem-backup.md) for how `brfs` applies them), `rpo` (a
duration string, e.g. `"24h"`), `backup_window` (a list of cron expressions, e.g.
`["0 2 * * *", "0 20 * * *"]`), and `destination` (a `host:port` string, the target `bwfs` for this
policy's backups). A `"storage"` policy instead has `port` and `config` (an opaque JSON object,
validated as well-formed at load time but never interpreted).
```

- [ ] **Step 8: Update `docs/protocols/policy-server.md`**

Replace the `message Policy { ... }` block's `hostname`/`port`/`config` lines:

```proto
  string hostname = 11;
  int32 port = 12;
  string config = 13;
```

with:

```proto
  reserved 11; // formerly hostname -- removed, see below
  int32 port = 12;
  string config = 13;
```

Replace the `message CreatePolicyRequest { ... }` block's:

```proto
  string hostname = 8;
  int32 port = 9;
  string config = 10;
```

with:

```proto
  reserved 8; // formerly hostname -- removed, see below
  int32 port = 9;
  string config = 10;
```

Replace the `message UpdatePolicyRequest { ... }` block's:

```proto
  string hostname = 8;
  int32 port = 9;
  string config = 10;
```

with:

```proto
  reserved 8; // formerly hostname -- removed, see below
  int32 port = 9;
  string config = 10;
```

Replace:

```
- `hostname`/`port`/`config` are only meaningful on a `"storage"`-typed policy -- unset/zero on a
  `"backup"`-typed one, and vice versa for `object_filters`/`rpo`/`backup_window`/`destination`.
  `config` is opaque, pass-through JSON text -- `policy-server` validates it's well-formed at load
  and write time but never interprets its contents.
```

with:

```
- `port`/`config` are only meaningful on a `"storage"`-typed policy -- unset/zero on a
  `"backup"`-typed one, and vice versa for `object_filters`/`rpo`/`backup_window`/`destination`.
  `config` is opaque, pass-through JSON text -- `policy-server` validates it's well-formed at load
  and write time but never interprets its contents. There is no separate `hostname` field on a
  storage policy (removed -- see
  [Design: agent storage-policy supervision](../../docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md));
  targeting a node is `client_filters` only, identical to a backup policy.
```

- [ ] **Step 9: Commit**

```bash
cd src && go build ./... && go test ./cmd/policy-server/...
git add src/api/policyserver.proto src/api/policyserver.pb.go \
  src/cmd/policy-server/storage_policy.go src/cmd/policy-server/write.go \
  src/cmd/policy-server/storage_policy_test.go src/cmd/policy-server/write_test.go \
  src/cmd/policy-server/server_test.go src/cmd/policy-server/cache_test.go \
  docs/components/policy-server.md docs/protocols/policy-server.md
git commit -m "$(cat <<'EOF'
fix(policy-server): remove StoragePolicy.Hostname, target via client_filters

Targeting which node runs a storage policy is now client_filters --
the same mechanism a backup policy already uses -- instead of a
separate Hostname field. Proto field numbers are retired via
`reserved`, not reused.
EOF
)"
```

---

## Task 2: `api-server` — remove `Hostname`, keep `client_filters` passthrough

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/policies_test.go`
- Modify: `docs/components/api-server.md`
- Modify: `docs/api/rest-v1.md`

**Interfaces:**
- Consumes: `pb.Policy`/`pb.CreatePolicyRequest`/`pb.UpdatePolicyRequest` with no `Hostname` field (Task 1).
- Produces: `policyDTO{..., ClientFilters clientFiltersDTO, Port int32, Config string, ...}` (no `Hostname` field), `storagePolicyInput{Name string, ClientFilters clientFiltersDTO, Port int32, Config string}` (no `Hostname` field). Consumed by Task 3 (the web layer decodes/builds this exact JSON shape).

This code was merged into `main` from a previously-unmerged branch that implemented the *original* design (a raw `Hostname` field). It compiles against today's `main` (pre-Task-1) but will not compile once Task 1's proto/Go changes land — do this task after confirming Task 1 is committed, in the same branch.

- [ ] **Step 1: Write the failing test**

Replace `TestToPolicyDTO_IncludesStorageFields` in `src/cmd/api-server/policies_test.go`:

```go
func TestToPolicyDTO_IncludesStorageFields(t *testing.T) {
	p := &pb.Policy{
		Id:     "s1",
		Name:   "east-1-storage",
		Type:   "storage",
		Port:   9400,
		Config: `{"backend": "filesystem", "root": "/data/storage"}`,
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, int32(9400), dto.Port)
	assert.Equal(t, `{"backend": "filesystem", "root": "/data/storage"}`, dto.Config)
}
```

Replace `TestHandleCreateStoragePolicy_ReturnsCreatedPolicy`:

```go
func TestHandleCreateStoragePolicy_ReturnsCreatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "s1", Name: "east-1-storage", Type: "storage",
		Port: 9400, Config: `{"backend": "filesystem"}`,
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"storage-east-1.internal"}, Labels: map[string]string{}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "east-1-storage",
		"client_filters": {"hostnames": ["storage-east-1.internal"], "labels": {}},
		"port": 9400,
		"config": "{\"backend\": \"filesystem\"}"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "storage", fake.lastCreateReq.GetType())
	assert.Equal(t, "east-1-storage", fake.lastCreateReq.GetName())
	assert.Equal(t, []string{"storage-east-1.internal"}, fake.lastCreateReq.GetClientFilters().GetHostnames())
	assert.Equal(t, int32(9400), fake.lastCreateReq.GetPort())
	assert.Equal(t, `{"backend": "filesystem"}`, fake.lastCreateReq.GetConfig())

	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, []any{"storage-east-1.internal"}, respBody["client_filters"].(map[string]any)["hostnames"])
}
```

Replace `TestHandleCreateStoragePolicy_BackendValidationErrorReturns400`'s fake error text (it no longer needs to say "hostname"; the test's actual point — a backend `InvalidArgument` surfaces as `400` — is unaffected by which message it carries):

```go
func TestHandleCreateStoragePolicy_BackendValidationErrorReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{createErr: status.Error(codes.InvalidArgument, "port must be between 1 and 65535, got 0")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-policies", strings.NewReader(`{"name": "x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

Replace `TestHandleUpdateStoragePolicy_ReturnsUpdatedPolicy`:

```go
func TestHandleUpdateStoragePolicy_ReturnsUpdatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{
		Id: "s1", Name: "east-1-storage-renamed", Type: "storage",
		Port: 9401, Config: `{"backend": "filesystem"}`,
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"storage-east-2.internal"}, Labels: map[string]string{}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "east-1-storage-renamed",
		"client_filters": {"hostnames": ["storage-east-2.internal"], "labels": {}},
		"port": 9401,
		"config": "{\"backend\": \"filesystem\"}"
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage-policies/s1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	assert.Equal(t, "s1", fake.lastUpdateReq.GetId())
	assert.Equal(t, []string{"storage-east-2.internal"}, fake.lastUpdateReq.GetClientFilters().GetHostnames())
	assert.Equal(t, int32(9401), fake.lastUpdateReq.GetPort())
}
```

Replace `TestHandleUpdateStoragePolicy_UnknownIDReturns404`'s request body (drop the raw `"hostname"` key, since `storagePolicyInput` no longer has one — an unknown extra JSON key would be silently ignored by `encoding/json` anyway, but the body should reflect the real shape):

```go
func TestHandleUpdateStoragePolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{updateErr: status.Error(codes.NotFound, "policy \"ghost\" not found")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage-policies/ghost", strings.NewReader(`{"name": "x", "port": 1, "config": "{}"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
```

- [ ] **Step 2: Run tests to confirm they fail**

Run: `cd src && go build ./cmd/api-server/... 2>&1 | head -20`
Expected: FAIL to build — `pb.Policy`/`pb.CreatePolicyRequest`/`pb.UpdatePolicyRequest` still have `Hostname` in this step (Task 1 already removed it from the proto in the same branch), so `policies.go` itself is what's now broken (references `GetHostname()`/`Hostname` that no longer exist) — this confirms the test file changes above are consistent with the *target* shape, and that `policies.go` genuinely needs the edit in Step 3.

- [ ] **Step 3: Update `policies.go`**

Replace the `policyDTO` struct:

```go
type policyDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
	ClientFilters clientFiltersDTO  `json:"client_filters"`
	ObjectFilters []objectFilterDTO `json:"object_filters"`
	RPO           string            `json:"rpo"`
	BackupWindow  []string          `json:"backup_window"`
	Destination   string            `json:"destination"`
	Type          string            `json:"type"`
	Hostname      string            `json:"hostname"`
	Port          int32             `json:"port"`
	Config        string            `json:"config"`
}
```

with:

```go
type policyDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
	ClientFilters clientFiltersDTO  `json:"client_filters"`
	ObjectFilters []objectFilterDTO `json:"object_filters"`
	RPO           string            `json:"rpo"`
	BackupWindow  []string          `json:"backup_window"`
	Destination   string            `json:"destination"`
	Type          string            `json:"type"`
	Port          int32             `json:"port"`
	Config        string            `json:"config"`
}
```

Replace `toPolicyDTO`'s composite literal:

```go
	return policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters: objectFilters,
		RPO:           p.GetRpo(),
		BackupWindow:  p.GetBackupWindow(),
		Destination:   p.GetDestination(),
		Type:          p.GetType(),
		Hostname:      p.GetHostname(),
		Port:          p.GetPort(),
		Config:        p.GetConfig(),
	}
```

with:

```go
	return policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters: objectFilters,
		RPO:           p.GetRpo(),
		BackupWindow:  p.GetBackupWindow(),
		Destination:   p.GetDestination(),
		Type:          p.GetType(),
		Port:          p.GetPort(),
		Config:        p.GetConfig(),
	}
```

Replace the `storagePolicyInput` struct:

```go
type storagePolicyInput struct {
	Name          string           `json:"name"`
	ClientFilters clientFiltersDTO `json:"client_filters"`
	Hostname      string           `json:"hostname"`
	Port          int32            `json:"port"`
	Config        string           `json:"config"`
}
```

with:

```go
type storagePolicyInput struct {
	Name          string           `json:"name"`
	ClientFilters clientFiltersDTO `json:"client_filters"`
	Port          int32            `json:"port"`
	Config        string           `json:"config"`
}
```

Replace `handleCreateStoragePolicy`'s request-building call:

```go
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		Type:          "storage",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Hostname:      in.Hostname,
		Port:          in.Port,
		Config:        in.Config,
	})
```

with:

```go
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		Type:          "storage",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Port:          in.Port,
		Config:        in.Config,
	})
```

Replace `handleUpdateStoragePolicy`'s request-building call:

```go
	resp, err := s.policy.UpdatePolicy(r.Context(), &pb.UpdatePolicyRequest{
		Id:            id,
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Hostname:      in.Hostname,
		Port:          in.Port,
		Config:        in.Config,
	})
```

with:

```go
	resp, err := s.policy.UpdatePolicy(r.Context(), &pb.UpdatePolicyRequest{
		Id:            id,
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		Port:          in.Port,
		Config:        in.Config,
	})
```

- [ ] **Step 4: Run tests to confirm they pass**

Run: `cd src && go build ./cmd/api-server/... && go test ./cmd/api-server/... -v`
Expected: PASS, every test in the package (this also re-verifies every backup-policy test, which this task doesn't touch, still passes unchanged).

- [ ] **Step 5: Update `docs/components/api-server.md`**

Replace:

```
`policy-server` also supports a `"storage"` policy type (`hostname`/`port`/`config`).
`GET /policies` accepts an optional `?type=backup|storage` query parameter to filter by type;
without it, every policy of every type is returned, each with `hostname`/`port`/`config` populated
in the response DTO when applicable (empty/zero for a `"backup"`-typed policy, and vice versa for
`rpo`/`destination`/`object_filters`). Creating or updating a storage policy uses a separate pair of
endpoints, `POST /storage-policies` and `PUT /storage-policies/{id}`, since a storage policy's input
shape (`hostname`/`port`/`config`) shares nothing with a backup policy's
(`object_filters`/`rpo`/`backup_window`/`destination`) beyond `name`/`client_filters`. `GET
/policies/{id}` and `DELETE /policies/{id}` are shared across both types — both operations are
already type-agnostic, looking a policy up or removing it by `id` alone.
```

with:

```
`policy-server` also supports a `"storage"` policy type (`port`/`config`).
`GET /policies` accepts an optional `?type=backup|storage` query parameter to filter by type;
without it, every policy of every type is returned, each with `port`/`config` populated
in the response DTO when applicable (zero for a `"backup"`-typed policy, and vice versa for
`rpo`/`destination`/`object_filters`). Creating or updating a storage policy uses a separate pair of
endpoints, `POST /storage-policies` and `PUT /storage-policies/{id}`, since a storage policy's input
shape (`port`/`config`) shares nothing with a backup policy's
(`object_filters`/`rpo`/`backup_window`/`destination`) beyond `name`/`client_filters` — which is also
how a storage policy targets a node (there is no separate `hostname` field; set
`client_filters.hostnames` the same way a backup policy would). `GET
/policies/{id}` and `DELETE /policies/{id}` are shared across both types — both operations are
already type-agnostic, looking a policy up or removing it by `id` alone.
```

- [ ] **Step 6: Update `docs/api/rest-v1.md`**

Replace the `GET /api/v1/policies` example response's storage-policy fields:

```
      "type": "backup",
      "hostname": "",
      "port": 0,
      "config": ""
```

with:

```
      "type": "backup",
      "port": 0,
      "config": ""
```

Replace the `POST /api/v1/storage-policies` section:

```
Creates a new `"storage"`-typed policy. Body:

```json
{
  "name": "east-1-storage",
  "client_filters": {"hostnames": [], "labels": {}},
  "hostname": "storage-east-1.internal",
  "port": 9400,
  "config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
}
```

`config` is a JSON string, not a nested object — `policy-server` treats it as opaque, pass-through
text; the web UI is the one that gives it the `backend`/`root` shape shown above. `201` with the
created policy on success. `400` if `name` is empty, `hostname` is empty, `port` isn't in `[1,
65535]`, or `config` isn't well-formed JSON — no file is written when validation fails.
```

with:

```
Creates a new `"storage"`-typed policy. Body:

```json
{
  "name": "east-1-storage",
  "client_filters": {"hostnames": ["storage-east-1.internal"], "labels": {}},
  "port": 9400,
  "config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
}
```

`config` is a JSON string, not a nested object — `policy-server` treats it as opaque, pass-through
text; the web UI is the one that gives it the `backend`/`root` shape shown above. There is no
`hostname` field — targeting a node is `client_filters.hostnames`, identical to a backup policy.
`201` with the created policy on success. `400` if `name` is empty, `port` isn't in `[1,
65535]`, or `config` isn't well-formed JSON — no file is written when validation fails.
```

- [ ] **Step 7: Commit**

```bash
cd src && go build ./cmd/api-server/... && go test ./cmd/api-server/...
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go \
  docs/components/api-server.md docs/api/rest-v1.md
git commit -m "$(cat <<'EOF'
fix(api-server): remove Hostname from storage-policy DTO/input

Follows policy-server's removal of StoragePolicy.Hostname (previous
commit) -- a storage policy's client_filters is now the only
targeting mechanism, identical to a backup policy. This code landed
via a merge of a previously-unmerged branch that implemented the
original (Hostname-based) design; this brings it in line with the
client_filters-only decision.
EOF
)"
```

---

## Task 3: `web` — `StorageEditModal`/`StorageView`: "Target hostname" via `client_filters`

**Files:**
- Modify: `web/src/components/storage/StorageEditModal.vue`
- Modify: `web/src/components/storage/StorageEditModal.spec.js`
- Modify: `web/src/views/StorageView.vue`
- Modify: `web/src/views/StorageView.spec.js`
- Modify: `docs/components/web.md`

**Interfaces:**
- Consumes: `policyDTO`/`storagePolicyInput` with no `Hostname` field, `client_filters: {hostnames, labels}` (Task 2).
- Produces: a `StorageEditModal` that submits `{name, port, config, client_filters: {hostnames: [target], labels: {}}}`; a `StorageView` whose table shows a "Target Hostname" column derived from `client_filters.hostnames[0]`. Nothing downstream in this plan consumes these — `web/src/stores/storagePolicies.js` and its spec are untouched (the store forwards whatever payload it's given without reading a `.hostname` field, so nothing there needs to change).

- [ ] **Step 1: Write the failing tests**

Replace `StorageEditModal.spec.js`'s `'renders empty fields in create mode'`:

```javascript
  it('renders empty fields in create mode', () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    expect(wrapper.find('[data-test="storage-name-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-target-hostname-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-port-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-path-input"]').element.value).toBe('')
  })
```

Replace `'pre-fills fields from the policy prop in edit mode'`:

```javascript
  it('pre-fills fields from the policy prop in edit mode', () => {
    const wrapper = mount(StorageEditModal, {
      props: {
        policy: {
          id: 's1',
          name: 'east-1-storage',
          client_filters: { hostnames: ['storage-east-1.internal'], labels: {} },
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage"}',
        },
      },
    })
    expect(wrapper.find('[data-test="storage-name-input"]').element.value).toBe('east-1-storage')
    expect(wrapper.find('[data-test="storage-target-hostname-input"]').element.value).toBe('storage-east-1.internal')
    expect(wrapper.find('[data-test="storage-port-input"]').element.value).toBe('9400')
    expect(wrapper.find('[data-test="storage-path-input"]').element.value).toBe('/data/storage')
  })
```

Replace `'emits save with the built payload on valid submit'`:

```javascript
  it('emits save with the built payload on valid submit', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-name-input"]').setValue('east-1-storage')
    await wrapper.find('[data-test="storage-target-hostname-input"]').setValue('storage-east-1.internal')
    await wrapper.find('[data-test="storage-port-input"]').setValue('9400')
    await wrapper.find('[data-test="storage-path-input"]').setValue('/data/storage')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({
      name: 'east-1-storage',
      port: 9400,
      config: JSON.stringify({ backend: 'filesystem', root: '/data/storage' }),
      client_filters: { hostnames: ['storage-east-1.internal'], labels: {} },
    })
  })
```

Replace the `hostname`/`config` fixtures in `'preserves unknown config keys when editing and saving'` and `'does not throw and falls back to defaults when config is the literal null'` (both currently set `hostname: 'storage-east-1.internal'` on the `policy` prop — that key is simply dropped, since the modal no longer reads `policy.hostname` at all; these two tests don't assert on the hostname field, so no other change is needed beyond removing that now-meaningless key):

```javascript
  it('preserves unknown config keys when editing and saving', async () => {
    const wrapper = mount(StorageEditModal, {
      props: {
        policy: {
          id: 's1',
          name: 'east-1-storage',
          client_filters: { hostnames: ['storage-east-1.internal'], labels: {} },
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage", "compression": "zstd"}',
        },
      },
    })
```

(only the `props.policy` object changes — the rest of that test body is unchanged)

```javascript
  it('does not throw and falls back to defaults when config is the literal null', () => {
    const wrapper = mount(StorageEditModal, {
      props: {
        policy: {
          id: 's1',
          name: 'east-1-storage',
          client_filters: { hostnames: ['storage-east-1.internal'], labels: {} },
          port: 9400,
          config: 'null',
        },
      },
    })
```

(same — only `props.policy` changes, assertions below are unchanged)

Replace `'rejects a port outside 1-65535'`:

```javascript
  it('rejects a port outside 1-65535', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-name-input"]').setValue('x')
    await wrapper.find('[data-test="storage-target-hostname-input"]').setValue('h')
    await wrapper.find('[data-test="storage-port-input"]').setValue('70000')
    await wrapper.find('[data-test="storage-path-input"]').setValue('/data')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.text()).toContain('port')
  })
```

Add one new test for the required-target-hostname validation (mirrors the existing blank-fields test, which already covers "some field is empty" generically — this one pins down that target hostname specifically is checked):

```javascript
  it('requires a target hostname before emitting save', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-name-input"]').setValue('x')
    await wrapper.find('[data-test="storage-port-input"]').setValue('9400')
    await wrapper.find('[data-test="storage-path-input"]').setValue('/data')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.text()).toContain('required')
  })
```

Replace `StorageView.spec.js`'s `'renders each storage policy in the table'`:

```javascript
  it('renders each storage policy in the table', () => {
    const { wrapper } = mountView({
      list: [
        {
          id: 's1',
          name: 'east-1-storage',
          client_filters: { hostnames: ['storage-east-1.internal'], labels: {} },
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage"}',
        },
      ],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('east-1-storage')
    expect(wrapper.text()).toContain('storage-east-1.internal')
    expect(wrapper.text()).toContain('9400')
    expect(wrapper.text()).toContain('filesystem')
  })
```

Every other `list: [{ id: 's1', name: 'east-1-storage', hostname: 'h', port: 9400, config: '{}' }]`
fixture in `StorageView.spec.js` (there are four: `'opens the modal in edit mode when a row is
clicked'`, `'calls update and closes the modal on save in edit mode'`, `'deletes a storage policy
after confirming'`, `'does not delete when the confirm dialog is dismissed'`) drops the `hostname:
'h'` key — it's never asserted on directly in those tests (they only check `id`/behavior), so simply
remove that key from each fixture object, e.g.:

```javascript
      list: [{ id: 's1', name: 'east-1-storage', port: 9400, config: '{}' }],
```

`'opens the modal in edit mode when a row is clicked'` additionally asserts the exact `policy` prop
object passed to the modal — update that expectation the same way:

```javascript
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).props('policy')).toEqual({
      id: 's1',
      name: 'east-1-storage',
      port: 9400,
      config: '{}',
    })
```

The three `save`-payload fixtures (`'calls create and closes the modal on save in create mode'`,
`'calls update and closes the modal on save in edit mode'`, `'keeps the modal open and shows the
server error when create fails'`) already use `client_filters: { hostnames: [], labels: {} }`
alongside a stray `hostname: 'h'` key — drop the stray key, e.g.:

```javascript
    const payload = { name: 'new-storage', port: 1, config: '{}', client_filters: { hostnames: [], labels: {} } }
```

(same edit — remove `hostname: 'h', ` — applies to all three of those payload literals)

- [ ] **Step 2: Run tests to confirm they fail**

Run: `cd web && npx vitest run src/components/storage/StorageEditModal.spec.js src/views/StorageView.spec.js`
Expected: FAIL — `[data-test="storage-target-hostname-input"]` doesn't exist yet, and the emitted/expected payload shapes don't match the current component's output.

- [ ] **Step 3: Update `StorageEditModal.vue`**

Replace the `form` reactive object:

```javascript
const form = reactive({
  name: props.policy?.name || '',
  hostname: props.policy?.hostname || '',
  port: props.policy ? String(props.policy.port) : '',
  storageType: parseConfig(props.policy?.config).backend || 'filesystem',
  path: parseConfig(props.policy?.config).root || '',
})
```

with:

```javascript
const form = reactive({
  name: props.policy?.name || '',
  targetHostname: props.policy?.client_filters?.hostnames?.[0] || '',
  port: props.policy ? String(props.policy.port) : '',
  storageType: parseConfig(props.policy?.config).backend || 'filesystem',
  path: parseConfig(props.policy?.config).root || '',
})
```

Replace the `submit` function:

```javascript
function submit() {
  errors.message = ''
  const port = Number(form.port)

  if (!form.name.trim()) {
    errors.message = 'Name is required.'
    return
  }
  if (!form.hostname.trim()) {
    errors.message = 'Hostname is required.'
    return
  }
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    errors.message = 'A valid port between 1 and 65535 is required.'
    return
  }
  if (!form.path.trim()) {
    errors.message = 'Filesystem path is required.'
    return
  }

  emit('save', {
    name: form.name.trim(),
    hostname: form.hostname.trim(),
    port,
    config: JSON.stringify({
      ...parseConfig(props.policy?.config),
      backend: form.storageType,
      root: form.path.trim(),
    }),
    client_filters: { hostnames: [], labels: {} },
  })
}
```

with:

```javascript
function submit() {
  errors.message = ''
  const port = Number(form.port)

  if (!form.name.trim()) {
    errors.message = 'Name is required.'
    return
  }
  if (!form.targetHostname.trim()) {
    errors.message = 'Target hostname is required.'
    return
  }
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    errors.message = 'A valid port between 1 and 65535 is required.'
    return
  }
  if (!form.path.trim()) {
    errors.message = 'Filesystem path is required.'
    return
  }

  emit('save', {
    name: form.name.trim(),
    port,
    config: JSON.stringify({
      ...parseConfig(props.policy?.config),
      backend: form.storageType,
      root: form.path.trim(),
    }),
    client_filters: { hostnames: [form.targetHostname.trim()], labels: {} },
  })
}
```

Replace the "Hostname" field in the template:

```html
        <div>
          <label class="block font-medium mb-1">Hostname</label>
          <input data-test="storage-hostname-input" v-model="form.hostname" class="w-full border rounded px-2 py-1" />
        </div>
```

with:

```html
        <div>
          <label class="block font-medium mb-1">Target Hostname</label>
          <input data-test="storage-target-hostname-input" v-model="form.targetHostname" class="w-full border rounded px-2 py-1" />
        </div>
```

- [ ] **Step 4: Update `StorageView.vue`**

Replace the `storageBackend` helper and add a matching `targetHostname` helper right after it:

```javascript
function storageBackend(configText) {
  try {
    return JSON.parse(configText).backend || '—'
  } catch {
    return '—'
  }
}
```

with:

```javascript
function storageBackend(configText) {
  try {
    return JSON.parse(configText).backend || '—'
  } catch {
    return '—'
  }
}

function targetHostname(clientFilters) {
  return clientFilters?.hostnames?.[0] || '—'
}
```

Replace the `columns` array:

```javascript
const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'Hostname', field: 'hostname', sortable: true },
  { label: 'Port', field: 'port', sortable: true },
  { label: 'Storage Type', field: 'storageType', sortable: false },
  { label: '', field: 'actions', sortable: false },
]
```

with:

```javascript
const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'Target Hostname', field: 'targetHostname', sortable: false },
  { label: 'Port', field: 'port', sortable: true },
  { label: 'Storage Type', field: 'storageType', sortable: false },
  { label: '', field: 'actions', sortable: false },
]
```

Replace the row-rendering template's column dispatch:

```html
          <span v-else-if="column.field === 'storageType'">{{ storageBackend(row.config) }}</span>
```

with:

```html
          <span v-else-if="column.field === 'storageType'">{{ storageBackend(row.config) }}</span>
          <span v-else-if="column.field === 'targetHostname'">{{ targetHostname(row.client_filters) }}</span>
```

- [ ] **Step 5: Run tests to confirm they pass**

Run: `cd web && npx vitest run src/components/storage/StorageEditModal.spec.js src/views/StorageView.spec.js`
Expected: PASS, all tests.

- [ ] **Step 6: Run the full web test suite**

Run: `cd web && npx vitest run`
Expected: PASS across every spec file (confirms nothing else in the frontend referenced the removed `hostname` field/column).

- [ ] **Step 7: Update `docs/components/web.md`**

Replace:

```
- `/storage` — every storage policy (name, hostname, port, storage type), with a "New Storage
  Policy" action and a click-to-edit name column, both opening the same `StorageEditModal` (fields:
  name, hostname, port, storage type — `filesystem` only today — and, when `filesystem` is selected,
  a filesystem path). Kept fully separate from `/policies`: its own store
  (`stores/storagePolicies.js`), its own component folder (`components/storage/`), and no detail or
  form routes of its own — list and modal only. `/policies` itself now requests only `type=backup`
  policies, so a storage policy never appears there.
```

with:

```
- `/storage` — every storage policy (name, target hostname, port, storage type), with a "New Storage
  Policy" action and a click-to-edit name column, both opening the same `StorageEditModal` (fields:
  name, target hostname, port, storage type — `filesystem` only today — and, when `filesystem` is
  selected, a filesystem path). "Target hostname" submits as `client_filters.hostnames` — the same
  targeting mechanism `/policies` uses, not a separate field. Kept fully separate from `/policies`:
  its own store (`stores/storagePolicies.js`), its own component folder (`components/storage/`), and
  no detail or form routes of its own — list and modal only. `/policies` itself now requests only
  `type=backup` policies, so a storage policy never appears there.
```

- [ ] **Step 8: Commit**

```bash
cd web && npx vitest run
git add web/src/components/storage/StorageEditModal.vue web/src/components/storage/StorageEditModal.spec.js \
  web/src/views/StorageView.vue web/src/views/StorageView.spec.js docs/components/web.md
git commit -m "$(cat <<'EOF'
fix(web): Storage section targets a node via client_filters, not Hostname

StorageEditModal's Hostname field becomes "Target hostname",
submitting client_filters.hostnames instead of a raw hostname field
-- following api-server's and policy-server's removal of
StoragePolicy.Hostname (previous two commits). StorageView's table
column reads the same client_filters.hostnames[0] for display.
EOF
)"
```

---

## Task 4: `policyclient` — carry `port`/`config` through to `CachedPolicy`

**Files:**
- Modify: `src/cmd/policyclient/fetch.go`
- Modify: `src/cmd/policyclient/fetch_test.go`
- Modify: `docs/components/policyclient.md`

**Interfaces:**
- Produces: `CachedPolicy{..., Port int32 \`json:"port,omitempty"\`, Config string \`json:"config,omitempty"\`}`.
- Consumed by: Task 5 (`agent`'s own `cachedPolicy` mirror gains the identical two fields, read from the same `policies-cache.json`).

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/policyclient/fetch_test.go`:

```go
func TestRunFetch_StoragePolicyCarriesPortAndConfig(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:        "storage-uuid-1",
				Name:      "east-1-storage",
				CreatedAt: created,
				UpdatedAt: updated,
				Type:      "storage",
				Port:      9400,
				Config:    `{"backend": "filesystem", "root": "/data/storage"}`,
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
	assert.Equal(t, "storage", got[0].Type)
	assert.EqualValues(t, 9400, got[0].Port)
	assert.JSONEq(t, `{"backend": "filesystem", "root": "/data/storage"}`, got[0].Config)
}
```

- [ ] **Step 2: Run it to confirm it fails**

Run: `cd src && go test ./cmd/policyclient/... -run TestRunFetch_StoragePolicyCarriesPortAndConfig -v`
Expected: FAIL — `CachedPolicy` has no field `Port`/`Config` yet (compile error), or the assertions fail with zero values if you stub the fields without wiring `toCachedPolicies` yet.

- [ ] **Step 3: Add the fields and wire them through**

In `src/cmd/policyclient/fetch.go`, replace the `CachedPolicy` struct:

```go
type CachedPolicy struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	// Derived by policy-server from the subfolder the policy file was
	// loaded from (e.g. "backup"). Pure passthrough here -- policyclient
	// itself never branches on it; agent does (see
	// cmd/agent/backup.go's backupTasks).
	Type string `json:"type"`
}
```

with:

```go
type CachedPolicy struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	// Storage-policy-only fields, zero/empty for a backup policy -- same
	// additive convention the proto itself uses. Passthrough here; agent
	// (cmd/agent/storage.go) is what interprets Config.
	Port   int32  `json:"port,omitempty"`
	Config string `json:"config,omitempty"`
	// Derived by policy-server from the subfolder the policy file was
	// loaded from (e.g. "backup"). Pure passthrough here -- policyclient
	// itself never branches on it; agent does (see
	// cmd/agent/backup.go's backupTasks and cmd/agent/storage.go's
	// storageTasks).
	Type string `json:"type"`
}
```

In `toCachedPolicies`, replace:

```go
		out = append(out, CachedPolicy{
			ID:            p.GetId(),
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
			Type:          p.GetType(),
		})
```

with:

```go
		out = append(out, CachedPolicy{
			ID:            p.GetId(),
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
			Port:          p.GetPort(),
			Config:        p.GetConfig(),
			Type:          p.GetType(),
		})
```

- [ ] **Step 4: Run the test to confirm it passes**

Run: `cd src && go test ./cmd/policyclient/... -v`
Expected: PASS, including the new test and every pre-existing one.

- [ ] **Step 5: Update `docs/components/policyclient.md`**

Replace the example cache JSON block:

```json
[
  {
    "id": "b1f2c3d4-...",
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": [
      {"id": "a9e8d7c6-...", "path": "/var/lib/postgres", "include": ["*.sql"]},
      {"id": "f0e1d2c3-...", "path": "/etc/postgres"}
    ],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"],
    "destination": "bwfs-east.internal:8080",
    "type": "backup"
  }
]
```

with:

```json
[
  {
    "id": "b1f2c3d4-...",
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": [
      {"id": "a9e8d7c6-...", "path": "/var/lib/postgres", "include": ["*.sql"]},
      {"id": "f0e1d2c3-...", "path": "/etc/postgres"}
    ],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"],
    "destination": "bwfs-east.internal:8080",
    "type": "backup"
  },
  {
    "id": "c2d3e4f5-...",
    "name": "east-1-storage",
    "created_at": "2026-07-28T00:00:00Z",
    "updated_at": "2026-07-28T00:00:00Z",
    "port": 9400,
    "config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}",
    "type": "storage"
  }
]
```

Replace:

```
`type` is derived by `policy-server` from the subfolder the policy file lives in (`policies/backup/`
today) — pure passthrough data as far as `policyclient` is concerned; see
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md).
`agent` is the consumer that actually branches on it (see
[agent](./agent.md#policy-driven-backup-execution)).
```

with:

```
`type` is derived by `policy-server` from the subfolder the policy file lives in (`policies/backup/`
or `policies/storage/`) — pure passthrough data as far as `policyclient` is concerned; see
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md).
`port`/`config` are populated only for a `"storage"`-typed policy (zero/empty for `"backup"`), same
additive convention as the RPC response. `agent` is the consumer that actually branches on `type`
(see [agent](./agent.md#policy-driven-backup-execution) for backup policies,
[agent](./agent.md#storage-policy-supervision) for storage ones).
```

- [ ] **Step 6: Commit**

```bash
cd src && go test ./cmd/policyclient/...
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go docs/components/policyclient.md
git commit -m "$(cat <<'EOF'
feat(policyclient): carry storage policy's port/config into the cache

Previously dropped -- agent needs these to actually run bwfs for a
cached storage policy (see cmd/agent/storage.go).
EOF
)"
```

---

## Task 5: `agent` — `storage.go`: `storageTask`/`storageTasks` (config parsing)

**Files:**
- Modify: `src/cmd/agent/backup.go` (extend `cachedPolicy` mirror struct)
- Create: `src/cmd/agent/storage.go`
- Create: `src/cmd/agent/storage_test.go`

**Interfaces:**
- Consumes: `readCachedPolicies(policiesCachePath string) ([]cachedPolicy, bool)` (already exists in `backup.go`, unchanged).
- Produces: `type storageTask struct { ID string; Args []string }`, `func storageTaskID(policyName string) string`, `func storageTasks(policiesCachePath string, logger *slog.Logger) ([]storageTask, bool)`. Task 6 constructs a `storageSupervisor` per `storageTask`, using `Args` directly as the `bwfs` command-line arguments (binary itself is resolved separately, see Task 8).

- [ ] **Step 1: Extend `cachedPolicy` in `backup.go`**

In `src/cmd/agent/backup.go`, replace:

```go
// cachedPolicy mirrors the subset of policyclient's on-disk CachedPolicy
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type cachedPolicy struct {
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}
```

with:

```go
// cachedPolicy mirrors the subset of policyclient's on-disk CachedPolicy
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type cachedPolicy struct {
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	// Storage-policy-only fields, zero/empty for a backup policy -- see
	// storage.go's storageTasks, the consumer that reads these.
	Port   int32  `json:"port,omitempty"`
	Config string `json:"config,omitempty"`
}
```

This is additive only; every existing test/consumer of `cachedPolicy` (all in `backup.go`/`backup_test.go`) is unaffected.

- [ ] **Step 2: Write the failing tests**

Create `src/cmd/agent/storage_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageTasks_BuildsTaskFromFilesystemConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "east-1-storage",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
	}]`)

	tasks, ok := storageTasks(path, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, "storage:east-1-storage", tasks[0].ID)
	assert.Equal(t, []string{"/data/storage", "server", "--port", "9400"}, tasks[0].Args)
}

func TestStorageTasks_SkipsUnsupportedBackend(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"s3\", \"root\": \"/data/storage\"}"
	}]`)

	tasks, ok := storageTasks(path, testLogger())
	assert.True(t, ok, "the file itself was still validly read")
	assert.Empty(t, tasks)
}

func TestStorageTasks_SkipsMissingRoot(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\"}"
	}]`)

	tasks, ok := storageTasks(path, testLogger())
	assert.True(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_SkipsUnparseableConfigJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "not json"
	}]`)

	tasks, ok := storageTasks(path, testLogger())
	assert.True(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_IgnoresNonStorageType(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "backup",
		"object_filters": [{"path": "/data"}],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)

	tasks, ok := storageTasks(path, testLogger())
	assert.True(t, ok)
	assert.Empty(t, tasks, "a cached policy whose type isn't \"storage\" must contribute zero storage tasks")
}

func TestStorageTasks_MissingCacheFileReturnsOkFalse(t *testing.T) {
	tasks, ok := storageTasks(filepath.Join(t.TempDir(), "does-not-exist.json"), testLogger())
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_CorruptCacheFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	tasks, ok := storageTasks(path, testLogger())
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestStorageTasks_MultiplePoliciesEachGetTheirOwnTask(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[
		{"name": "a", "type": "storage", "port": 9400, "config": "{\"backend\": \"filesystem\", \"root\": \"/data/a\"}"},
		{"name": "b", "type": "storage", "port": 9401, "config": "{\"backend\": \"filesystem\", \"root\": \"/data/b\"}"}
	]`)

	tasks, ok := storageTasks(path, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 2)
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "storage:a")
	assert.Contains(t, ids, "storage:b")
}
```

- [ ] **Step 3: Run tests to confirm they fail**

Run: `cd src && go test ./cmd/agent/... -run TestStorageTasks -v`
Expected: FAIL — `storageTasks`/`storageTask`/`storageTaskID` undefined.

- [ ] **Step 4: Implement `storage.go`**

Create `src/cmd/agent/storage.go`:

```go
// storage.go derives agent's "ensure this bwfs server is running" tasks
// from cached "storage"-type policies -- see storage_supervisor.go... no
// wait, kept in this same file: storageSupervisor/storageManager (Tasks 4-5
// of docs/superpowers/plans/2026-07-28-agent-storage-supervision.md) also
// live here. Unlike backupTasks (backup.go), there is no per-node targeting
// check: policy-server's GetPolicies already applied ClientFilters.Matches
// before a policy ever reached policies-cache.json, so anything with
// Type == "storage" in the cache is already scoped to this node. See
// docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
)

// storageTask is one bwfs server this node should be running, derived from
// a cached "storage" policy.
type storageTask struct {
	ID   string
	Args []string
}

// storageTaskID is the stable identifier for one storage policy's task in
// agent-state.json -- mirrors backup.go's "backup:" prefix convention.
// Like backupTaskID, this assumes policy names are effectively unique
// (the same pre-existing assumption backup tasks already make; not solved
// fresh here).
func storageTaskID(policyName string) string {
	return fmt.Sprintf("storage:%s", policyName)
}

// storageConfig is the subset of a storage policy's opaque config this
// agent understands -- today, exactly one backend.
type storageConfig struct {
	Backend string `json:"backend"`
	Root    string `json:"root"`
}

// storageTasks derives one ensure-running task per cached "storage" policy,
// valid at the instant it's called -- callers that need to notice
// policies-cache.json changing over time (agent serve's reconcile loop)
// must call this fresh every tick rather than caching its result once.
//
// ok=false mirrors backupTasks's contract: it means this tick's read of
// policiesCachePath failed, and callers must never treat that as "there are
// zero storage tasks."
//
// A policy whose config doesn't parse as a filesystem-backend JSON object,
// or whose root is empty, is skipped with a logged error -- the same
// fail-safe "skip, don't block the rest" direction backupTasks already uses
// for an unparseable rpo or missing backup_window.
func storageTasks(policiesCachePath string, logger *slog.Logger) ([]storageTask, bool) {
	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []storageTask
	for _, p := range cachedPolicies {
		if p.Type != "storage" {
			continue
		}
		var cfg storageConfig
		if err := json.Unmarshal([]byte(p.Config), &cfg); err != nil || cfg.Backend != "filesystem" || cfg.Root == "" {
			logger.Error("storage policy has unsupported or unparseable config, skipping", "policy", p.Name)
			continue
		}
		tasks = append(tasks, storageTask{
			ID:   storageTaskID(p.Name),
			Args: []string{cfg.Root, "server", "--port", strconv.Itoa(int(p.Port))},
		})
	}
	return tasks, true
}
```

- [ ] **Step 5: Run tests to confirm they pass**

Run: `cd src && go test ./cmd/agent/... -run TestStorageTasks -v`
Expected: PASS, all 8 tests.

- [ ] **Step 6: Commit**

```bash
cd src && go test ./cmd/agent/...
git add src/cmd/agent/backup.go src/cmd/agent/storage.go src/cmd/agent/storage_test.go
git commit -m "$(cat <<'EOF'
feat(agent): derive ensure-running tasks from cached storage policies

storageTasks parses a storage policy's config for a filesystem
backend's root path and builds the bwfs server command line. No
per-node targeting check needed here -- policy-server's GetPolicies
already scoped the cache to this node via client_filters.
EOF
)"
```

---

## Task 6: `agent` — `storageSupervisor`

**Files:**
- Modify: `src/cmd/agent/storage.go`
- Modify: `src/cmd/agent/storage_test.go`

**Interfaces:**
- Consumes: `backoff(failures int) time.Duration`, `backoffBase`/`backoffMax` (package vars, already in `reconcile.go`).
- Produces: `func newStorageSupervisor(binary string, args []string, logger *slog.Logger, onOutcome func(err error)) *storageSupervisor`, `func (s *storageSupervisor) Start(ctx context.Context)`, `func (s *storageSupervisor) Stop()`, field `s.loopDone chan struct{}` (closed when the supervise loop exits, for tests to synchronize on), field `s.onSpawnForTest func()` (test-only hook). `onOutcome` is called with `nil` immediately after every successful process start (this is this task's notion of "success" — a server process isn't expected to exit on its own), and with a non-nil error only when the process exits unexpectedly (never called for a deliberate `Stop()`). Consumed by Task 7 (`storageManager` constructs one supervisor per task, passing an `onOutcome` closure that calls into `reconcileState.recordOutcome`).

This type is a close copy of the existing `vectorSupervisor` in `vector.go` — read that file's `spawnAndWait`/`superviseLoop`/`Stop` before starting, and preserve its comments' reasoning (documented there) about why `cmd.Start()` happens under the mutex and why `spawnAndWait` sends `SIGTERM` itself instead of relying on `exec.CommandContext`'s default kill-on-cancel. Two differences from `vectorSupervisor`: no `TriggerRestart` (confirmed unnecessary — `bwfs` already hot-reloads its identity cert per-handshake via `mtls.LoadServerCredentials`, unlike Vector), and an `onOutcome` callback so a supervised `bwfs`'s state reaches `agent-state.json`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/agent/storage_test.go`:

```go
func TestStorageSupervisor_StartsAndStopsCleanlyOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	var spawns int64
	sup := newStorageSupervisor(script, nil, testLogger(), func(error) {})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	sup.Start(ctx)

	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt64(&spawns))
	cancel()

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context cancellation")
	}
	assert.EqualValues(t, 1, atomic.LoadInt64(&spawns), "no respawn should happen once ctx is cancelled")
}

func TestStorageSupervisor_RestartsOnUnexpectedExitAndRecordsFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\nexit 1\n"))

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Millisecond, 30*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	var spawns int64
	var mu sync.Mutex
	var outcomes []error
	sup := newStorageSupervisor(script, nil, testLogger(), func(err error) {
		mu.Lock()
		defer mu.Unlock()
		outcomes = append(outcomes, err)
	})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Start(ctx)

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context timeout")
	}

	assert.GreaterOrEqual(t, atomic.LoadInt64(&spawns), int64(2), "a persistently crashing bwfs must be respawned more than once")

	mu.Lock()
	defer mu.Unlock()
	var sawFailure bool
	for _, err := range outcomes {
		if err != nil {
			sawFailure = true
		}
	}
	assert.True(t, sawFailure, "at least one crash must be recorded as a failure")
}

func TestStorageSupervisor_SuccessfulStartRecordsSuccessImmediately(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	outcomes := make(chan error, 1)
	sup := newStorageSupervisor(script, nil, testLogger(), func(err error) { outcomes <- err })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	select {
	case err := <-outcomes:
		assert.NoError(t, err, "a successful start must record success without waiting for the process to exit")
	case <-time.After(time.Second):
		t.Fatal("onOutcome was never called for a successful start")
	}
	sup.Stop()
}

func TestStorageSupervisor_DeliberateStopDoesNotRecordFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	var mu sync.Mutex
	var outcomes []error
	sup := newStorageSupervisor(script, nil, testLogger(), func(err error) {
		mu.Lock()
		defer mu.Unlock()
		outcomes = append(outcomes, err)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	time.Sleep(100 * time.Millisecond) // let it start (records one nil outcome)

	sup.Stop()
	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after Stop()")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, err := range outcomes {
		assert.NoError(t, err, "a deliberate Stop() must never record a failure outcome")
	}
}

// osWriteExecutable writes content to path as an executable file --
// shared test helper so every fake-bwfs.sh fixture above is one line.
func osWriteExecutable(t *testing.T, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o755)
}
```

Add the now-needed imports to `src/cmd/agent/storage_test.go`'s import block: `"context"`, `"os"`, `"sync"`, `"sync/atomic"`, `"time"`.

- [ ] **Step 2: Run tests to confirm they fail**

Run: `cd src && go test ./cmd/agent/... -run TestStorageSupervisor -v`
Expected: FAIL — `newStorageSupervisor`/`storageSupervisor` undefined.

- [ ] **Step 3: Implement `storageSupervisor` in `storage.go`**

Append to `src/cmd/agent/storage.go` (and add `"context"`, `"os/exec"`, `"sync"`, `"syscall"`, `"time"` to its imports):

```go
// storageSupervisor owns the lifecycle of one supervised bwfs server
// process: a long-running child, not a due/execute/complete Policy, so it
// gets its own small supervise loop -- modeled directly on vector.go's
// vectorSupervisor. Two differences: no TriggerRestart (bwfs already
// hot-reloads its identity cert per-handshake via mtls.LoadServerCredentials,
// unlike Vector, so a cert-rotation-triggered restart would only add
// disruption with no benefit), and an onOutcome callback so a supervised
// bwfs's state reaches agent-state.json via reconcileState.recordOutcome
// (see storageManager in this same file).
type storageSupervisor struct {
	binary string
	args   []string
	logger *slog.Logger

	mu           sync.Mutex
	cmd          *exec.Cmd
	shuttingDown bool

	// onSpawnForTest, when non-nil, is called once per spawn attempt --
	// test-only instrumentation, never set in production.
	onSpawnForTest func()

	// onOutcome is called with nil immediately after every successful
	// process start (this supervisor's notion of "success" -- a server
	// isn't expected to exit on its own), and with a non-nil error only
	// when the process exits unexpectedly. Never called for a deliberate
	// Stop().
	onOutcome func(err error)

	// loopDone is closed when superviseLoop returns, giving callers (and
	// tests) a real signal to synchronize on instead of guessing at a
	// sleep duration.
	loopDone chan struct{}
}

func newStorageSupervisor(binary string, args []string, logger *slog.Logger, onOutcome func(err error)) *storageSupervisor {
	return &storageSupervisor{binary: binary, args: args, logger: logger, onOutcome: onOutcome}
}

// Start launches the supervise loop in its own goroutine and returns
// immediately; the loop itself runs until ctx is done, at which point the
// currently-running bwfs process (if any) is also signalled to exit.
func (s *storageSupervisor) Start(ctx context.Context) {
	s.loopDone = make(chan struct{})
	go func() {
		defer close(s.loopDone)
		s.superviseLoop(ctx)
	}()
}

// Stop signals the currently-running bwfs process to exit (SIGTERM -- a
// graceful drain once bwfs's own signal.NotifyContext fix lands, see Task 11)
// and tells the supervise loop not to respawn it.
func (s *storageSupervisor) Stop() {
	s.mu.Lock()
	s.shuttingDown = true
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (s *storageSupervisor) superviseLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		err := s.spawnAndWait(ctx)

		s.mu.Lock()
		shuttingDown := s.shuttingDown
		s.mu.Unlock()

		if shuttingDown || ctx.Err() != nil {
			return
		}

		failures++
		s.logger.Error("bwfs exited unexpectedly, restarting with backoff", "failures", failures, "error", err)
		if s.onOutcome != nil {
			s.onOutcome(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(failures)):
		}
	}
}

// spawnAndWait starts bwfs and blocks until it exits, calling onOutcome(nil)
// immediately on a successful start. If ctx is cancelled while bwfs is still
// running, it is sent SIGTERM -- see vectorSupervisor.spawnAndWait in
// vector.go for the detailed reasoning behind starting cmd.Start() under
// the mutex and handling ctx cancellation this way; identical here.
func (s *storageSupervisor) spawnAndWait(ctx context.Context) error {
	cmd := exec.Command(s.binary, s.args...)

	s.mu.Lock()
	err := cmd.Start()
	if err == nil {
		s.cmd = cmd
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start bwfs: %w", err)
	}
	if s.onSpawnForTest != nil {
		s.onSpawnForTest()
	}
	if s.onOutcome != nil {
		s.onOutcome(nil)
	}

	waitDone := make(chan struct{})
	defer close(waitDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case <-waitDone:
		}
	}()

	return cmd.Wait()
}
```

Note: on a `cmd.Start()` failure, `spawnAndWait` returns early without calling `onOutcome` itself — `superviseLoop` still calls `s.onOutcome(err)` for that case since it's after the `shuttingDown`/`ctx.Err()` checks, same as any other unexpected exit. This means a `Start()` failure is recorded as a failure, correctly.

- [ ] **Step 4: Run tests to confirm they pass**

Run: `cd src && go test ./cmd/agent/... -run TestStorageSupervisor -v`
Expected: PASS, all 4 tests.

- [ ] **Step 5: Commit**

```bash
cd src && go test ./cmd/agent/...
git add src/cmd/agent/storage.go src/cmd/agent/storage_test.go
git commit -m "$(cat <<'EOF'
feat(agent): storageSupervisor -- crash-restart supervision for bwfs

Modeled on vector.go's vectorSupervisor. No TriggerRestart -- unlike
Vector, bwfs already hot-reloads its identity cert per-handshake, so
a cert-rotation restart would add disruption with no benefit. An
onOutcome callback lets storageManager (next task) wire this into
agent-state.json.
EOF
)"
```

---

## Task 7: `agent` — `storageManager`

**Files:**
- Modify: `src/cmd/agent/storage.go`
- Modify: `src/cmd/agent/storage_test.go`

**Interfaces:**
- Consumes: `storageTask{ID, Args}` (Task 5), `newStorageSupervisor(binary, args, logger, onOutcome) *storageSupervisor` (Task 6), `reconcileState.recordOutcome(id string, attemptErr error, attemptTime time.Time)` (already exists in `reconcile.go`).
- Produces: `func newStorageManager(binary string, logger *slog.Logger) *storageManager`, `func (m *storageManager) reconcile(ctx context.Context, rs *reconcileState, tasks []storageTask)`, `func (m *storageManager) StopAll()`. Field `m.supervisors map[string]*storageSupervisor` (test-visible, same package). Consumed by Task 8 (`run()`'s loop calls `.reconcile(...)` each tick) and Task 10 (`main.go` calls `.StopAll()` on shutdown).

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/agent/storage_test.go`:

```go
func TestStorageManager_StartsSupervisorForNewTask(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Args: nil}})

	require.Eventually(t, func() bool {
		return rs.get("storage:east-1").LastSuccessAt != nil
	}, time.Second, 10*time.Millisecond, "a newly-appeared task must get a running supervisor recorded as successful")

	mgr.StopAll()
}

func TestStorageManager_StopsSupervisorForRemovedTask(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Args: nil}})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 1
	}, time.Second, 10*time.Millisecond)

	mgr.reconcile(ctx, rs, nil) // task no longer present

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	assert.Empty(t, mgr.supervisors, "a supervisor for a removed task must be stopped and dropped")
}

func TestStorageManager_RestartsSupervisorWhenArgsChange(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Args: []string{"/data/old", "server", "--port", "9400"}}})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 1
	}, time.Second, 10*time.Millisecond)
	mgr.mu.Lock()
	firstSup := mgr.supervisors["storage:east-1"]
	mgr.mu.Unlock()

	mgr.reconcile(ctx, rs, []storageTask{{ID: "storage:east-1", Args: []string{"/data/new", "server", "--port", "9401"}}})

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.supervisors, 1)
	assert.NotSame(t, firstSup, mgr.supervisors["storage:east-1"], "a task whose args changed must get a fresh supervisor")
}

func TestStorageManager_DoesNotDoubleStartAlreadySupervisedTask(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := storageTask{ID: "storage:east-1", Args: []string{"/data", "server", "--port", "9400"}}
	mgr.reconcile(ctx, rs, []storageTask{task})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 1
	}, time.Second, 10*time.Millisecond)
	mgr.mu.Lock()
	firstSup := mgr.supervisors["storage:east-1"]
	mgr.mu.Unlock()

	mgr.reconcile(ctx, rs, []storageTask{task}) // same task, second tick

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	assert.Same(t, firstSup, mgr.supervisors["storage:east-1"], "an unchanged task must not be restarted")
}

func TestStorageManager_StopAllStopsEverySupervisor(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))

	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}
	mgr := newStorageManager(script, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.reconcile(ctx, rs, []storageTask{
		{ID: "storage:a", Args: nil},
		{ID: "storage:b", Args: nil},
	})
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		return len(mgr.supervisors) == 2
	}, time.Second, 10*time.Millisecond)

	mgr.mu.Lock()
	dones := make([]chan struct{}, 0, len(mgr.supervisors))
	for _, sup := range mgr.supervisors {
		dones = append(dones, sup.loopDone)
	}
	mgr.mu.Unlock()

	mgr.StopAll()

	for _, done := range dones {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("StopAll did not stop every supervisor")
		}
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

Run: `cd src && go test ./cmd/agent/... -run TestStorageManager -v`
Expected: FAIL — `newStorageManager`/`storageManager` undefined.

- [ ] **Step 3: Implement `storageManager` in `storage.go`**

Append to `src/cmd/agent/storage.go` (add `"slices"` to its imports):

```go
// storageManager holds one storageSupervisor per current storage task,
// keyed by task ID, and reconciles that set against agent's latest read of
// policies-cache.json every tick (see reconcile.go's run(), which calls
// reconcile once per loop iteration).
type storageManager struct {
	binary string
	logger *slog.Logger

	mu          sync.Mutex
	supervisors map[string]*storageSupervisor
	args        map[string][]string // last-started args, to detect a changed task
}

func newStorageManager(binary string, logger *slog.Logger) *storageManager {
	return &storageManager{
		binary:      binary,
		logger:      logger,
		supervisors: map[string]*storageSupervisor{},
		args:        map[string][]string{},
	}
}

// reconcile starts a supervisor for every newly-appeared task, stops and
// removes one for every task that disappeared or whose Args changed
// (port/path edited on the same policy -- the old process is stopped, a
// fresh one started with the new args), and leaves an unchanged task's
// supervisor running untouched. rs is the same reconcileState run()'s own
// loop already uses -- recordOutcome is mutex-guarded internally, so this
// is safe to call from storageSupervisor's own background goroutines
// concurrently with run()'s main loop, exactly like backup-task goroutines
// already do.
func (m *storageManager) reconcile(ctx context.Context, rs *reconcileState, tasks []storageTask) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wanted := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		wanted[t.ID] = t.Args
	}

	for id, sup := range m.supervisors {
		newArgs, stillWanted := wanted[id]
		if !stillWanted || !slices.Equal(newArgs, m.args[id]) {
			sup.Stop()
			delete(m.supervisors, id)
			delete(m.args, id)
		}
	}

	for _, t := range tasks {
		if _, exists := m.supervisors[t.ID]; exists {
			continue
		}
		id := t.ID
		sup := newStorageSupervisor(m.binary, t.Args, m.logger, func(err error) {
			rs.recordOutcome(id, err, time.Now())
		})
		sup.Start(ctx)
		m.supervisors[t.ID] = sup
		m.args[t.ID] = t.Args
	}
}

// StopAll stops every currently-supervised bwfs process -- called on agent
// shutdown so none are orphaned.
func (m *storageManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sup := range m.supervisors {
		sup.Stop()
	}
}
```

- [ ] **Step 4: Run tests to confirm they pass**

Run: `cd src && go test ./cmd/agent/... -run TestStorageManager -v`
Expected: PASS, all 5 tests.

- [ ] **Step 5: Commit**

```bash
cd src && go test ./cmd/agent/...
git add src/cmd/agent/storage.go src/cmd/agent/storage_test.go
git commit -m "$(cat <<'EOF'
feat(agent): storageManager -- start/stop/restart bwfs per storage task

Diffs the current storage task set against running supervisors each
tick: starts new ones, stops removed ones, restarts ones whose
port/path changed. Shares reconcileState with run()'s own loop so
crash/success state lands in the same agent-state.json.
EOF
)"
```

---

## Task 8: `agent` — wire storage supervision into `reconcile.go`'s `run()`

**Files:**
- Modify: `src/cmd/agent/reconcile.go`
- Modify: `src/cmd/agent/reconcile_test.go`
- Modify: `src/cmd/agent/integration_test.go`

**Interfaces:**
- Consumes: `storageTask` (Task 5), `storageManager.reconcile(ctx, rs, tasks)` (Task 7).
- Produces: `run(ctx, logger, cachePath, reconcileInterval, execute, policiesFunc, maxConcurrentBackgroundJobs, onSuccess, storageTasksFunc, storageMgr) error` (two new trailing parameters — `storageTasksFunc func() ([]storageTask, bool)` and `storageMgr *storageManager`; both `nil` fully disables storage supervision, preserving today's exact behavior). Also `resolveExecPath(binary string) string`, extracted from `realExec`. Consumed by Task 10 (`main.go`'s two `run()`/binary-resolution call sites).

- [ ] **Step 1: Extract `resolveExecPath` from `realExec`**

In `src/cmd/agent/reconcile.go`, replace:

```go
func realExec(ctx context.Context, binary string, args []string) error {
	path := binary
	if !strings.Contains(binary, string(filepath.Separator)) {
		if exePath, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(exePath), binary)
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
			}
		}
	}
	return exec.CommandContext(ctx, path, args...).Run()
}
```

with:

```go
// resolveExecPath resolves binary to a colocated sibling of this agent's
// own executable when one exists there (bare name, no path separator),
// falling back to binary unchanged otherwise so exec.Command's normal
// $PATH lookup applies -- the same "colocated sibling binary" layout used
// elsewhere in this repo (see deploy/control-plane/catalog's
// entrypoint.sh, and common/config.ResolveBaseDir/ResolveVarDir). Shared by
// realExec (one-shot policy execs) and main.go's bwfs resolution for
// storage.go's storageManager (which resolves "bwfs" once at construction
// rather than per-spawn).
func resolveExecPath(binary string) string {
	if strings.Contains(binary, string(filepath.Separator)) {
		return binary
	}
	exePath, err := os.Executable()
	if err != nil {
		return binary
	}
	candidate := filepath.Join(filepath.Dir(exePath), binary)
	if _, err := os.Stat(candidate); err != nil {
		return binary
	}
	return candidate
}

func realExec(ctx context.Context, binary string, args []string) error {
	return exec.CommandContext(ctx, resolveExecPath(binary), args...).Run()
}
```

This is a pure refactor (identical behavior) — run the existing tests before continuing to confirm nothing broke:

Run: `cd src && go test ./cmd/agent/... -run TestRealExec -v`
Expected: PASS (both `TestRealExec_ResolvesBinaryColocatedWithOwnExecutable` and `TestRealExec_FallsBackToPathWhenNotColocated`), unchanged.

- [ ] **Step 2: Write the failing test for `run()`'s new storage integration**

Add to `src/cmd/agent/integration_test.go`:

```go
func TestRun_StorageTaskFromRealCacheFileStartsAndPrunesBwfsSupervisor(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	policiesCachePath := filepath.Join(dir, "policies-cache.json")

	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"), 0o755))

	cacheJSON := `[{
		"name": "east-1-storage",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
	}]`
	require.NoError(t, os.WriteFile(policiesCachePath, []byte(cacheJSON), 0o644))

	storageTasksFunc := func() ([]storageTask, bool) { return storageTasks(policiesCachePath, testLogger()) }
	mgr := newStorageManager(script, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec,
			func() ([]Policy, bool) { return nil, true }, 2, nil, storageTasksFunc, mgr)
	}()

	require.Eventually(t, func() bool {
		cache, err := readCache(cachePath)
		return err == nil && cache["storage:east-1-storage"].LastSuccessAt != nil
	}, time.Second, 10*time.Millisecond, "storage task must start and record success")

	// Remove the policy from the cache -- its task must be pruned from
	// agent-state.json and its bwfs supervisor stopped.
	require.NoError(t, os.WriteFile(policiesCachePath, []byte(`[]`), 0o644))

	require.Eventually(t, func() bool {
		cache, err := readCache(cachePath)
		return err == nil && len(cache) == 0
	}, time.Second, 10*time.Millisecond, "removed storage task must be pruned from agent-state.json")

	cancel()
	<-done
}
```

- [ ] **Step 3: Run it to confirm it fails**

Run: `cd src && go test ./cmd/agent/... -run TestRun_StorageTask -v`
Expected: FAIL — `run` doesn't accept the two new trailing arguments yet (compile error).

- [ ] **Step 4: Update `run()`'s signature and loop body**

In `src/cmd/agent/reconcile.go`, replace the `run` function's signature line:

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int, onSuccess func(policyID string)) error {
```

with:

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int, onSuccess func(policyID string), storageTasksFunc func() ([]storageTask, bool), storageMgr *storageManager) error {
```

Also update its doc comment (immediately above) to add one sentence: `// storageTasksFunc/storageMgr add ensure-running bwfs supervision alongside the due/execute policy loop below -- either nil disables it entirely, preserving prior behavior exactly (see storage.go).`

Replace the top of the `for ctx.Err() == nil {` loop body:

```go
	for ctx.Err() == nil {
		now := time.Now()
		policyList, ok := policiesFunc()
		if ok {
			currentIDs := make(map[string]struct{}, len(policyList))
			for _, p := range policyList {
				currentIDs[p.ID] = struct{}{}
			}
			rs.prune(currentIDs)
		}

		for _, p := range policyList {
```

with:

```go
	for ctx.Err() == nil {
		now := time.Now()
		policyList, ok := policiesFunc()

		var storageTaskList []storageTask
		storageOk := true
		if storageTasksFunc != nil {
			storageTaskList, storageOk = storageTasksFunc()
		}

		if ok && storageOk {
			currentIDs := make(map[string]struct{}, len(policyList)+len(storageTaskList))
			for _, p := range policyList {
				currentIDs[p.ID] = struct{}{}
			}
			for _, t := range storageTaskList {
				currentIDs[t.ID] = struct{}{}
			}
			rs.prune(currentIDs)
		}

		if storageMgr != nil && storageOk {
			storageMgr.reconcile(ctx, rs, storageTaskList)
		}

		for _, p := range policyList {
```

Nothing else in `run()` changes — the rest of the per-policy dispatch loop, `sleepOrDone`, and `wg.Wait()` at the end are untouched.

When `storageTasksFunc` is `nil` (every existing test), `storageOk` stays `true` and `storageTaskList` stays `nil`, so `currentIDs`'s union is unchanged from today and `storageMgr.reconcile` is never called — identical behavior to before this task for every pre-existing call site.

- [ ] **Step 5: Update every existing `run()` call site**

Both `src/cmd/agent/reconcile_test.go` and `src/cmd/agent/integration_test.go` call `run(...)` with the old 8-argument signature at many call sites. Append `, nil, nil` (for the new `storageTasksFunc`, `storageMgr` parameters) to every one of them **except** the new test added in Step 2 (which already has the full 10-argument call).

Run: `cd src && go build ./cmd/agent/...`
Expected: FAIL, with the compiler listing every `run(...)` call site still using the old arity — e.g. `not enough arguments in call to run`. Fix each reported line by appending `, nil, nil` before the closing `)`.

Run again: `cd src && go build ./cmd/agent/...`
Expected: PASS once every call site is updated.

- [ ] **Step 6: Run the full agent test suite**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS — every pre-existing test (behavior unchanged, confirmed by the `nil, nil` appended arguments) plus the new `TestRun_StorageTaskFromRealCacheFileStartsAndPrunesBwfsSupervisor`.

- [ ] **Step 7: Commit**

```bash
cd src && go test ./cmd/agent/...
git add src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go src/cmd/agent/integration_test.go
git commit -m "$(cat <<'EOF'
feat(agent): wire storage supervision into run()'s reconcile loop

run() gains two trailing, nil-able parameters (storageTasksFunc,
storageMgr) so a storage task's ID is unioned into the same prune
pass the static/backup policies already use, and storageManager.
reconcile is called once per tick -- both nil (every pre-existing
call site) preserves today's behavior exactly. Also extracts
resolveExecPath from realExec so main.go's bwfs binary resolution
(next task) reuses the same colocated-sibling-with-$PATH-fallback
logic.
EOF
)"
```

---

## Task 9: `agent` — `list-policies` shows storage tasks

**Files:**
- Modify: `src/cmd/agent/list.go`
- Modify: `src/cmd/agent/list_test.go`

**Interfaces:**
- Consumes: `storageTask{ID, Args}` (Task 5), `health(s PolicyState) string`, `formatTime`, `formatError` (all already exist in `list.go`, unchanged).
- Produces: `renderPolicies(w io.Writer, cachePath string, now time.Time, policies []Policy, storageTasks []storageTask) error` (one new trailing parameter). Consumed by Task 10 (`main.go`'s two call sites).

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/agent/list_test.go`:

```go
func TestRenderPolicies_StorageTaskShowsOkAndDashForNextRun(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"storage:east-1-storage": {LastSuccessAt: &now},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, nil, []storageTask{{ID: "storage:east-1-storage"}}))

	out := buf.String()
	assert.Contains(t, out, "storage:east-1-storage")
	assert.Contains(t, out, "ok")
}

func TestRenderPolicies_StorageTaskCrashLoopingShowsRetryingWithCount(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	now := time.Now()
	retryAt := now.Add(time.Minute)
	require.NoError(t, writeCache(cachePath, Cache{
		"storage:east-1-storage": {LastAttemptAt: &now, ConsecutiveFailures: 4, NextRetryAt: &retryAt, LastError: "bind: address already in use"},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, nil, []storageTask{{ID: "storage:east-1-storage"}}))

	out := buf.String()
	assert.Contains(t, out, "retrying (4 failures)")
	assert.Contains(t, out, "bind: address already in use")
}
```

- [ ] **Step 2: Run tests to confirm they fail**

Run: `cd src && go test ./cmd/agent/... -run TestRenderPolicies_StorageTask -v`
Expected: FAIL — `renderPolicies` doesn't accept a 5th argument yet (compile error).

- [ ] **Step 3: Update `renderPolicies`**

In `src/cmd/agent/list.go`, replace:

```go
// renderPolicies reads cachePath and writes a table of every embedded
// policy's reconciliation state to w. It never executes a policy — purely
// a read-only view of what `agent serve` last recorded.
func renderPolicies(w io.Writer, cachePath string, now time.Time, policies []Policy) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "POLICY\tSTATE\tLAST SUCCESS\tLAST ATTEMPT\tFAILURES\tERROR\tNEXT RUN")
	for _, p := range policies {
		s := cache[p.ID]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			p.ID,
			health(s),
			formatTime(s.LastSuccessAt),
			formatTime(s.LastAttemptAt),
			s.ConsecutiveFailures,
			formatError(s.LastError),
			formatNextRun(estimatedNextRun(p, s, now), now),
		)
	}
	return tw.Flush()
}
```

with:

```go
// renderPolicies reads cachePath and writes a table of every embedded
// policy's reconciliation state, plus every supervised storage task's, to
// w. It never executes a policy or starts a bwfs process — purely a
// read-only view of what `agent serve` last recorded.
func renderPolicies(w io.Writer, cachePath string, now time.Time, policies []Policy, storageTasks []storageTask) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "POLICY\tSTATE\tLAST SUCCESS\tLAST ATTEMPT\tFAILURES\tERROR\tNEXT RUN")
	for _, p := range policies {
		s := cache[p.ID]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			p.ID,
			health(s),
			formatTime(s.LastSuccessAt),
			formatTime(s.LastAttemptAt),
			s.ConsecutiveFailures,
			formatError(s.LastError),
			formatNextRun(estimatedNextRun(p, s, now), now),
		)
	}
	// Storage tasks are ensure-running daemons, not scheduled jobs -- there
	// is no next-run estimate to show, so NEXT RUN is always "-".
	for _, t := range storageTasks {
		s := cache[t.ID]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			t.ID,
			health(s),
			formatTime(s.LastSuccessAt),
			formatTime(s.LastAttemptAt),
			s.ConsecutiveFailures,
			formatError(s.LastError),
			"-",
		)
	}
	return tw.Flush()
}
```

- [ ] **Step 4: Update every existing `renderPolicies()` call site**

Run: `cd src && go build ./cmd/agent/...`
Expected: FAIL, listing every `renderPolicies(...)` call site (in `list_test.go` and `main.go`) still using the old 4-argument arity. In each test call site, append `, nil` for the new `storageTasks` parameter. Leave `main.go` for Task 10.

Run: `cd src && go test ./cmd/agent/... -run TestRenderPolicies -v`
Expected: PASS, all tests including the two new ones (note `main.go` will still fail to build until Task 10 — that's expected and fixed there; if you want a clean build checkpoint here, temporarily append `, nil` to `main.go`'s one `renderPolicies` call too, matching what Task 10 will replace anyway).

- [ ] **Step 5: Commit**

```bash
cd src && go test ./cmd/agent/... -run TestRenderPolicies -v
git add src/cmd/agent/list.go src/cmd/agent/list_test.go
git commit -m "$(cat <<'EOF'
feat(agent): list-policies shows supervised storage tasks

Reuses health()/formatTime/formatError unchanged so ok/retrying(N
failures)/never run mean exactly what they mean everywhere else;
NEXT RUN is always "-" since these are ensure-running daemons, not
scheduled jobs.
EOF
)"
```

---

## Task 10: `agent` — wire everything into `main.go`, plus `docs/components/agent.md`

**Files:**
- Modify: `src/cmd/agent/main.go`
- Modify: `docs/components/agent.md`

**Interfaces:**
- Consumes: `resolveExecPath(binary string) string` (Task 8), `newStorageManager(binary, logger) *storageManager` (Task 7), `storageTasks(policiesCachePath, logger) ([]storageTask, bool)` (Task 5), `run(..., storageTasksFunc, storageMgr)` (Task 8), `renderPolicies(w, cachePath, now, policies, storageTasks)` (Task 9), `storageManager.StopAll()` (Task 7).
- Produces: the fully wired `agent serve`/`agent list-policies` binary. Nothing downstream consumes `main.go` — this is the top of the call graph.

- [ ] **Step 1: Update imports**

In `src/cmd/agent/main.go`, add `"io"` and `"log/slog"` to the import block (needed for the `list-policies` branch's silent logger):

```go
import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
)
```

- [ ] **Step 2: Wire storage supervision into the `serve` case**

Replace:

```go
		vectorBinary, err := resolveVectorBinary()
		if err != nil {
			logger.Error("vector binary resolution failed", "error", err)
			os.Exit(1)
		}
```

with:

```go
		vectorBinary, err := resolveVectorBinary()
		if err != nil {
			logger.Error("vector binary resolution failed", "error", err)
			os.Exit(1)
		}
		bwfsBinary := resolveExecPath("bwfs")
		storageMgr := newStorageManager(bwfsBinary, logger)
		storageTasksFunc := func() ([]storageTask, bool) {
			return storageTasks(policiesCachePath, logger)
		}
```

Replace:

```go
		vectorSup := newVectorSupervisor(vectorBinary, vectorConfigPath, logger)
		vectorSup.Start(signalCtx)
		defer vectorSup.Stop()
```

with:

```go
		vectorSup := newVectorSupervisor(vectorBinary, vectorConfigPath, logger)
		vectorSup.Start(signalCtx)
		defer vectorSup.Stop()
		defer storageMgr.StopAll()
```

Replace:

```go
		logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath, "vector_config", vectorConfigPath)
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs, onSuccess); err != nil {
			logger.Error("agent exited with error", "error", err)
			os.Exit(1)
		}
```

with:

```go
		logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath, "vector_config", vectorConfigPath)
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs, onSuccess, storageTasksFunc, storageMgr); err != nil {
			logger.Error("agent exited with error", "error", err)
			os.Exit(1)
		}
```

- [ ] **Step 3: Wire storage tasks into the `list-policies` case**

Replace:

```go
	case "list-policies":
		allPolicies, _ := policiesFunc()
		if err := renderPolicies(os.Stdout, cachePath, time.Now(), allPolicies); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
	}
```

with:

```go
	case "list-policies":
		allPolicies, _ := policiesFunc()
		// list-policies never executes anything -- a silent logger here
		// keeps storageTasks' own skip-with-log warnings out of stdout's
		// table, matching this command's existing read-only, no-noise
		// character.
		silentLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
		storageTaskList, _ := storageTasks(policiesCachePath, silentLogger)
		if err := renderPolicies(os.Stdout, cachePath, time.Now(), allPolicies, storageTaskList); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
	}
```

- [ ] **Step 4: Build and run the full agent test suite**

Run: `cd src && go build ./cmd/agent/... && go test ./cmd/agent/... -v`
Expected: PASS, every test in the package.

- [ ] **Step 5: Manual smoke test**

Run: `cd src && go build -o /tmp/agent-smoke ./cmd/agent && /tmp/agent-smoke list-policies`
Expected: prints the usual table with the three static policies (all "never run" since there's no real `agent-state.json` at the default location) and no crash — confirms the wiring compiles and runs end to end even with no storage policies present.

- [ ] **Step 6: Update `docs/components/agent.md`**

Add a new section, right after the existing "## Policy-driven backup execution" section (before "## Logging and correlation"):

```markdown
## Storage-policy supervision

Every reconcile tick, alongside deriving backup tasks, `agent` also derives one **ensure-running**
task per cached policy whose `type` is `"storage"` — unlike a backup task (or the three static
policies), this isn't a due/execute/complete unit: it's "this `bwfs server` process should be
running," checked and corrected every tick rather than scheduled on an interval. There is no
per-node targeting check here — `policy-server`'s `GetPolicies` already scoped
`policies-cache.json` to this node via `client_filters` (the same mechanism a backup policy uses),
so every `"storage"`-typed policy in the cache is already meant for this node.

A storage policy's `config` is opaque JSON to `policy-server`, but `agent` interprets one shape:
`{"backend": "filesystem", "root": "/data/storage"}`. Any other or missing `backend` value is
skipped with a logged error, the same fail-safe direction as an unparseable `rpo` or missing
`backup_window` for backup tasks. A matching policy becomes `bwfs <root> server --port <port>`.

Each storage task is supervised independently (`storage:<policy-name>`, mirroring the `backup:`
prefix convention): a successful start is recorded immediately as success (not "exited
successfully" — a server isn't expected to exit on its own), an unexpected exit is recorded as a
failure with the same jittered `backoff()` reconcile.go already uses elsewhere, and a policy that's
edited (port/path changed) or removed causes the running `bwfs` to be stopped (`SIGTERM`, a graceful
drain since `bwfs` now honors it — see [bwfs](./bwfs.md)) and, for an edit, a fresh one started with
the new arguments. `agent list-policies` shows each supervised storage task as an additional row,
reusing the same STATE/FAILURES/ERROR columns as everything else, with `NEXT RUN` always `-` since
there's no schedule to estimate.

See [Design: agent storage-policy supervision](../superpowers/specs/2026-07-28-agent-storage-supervision-design.md).
```

Also update the file's opening paragraph (line 1-10) to mention the new capability. Replace:

```
Node-level agent that reconciles local state against a small, config-driven set of policies.
It runs three embedded, statically-configured policies — `bootstrap-refresh`, `operating-refresh`,
and `policy-update` — the first two keep this node's two-tier mTLS credential (see
[Security Model](../SECURITY.md)) fresh via `certclient`; the third fetches this node's applicable
backup policies from `policy-server` (see [policy-server](./policy-server.md)) into a local cache
via `policyclient`. On top of those three, `agent` also derives a dynamic **backup task** for every
`(cached policy, object_filters path)` pair in that cache, and executes `brfs` for each one on its
own schedule — see "Policy-driven backup execution" below.
```

with:

```
Node-level agent that reconciles local state against a small, config-driven set of policies.
It runs three embedded, statically-configured policies — `bootstrap-refresh`, `operating-refresh`,
and `policy-update` — the first two keep this node's two-tier mTLS credential (see
[Security Model](../SECURITY.md)) fresh via `certclient`; the third fetches this node's applicable
policies (backup and storage) from `policy-server` (see [policy-server](./policy-server.md)) into a
local cache via `policyclient`. On top of those three, `agent` derives two kinds of dynamic work
from that cache: a **backup task** for every `(cached policy, object_filters path)` pair, executed
via `brfs` on its own schedule (see "Policy-driven backup execution" below), and a supervised
`bwfs server` process for every cached `"storage"`-typed policy, kept running rather than scheduled
(see "Storage-policy supervision" below).
```

- [ ] **Step 7: Commit**

```bash
cd src && go test ./cmd/agent/...
git add src/cmd/agent/main.go docs/components/agent.md
git commit -m "$(cat <<'EOF'
feat(agent): wire storage supervision into main.go

agent serve now resolves bwfs, constructs a storageManager, and
passes storage tasks through to run()'s reconcile loop; agent
list-policies shows supervised storage tasks alongside everything
else. Documents the new behavior in docs/components/agent.md.
EOF
)"
```

---

## Task 11: `bwfs` graceful shutdown fix, plus `ARCHITECTURE.md`/`CHANGELOG.md`

**Files:**
- Modify: `src/cmd/bwfs/main.go`
- Modify: `docs/components/bwfs.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- No new Go interfaces — this task fixes `bwfs`'s missing `signal.NotifyContext` wiring (every other gRPC server in this repo already has it) and wraps up the repo-wide documentation this whole plan's CLAUDE.md-required doc trail still owes.

- [ ] **Step 1: Add the signal-aware context to `bwfs/main.go`'s `server` case**

In `src/cmd/bwfs/main.go`, add `"os/signal"` and `"syscall"` to the import block:

```go
import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"google.golang.org/grpc"
)
```

Replace the start of the `"server"` case:

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
```

with:

```go
	case "server":
		logger.Info("Backup writer started",
			"StoragePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)

		// Every other gRPC server in this repo wires signal.NotifyContext
		// before starting -- bwfs was the one outlier, meaning
		// common/connection/server.go's existing GracefulStop() path (on
		// <-ctx.Done()) was dead code here: a SIGTERM killed bwfs
		// immediately, hard-terminating any in-flight BackupService/
		// RestoreService stream instead of letting it finish. This matters
		// now specifically because agent (see docs/components/agent.md's
		// "Storage-policy supervision") routinely sends bwfs SIGTERM --
		// on its own shutdown, and whenever a storage policy is edited or
		// removed.
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		backupServer, err := NewBackupServer(signalCtx, logger, arguments.StoragePath)
		if err != nil {
			logger.Error("Server initialization failed", "error", err)
			os.Exit(1)
		}
		defer backupServer.store.Close()
```

Then replace the remaining two `ctx` references within this same `case "server":` block with `signalCtx`:

```go
		go watchStaleJobs(ctx, backupServer, time.Duration(conf.JobTimeoutSec)*time.Second)
```

becomes:

```go
		go watchStaleJobs(signalCtx, backupServer, time.Duration(conf.JobTimeoutSec)*time.Second)
```

and:

```go
		if err := connection.StartServer(ctx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
```

becomes:

```go
		if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
```

(The `"list"` case and everything before the `switch` are unaffected — they don't use `ctx` for anything cancellation-sensitive.)

- [ ] **Step 2: Build and run bwfs's existing tests**

Run: `cd src && go build ./cmd/bwfs/... && go test ./cmd/bwfs/...`
Expected: PASS — this is a pure wiring fix, no test in this repo exercises signal delivery directly (same as every sibling server that already has this wiring), so no new test is expected or required here; confirm by grepping for an existing precedent: `grep -rn "signal.NotifyContext" src/cmd/*/main.go` should now include `bwfs` alongside every other server.

- [ ] **Step 3: Manual smoke test of the graceful shutdown**

Run:
```bash
cd src && go build -o /tmp/bwfs-smoke ./cmd/bwfs
mkdir -p /tmp/bwfs-smoke-store
```

This requires real certs to actually start listening, so a full manual RPC-level test isn't practical here without the demo lab; instead, confirm the wiring is correct by inspection: `grep -n "signalCtx" src/cmd/bwfs/main.go` should show `signalCtx` used for `NewBackupServer`, `watchStaleJobs`, and `connection.StartServer` — the three places `ctx` was previously used inside the `"server"` case.

- [ ] **Step 4: Update `docs/components/bwfs.md`**

In the `### server` section, after the paragraph ending "...before accepting connections", add:

```markdown
On `SIGTERM`/`SIGINT`, `bwfs` now shuts down gracefully: `grpc.Server.GracefulStop()` lets any
in-flight `BackupService`/`ListService`/`RestoreService` call finish before the process exits,
rather than killing it mid-stream — the same behavior every other gRPC server in this repo already
had. This matters for [agent](./agent.md#storage-policy-supervision), which supervises a `bwfs
server` process per storage policy targeting this node and routinely sends it `SIGTERM` (on its own
shutdown, or when a storage policy is edited/removed).
```

- [ ] **Step 5: Update `docs/ARCHITECTURE.md`**

Replace the sentence in the `agent` paragraph:

```
`agent` also derives a dynamic
backup task per cached policy's object filter (a path plus optional include/exclude glob patterns,
passed straight through to `brfs`) and executes `brfs` for each one on a schedule gated by
that policy's `backup_window` and `rpo` — see [agent](components/agent.md#policy-driven-backup-execution).
Each policy's (and backup task's) outcome is tracked in the same local cache (`agent list-policies`
inspects it). See [agent](components/agent.md).
```

with:

```
`agent` also derives a dynamic
backup task per cached policy's object filter (a path plus optional include/exclude glob patterns,
passed straight through to `brfs`) and executes `brfs` for each one on a schedule gated by
that policy's `backup_window` and `rpo` — see [agent](components/agent.md#policy-driven-backup-execution).
`agent` additionally supervises a `bwfs server` process for every cached `"storage"`-typed policy
targeting this node (ensure-running, not scheduled — see
[agent](components/agent.md#storage-policy-supervision)), the first actual consumer of the
`"storage"` policy type. Each policy's (and backup task's, and storage task's) outcome is tracked in
the same local cache (`agent list-policies` inspects it). See [agent](components/agent.md).
```

In the "Backup Machine" mermaid subgraph, add `agent` supervising `bwfs`. Replace:

```
    subgraph "Backup Machine"
        bwfs[bwfs<br/>Backup Writer]
        BackupFS[Backup Filesystem]
        DB[(SQLite Database)]
        catalogsync[catalogsync<br/>Catalog Replicator]
    end
```

with:

```
    subgraph "Backup Machine"
        bwfs[bwfs<br/>Backup Writer]
        BackupFS[Backup Filesystem]
        DB[(SQLite Database)]
        catalogsync[catalogsync<br/>Catalog Replicator]
        bwfsAgent[agent<br/>Node Agent]
    end
```

Replace:

```
    %% Backup Flow
    SrcFS -->|reads files| brfs
    brfs -->|backup protocol<br/>network/unix socket, mTLS| bwfs
    bwfs -->|stores chunks| BackupFS
    bwfs -->|stores metadata| DB
```

with:

```
    %% Backup Flow
    SrcFS -->|reads files| brfs
    brfs -->|backup protocol<br/>network/unix socket, mTLS| bwfs
    bwfs -->|stores chunks| BackupFS
    bwfs -->|stores metadata| DB

    %% Storage-policy supervision -- agent ensures bwfs is running (not a
    %% scheduled job, unlike agent's backup tasks above)
    bwfsAgent -.->|supervises: start/crash-restart/stop| bwfs
```

Replace the final `class` lines:

```
    class SrcFS,BackupFS,DstFS filesystem
    class brfs,bwfs,catalogsync,Catalog component
    class rwfs component
    class DB database
```

with:

```
    class SrcFS,BackupFS,DstFS filesystem
    class brfs,bwfs,catalogsync,Catalog,bwfsAgent component
    class rwfs component
    class DB database
```

- [ ] **Step 6: Add the `CHANGELOG.md` entry**

Add a new dated section at the top of `CHANGELOG.md`, immediately after the `# Changelog` header and its intro line, above the existing `## 2026-07-28 — policy-server: add storage policy type` entry:

```markdown
## 2026-07-28 — agent: supervise bwfs for storage policies

`agent` is now the first consumer of the `"storage"` policy type added earlier today: every
reconcile tick it derives an ensure-running task (not a scheduled job) for each cached storage
policy targeting this node, and starts/crash-restarts/stops a `bwfs server` process accordingly —
`agent list-policies` shows each one alongside the three static policies and backup tasks.

**Breaking change:** `StoragePolicy.Hostname` (`policy-server`) is removed. Targeting which node
runs a storage policy is now `client_filters` — the same mechanism a backup policy already uses —
not a separate field; the corresponding proto field numbers are retired (`reserved`), not reused.

Also fixes `bwfs`, the one gRPC server in this repo that never wired `signal.NotifyContext`: a
`SIGTERM` previously killed it immediately instead of triggering the existing `GracefulStop()`
path, which matters now that `agent` routinely sends it one.
```

- [ ] **Step 7: Final full build and test run**

Run: `cd src && go build ./... && go test ./...`
Expected: PASS across the entire module — this is the last task, so this is the final confirmation the whole plan's changes compile and pass together.

- [ ] **Step 8: Commit**

```bash
cd src && go build ./... && go test ./...
git add src/cmd/bwfs/main.go docs/components/bwfs.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
fix(bwfs): wire signal.NotifyContext for graceful shutdown

bwfs was the one gRPC server in this repo missing this -- SIGTERM
killed it immediately instead of triggering the existing
GracefulStop() path in common/connection/server.go. Matters now that
agent routinely sends bwfs SIGTERM (storage-policy supervision, see
docs/components/agent.md). Also updates ARCHITECTURE.md and
CHANGELOG.md for the whole storage-supervision feature.
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** every section of `docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md` maps to a task — `policy-server` Hostname removal (Task 1), web UI spec update (already done in a prior commit — and, since that spec's implementation turned out to already exist unmerged and has since been merged, the corresponding real-code cleanup is Task 2 `api-server` + Task 3 `web`), `policyclient` cache schema (Task 4), `storage.go`'s task derivation (Task 5), supervisor (Task 6), manager (Task 7), `reconcile.go` integration (Task 8), `list-policies` visibility (Task 9), `main.go` wiring + agent docs (Task 10), `bwfs` fix + remaining docs (Task 11).
- **Placeholder scan:** no TBD/TODO; every step shows the actual code, not a description of it.
- **Type consistency:** `storageTask{ID string, Args []string}` (Task 5) is used identically by `storageSupervisor`'s constructor (Task 6, takes `args []string` positionally from `Args`), `storageManager.reconcile` (Task 7, keys its maps by `.ID`, compares `.Args` via `slices.Equal`), `run()`'s `storageTasksFunc func() ([]storageTask, bool)` (Task 8), and `renderPolicies`'s `storageTasks []storageTask` parameter (Task 9) — same field names and types throughout. `onOutcome func(err error)` (Task 6) matches exactly how Task 7 constructs it (`func(err error) { rs.recordOutcome(id, err, time.Now()) }`) and how Task 6's own tests exercise it.
