# Restore Policy Type Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a third `policy-server` policy type, `"restore"`, plus a `POST /api/v1/restore` `api-server` endpoint, so a restore directive can be routed to a specific mesh node the same way backup/storage policies already are.

**Architecture:** `RestorePolicy` is a new Go type implementing the existing `Policy` interface (`src/cmd/policy-server`), registered in the existing type registry — no changes to directory-walking, hot-reload, or generic RPC-handling code. It carries `client_filters` (targets the executing node), `source_store` (new field, `host:port` of the source `bwfs`), and reuses the existing `config` field (currently storage-only) for its opaque restore spec. It has no `rpo`/`backup_window` and is never updatable via `UpdatePolicy`. `api-server` gets one new creation-only endpoint, `POST /api/v1/restore`.

**Tech Stack:** Go, gRPC/protobuf (`protoc` via `make proto`), `testify` (`assert`/`require`), Go's stdlib `net/http`/`httptest`.

## Global Constraints

- Proto field numbers: `Policy.source_store = 18` (next after `destinations = 17`); `CreatePolicyRequest.source_store = 13` (next after `storage_policy_id = 12`). `UpdatePolicyRequest` gets **no** `source_store` field — restore policies are not updatable.
- `config` is reused as-is for restore's spec (not a new field) — exact same field, same well-formed-JSON-only validation, that `StoragePolicy` already uses.
- No `rpo`, `backup_window`, `object_filters`, `storage_policy_id`, or `port` on a restore policy — a `CreatePolicyRequest{Type: "restore"}` that sets any of these (or `port`) is rejected with `INVALID_ARGUMENT`.
- `source_store` must be a non-empty, syntactically valid `host:port` (`net.SplitHostPort`), checked in `RestorePolicy.Validate()`.
- Every doc/CHANGELOG update follows this project's `.claude/CLAUDE.md` documentation rules (see each task).
- Match existing code style exactly: doc comments in the same voice as neighboring code, `testify`'s `assert`/`require`, table-free plain Go tests (this codebase doesn't use table-driven tests for these files — mirror the one-test-per-function style already in `write_test.go`/`storage_policy_test.go`/`policies_test.go`).

---

### Task 1: Proto — add `source_store`, document non-updatability, regenerate

**Files:**
- Modify: `src/api/policyserver.proto`
- Modify: `docs/protocols/policy-server.md`
- Generated (via `make proto`, do not hand-edit): `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go`

**Interfaces:**
- Produces: `pb.Policy.GetSourceStore() string`, `pb.CreatePolicyRequest.GetSourceStore() string` — used by every later task.

- [ ] **Step 1: Edit `src/api/policyserver.proto`**

In `message Policy`, after the `destinations` field (currently the last field, `= 17`), add:

```proto
  // "restore" policy only. host:port of the source bwfs to restore from.
  string source_store = 18;
```

Update the `config` field's comment (currently `// storage policy only -- opaque JSON text, ...`) to:

```proto
  // "storage" and "restore" policy only -- opaque JSON text, verbatim
  // passthrough. Never parsed or interpreted by policy-server beyond
  // checking well-formedness. For "restore", this carries the restore spec
  // (file list etc.); its shape is defined by a future design.
  string config = 13;
```

In `message CreatePolicyRequest`, after `string storage_policy_id = 12;`, add:

```proto
  // "restore" policy only, required. host:port of the source bwfs to
  // restore from.
  string source_store = 13;
```

Update `CreatePolicyRequest.type`'s comment (currently `// "backup" or "storage" -- required. ...`) to:

```proto
  // "backup", "storage", or "restore" -- required. Determines which of the
  // type-specific fields are valid; mixing fields across types is rejected
  // (e.g. a "restore" request must not set object_filters/rpo/
  // backup_window/storage_policy_id/port).
  string type = 7;
```

In `message UpdatePolicyRequest`, update the existing comment block (currently starting `// A policy's type is immutable via UpdatePolicy -- there is no type field`) to:

```proto
  // A policy's type is immutable via UpdatePolicy -- there is no type field
  // here. port/config are only valid when the policy being updated is
  // already type "storage"; object_filters/rpo/backup_window/storage_policy_id
  // are only valid when it's already type "backup". "restore"-typed
  // policies cannot be updated at all -- UpdatePolicy rejects any request
  // whose target policy is type "restore" regardless of which fields it
  // sets, which is why there is no source_store field here.
```

In `message ListPoliciesRequest`, update the `type` field's comment from `// Optional. "backup" or "storage" -- ...` to:

```proto
  // Optional. "backup", "storage", or "restore" -- when set, only policies
  // of this type are returned. Empty returns every type (unfiltered,
  // today's behavior).
  string type = 1;
```

- [ ] **Step 2: Regenerate and verify it compiles**

Run:

```bash
make proto
cd src && go build ./... && cd -
```

Expected: both commands succeed; `git diff src/api/policyserver.pb.go` shows a new `SourceStore` field/getter on `Policy` and `CreatePolicyRequest`, and no changes to `UpdatePolicyRequest`.

- [ ] **Step 3: Update `docs/protocols/policy-server.md`**

Update the fenced `message Policy { ... }` block to match the new proto exactly (add `source_store = 18` with its comment, update `config`'s comment). Do the same for the `message CreatePolicyRequest { ... }` block (add `source_store = 13`). Update the `message ListPoliciesRequest { ... }` block's `type` comment the same way as the proto.

In the `## Behavior` section, add a new bullet after the existing `port`/`config` bullet (the one starting `` - `port`/`config` are only meaningful on a `"storage"`-typed policy ... ``):

```markdown
- A `"restore"`-typed policy carries `client_filters` (targets the node that will execute the
  restore), `source_store` (required, a syntactically valid `host:port` naming the source `bwfs`),
  and `config` (required, well-formed JSON -- the restore spec, format defined by a future design;
  the same field a `"storage"` policy uses, not a separate one). It has no `object_filters`, `rpo`,
  `backup_window`, `storage_policy_id`, or `port` -- a `CreatePolicyRequest` of this type setting any
  of those is rejected with `INVALID_ARGUMENT`. Unlike every other type, a `"restore"` policy is
  **never updatable**: `UpdatePolicyRequest` has no `source_store` field, and `UpdatePolicy` rejects
  any request whose target policy is type `"restore"` with `INVALID_ARGUMENT`, regardless of which
  fields the request sets. See
  [Design: Restore Policy Type](../superpowers/specs/2026-08-09-restore-policy-type-design.md).
```

- [ ] **Step 4: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go docs/protocols/policy-server.md
git commit -m "feat: add source_store field for restore policy type to policyserver.proto"
```

---

### Task 2: `RestorePolicy` Go type

**Files:**
- Create: `src/cmd/policy-server/restore_policy.go`
- Create: `src/cmd/policy-server/restore_policy_test.go`
- Modify: `src/cmd/policy-server/policy.go:110-113` (`policyParsers` map)

**Interfaces:**
- Consumes: `Policy` interface, `PolicyBase`, `validateCommon`, `toProtoClientFilters` (all in `policy.go`/`server.go`, unchanged); `pb.Policy` (Task 1).
- Produces: `RestorePolicy` struct, `parseRestorePolicyJSON(data []byte) (Policy, error)` — consumed by Task 3's `buildPolicyForCreate` and by `policyParsers["restore"]`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/policy-server/restore_policy_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicyFile_RestorePolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "web01-emergency.json", `{
		"metadata": {"name": "web01-emergency"},
		"client_filters": {"hostnames": ["web-01"]},
		"source_store": "bwfs-east.internal:8080",
		"config": {"files": ["/var/www/index.html"]}
	}`)

	got, err := parsePolicyFile(path, "restore")
	require.NoError(t, err)
	p, ok := got.(*RestorePolicy)
	require.True(t, ok)
	assert.Equal(t, "web01-emergency", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, []string{"web-01"}, p.ClientFilters.Hostnames)
	assert.Equal(t, "bwfs-east.internal:8080", p.SourceStore)
	assert.JSONEq(t, `{"files": ["/var/www/index.html"]}`, string(p.Config))
	assert.Equal(t, "restore", p.Kind())
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_RestoreAndBackupSameBasenameYieldDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	pathBackup := writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}, "storage_policy_id": "sp-1"}`)
	pathRestore := writePolicyFile(t, filepath.Join(dir, "restore"), "nightly.json", `{
		"metadata": {"name": "nightly"}, "source_store": "bwfs:8080", "config": {}
	}`)

	pBackup, err := parsePolicyFile(pathBackup, "backup")
	require.NoError(t, err)
	pRestore, err := parsePolicyFile(pathRestore, "restore")
	require.NoError(t, err)

	assert.NotEqual(t, pBackup.Meta().ID, pRestore.Meta().ID, "same basename in different type subfolders must not collide")
}

func TestRestorePolicy_ValidateValidPolicyReturnsNil(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "ok"}},
		SourceStore: "bwfs:8080",
		Config:      []byte(`{"files": []}`),
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ValidateMissingNameFails(t *testing.T) {
	p := &RestorePolicy{SourceStore: "bwfs:8080", Config: []byte(`{}`)}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateEmptySourceStoreFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Config:     []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateSourceStoreMissingPortFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "x"}},
		SourceStore: "bwfs-no-port",
		Config:      []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateEmptyConfigFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "x"}},
		SourceStore: "bwfs:8080",
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateMalformedConfigJSONFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "x"}},
		SourceStore: "bwfs:8080",
		Config:      []byte(`not json`),
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_CloneDeepCopiesConfig(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:  PolicyBase{Metadata: Metadata{Name: "x"}},
		SourceStore: "bwfs:8080",
		Config:      []byte(`{"a":1}`),
	}
	cloned := p.Clone().(*RestorePolicy)
	cloned.Config[2] = 'X'
	assert.Equal(t, `{"a":1}`, string(p.Config), "mutating the clone's Config must not affect the original")
}

func TestRestorePolicy_ToProtoSetsTypeSpecificFields(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{ID: "r1", Name: "web01-emergency"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-01"}},
			Type:          "restore",
		},
		SourceStore: "bwfs:8080",
		Config:      []byte(`{"files":[]}`),
	}

	pp := p.ToProto(true)

	assert.Equal(t, "r1", pp.GetId())
	assert.Equal(t, "restore", pp.GetType())
	assert.Equal(t, "bwfs:8080", pp.GetSourceStore())
	assert.JSONEq(t, `{"files":[]}`, pp.GetConfig())
	assert.Equal(t, []string{"web-01"}, pp.GetClientFilters().GetHostnames())
}

func TestRestorePolicy_ToProtoOmitsClientFiltersWhenNotRequested(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{ID: "r1", Name: "x"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-01"}},
		},
		SourceStore: "bwfs:8080",
		Config:      []byte(`{}`),
	}

	pp := p.ToProto(false)

	assert.Nil(t, pp.GetClientFilters())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run 'RestorePolicy|Restore' -v`
Expected: FAIL to compile — `RestorePolicy`, `parseRestorePolicyJSON` undefined, `GetSourceStore` undefined on `*pb.Policy` (only if Task 1 wasn't run first; if Task 1 is already done, `GetSourceStore` exists and only `RestorePolicy` is undefined).

- [ ] **Step 3: Create `src/cmd/policy-server/restore_policy.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"net"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RestorePolicy is the "restore" policy type: a one-shot directive telling a
// specific mesh node (via PolicyBase's ClientFilters, the same targeting
// mechanism BackupPolicy/StoragePolicy already use) to restore files from a
// source bwfs. Unlike BackupPolicy/StoragePolicy it has no recurring-
// schedule concept (no rpo/backup_window) -- it's meant to be picked up
// once by a future agent-side consumer (not yet built), and is never
// updatable via UpdatePolicy (see buildPolicyForUpdate in write.go). It
// reuses Config, the same field StoragePolicy already carries, for its
// restore spec rather than introducing a second opaque-JSON field -- same
// load-time-well-formed-only semantics, contents interpreted by neither
// type. See docs/superpowers/specs/2026-08-09-restore-policy-type-design.md.
type RestorePolicy struct {
	PolicyBase
	// host:port of the source bwfs to restore from.
	SourceStore string `json:"source_store"`
	// Opaque JSON text describing what to restore (file list etc.) -- format
	// left for a future design. policy-server never interprets it beyond
	// checking well-formedness, the same way StoragePolicy.Config is opaque.
	Config json.RawMessage `json:"config"`
}

func parseRestorePolicyJSON(data []byte) (Policy, error) {
	var p RestorePolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the fields an operator can set on a restore policy,
// independent of where it came from (a file on disk or a CreatePolicy RPC
// request): the fields validateCommon checks (including client_filters,
// which is how a restore policy targets the node that executes it),
// source_store must be a non-empty, syntactically valid "host:port", and
// config must be non-empty, well-formed JSON -- its contents are never
// interpreted further.
func (p *RestorePolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(p.SourceStore); err != nil {
		return fmt.Errorf("source_store must be a valid host:port: %w", err)
	}
	if len(p.Config) == 0 {
		return fmt.Errorf("config is required")
	}
	if !json.Valid(p.Config) {
		return fmt.Errorf("config must be well-formed JSON")
	}
	return nil
}

// Clone deep-copies every reference-typed field so mutating the returned
// value never affects the cached original.
func (p *RestorePolicy) Clone() Policy {
	config := make(json.RawMessage, len(p.Config))
	copy(config, p.Config)
	return &RestorePolicy{
		PolicyBase:  p.PolicyBase.clone(),
		SourceStore: p.SourceStore,
		Config:      config,
	}
}

// ToProto converts to the wire representation GetPolicies/ListPolicies/
// CreatePolicy return (never UpdatePolicy -- restore policies are not
// updatable). client_filters is only populated when includeClientFilters is
// true, matching BackupPolicy.ToProto/StoragePolicy.ToProto.
func (p *RestorePolicy) ToProto(includeClientFilters bool) *pb.Policy {
	pp := &pb.Policy{
		Id:          p.Metadata.ID,
		Name:        p.Metadata.Name,
		CreatedAt:   timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:   timestamppb.New(p.Metadata.UpdatedAt),
		Type:        p.Type,
		SourceStore: p.SourceStore,
		Config:      string(p.Config),
	}
	if !p.Metadata.DisabledAt.IsZero() {
		pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
```

- [ ] **Step 4: Register the new type in `policy.go`**

In `src/cmd/policy-server/policy.go`, change:

```go
var policyParsers = map[string]func(data []byte) (Policy, error){
	"backup":  parseBackupPolicyJSON,
	"storage": parseStoragePolicyJSON,
}
```

to:

```go
var policyParsers = map[string]func(data []byte) (Policy, error){
	"backup":  parseBackupPolicyJSON,
	"storage": parseStoragePolicyJSON,
	"restore": parseRestorePolicyJSON,
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run 'RestorePolicy|Restore' -v`
Expected: PASS, all new tests green.

- [ ] **Step 6: Run the full package test suite to check for regressions**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS (existing backup/storage tests unaffected).

- [ ] **Step 7: Commit**

```bash
git add src/cmd/policy-server/restore_policy.go src/cmd/policy-server/restore_policy_test.go src/cmd/policy-server/policy.go
git commit -m "feat: add RestorePolicy type to policy-server"
```

---

### Task 3: `CreatePolicy` support for `"restore"`

**Files:**
- Modify: `src/cmd/policy-server/write.go` (`buildPolicyForCreate`, and `buildPolicy`'s doc comment)
- Modify: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: `RestorePolicy`, `parseRestorePolicyJSON` (Task 2); `backupFieldsSet` (`write.go`, unchanged, existing helper).
- Produces: `CreatePolicy` now accepts `Type: "restore"` — consumed by Task 5's `api-server` handler.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/write_test.go`:

```go
func TestCreatePolicy_RestorePolicyWritesIntoRestoreDir(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "Web01 Emergency Restore",
		Type:          "restore",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-01"}},
		SourceStore:   "bwfs-east.internal:8080",
		Config:        `{"files": ["/var/www/index.html"]}`,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "restore", resp.Type)
	assert.Equal(t, "bwfs-east.internal:8080", resp.SourceStore)
	assert.JSONEq(t, `{"files": ["/var/www/index.html"]}`, resp.Config)

	_, err = os.Stat(filepath.Join(dir, "restore", "web01-emergency-restore.json"))
	require.NoError(t, err)
}

func TestCreatePolicy_ResponseIncludesRestoreType(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "quick-restore", Type: "restore", SourceStore: "bwfs:8080", Config: `{}`,
	})

	require.NoError(t, err)
	assert.Equal(t, "restore", resp.Type)
}

func TestCreatePolicy_RestoreTypeWithBackupFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "bad",
		Type:            "restore",
		SourceStore:     "bwfs:8080",
		Config:          `{}`,
		StoragePolicyId: "sp-1",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_RestoreTypeWithPortRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:        "bad",
		Type:        "restore",
		SourceStore: "bwfs:8080",
		Config:      `{}`,
		Port:        9400,
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_RestoreMissingSourceStoreReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "no-source", Type: "restore", Config: `{}`,
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_RestoreInvalidSourceStoreFormatReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "bad-source", Type: "restore", SourceStore: "no-port-here", Config: `{}`,
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestCreatePolicy_Restore -v`
Expected: FAIL — `TestCreatePolicy_RestorePolicyWritesIntoRestoreDir` etc. fail because `CreatePolicy` still routes `Type: "restore"` into `buildPolicy`'s `default` case (`"unknown policy type"`), so every one currently returns `InvalidArgument` even for the valid-input tests (which expect `NoError`).

- [ ] **Step 3: Implement — edit `buildPolicyForCreate` in `write.go`**

Change:

```go
func buildPolicyForCreate(req *pb.CreatePolicyRequest, now time.Time) (Policy, error) {
	base := PolicyBase{
		Metadata: Metadata{
			Name:       req.GetName(),
			CreatedAt:  now,
			UpdatedAt:  now,
			DisabledAt: disabledAtFromProto(req.GetDisabledAt()),
		},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
	}
	return buildPolicy(req.GetType(), base, req)
}
```

to:

```go
func buildPolicyForCreate(req *pb.CreatePolicyRequest, now time.Time) (Policy, error) {
	base := PolicyBase{
		Metadata: Metadata{
			Name:       req.GetName(),
			CreatedAt:  now,
			UpdatedAt:  now,
			DisabledAt: disabledAtFromProto(req.GetDisabledAt()),
		},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
	}
	if req.GetType() == "restore" {
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetStoragePolicyId()) || req.GetPort() != 0 {
			return nil, fmt.Errorf("a restore policy must not set object_filters/rpo/backup_window/storage_policy_id/port")
		}
		return &RestorePolicy{
			PolicyBase:  base,
			SourceStore: req.GetSourceStore(),
			Config:      json.RawMessage(req.GetConfig()),
		}, nil
	}
	return buildPolicy(req.GetType(), base, req)
}
```

Update `buildPolicy`'s doc comment (directly above `func buildPolicy(kind string, base PolicyBase, req policyFieldsGetter) (Policy, error) {`) from:

```go
// buildPolicy constructs the concrete Policy kind asks for out of base and
// req's type-specific fields, rejecting a request that also sets fields
// belonging to the other type. Shared by buildPolicyForCreate (kind ==
// req.GetType()) and buildPolicyForUpdate (kind == existing.Kind(), since a
// policy's type is immutable via update).
```

to:

```go
// buildPolicy constructs the concrete "backup" or "storage" Policy kind
// asks for out of base and req's type-specific fields, rejecting a request
// that also sets fields belonging to the other type. Shared by
// buildPolicyForCreate (kind == req.GetType()) and buildPolicyForUpdate
// (kind == existing.Kind(), since a policy's type is immutable via update).
// "restore" is handled separately in buildPolicyForCreate, not routed
// through here or through policyFieldsGetter -- it's create-only (see
// buildPolicyForUpdate) and UpdatePolicyRequest has no source_store field
// for policyFieldsGetter to expose.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestCreatePolicy_Restore|Restore' -v`
Expected: PASS.

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go
git commit -m "feat: CreatePolicy supports the restore policy type"
```

---

### Task 4: `UpdatePolicy` rejects restore-typed policies; finish `policy-server` docs

**Files:**
- Modify: `src/cmd/policy-server/write.go` (`buildPolicyForUpdate`)
- Modify: `src/cmd/policy-server/write_test.go`
- Modify: `docs/components/policy-server.md`

**Interfaces:**
- Consumes: `RestorePolicy` (Task 2), `CreatePolicy` accepting `Type: "restore"` (Task 3).
- Produces: `UpdatePolicy` now returns `INVALID_ARGUMENT` for any existing policy whose `Kind() == "restore"` — this is what makes `api-server`'s generic `PUT /api/v1/policies/{id}` reject restore policies (Task 5 relies on this; no new `api-server` code needed for that rejection).

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/policy-server/write_test.go`:

```go
func TestUpdatePolicy_RestoreTypeRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	created, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "quick-restore", Type: "restore", SourceStore: "bwfs:8080", Config: `{}`,
	})
	require.NoError(t, err)

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:   created.Id,
		Name: "renamed",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())

	data, readErr := os.ReadFile(filepath.Join(dir, "restore", "quick-restore.json"))
	require.NoError(t, readErr)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.Equal(t, "quick-restore", onDisk["metadata"].(map[string]any)["name"], "the file must be left untouched when the update is rejected")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/policy-server/... -run TestUpdatePolicy_RestoreTypeRejected -v`
Expected: FAIL — today, `buildPolicyForUpdate` falls into its `kind != "backup" && kind != "storage"` branch for `kind == "restore"` too, which *does* already return an error... but only incidentally, with the misleading message `existing policy has unknown type "restore"`. Run it first to confirm current behavior (it may already pass on the `InvalidArgument` assertion alone) — the point of this task is the clearer, intentional rejection path and message, not a change in status code. If the test already passes as written, proceed straight to Step 3 anyway so the rejection is explicit and correctly documented rather than an accident of the fallback branch.

- [ ] **Step 3: Implement — edit `buildPolicyForUpdate` in `write.go`**

Change:

```go
func buildPolicyForUpdate(req *pb.UpdatePolicyRequest, kind string, existingMeta Metadata, now time.Time) (Policy, error) {
	if kind != "backup" && kind != "storage" {
		return nil, fmt.Errorf("existing policy has unknown type %q", kind)
	}
```

to:

```go
func buildPolicyForUpdate(req *pb.UpdatePolicyRequest, kind string, existingMeta Metadata, now time.Time) (Policy, error) {
	if kind == "restore" {
		return nil, fmt.Errorf("restore policies cannot be updated")
	}
	if kind != "backup" && kind != "storage" {
		return nil, fmt.Errorf("existing policy has unknown type %q", kind)
	}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd src && go test ./cmd/policy-server/... -run TestUpdatePolicy_RestoreTypeRejected -v`
Expected: PASS.

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS.

- [ ] **Step 6: Update `docs/components/policy-server.md`**

In the `### Policy types and directory layout` section, after the paragraph beginning `` A `"storage"` policy describes how a future storage server should be configured: ... `` (ends `...which is the first actual consumer of \`storage\`-typed policies. See [Design: link backup policies to storage policies by id](...)`), add a new paragraph:

```markdown
A `"restore"` policy is a one-shot directive: `client_filters` targets the node that will execute
the restore, `source_store` (required, a syntactically valid `host:port`) names the source `bwfs`
to restore from, and `config` (required, well-formed JSON, the same field a `"storage"` policy
carries -- not a separate one) holds the restore spec, whose shape is defined by a future design.
It has no `object_filters`, `rpo`, `backup_window`, `storage_policy_id`, or `port`. Unlike every
other type, a `"restore"` policy is never updatable -- `UpdatePolicy` rejects any request targeting
one with `INVALID_ARGUMENT`, regardless of which fields the request sets, so `api-server`'s generic
`PUT /api/v1/policies/{id}` rejects it too, with no `api-server`-side special-casing needed.
`policy-server` only defines this type's schema and validation; a future design covers `agent`
actually picking up `"restore"`-typed policies and executing a restore, the same way "Storage
Policy Type" and "agent storage-policy supervision" were split into separate specs. See
[Design: Restore Policy Type](../superpowers/specs/2026-08-09-restore-policy-type-design.md).
```

- [ ] **Step 7: Commit**

```bash
git add src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go docs/components/policy-server.md
git commit -m "fix: UpdatePolicy explicitly rejects restore-typed policies"
```

---

### Task 5: `api-server` — `POST /api/v1/restore`, DTO, docs

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/policies_test.go`
- Modify: `docs/components/api-server.md`
- Modify: `docs/api/rest-v1.md`

**Interfaces:**
- Consumes: `pb.CreatePolicyRequest.SourceStore`, `pb.Policy.GetSourceStore()` (Task 1); `CreatePolicy` accepting `Type: "restore"` (Task 3); `UpdatePolicy` rejecting `"restore"`-typed policies (Task 4, exercised via the existing generic `PUT /api/v1/policies/{id}` route — no new code).
- Produces: `POST /api/v1/restore` route, `s.handleCreateRestore`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/policies_test.go`:

```go
func TestToPolicyDTO_IncludesSourceStoreForRestore(t *testing.T) {
	p := &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		SourceStore: "bwfs-east.internal:8080",
		Config:      `{"files": ["/var/www/index.html"]}`,
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, "bwfs-east.internal:8080", dto.SourceStore)
	assert.Equal(t, `{"files": ["/var/www/index.html"]}`, dto.Config)
}

func TestHandleCreateRestore_ReturnsCreatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		SourceStore: "bwfs-east.internal:8080",
		Config:      `{"files": ["/var/www/index.html"]}`,
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-01"}, Labels: map[string]string{}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web01-emergency",
		"client_filters": {"hostnames": ["web-01"], "labels": {}},
		"source_store": "bwfs-east.internal:8080",
		"config": "{\"files\": [\"/var/www/index.html\"]}"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "restore", fake.lastCreateReq.GetType())
	assert.Equal(t, "web01-emergency", fake.lastCreateReq.GetName())
	assert.Equal(t, []string{"web-01"}, fake.lastCreateReq.GetClientFilters().GetHostnames())
	assert.Equal(t, "bwfs-east.internal:8080", fake.lastCreateReq.GetSourceStore())
	assert.Equal(t, `{"files": ["/var/www/index.html"]}`, fake.lastCreateReq.GetConfig())

	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "bwfs-east.internal:8080", respBody["source_store"])
}

func TestHandleCreateRestore_MalformedJSONReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastCreateReq, "backend must not be called on malformed input")
}

func TestHandleCreateRestore_BackendValidationErrorReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{createErr: status.Error(codes.InvalidArgument, "source_store must be a valid host:port")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", strings.NewReader(`{"name": "x", "source_store": "bad"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdatePolicy_RestoreTypeRejectedReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{updateErr: status.Error(codes.InvalidArgument, "restore policies cannot be updated")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/r1", strings.NewReader(`{"name": "renamed"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'Restore' -v`
Expected: FAIL to compile — `s.handleCreateRestore` undefined, route `POST /api/v1/restore` not registered (404 instead of the expected status), `dto.SourceStore` undefined.

- [ ] **Step 3: Implement — `policies.go` DTO field**

In `policyDTO` (`src/cmd/api-server/policies.go`), add a field after `StoragePolicyID`:

```go
type policyDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
	ClientFilters   clientFiltersDTO  `json:"client_filters"`
	ObjectFilters   []objectFilterDTO `json:"object_filters"`
	RPO             string            `json:"rpo"`
	BackupWindow    []string          `json:"backup_window"`
	Destinations    []string          `json:"destinations"`
	StoragePolicyID string            `json:"storage_policy_id,omitempty"`
	Type            string            `json:"type"`
	Port            int32             `json:"port"`
	Config          string            `json:"config"`
	SourceStore     string            `json:"source_store,omitempty"`
	DisabledAt      int64             `json:"disabled_at,omitempty"`
	Checkins        []checkinDTO      `json:"checkins"`
}
```

In `toPolicyDTO`, add `SourceStore: p.GetSourceStore(),` to the `dto := policyDTO{...}` literal (next to `Config: p.GetConfig(),`).

- [ ] **Step 4: Implement — `handleCreateRestore` in `policies.go`**

Add, after `handleUpdateStoragePolicy` and before `handleDeletePolicy`:

```go
type restorePolicyInput struct {
	Name          string           `json:"name"`
	ClientFilters clientFiltersDTO `json:"client_filters"`
	SourceStore   string           `json:"source_store"`
	Config        string           `json:"config"`
	DisabledAt    int64            `json:"disabled_at,omitempty"`
}

func decodeRestorePolicyInput(r *http.Request) (restorePolicyInput, error) {
	var in restorePolicyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return restorePolicyInput{}, err
	}
	return in, nil
}

// handleCreateRestore is the sole creation path for "restore"-typed
// policies: POST /api/v1/restore, not POST/PUT /api/v1/restore-policies --
// a restore policy is launched, not managed as a long-lived resource, and
// is never updatable (PUT /api/v1/policies/{id} against one is rejected by
// policy-server itself, see write.go's buildPolicyForUpdate).
func (s *server) handleCreateRestore(w http.ResponseWriter, r *http.Request) {
	in, err := decodeRestorePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		Type:          "restore",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		SourceStore:   in.SourceStore,
		Config:        in.Config,
		DisabledAt:    disabledAtToProto(in.DisabledAt),
	})
	if err != nil {
		s.logger.Error("handleCreateRestore: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}
```

- [ ] **Step 5: Implement — register the route in `server.go`**

In `registerRoutes` (`src/cmd/api-server/server.go`), add after the `storage-policies` routes:

```go
	mux.HandleFunc("POST /api/v1/storage-policies", s.handleCreateStoragePolicy)
	mux.HandleFunc("PUT /api/v1/storage-policies/{id}", s.handleUpdateStoragePolicy)
	mux.HandleFunc("POST /api/v1/restore", s.handleCreateRestore)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run 'Restore' -v`
Expected: PASS.

- [ ] **Step 7: Run the full package test suite to check for regressions**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS.

- [ ] **Step 8: Update `docs/components/api-server.md`**

In the `## Endpoints` section, after the paragraph about `POST /policies/adhoc` (ends `` ...see [Design: link backup policies to storage policies by id](...).`` ), add:

```markdown
`POST /restore-policies` doesn't exist -- restore policies have exactly one creation path,
`POST /restore` (fields: `name`/`client_filters`/`source_store`/`config`), and no update path at
all: `PUT /policies/{id}` against a `"restore"`-typed policy is rejected with `400`, enforced by
`policy-server` itself (`UpdatePolicy` refuses any request whose target policy is type `"restore"`),
not by any `api-server`-side special-casing. `GET /policies/{id}` and `DELETE /policies/{id}` remain
type-agnostic and work on restore policies like any other type. See
[Design: Restore Policy Type](../superpowers/specs/2026-08-09-restore-policy-type-design.md).
```

- [ ] **Step 9: Update `docs/api/rest-v1.md`**

After the `## PUT /api/v1/storage-policies/{id}` section (and before `## GET /api/v1/jobs`), add:

```markdown
## `POST /api/v1/restore`

Creates a new `"restore"`-typed policy -- the only way to create one; there is no
`POST /api/v1/restore-policies` and no update endpoint. Body:

\`\`\`json
{
  "name": "web01-emergency",
  "client_filters": {"hostnames": ["web-01"], "labels": {}},
  "source_store": "bwfs-east.internal:8080",
  "config": "{\"files\": [\"/var/www/index.html\"]}"
}
\`\`\`

`source_store` must be a syntactically valid `host:port` naming the source `bwfs` to restore from.
`config` is a JSON string, not a nested object -- `policy-server` treats it as opaque, pass-through
text; its shape (file list etc.) is defined by a future design, and it's the same field a storage
policy's `config` is, not a separate one. `client_filters` targets the node that will execute the
restore, the same mechanism every other policy type uses. `201` with the created policy on success.
`400` if `name` is empty, `source_store` isn't a valid `host:port`, or `config` isn't well-formed
JSON -- no file is written when validation fails.

Restore policies are never updatable: `PUT /api/v1/policies/{id}` against one returns `400`.
`GET /api/v1/policies/{id}` and `DELETE /api/v1/policies/{id}` work on them like any other type.
```

Also update the `## GET /api/v1/policies` section's example query-parameter sentence (currently
`` Accepts an optional `?type=backup` or `?type=storage` query parameter ... ``) to:

```markdown
Accepts an optional `?type=backup`, `?type=storage`, or `?type=restore` query parameter to restrict
the response to one policy type; omitted returns every type.
```

- [ ] **Step 10: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/server.go src/cmd/api-server/policies_test.go docs/components/api-server.md docs/api/rest-v1.md
git commit -m "feat: add POST /api/v1/restore endpoint to api-server"
```

---

### Task 6: Changelog

**Files:**
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing (documentation-only).
- Produces: nothing (terminal task).

- [ ] **Step 1: Add a dated entry**

At the top of `CHANGELOG.md`, immediately after the `All notable changes...` line, insert (using today's actual date if different from the one below):

```markdown
## 2026-08-09 — policy-server gains a restore policy type

`policy-server` now supports a third policy type, `"restore"`, alongside `"backup"`/`"storage"`: a
one-shot directive (`client_filters` for the executing node, `source_store` for the source `bwfs`,
and the existing `config` field reused for an opaque restore spec) with no recurring-schedule
fields and no update path -- restores are normally ad hoc, but still need to reach a specific mesh
node the same way backup/storage policies already do, since there's no direct-access channel to an
arbitrary node otherwise. `api-server` gains `POST /api/v1/restore`, the sole creation path.
Actual restore execution (`agent` picking up a restore policy and running it) is deliberately out
of scope here, left for a future design the same way storage-policy-type and agent-storage-
supervision were split.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: add changelog entry for the restore policy type"
```

---

## Self-Review Notes

- **Spec coverage:** every requirement in `docs/superpowers/specs/2026-08-09-restore-policy-type-design.md` maps to a task — schema/proto (Task 1), directory/validation/lifecycle (Task 2-3), no-update enforcement (Task 4), REST surface (Task 5), docs/changelog (Tasks 1, 4, 5, 6 respectively, per this project's CLAUDE.md rules).
- **Type/signature consistency checked:** `RestorePolicy.SourceStore` / `pb.Policy.SourceStore` / `pb.CreatePolicyRequest.SourceStore` / `restorePolicyInput.SourceStore` / `policyDTO.SourceStore` all named identically end-to-end; `Config` reused (not renamed) at every layer, matching `StoragePolicy.Config` exactly.
- **No update surface anywhere:** confirmed no `source_store` added to `UpdatePolicyRequest` (Task 1), `buildPolicyForUpdate` explicitly rejects `kind == "restore"` (Task 4), and `api-server` adds no restore-specific update handler or route (Task 5) — rejection flows entirely from `policy-server`.
