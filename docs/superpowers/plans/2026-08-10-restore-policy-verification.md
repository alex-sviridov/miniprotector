# Restore Policy Verification Execution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign the `"restore"` policy's schema (live `storage_policy_id` resolution instead of a baked-in `source_store`, small typed `rules` instead of a client-expanded `config.files` list), then wire it end-to-end so `agent` verifies a restore policy's files against the resolved source `bwfs` via a new `rwfs verify --rules-stdin` mode.

**Architecture:** `policy-server`'s `RestorePolicy` gains `storage_policy_id` (reusing the exact live-checkin resolution `BackupPolicy` already has) and `rules` (a small `{host,path,include}` list, mirroring the restore cart's own rule shape). `catalog` gains a `ListStoreFacets` RPC so the web UI can cheaply discover which stores a selection touches without enumerating every file. `agent` derives one one-shot task per cached `"restore"` policy and execs `rwfs verify <resolved store> --rules-stdin`, piping the policy's rules; `rwfs` ports the cart's longest-matching-rule resolution to Go and applies it against a broad `ListFiles` call.

**Tech Stack:** Go (policy-server, catalog, api-server, agent, rwfs, policyclient — gRPC/protobuf via `protoc`, GORM/SQLite), Vue 3 + Pinia (web), Vitest (web tests), `testify` (Go tests).

## Global Constraints

- No new gRPC message-size overrides anywhere — this design's whole point is staying well under the existing default limits, not raising them.
- `"restore"` policies remain non-updatable (`UpdatePolicy` rejects them) — unchanged from the prior design, not touched by any task here.
- Every doc file the spec's "Documentation Impact" section names must be updated in the same task as the code it documents, per this repo's `.claude/CLAUDE.md` feature/protocol-change rules — not deferred to a separate catch-all task.
- Proto field numbers already assigned to other types are never reused for something else; a removed field is `reserved`, never silently dropped.
- Follow this repo's existing test style exactly: table-driven where the existing sibling file already is, `testify`'s `assert`/`require`, fakes over mocks (see `fakeRunner` in `reconcile_test.go`, `fakePolicyServiceClient` in `policyclient`).

---

## File Structure

| File | Responsibility |
|---|---|
| `src/api/policyserver.proto` | `RestoreRule` message; `Policy`/`CreatePolicyRequest` gain `rules`; `source_store` fields reserved and removed |
| `src/api/catalog.proto` | New `ListStoreFacets` RPC on `CatalogService` |
| `src/cmd/policy-server/restore_policy.go` | `RestorePolicy` struct, `Validate`/`Clone`/`ToProto` — rewritten for `storage_policy_id`+`rules` |
| `src/cmd/policy-server/restore_policy_test.go` | Matching test rewrite |
| `src/cmd/policy-server/write.go` | Cross-type field rejection updated for `rules`/`storage_policy_id` moving between types |
| `src/cmd/policy-server/server.go` | `attachDestination` extended to resolve `destinations` for `"restore"` too |
| `src/cmd/policy-server/server_test.go` | Matching test updates |
| `src/storage/catalog/facets.go` | New `Store.ListStoreFacets` |
| `src/storage/catalog/facets_test.go` | Matching tests |
| `src/cmd/catalog/server.go` | New `catalogServer.ListStoreFacets` gRPC handler |
| `src/cmd/catalog/server_test.go` | Matching tests |
| `src/cmd/api-server/catalog.go` | New `handleListCatalogStores` (`GET /api/v1/catalog/stores`) |
| `src/cmd/api-server/server.go` | Route registration; `catalogQueryClient` interface gains `ListStoreFacets` |
| `src/cmd/api-server/catalog_test.go` | Matching tests; fake catalog client gains `ListStoreFacets` |
| `src/cmd/api-server/policies.go` | `restorePolicyInput`/`handleCreateRestore`/`policyDTO` updated for `storage_policy_id`+`rules` |
| `src/cmd/api-server/policies_test.go` | Matching test rewrite |
| `src/cmd/policyclient/fetch.go` | `CachedPolicy` gains `Rules` |
| `src/cmd/policyclient/fetch_test.go` | Matching test |
| `src/cmd/agent/reconcile.go` | `runner`/`Policy.Stdin` threading |
| `src/cmd/agent/reconcile_test.go` | `fakeRunner` signature update + new stdin-threading test |
| `src/cmd/agent/policy.go` | `Policy` struct gains `Stdin []byte` |
| `src/cmd/agent/restore.go` (new) | `RestoreRule`, `restoreTasks` — one-shot task derivation |
| `src/cmd/agent/restore_test.go` (new) | Matching tests |
| `src/cmd/agent/main.go` | Wires `restoreTasks` into `policiesFunc` |
| `src/cmd/rwfs/arguments.go` | New `--rules-stdin` flag; skips the local-hostname default when set |
| `src/cmd/rwfs/arguments_test.go` | Matching tests |
| `src/cmd/rwfs/rules.go` (new) | `RestoreRule`, ported longest-matching-rule resolution |
| `src/cmd/rwfs/rules_test.go` (new) | Matching tests |
| `src/cmd/rwfs/verify.go` | Reads stdin rules, broad `ListFiles`, per-rule not-found accounting |
| `src/cmd/rwfs/verify_test.go` (new) | Matching tests |
| `web/src/stores/restoreSubmission.js` | Rewritten submission flow (store facets → id lookup → rules POST) |
| `web/src/stores/restoreSubmission.spec.js` | Matching test rewrite |
| Docs (per task, listed inline) | Updated alongside the code that changes their subject |

---

### Task 1: `policyserver.proto` — `RestoreRule`, `rules`, reserve `source_store`

**Files:**
- Modify: `src/api/policyserver.proto`
- Generated (via `make proto`, not hand-edited): `src/api/policyserver.pb.go`

**Interfaces:**
- Produces: `pb.RestoreRule{Host, Path, Include}`; `pb.Policy.Rules []*pb.RestoreRule` / `GetRules()`; `pb.CreatePolicyRequest.Rules []*pb.RestoreRule` / `GetRules()`. `pb.Policy.StoragePolicyId`/`GetStoragePolicyId()` and `pb.CreatePolicyRequest.StoragePolicyId`/`GetStoragePolicyId()` already exist (fields 15/12) — unchanged, just now also meaningful for `type: "restore"`. `pb.Policy.SourceStore`/`pb.CreatePolicyRequest.SourceStore` no longer exist.

There is no test-first step for a `.proto` file itself — protobuf codegen has no "red" state to assert against. Verification is "it compiles," done at the end of this task.

- [ ] **Step 1: Edit `Policy` message**

In `src/api/policyserver.proto`, replace:
```proto
  repeated string destinations = 17;
  // "restore" policy only. host:port of the source bwfs to restore from.
  string source_store = 18;
}
```
with:
```proto
  repeated string destinations = 17;
  // "restore" policy only. host:port of the source bwfs to restore from.
  // Removed 2026-08-10: replaced by storage_policy_id (field 15, shared
  // with "backup") + live destinations resolution, the same mechanism
  // backup already uses -- see
  // docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md.
  reserved 18;
  reserved "source_store";
  // "restore" policy only.
  repeated RestoreRule rules = 19;
}
```

- [ ] **Step 2: Edit `CreatePolicyRequest` message**

Replace:
```proto
  // "backup" only, required.
  string storage_policy_id = 12;
  // "restore" policy only, required. host:port of the source bwfs to
  // restore from.
  string source_store = 13;
}
```
with:
```proto
  // "backup" and "restore", both required.
  string storage_policy_id = 12;
  // Removed 2026-08-10 -- see Policy.rules above.
  reserved 13;
  reserved "source_store";
  // "restore" policy only, required.
  repeated RestoreRule rules = 14;
}
```

- [ ] **Step 3: Add the `RestoreRule` message**

Add directly above `message Policy {` (after `PolicyCheckin`):
```proto
// One restore-cart selection rule: host-agnostic (Host == "") folder rules
// and host-specific file rules resolve by longest-matching-path-ancestor,
// exactly like web/src/utils/restoreRules.js's resolveFile. policy-server
// never interprets these beyond the load-time validation in
// RestorePolicy.Validate (non-empty Path); resolution happens at verify
// time, in rwfs.
message RestoreRule {
  string host    = 1; // "" = host-agnostic, matches every source host
  string path    = 2;
  bool   include = 3;
}
```

- [ ] **Step 4: Update the `type` field comments**

In `CreatePolicyRequest`, the existing `string type = 7;` field's comment says `mixing fields across types is rejected (e.g. a "restore" request must not set object_filters/rpo/backup_window/storage_policy_id/port)`. Update it:
```proto
  // "backup", "storage", or "restore" -- required. Determines which of the
  // type-specific fields are valid; mixing fields across types is rejected
  // (e.g. a "restore" request must not set object_filters/rpo/
  // backup_window/port/config).
  string type = 7;
```

- [ ] **Step 5: Regenerate and build**

Run: `make proto && cd src && go build ./...`
Expected: succeeds. `grep -rn "SourceStore" src/cmd src/api` now returns nothing outside historical doc/comment text (checked in Task 3/4/9, not here).

- [ ] **Step 6: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go
git commit -m "feat: replace restore policy's source_store with storage_policy_id and typed rules"
```

---

### Task 2: `catalog.proto` — `ListStoreFacets` RPC

**Files:**
- Modify: `src/api/catalog.proto`
- Generated: `src/api/catalog.pb.go`, `src/api/catalog_grpc.pb.go`

**Interfaces:**
- Produces: `pb.CatalogServiceClient.ListStoreFacets(ctx, *pb.ListFacetsRequest, ...) (*pb.ListFacetsResponse, error)` and the matching server method on `pb.CatalogServiceServer`/`pb.UnimplementedCatalogServiceServer`.

- [ ] **Step 1: Add the RPC**

In `src/api/catalog.proto`, in `service CatalogService`, add directly after `rpc ListDirectoryFacets(...)`:
```proto
  rpc ListStoreFacets(ListFacetsRequest) returns (ListFacetsResponse);
```

- [ ] **Step 2: Regenerate and build**

Run: `make proto && cd src && go build ./...`
Expected: succeeds. `catalogServer` (in `cmd/catalog`) embeds `pb.UnimplementedCatalogServiceServer`, which satisfies every `pb.CatalogServiceServer` method — including the new `ListStoreFacets` — with a stub that returns a gRPC `Unimplemented` status at *runtime*, not a compile error. So there is no build break here: `go build ./...` is expected to succeed cleanly, and calling `ListStoreFacets` before Task 7 lands would simply fail at runtime with `Unimplemented`, not at compile time.

- [ ] **Step 3: Commit**

```bash
git add src/api/catalog.proto src/api/catalog.pb.go src/api/catalog_grpc.pb.go
git commit -m "feat: add ListStoreFacets RPC to catalog service"
```

---

### Task 3: `policy-server` — `RestorePolicy` schema rewrite

**Files:**
- Modify: `src/cmd/policy-server/restore_policy.go`
- Modify: `src/cmd/policy-server/restore_policy_test.go`

**Interfaces:**
- Consumes: `pb.RestoreRule`, `pb.Policy.GetRules()`/`.GetStoragePolicyId()`, `pb.CreatePolicyRequest` (Task 1).
- Produces: `RestoreRule{Host, Path string; Include bool}` (policy-server's own Go type); `RestorePolicy{PolicyBase; StoragePolicyID string; Rules []RestoreRule}`; `(*RestorePolicy).Validate() error`; `(*RestorePolicy).Clone() Policy`; `(*RestorePolicy).ToProto(bool) *pb.Policy` (sets `StoragePolicyId`/`Rules`, leaves `Destinations` empty for `attachDestination`, Task 5, to fill).

- [ ] **Step 1: Write the failing tests**

Replace the entire contents of `src/cmd/policy-server/restore_policy_test.go`:

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
		"storage_policy_id": "sp-1",
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}]
	}`)

	got, err := parsePolicyFile(path, "restore")
	require.NoError(t, err)
	p, ok := got.(*RestorePolicy)
	require.True(t, ok)
	assert.Equal(t, "web01-emergency", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, []string{"web-01"}, p.ClientFilters.Hostnames)
	assert.Equal(t, "sp-1", p.StoragePolicyID)
	assert.Equal(t, []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}}, p.Rules)
	assert.Equal(t, "restore", p.Kind())
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_RestorePolicyRuleHostAgnosticWhenNull(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "folder.json", `{
		"metadata": {"name": "folder"},
		"storage_policy_id": "sp-1",
		"rules": [{"host": null, "path": "/var/log", "include": true}]
	}`)

	got, err := parsePolicyFile(path, "restore")
	require.NoError(t, err)
	p := got.(*RestorePolicy)
	assert.Equal(t, "", p.Rules[0].Host, "a JSON null host decodes to the host-agnostic empty string")
}

func TestParsePolicyFile_RestoreAndBackupSameBasenameYieldDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	pathBackup := writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}, "storage_policy_id": "sp-1"}`)
	pathRestore := writePolicyFile(t, filepath.Join(dir, "restore"), "nightly.json", `{
		"metadata": {"name": "nightly"}, "storage_policy_id": "sp-1", "rules": [{"path": "/x", "include": true}]
	}`)

	pBackup, err := parsePolicyFile(pathBackup, "backup")
	require.NoError(t, err)
	pRestore, err := parsePolicyFile(pathRestore, "restore")
	require.NoError(t, err)

	assert.NotEqual(t, pBackup.Meta().ID, pRestore.Meta().ID, "same basename in different type subfolders must not collide")
}

func TestRestorePolicy_ValidateValidPolicyReturnsNil(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "ok"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/x", Include: true}},
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ValidateMissingNameFails(t *testing.T) {
	p := &RestorePolicy{StoragePolicyID: "sp-1", Rules: []RestoreRule{{Path: "/x", Include: true}}}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateEmptyStoragePolicyIDFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Rules:      []RestoreRule{{Path: "/x", Include: true}},
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateEmptyRulesFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateRuleWithEmptyPathFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "", Include: true}},
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_CloneDeepCopiesRules(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true}},
	}
	cloned := p.Clone().(*RestorePolicy)
	cloned.Rules[0].Path = "/mutated"
	assert.Equal(t, "/a", p.Rules[0].Path, "mutating the clone's Rules must not affect the original")
}

func TestRestorePolicy_ToProtoSetsTypeSpecificFields(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{ID: "r1", Name: "web01-emergency"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-01"}},
			Type:          "restore",
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
	}

	pp := p.ToProto(true)

	assert.Equal(t, "r1", pp.GetId())
	assert.Equal(t, "restore", pp.GetType())
	assert.Equal(t, "sp-1", pp.GetStoragePolicyId())
	require.Len(t, pp.GetRules(), 1)
	assert.Equal(t, "web-01", pp.GetRules()[0].GetHost())
	assert.Equal(t, "/var/www/index.html", pp.GetRules()[0].GetPath())
	assert.True(t, pp.GetRules()[0].GetInclude())
	assert.Empty(t, pp.GetDestinations(), "ToProto never resolves destinations itself -- attachDestination does")
	assert.Equal(t, []string{"web-01"}, pp.GetClientFilters().GetHostnames())
}

func TestRestorePolicy_ToProtoOmitsClientFiltersWhenNotRequested(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{ID: "r1", Name: "x"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-01"}},
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/x", Include: true}},
	}

	pp := p.ToProto(false)

	assert.Nil(t, pp.GetClientFilters())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestRestorePolicy -v`
Expected: FAIL to compile — `RestorePolicy` has no field `Rules`/`StoragePolicyID`, `RestoreRule` undefined.

- [ ] **Step 3: Rewrite `restore_policy.go`**

Replace the entire file:

```go
package main

import (
	"encoding/json"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RestoreRule is one restore-cart selection rule -- {host, path, include}
// mirroring web/src/utils/restoreRules.js's rule shape exactly, so the
// frontend can send its cart.rules through with no reshaping. Host == ""
// means host-agnostic (a folder rule that applies across every source
// host, matching restoreRules.js's `host: null` convention -- a JSON null
// decodes to Go's zero-value "" automatically); a non-empty Host scopes the
// rule to exactly that source. policy-server never resolves these against
// any real file listing -- resolution happens at verify time, in rwfs. See
// docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md.
type RestoreRule struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Include bool   `json:"include"`
}

// RestorePolicy is the "restore" policy type: a one-shot directive telling
// a specific mesh node (via PolicyBase's ClientFilters, the same targeting
// mechanism BackupPolicy/StoragePolicy already use) to restore files from
// a source bwfs. Unlike BackupPolicy/StoragePolicy it has no recurring-
// schedule concept (no rpo/backup_window) -- it's meant to be picked up
// once by agent's restoreTasks (cmd/agent/restore.go), and is never
// updatable via UpdatePolicy (see buildPolicyForUpdate in write.go).
//
// StoragePolicyID reuses BackupPolicy's exact mechanism (references a
// "storage"-typed Policy.id; the dialable address is resolved live from its
// checkins, see server.go's attachDestination) rather than a raw
// source_store host:port baked in at creation time -- avoiding the
// staleness a pre-resolved address would have if the storage node's
// checked-in address changes before this one-shot policy is ever executed.
type RestorePolicy struct {
	PolicyBase
	StoragePolicyID string        `json:"storage_policy_id"`
	Rules           []RestoreRule `json:"rules"`
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
// storage_policy_id must be non-empty (existence against a live "storage"
// policy is checked separately in CreatePolicy, where a current cache is
// in scope -- the same split BackupPolicy.Validate already documents), and
// rules must contain at least one entry, each with a non-empty path.
func (p *RestorePolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if p.StoragePolicyID == "" {
		return fmt.Errorf("storage_policy_id is required")
	}
	if len(p.Rules) == 0 {
		return fmt.Errorf("rules must contain at least one entry")
	}
	for i, r := range p.Rules {
		if r.Path == "" {
			return fmt.Errorf("rules[%d]: path is required", i)
		}
	}
	return nil
}

// Clone deep-copies Rules so mutating the returned value never affects the
// cached original.
func (p *RestorePolicy) Clone() Policy {
	rules := make([]RestoreRule, len(p.Rules))
	copy(rules, p.Rules)
	return &RestorePolicy{
		PolicyBase:      p.PolicyBase.clone(),
		StoragePolicyID: p.StoragePolicyID,
		Rules:           rules,
	}
}

// ToProto converts to the wire representation GetPolicies/ListPolicies/
// CreatePolicy return (never UpdatePolicy -- restore policies are not
// updatable). Destinations is intentionally left unset here -- the caller
// (server.go's attachDestination) resolves it live from StoragePolicyId's
// checkins, the same split BackupPolicy.ToProto already uses. client_filters
// is only populated when includeClientFilters is true.
func (p *RestorePolicy) ToProto(includeClientFilters bool) *pb.Policy {
	rules := make([]*pb.RestoreRule, len(p.Rules))
	for i, r := range p.Rules {
		rules[i] = &pb.RestoreRule{Host: r.Host, Path: r.Path, Include: r.Include}
	}
	pp := &pb.Policy{
		Id:              p.Metadata.ID,
		Name:            p.Metadata.Name,
		CreatedAt:       timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:       timestamppb.New(p.Metadata.UpdatedAt),
		Type:            p.Type,
		StoragePolicyId: p.StoragePolicyID,
		Rules:           rules,
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

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run TestRestorePolicy -v` and `go test ./cmd/policy-server/... -run TestParsePolicyFile_Restore -v`
Expected: PASS.

- [ ] **Step 5: Update `docs/protocols/policy-server.md`**

Find the restore-policy proto section (added by the 2026-08-09 design) and replace its `source_store`/`config` description with:

> A `"restore"` policy has `storage_policy_id` (required, references a `"storage"`-typed policy's `id` — the same field and live-resolution mechanism a `"backup"` policy already uses; its `destinations` is computed the identical way) and `rules` (required, at least one entry — `{host, path, include}`, where an empty `host` means the rule applies across every source host). It has no `object_filters`, `rpo`, `backup_window`, `port`, or `config`.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/restore_policy.go src/cmd/policy-server/restore_policy_test.go docs/protocols/policy-server.md
git commit -m "feat: rewrite RestorePolicy for storage_policy_id and typed rules"
```

---

### Task 4: `policy-server` — `write.go` cross-type field rejection

**IMPORTANT CORRECTION (found mid-execution, after Task 3 landed):** the original version of this
task said to modify `server_test.go`. That was wrong — `server_test.go` has no `CreatePolicy` tests
at all; every `TestCreatePolicy_*`/`TestUpdatePolicy_*`/`TestDeletePolicy_*` test (including the
pre-existing restore-policy ones using the old `source_store`/`config` schema) lives in a separate
file, `src/cmd/policy-server/write_test.go`, using its own `newTestWriteServer(t, dir)
*policyServerServer` and `createTestStoragePolicy(t, srv, hostname string, port int32) string`
helpers (the latter both creates a real `"storage"` policy and records a checkin for it, so its
returned id is immediately usable as a resolvable `storage_policy_id`). This correction also
surfaced a real gap in the original design: since `storage_policy_id` is now a *reference* (unlike
the old free-form `source_store` string), `CreatePolicy` should reject a restore request whose
`storage_policy_id` doesn't name an existing `"storage"` policy, the exact same way it already does
for `"backup"` policies — the original task text never added this. Both are fixed below.

**Files:**
- Modify: `src/cmd/policy-server/write.go`
- Modify: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: `RestorePolicy{StoragePolicyID, Rules}` (Task 3), `pb.CreatePolicyRequest.GetRules()`/`.GetStoragePolicyId()` (Task 1).
- Produces: `buildPolicyForCreate` builds a `*RestorePolicy` from `storage_policy_id`+`rules` instead of `source_store`+`config`; `CreatePolicy` rejects an unresolvable `storage_policy_id` for a restore policy, mirroring its existing backup-policy check.

- [ ] **Step 1: Write the failing tests**

In `src/cmd/policy-server/write_test.go`, delete these two tests outright — their entire premise
was "setting `source_store` on a non-restore policy is rejected," and `source_store` no longer
exists as a field to set (equivalent coverage for the new `rules` field is added below):
- `TestCreatePolicy_BackupTypeWithSourceStoreRejected` (currently ~line 418)
- `TestCreatePolicy_StorageTypeWithSourceStoreRejected` (currently ~line 435)

Delete this one too — it tested `source_store`'s `host:port` format validation, which has no
equivalent for a plain reference string like `storage_policy_id`:
- `TestCreatePolicy_RestoreInvalidSourceStoreFormatReturnsInvalidArgument` (currently ~line 755)

Rewrite these five in place, replacing every `SourceStore`/`Config` use with
`StoragePolicyId`/`Rules`, and (since `storage_policy_id` is now existence-checked) creating a real
storage policy first via `createTestStoragePolicy` wherever the test needs `CreatePolicy` to
actually succeed:

```go
func TestCreatePolicy_RestorePolicyWritesIntoRestoreDir(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "Web01 Emergency Restore",
		Type:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "restore", resp.Type)
	assert.Equal(t, storageID, resp.StoragePolicyId)
	require.Len(t, resp.Rules, 1)
	assert.Equal(t, "/var/www/index.html", resp.Rules[0].Path)

	_, err = os.Stat(filepath.Join(dir, "restore", "web01-emergency-restore.json"))
	require.NoError(t, err)
}

func TestCreatePolicy_ResponseIncludesRestoreType(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "quick-restore", Type: "restore", StoragePolicyId: storageID,
		Rules: []*pb.RestoreRule{{Path: "/x", Include: true}},
	})

	require.NoError(t, err)
	assert.Equal(t, "restore", resp.Type)
}

func TestCreatePolicy_RestoreTypeWithBackupFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	// storage_policy_id is now required for restore, not disqualifying -- rpo
	// (a genuine backup-only field) is what this test must set instead to
	// stay meaningful.
	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "bad",
		Type:            "restore",
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
		Rpo:             "24h",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_RestoreTypeWithPortRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "bad",
		Type:            "restore",
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
		Port:            9400,
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestUpdatePolicy_RestoreTypeRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)
	created, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "quick-restore", Type: "restore", StoragePolicyId: storageID,
		Rules: []*pb.RestoreRule{{Path: "/x", Include: true}},
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

Rename this test (its old name embedded the removed field's name) and rewrite its body to omit
`storage_policy_id` entirely rather than test a since-removed field:

```go
func TestCreatePolicy_RestoreMissingStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name: "no-storage-ref", Type: "restore", Rules: []*pb.RestoreRule{{Path: "/x", Include: true}},
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}
```
(replaces `TestCreatePolicy_RestoreMissingSourceStoreReturnsInvalidArgument`, currently ~line 742)

Add these new tests, covering the new `rules`-field rejection and the new restore existence-check:

```go
func TestCreatePolicy_RestoreRejectsConfigField(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "x",
		Type:            "restore",
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
		Config:          `{"a":1}`,
	})
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "config")
}

func TestCreatePolicy_StorageRejectsRulesField(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:   "x",
		Type:   "storage",
		Port:   8080,
		Config: `{"backend":"filesystem","root":"/data"}`,
		Rules:  []*pb.RestoreRule{{Path: "/x", Include: true}},
	})
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "rules")
}

func TestCreatePolicy_BackupRejectsRulesField(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs", 8080)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "x",
		Type:            "backup",
		Rpo:             "24h",
		BackupWindow:    []string{"0 2 * * *"},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
	})
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "rules")
}

func TestCreatePolicy_RestoreUnknownStoragePolicyIdReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "orphan-restore",
		Type:            "restore",
		StoragePolicyId: "does-not-exist",
		Rules:           []*pb.RestoreRule{{Path: "/x", Include: true}},
	})
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestCreatePolicy|TestUpdatePolicy_RestoreTypeRejected' -v`
Expected: FAIL to compile — `write_test.go` still references the removed `SourceStore`/`Config`
fields on `pb.CreatePolicyRequest` in the tests this step didn't yet touch, and `write.go` itself
still builds `RestorePolicy{SourceStore: ...}`.

- [ ] **Step 3: Update `write.go`**

`buildPolicy` (the shared backup/storage constructor, driven by the `policyFieldsGetter` interface
so it works for both `CreatePolicyRequest` and `UpdatePolicyRequest`) is **not modified** by this
task — its `case "storage":`/`case "backup":` bodies keep checking exactly what they already check
today (port/config, object_filters/rpo/backup_window/storage_policy_id respectively).
`policyFieldsGetter` does **not** gain a `GetRules()` method — `UpdatePolicyRequest` has no `rules`
field to expose (restore stays non-updatable), and restore is already handled separately in
`buildPolicyForCreate`, never through `buildPolicy`. All of this task's actual changes are in
`buildPolicyForCreate`, which still has the concrete `*pb.CreatePolicyRequest` in scope.

Add a new helper next to `backupFieldsSet`:
```go
// restoreFieldsSet reports whether the restore-only rules field is set --
// used to reject a request mixing it into a backup or storage policy.
func restoreFieldsSet(rules []*pb.RestoreRule) bool {
	return len(rules) > 0
}
```

Replace `buildPolicyForCreate` in full:
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
		// storage_policy_id is deliberately passed as "" here, not
		// req.GetStoragePolicyId() -- unlike before this task, it's now a
		// required restore field, not a disqualifying one; only
		// object_filters/rpo/backup_window (plus port/config, checked via
		// storageFieldsSet below) still disqualify a restore request.
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), "") || storageFieldsSet(req.GetPort(), req.GetConfig()) {
			return nil, fmt.Errorf("a restore policy must not set object_filters/rpo/backup_window/port/config")
		}
		rules := make([]RestoreRule, len(req.GetRules()))
		for i, r := range req.GetRules() {
			rules[i] = RestoreRule{Host: r.GetHost(), Path: r.GetPath(), Include: r.GetInclude()}
		}
		return &RestorePolicy{
			PolicyBase:      base,
			StoragePolicyID: req.GetStoragePolicyId(),
			Rules:           rules,
		}, nil
	}
	// A non-restore request setting rules is rejected here, once, for every
	// other type -- covers both "storage must not set rules" and "backup
	// must not set rules" with one check and one message, rather than
	// duplicating the same test inside buildPolicy's per-type branches.
	if restoreFieldsSet(req.GetRules()) {
		return nil, fmt.Errorf("only a restore policy may set rules")
	}
	return buildPolicy(req.GetType(), base, req)
}
```

`storageFieldsSet`/`backupFieldsSet` themselves are unchanged from their current definitions
(`storageFieldsSet(port int32, config string) bool`, `backupFieldsSet(objectFilters []*pb.ObjectFilter, rpo string, backupWindow []string, storagePolicyID string) bool`) — only their call sites and arguments at this one spot changed.

Update `buildPolicy`'s doc comment (the one starting `// buildPolicy constructs...`) to drop its
outdated mention of `source_store`: replace its trailing sentence — `"restore" is handled
separately in buildPolicyForCreate, not routed through here or through policyFieldsGetter -- it's
create-only (see buildPolicyForUpdate) and UpdatePolicyRequest has no source_store field for
policyFieldsGetter to expose.` — with:
```go
// "restore" is handled separately in buildPolicyForCreate, not routed
// through here or through policyFieldsGetter -- it's create-only (see
// buildPolicyForUpdate) and UpdatePolicyRequest has no rules field for
// policyFieldsGetter to expose.
```

In `(*policyServerServer).CreatePolicy`, directly after the existing block:
```go
	if bp, ok := p.(*BackupPolicy); ok {
		if sp, found := s.cache.FindByID(bp.StoragePolicyID); !found || sp.Kind() != "storage" {
			s.logger.Error("CreatePolicy: storage_policy_id does not reference an existing storage policy", "storage_policy_id", bp.StoragePolicyID)
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("storage policy %q not found", bp.StoragePolicyID))
		}
	}
```
add the identical check for restore (this is the existence-check gap the correction note above
describes — `storage_policy_id` is now a real reference for restore too, not a free-form string,
so it deserves the same fail-fast validation `"backup"` already gets):
```go
	if rp, ok := p.(*RestorePolicy); ok {
		if sp, found := s.cache.FindByID(rp.StoragePolicyID); !found || sp.Kind() != "storage" {
			s.logger.Error("CreatePolicy: storage_policy_id does not reference an existing storage policy", "storage_policy_id", rp.StoragePolicyID)
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("storage policy %q not found", rp.StoragePolicyID))
		}
	}
```
`UpdatePolicy` needs no equivalent addition — a restore policy can never reach `UpdatePolicy`'s
matching check at all, since `buildPolicyForUpdate` already rejects `kind == "restore"` outright
before that check would run.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS, including every pre-existing `CreatePolicy`/`UpdatePolicy` test (backup/storage untouched in behavior).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go
git commit -m "fix: reject rules on non-restore policies, require it on restore, validate storage_policy_id exists"
```

---

### Task 5: `policy-server` — `attachDestination` resolves restore too

**IMPORTANT CORRECTION (found mid-execution, after Task 3/4 landed):** the original version of
this task invented a `TestAttachDestination_*`/`newTestServer`/`createStoragePolicyForTest` shape
that doesn't exist in this codebase — `server_test.go` tests this kind of behavior at the
`GetPolicies`/`ListPolicies` RPC level, against on-disk policy JSON files, not via a dedicated
`attachDestination` unit test or a `CreatePolicy` call. The real pattern (see
`TestGetPolicies_ResponseFieldsRoundTrip`, ~line 162, and the pre-existing
`TestGetPolicies_MatchesRestorePolicyAndRecordsCheckin`, ~line 330) is: write a `"storage"` policy
file, `NewCache()`+`Reload()` it to read back its id, write a dependent policy file referencing
that id, `newTestServerWithPolicies(t, dir)`, `srv.checkins.RecordCheckin(t.Context(), storageID,
hostname, time.Now())`, then call `GetPolicies` and assert on `p.Destinations`. This correction also
surfaced that the pre-existing `TestGetPolicies_MatchesRestorePolicyAndRecordsCheckin` test writes
an on-disk restore policy file using the *old* `source_store`/`config` JSON keys — since those keys
no longer exist on `RestorePolicy` (Task 3), that file now silently fails `RestorePolicy.Validate()`
at load time (a malformed-policy skip, not a compile error), so this pre-existing test would
currently fail (`require.Len(t, resp.Policies, 1)` sees zero policies). Fixing it (folded into Step
1 below, since it already covers almost exactly the case this task needs to add) is part of this
task, not a separate one.

**Files:**
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/server_test.go`

**Interfaces:**
- Consumes: `pp.GetType()`, `pp.GetStoragePolicyId()` (both already exist and are now populated for restore, Task 3).
- Produces: `attachDestination` now also fills `pp.Destinations` for a `"restore"`-typed `*pb.Policy`.

- [ ] **Step 1: Fix the pre-existing test to use the new schema, and assert destinations resolve**

Replace the pre-existing `TestGetPolicies_MatchesRestorePolicyAndRecordsCheckin` (~line 330) in
full:

```go
func TestGetPolicies_MatchesRestorePolicyAndRecordsCheckin(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east.json", `{
		"metadata": {"name": "east-storage"},
		"port": 8080,
		"config": {}
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	storageID := c.Policies()[0].Meta().ID

	writePolicyFile(t, filepath.Join(dir, "restore"), "web01-emergency.json", fmt.Sprintf(`{
		"metadata": {"name": "web01-emergency"},
		"client_filters": {"hostnames": ["web-01"]},
		"storage_policy_id": %q,
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}]
	}`, storageID))
	srv := newTestServerWithPolicies(t, dir)
	require.NoError(t, srv.checkins.RecordCheckin(t.Context(), storageID, "bwfs-east.internal", time.Now()))

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, "restore", p.Type)
	assert.Equal(t, storageID, p.StoragePolicyId)
	require.Len(t, p.Rules, 1)
	assert.Equal(t, "/var/www/index.html", p.Rules[0].Path)
	assert.Equal(t, []string{"bwfs-east.internal:8080"}, p.Destinations, "destinations must resolve live from storage_policy_id's checkins, same as backup")
	assert.Nil(t, p.ClientFilters)

	checkins, err := srv.checkins.CheckinsForPolicy(context.Background(), p.Id)
	require.NoError(t, err)
	require.Len(t, checkins, 1)
	assert.Equal(t, "web-01", checkins[0].Hostname)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/policy-server/... -run TestGetPolicies_MatchesRestorePolicyAndRecordsCheckin -v`
Expected: FAIL — `resp.Policies` is empty (the old-schema on-disk JSON no longer validates), or
once you've updated the JSON, `p.Destinations` is empty since `attachDestination` currently only
acts on `pp.GetType() == "backup"`.

- [ ] **Step 3: Update `attachDestination`**

In `server.go`, change:
```go
func attachDestination(ctx context.Context, pp *pb.Policy, cache *Cache, checkins *checkinstore.Store, logger *slog.Logger) {
	if pp.GetType() != "backup" || pp.GetStoragePolicyId() == "" {
		return
	}
```
to:
```go
func attachDestination(ctx context.Context, pp *pb.Policy, cache *Cache, checkins *checkinstore.Store, logger *slog.Logger) {
	if (pp.GetType() != "backup" && pp.GetType() != "restore") || pp.GetStoragePolicyId() == "" {
		return
	}
```

Update the function's doc comment (the one above it) to say `"backup" or "restore" policy` instead of just `backup policy` wherever it currently says the latter.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd src && go test ./cmd/policy-server/... -run TestGetPolicies_MatchesRestorePolicyAndRecordsCheckin -v`
Expected: PASS. Also run `cd src && go test ./cmd/policy-server/... -v` to confirm the rest of the
package (including every pre-existing backup-policy destination test) is unaffected.

- [ ] **Step 5: Update `docs/protocols/policy-server.md` and `docs/components/policy-server.md`**

In both files, wherever they describe `destinations` as `"backup" policy only`, change to `"backup" and "restore" policy only` (grep each file for `destinations` to find the exact spots — there are at least two per file per the earlier design's phrasing: the field-comment-mirroring prose and the "Policy files and hot reload" narrative section).

In `docs/components/policy-server.md`, also replace the restore-policy paragraph in "Policy types
and directory layout" (currently ~line 83, right after the `"storage"` policy paragraph) — it still
describes the pre-this-plan `source_store`/`config` shape and says restore "has no ...
storage_policy_id ... or port," which is now wrong (restore requires `storage_policy_id`).
Replace it in full:

```markdown
A `"restore"` policy is a one-shot directive: `client_filters` targets the node that will execute
the restore, `storage_policy_id` (required, references an existing `"storage"`-typed policy's `id`
— the same field, existence check, and live `destinations` resolution a `"backup"` policy already
uses) names the source `bwfs` to restore from, and `rules` (required, at least one entry —
`{host, path, include}`, mirroring the web restore cart's own rule shape; an empty/omitted `host`
means the rule applies across every source host) says what to restore. It has no `object_filters`,
`rpo`, `backup_window`, `port`, or `config`. Unlike every other type, a `"restore"` policy is never
updatable -- `UpdatePolicy` rejects any request targeting one with `INVALID_ARGUMENT`, regardless of
which fields the request sets, so `api-server`'s generic `PUT /api/v1/policies/{id}` rejects it too,
with no `api-server`-side special-casing needed. See
[Design: Restore Policy Verification Execution](../superpowers/specs/2026-08-10-restore-policy-verification-design.md).
```

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go docs/protocols/policy-server.md docs/components/policy-server.md
git commit -m "feat: resolve destinations for restore policies the same live way as backup"
```

---

### Task 6: `catalog` storage — `ListStoreFacets` query

**Files:**
- Modify: `src/storage/catalog/facets.go`
- Modify: `src/storage/catalog/facets_test.go`

**Interfaces:**
- Consumes: `EntryRecord{StoreNode string, ...}` (existing, `src/storage/catalog/models.go`), `FacetFilter{ReceivedAfter, ReceivedBefore, Pattern, SourceHosts, JobNames, ParentDirectories}` (existing).
- Produces: `(*Store).ListStoreFacets(ctx, FacetFilter) ([]Facet, error)`.

- [ ] **Step 1: Write the failing test**

**Correction (found mid-execution):** there is no `newTestStore`/`insertTestEntry` helper, and tests
never construct an `EntryRecord` GORM row directly. The real, existing pattern (see
`TestListDirectoryFacets_GroupsByParentDirectoryWithCountAndLastSeen`) is `store, err :=
New(t.TempDir())` + `defer store.Close()` + `store.EnsureEntries(t.Context(), []Entry{...})`, using
the public `Entry` input struct (`StoreNode`/`JobID`/`ObjectID`/`SourceHost`/`ParentDirectory`/
`StoreCreatedAt`, no `ReceivedAt` field — it's assigned internally, not caller-controlled, which is
also why the existing sibling test never asserts an exact `LastSeen` value, only `Count`). Add,
mirroring that exact pattern:

```go
func TestListStoreFacets_GroupsByStoreNode(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-1", JobID: "job-1", ObjectID: "o1", SourceHost: "web-01", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-1", JobID: "job-1", ObjectID: "o2", SourceHost: "web-01", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-2", JobID: "job-1", ObjectID: "o3", SourceHost: "web-02", ParentDirectory: "/etc", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListStoreFacets(t.Context(), FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 2)

	byName := map[string]Facet{}
	for _, f := range facets {
		byName[f.Name] = f
	}
	assert.Equal(t, int64(2), byName["bwfs-1"].Count)
	assert.Equal(t, int64(1), byName["bwfs-2"].Count)
}

func TestListStoreFacets_FiltersBySourceHostsAndPattern(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries(t.Context(), []Entry{
		{StoreNode: "bwfs-1", JobID: "job-1", ObjectID: "match-me", SourceHost: "web-01", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-2", JobID: "job-1", ObjectID: "no-match", SourceHost: "web-02", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListStoreFacets(t.Context(), FacetFilter{SourceHosts: []string{"web-01"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "bwfs-1", facets[0].Name)

	facets, err = store.ListStoreFacets(t.Context(), FacetFilter{Pattern: "match-me"})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "bwfs-1", facets[0].Name)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./storage/catalog/... -run TestListStoreFacets -v`
Expected: FAIL to compile — `ListStoreFacets` undefined.

- [ ] **Step 3: Implement `ListStoreFacets`**

Add to `src/storage/catalog/facets.go`, directly after `ListDirectoryFacets`:

```go
// ListStoreFacets groups entries matching filter by store_node (the bwfs
// node that sent the batch -- exposed to API callers as "store_host"),
// dropping rows where it's empty (shouldn't happen -- StoreNode is part of
// EntryRecord's unique key -- but mirrors ListClientFacets/
// ListDirectoryFacets's defensive empty-name drop for consistency). Both
// SourceHosts and JobNames narrow it, the same "apply every other
// dimension" rule the other three facet queries already follow; there is
// no "store_hosts" field on FacetFilter to ignore for its own dimension,
// unlike the other three.
func (s *Store) ListStoreFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).
		Select("store_node, received_at").
		Where("store_node != ''")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}

	var rows []struct {
		StoreNode  string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: r.StoreNode, ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
}
```

Also update `FacetFilter`'s doc comment (the one above its struct definition) to mention `ListStoreFacets` in its list of consumers, alongside the existing three.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd src && go test ./storage/catalog/... -run TestListStoreFacets -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/storage/catalog/facets.go src/storage/catalog/facets_test.go
git commit -m "feat: add ListStoreFacets catalog query grouping by store node"
```

---

### Task 7: `catalog` gRPC — `ListStoreFacets` handler

**Files:**
- Modify: `src/cmd/catalog/server.go`
- Modify: `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: `(*catalogstore.Store).ListStoreFacets` (Task 6), `pb.ListFacetsRequest`/`pb.ListFacetsResponse` (existing), `toProtoFacets` (existing helper in this file), `unixOrZero` (existing helper in this file).
- Produces: `(*catalogServer).ListStoreFacets(ctx, *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error)` — resolves Task 2's broken build.

- [ ] **Step 1: Write the failing test**

**Correction (found mid-execution):** the real helper is `newTestCatalogServer(t) (*catalogServer,
*catalogstore.Store)` — two return values, not one — and entries are seeded via
`store.EnsureEntries(t.Context(), []catalogstore.Entry{...})` (see
`TestListDirectoryFacets_ReturnsGroupedCounts`, ~line 439). Add, mirroring that exact pattern:

```go
func TestListStoreFacets_ReturnsGroupedCounts(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries(t.Context(), []catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-3", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListStoreFacets(context.Background(), &pb.ListFacetsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetFacets(), 2)

	byName := map[string]int64{}
	for _, f := range resp.GetFacets() {
		byName[f.GetName()] = f.GetCount()
	}
	assert.Equal(t, int64(2), byName["bwfs-a"])
	assert.Equal(t, int64(1), byName["bwfs-b"])
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go build ./... && go test ./cmd/catalog/... -run TestListStoreFacets -v`
Expected: build succeeds (see Task 2 Step 2 — `catalogServer`'s embedded `pb.UnimplementedCatalogServiceServer` already satisfies the interface). The test itself fails at runtime instead: `s.ListStoreFacets(...)` returns a gRPC `Unimplemented` error, so `require.NoError(t, err)` fails — that's the expected "red" state this step's implementation (below) turns "green."

- [ ] **Step 3: Implement the handler**

Add to `src/cmd/catalog/server.go`, directly after `ListDirectoryFacets`:

```go
func (s *catalogServer) ListStoreFacets(ctx context.Context, req *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error) {
	facets, err := s.store.ListStoreFacets(ctx, catalogstore.FacetFilter{
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		Pattern:        req.GetPattern(),
		SourceHosts:    req.GetSourceHosts(),
		JobNames:       req.GetJobNames(),
	})
	if err != nil {
		s.logger.Error("ListStoreFacets: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list store facets: %v", err)
	}
	return &pb.ListFacetsResponse{Facets: toProtoFacets(facets)}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass, and the whole module builds**

Run: `cd src && go build ./... && go test ./cmd/catalog/... -v`
Expected: build succeeds; all tests PASS.

- [ ] **Step 5: Update `docs/protocols/catalog-sync.md`**

This project's `.claude/CLAUDE.md` requires updating the protocol doc for any new/modified gRPC RPC
before committing. `docs/protocols/catalog-sync.md` already documents `ListClientFacets`/
`ListJobFacets`/`ListDirectoryFacets` (and is already cross-linked from `docs/components/catalog.md`
and `README.md` — no new cross-links needed, only content updates to this one file):

- In its `service CatalogService { ... }` proto listing, add `rpc ListStoreFacets(ListFacetsRequest) returns (ListFacetsResponse);` directly after the existing `ListDirectoryFacets` line.
- Rename its `## ListClientFacets / ListJobFacets / ListDirectoryFacets` section header to `## ListClientFacets / ListJobFacets / ListDirectoryFacets / ListStoreFacets`, and extend its explanatory prose (the sentence listing what each groups by) with: "; `ListStoreFacets` groups by `store_host` (the bwfs node that sent the batch)". Also extend the sentence about each RPC applying every filter dimension except its own to include `ListStoreFacets` (it applies `source_hosts`/`job_names`, has no own-dimension filter to ignore since `ListFacetsRequest` carries no `store_hosts` field).
- In its "See Also"-style REST mapping line (the one listing `GET /api/v1/catalog/clients` etc.), add `` `GET /api/v1/catalog/stores` (`ListStoreFacets`) ``.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/catalog/server.go src/cmd/catalog/server_test.go docs/protocols/catalog-sync.md
git commit -m "feat: implement ListStoreFacets gRPC handler"
```

---

### Task 8: `api-server` — `GET /api/v1/catalog/stores`

**Files:**
- Modify: `src/cmd/api-server/catalog.go`
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/catalog_test.go`
- Modify: `docs/api/rest-v1.md`
- Modify: `docs/components/api-server.md`

**Interfaces:**
- Consumes: `pb.CatalogServiceClient.ListStoreFacets` (Task 2/7), `facetDTO`/`toFacetDTO`/`parseDateRange`/`splitCommaParam` (existing helpers in `catalog.go`).
- Produces: `(*server).handleListCatalogStores(w, r)`; route `GET /api/v1/catalog/stores`.

- [ ] **Step 1: Write the failing test**

**Correction (found mid-execution):** the real `fakeCatalogQueryClient` (already defined in this
file) reuses one shared `facetsResp *pb.ListFacetsResponse` / `facetsErr error` / `lastFacetsReq
*pb.ListFacetsRequest` field set across all three existing facet methods (`ListClientFacets`/
`ListJobFacets`/`ListDirectoryFacets` each read/write the same three fields) — `ListStoreFacets`
should be added the same way, reusing those fields rather than adding new per-method ones. The real
server constructor is `newServer(nil, fake, nil, testLogger())`, and every handler test in this file
dispatches through a real `http.NewServeMux()`/`srv.registerRoutes(mux)`/`mux.ServeHTTP(rec, req)`
(see `TestHandleListCatalog_ReturnsDataAndHasMore`, ~line 55) — no direct `s.handleX(rec, req)`
shortcut exists. Add:

```go
func TestHandleListCatalogStores_ReturnsFacets(t *testing.T) {
	fake := &fakeCatalogQueryClient{
		facetsResp: &pb.ListFacetsResponse{
			Facets: []*pb.Facet{{Name: "bwfs-1", Count: 3, LastSeen: 100}},
		},
	}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/stores?pattern=/var/www", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string][]facetDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body["data"], 1)
	assert.Equal(t, "bwfs-1", body["data"][0].Name)
	assert.Equal(t, "/var/www", fake.lastFacetsReq.GetPattern())
}
```

Add a `ListStoreFacets` method to `fakeCatalogQueryClient`, reusing the existing shared fields
exactly like its three siblings do:
```go
func (f *fakeCatalogQueryClient) ListStoreFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error) {
	f.lastFacetsReq = in
	return f.facetsResp, f.facetsErr
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleListCatalogStores -v`
Expected: FAIL to compile — `handleListCatalogStores` undefined, fake doesn't implement `ListStoreFacets`.

- [ ] **Step 3: Add the handler**

In `src/cmd/api-server/catalog.go`, add directly after `handleListCatalogDirectories`:

```go
func (s *server) handleListCatalogStores(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	receivedAfter, receivedBefore, ok := parseDateRange(w, q)
	if !ok {
		return
	}

	resp, err := s.catalog.ListStoreFacets(r.Context(), &pb.ListFacetsRequest{
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		Pattern:        q.Get("pattern"),
		SourceHosts:    splitCommaParam(q.Get("source_hosts")),
		JobNames:       splitCommaParam(q.Get("job_names")),
	})
	if err != nil {
		s.logger.Error("handleListCatalogStores: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	facets := make([]facetDTO, len(resp.GetFacets()))
	for i, f := range resp.GetFacets() {
		facets[i] = toFacetDTO(f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": facets})
}
```

- [ ] **Step 4: Register the route and extend the interface**

In `src/cmd/api-server/server.go`:
- Add `ListStoreFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)` to the `catalogQueryClient` interface, directly after its existing `ListDirectoryFacets` line.
- Add `mux.HandleFunc("GET /api/v1/catalog/stores", s.handleListCatalogStores)` directly after the existing `mux.HandleFunc("GET /api/v1/catalog/directories", s.handleListCatalogDirectories)` line.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go build ./... && go test ./cmd/api-server/... -v`
Expected: build succeeds; all tests PASS.

- [ ] **Step 6: Update docs**

In `docs/api/rest-v1.md`, add a new section mirroring the existing `## GET /api/v1/catalog/clients` (or whichever facet endpoint is documented there — copy its structure exactly) section, named `## GET /api/v1/catalog/stores`, describing: query params (`pattern`, `source_hosts`, `job_names`, `received_after`, `received_before`), response shape (`{"data": [{"name", "count", "last_seen"}]}`), and one sentence noting `name` is the store's `store_host`.

In `docs/components/api-server.md`, find wherever the existing catalog facet endpoints are listed and add `GET /catalog/stores` to that list with a one-line description ("distinct store hosts a pattern/filter combination matches, for restore-cart submission's store discovery — see [Restore Policy Verification design](../superpowers/specs/2026-08-10-restore-policy-verification-design.md)").

- [ ] **Step 7: Commit**

```bash
git add src/cmd/api-server/catalog.go src/cmd/api-server/server.go src/cmd/api-server/catalog_test.go docs/api/rest-v1.md docs/components/api-server.md
git commit -m "feat: add GET /api/v1/catalog/stores endpoint"
```

---

### Task 9: `api-server` — `POST /api/v1/restore` body + DTO rewrite

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/policies_test.go`
- Modify: `docs/api/rest-v1.md`

**Interfaces:**
- Consumes: `pb.CreatePolicyRequest.StoragePolicyId`/`.Rules` (Task 1), `pb.Policy.GetRules()`/`.GetStoragePolicyId()` (Task 1/3).
- Produces: `ruleDTO{Host, Path string; Include bool}`; `policyDTO.Rules []ruleDTO` (replaces `SourceStore`); `restorePolicyInput{Name, ClientFilters, StoragePolicyID, Rules, DisabledAt}` (replaces `SourceStore`/`Config`); updated `handleCreateRestore`.

- [ ] **Step 1: Write the failing test**

**Correction (found mid-execution):** the real existing test names in `policies_test.go` are
`TestHandleCreateRestore_ReturnsCreatedPolicy` (not `_ComposesCreatePolicyRequest`),
`TestHandleCreateRestore_BackendValidationErrorReturns400` (not
`_BackendValidationFailureMapsThroughWriteGRPCError`), and `TestToPolicyDTO_IncludesSourceStoreForRestore`
(needs renaming, its premise is gone). The real server-construction pattern is `newServer(nil, nil,
fake, testLogger())` (matching `newServer`'s actual signature — no `s.handleCreateRestore(rec, req)`
direct-call shortcut exists; every test in this file dispatches through a real
`http.NewServeMux()`/`srv.registerRoutes(mux)`/`mux.ServeHTTP(rec, req)`, and this task should keep
that convention rather than introduce a different style). Replace these three existing tests in
place (find them by their real names above), plus rewrite `TestToPolicyDTO_IncludesSourceStoreForRestore`
under a new name:

```go
func TestToPolicyDTO_IncludesRulesAndStoragePolicyIDForRestore(t *testing.T) {
	p := &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		StoragePolicyId: "sp-1",
		Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		Destinations:    []string{"bwfs-east.internal:8080"},
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, "sp-1", dto.StoragePolicyID)
	require.Len(t, dto.Rules, 1)
	assert.Equal(t, "web-01", dto.Rules[0].Host)
	assert.Equal(t, "/var/www/index.html", dto.Rules[0].Path)
	assert.True(t, dto.Rules[0].Include)
	assert.Equal(t, []string{"bwfs-east.internal:8080"}, dto.Destinations)
}

func TestHandleCreateRestore_ReturnsCreatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		StoragePolicyId: "sp-1",
		Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}, Labels: map[string]string{}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web01-emergency",
		"client_filters": {"hostnames": ["web-01"], "labels": {}},
		"storage_policy_id": "sp-1",
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "restore", fake.lastCreateReq.GetType())
	assert.Equal(t, "web01-emergency", fake.lastCreateReq.GetName())
	assert.Equal(t, []string{"web-01"}, fake.lastCreateReq.GetClientFilters().GetHostnames())
	assert.Equal(t, "sp-1", fake.lastCreateReq.GetStoragePolicyId())
	require.Len(t, fake.lastCreateReq.GetRules(), 1)
	assert.Equal(t, "web-01", fake.lastCreateReq.GetRules()[0].GetHost())

	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "sp-1", respBody["storage_policy_id"])
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
	fake := &fakePolicyServiceClient{createErr: status.Error(codes.InvalidArgument, "storage_policy_id not found")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", strings.NewReader(`{"name": "x", "storage_policy_id": "missing"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

Leave `TestHandleUpdatePolicy_RestoreTypeRejectedReturns400` untouched — check it first, but it
should not reference `source_store`/`config` at all (restore was never updatable, so this test
never had type-specific fields to begin with).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestHandleCreateRestore|TestToPolicyDTO_IncludesRulesAndStoragePolicyIDForRestore' -v`
Expected: FAIL to compile — `policyDTO` has no `Rules`/`StoragePolicyID` distinct handling for restore, `restorePolicyInput` has no `Rules`.

- [ ] **Step 3: Rewrite the restore-related types in `policies.go`**

Replace `policyDTO`'s `SourceStore` field:
```go
	StoragePolicyID string            `json:"storage_policy_id,omitempty"`
```
(already exists, unchanged — now also populated for restore) and remove:
```go
	SourceStore     string            `json:"source_store,omitempty"`
```
Add, in its place:
```go
	Rules           []ruleDTO         `json:"rules,omitempty"`
```

Add the `ruleDTO` type and its converter directly above `policyDTO`:
```go
type ruleDTO struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Include bool   `json:"include"`
}
```

In `toPolicyDTO`, remove `SourceStore: p.GetSourceStore(),` and add:
```go
	rules := make([]ruleDTO, len(p.GetRules()))
	for i, r := range p.GetRules() {
		rules[i] = ruleDTO{Host: r.GetHost(), Path: r.GetPath(), Include: r.GetInclude()}
	}
```
then set `Rules: rules,` in the returned `dto` literal (alongside the existing `StoragePolicyID: p.GetStoragePolicyId(),`, which needs no change).

Replace `restorePolicyInput`:
```go
type restorePolicyInput struct {
	Name            string           `json:"name"`
	ClientFilters   clientFiltersDTO `json:"client_filters"`
	StoragePolicyID string           `json:"storage_policy_id"`
	Rules           []ruleDTO        `json:"rules"`
	DisabledAt      int64            `json:"disabled_at,omitempty"`
}
```

Update `handleCreateRestore`:
```go
func (s *server) handleCreateRestore(w http.ResponseWriter, r *http.Request) {
	in, err := decodeRestorePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rules := make([]*pb.RestoreRule, len(in.Rules))
	for i, ru := range in.Rules {
		rules[i] = &pb.RestoreRule{Host: ru.Host, Path: ru.Path, Include: ru.Include}
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:            in.Name,
		Type:            "restore",
		ClientFilters:   toProtoClientFiltersInput(in.ClientFilters),
		StoragePolicyId: in.StoragePolicyID,
		Rules:           rules,
		DisabledAt:      disabledAtToProto(in.DisabledAt),
	})
	if err != nil {
		s.logger.Error("handleCreateRestore: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go build ./... && go test ./cmd/api-server/... -v`
Expected: build succeeds; all tests PASS.

- [ ] **Step 5: Update `docs/api/rest-v1.md`**

Find the existing `## POST /api/v1/restore` section and replace its example body/response and field description:

```markdown
## `POST /api/v1/restore`

...(keep the existing intro paragraph)...

**Body:**
```json
{
  "name": "web01-emergency",
  "client_filters": {"hostnames": ["web-01"], "labels": {}},
  "storage_policy_id": "<id of an existing \"storage\" policy>",
  "rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}]
}
```

`storage_policy_id` must reference an existing `"storage"`-typed policy -- its dial address is
resolved live from that policy's check-ins, exactly like a `"backup"` policy's `destinations`.
`rules` must contain at least one entry; an entry with `"host": null` (or omitted) is
host-agnostic, applying across every source host under `path`.

`400` if `name` is empty, `storage_policy_id` doesn't reference an existing `"storage"` policy, or
`rules` is empty or contains an entry with an empty `path`.
```

(Replace whatever the prior `source_store`/`config` example and validation-error prose said with the above, keeping the section's surrounding structure otherwise intact.)

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go docs/api/rest-v1.md
git commit -m "feat: rewrite POST /api/v1/restore for storage_policy_id and rules"
```

---

### Task 10: `policyclient` — cache restore's `rules`

**Files:**
- Modify: `src/cmd/policyclient/fetch.go`
- Modify: `src/cmd/policyclient/fetch_test.go`

**Interfaces:**
- Consumes: `pb.Policy.GetRules()` (Task 1/3).
- Produces: `CachedPolicy.Rules []RestoreRule` (new field); `policyclient`'s own `RestoreRule{Host, Path string; Include bool}` type.

- [ ] **Step 1: Write the failing test**

**Correction (found mid-execution):** there is no `TestToCachedPolicies_*` test in this file, and
`toCachedPolicies` is never called directly by any test — every test here (`TestRunFetch_*`) drives
the full `runFetch` path against a `fakePolicyServiceClient` and reads back the written cache file,
exactly like `TestRunFetch_StoragePolicyCarriesPortAndConfig` (~line 111) does for storage's
type-specific fields. Add, mirroring that exact pattern:

```go
func TestRunFetch_RestorePolicyCarriesStoragePolicyIdAndRules(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:              "restore-uuid-1",
				Name:            "web01-emergency",
				CreatedAt:       created,
				UpdatedAt:       updated,
				Type:            "restore",
				StoragePolicyId: "sp-1",
				Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
				Destinations:    []string{"bwfs-east.internal:8080"},
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
	assert.Equal(t, "restore", got[0].Type)
	assert.Equal(t, []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}}, got[0].Rules)
	assert.Equal(t, []string{"bwfs-east.internal:8080"}, got[0].Destinations)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/policyclient/... -run TestRunFetch_RestorePolicy -v`
Expected: FAIL to compile — `CachedPolicy` has no `Rules` field, `RestoreRule` undefined.

- [ ] **Step 3: Implement**

In `fetch.go`, add directly above `CachedPolicy`:
```go
// RestoreRule mirrors policy-server's RestoreRule -- {host, path, include},
// where an empty Host means the rule applies across every source host.
// Pure passthrough here; agent (cmd/agent/restore.go) is what interprets
// it, via rwfs verify --rules-stdin.
type RestoreRule struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Include bool   `json:"include"`
}
```

In `CachedPolicy`, add a field near the existing storage-only fields:
```go
	// "restore" policy only, zero/empty for every other type.
	Rules []RestoreRule `json:"rules,omitempty"`
```

In `toCachedPolicies`, add rule conversion and include it in the returned literal:
```go
		rules := make([]RestoreRule, 0, len(p.GetRules()))
		for _, r := range p.GetRules() {
			rules = append(rules, RestoreRule{Host: r.GetHost(), Path: r.GetPath(), Include: r.GetInclude()})
		}
		out = append(out, CachedPolicy{
			// ...all existing fields unchanged...
			Rules: rules,
		})
```
(merge this into the existing `out = append(out, CachedPolicy{...})` literal rather than duplicating it — add the `rules := ...` line right before that `append` call, and add `Rules: rules,` as one more field in the existing literal.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policyclient/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go
git commit -m "feat: cache restore policy rules in policies-cache.json"
```

---

### Task 11: `agent` — thread `Stdin` through the runner

**Files:**
- Modify: `src/cmd/agent/reconcile.go`
- Modify: `src/cmd/agent/policy.go`
- Modify: `src/cmd/agent/reconcile_test.go`

**Interfaces:**
- Produces: `Policy.Stdin []byte` (new field, zero-value for every existing policy); `type runner func(ctx context.Context, binary string, args []string, stdin []byte) error` (signature change).

- [ ] **Step 1: Write the failing test**

In `reconcile_test.go`, update `fakeRunner` to capture stdin, and add a test:

```go
type fakeRunner struct {
	mu        sync.Mutex
	calls     int
	failN     int
	lastStdin []byte
}

func (f *fakeRunner) run(ctx context.Context, binary string, args []string, stdin []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastStdin = stdin
	if f.failN > 0 {
		f.failN--
		return errors.New("simulated failure")
	}
	return nil
}
```
(this replaces the existing `fakeRunner`/`run` definitions in place — every existing call site that passes `fr.run` as `execute runner` keeps compiling unchanged, since it's still passed by method value.)

Add:
```go
func TestRun_StdinIsPassedThroughToRunner(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	fr := &fakeRunner{}
	p := Policy{ID: "restore:x", Binary: "rwfs", Args: []string{"verify"}, Stdin: []byte(`{"rules":[]}`)}
	policiesFunc := func() ([]Policy, bool) { return []Policy{p}, true }

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_ = run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, policiesFunc, 2, nil, nil, nil)

	assert.Equal(t, []byte(`{"rules":[]}`), fr.lastStdin)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/agent/... -run TestRun_StdinIsPassed -v`
Expected: FAIL to compile — `Policy` has no field `Stdin`, `run`'s `execute` parameter type mismatch.

- [ ] **Step 3: Implement**

In `policy.go`, add to the `Policy` struct (after `Background bool`):
```go
	// Stdin, when non-nil, is piped to the exec'd binary's standard input --
	// used by restore.go's one-shot verification tasks to pass a policy's
	// rules to `rwfs verify --rules-stdin`. Every other policy/task leaves
	// this nil.
	Stdin []byte
```

In `reconcile.go`:
- Change the `runner` type:
```go
type runner func(ctx context.Context, binary string, args []string, stdin []byte) error
```
- Change `realExec`:
```go
func realExec(ctx context.Context, binary string, args []string, stdin []byte) error {
	cmd := exec.CommandContext(ctx, resolveExecPath(binary), args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.Run()
}
```
(add `"bytes"` to the import block.)
- Update both call sites in `run()` (the `Background` branch and the synchronous branch) from `execute(ctx, p.Binary, p.Args)` to `execute(ctx, p.Binary, p.Args, p.Stdin)`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go build ./... && go test ./cmd/agent/... -v`
Expected: build succeeds; every existing `reconcile_test.go` test still PASSes unchanged (they don't inspect stdin), plus the new test.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/policy.go src/cmd/agent/reconcile_test.go
git commit -m "feat: thread stdin through agent's policy exec runner"
```

---

### Task 12: `agent` — restore task derivation

**Files:**
- Create: `src/cmd/agent/restore.go`
- Create: `src/cmd/agent/restore_test.go`
- Modify: `src/cmd/agent/backup.go` (shared `cachedPolicy` struct)
- Modify: `src/cmd/agent/main.go`
- Modify: `docs/components/agent.md`
- Modify: `docs/ARCHITECTURE.md`

**Interfaces:**
- Consumes: `cachedPolicy` (extended below), `readCachedPolicies` (existing, `backup.go`), `Policy`/`Stdin` (Task 11), `slug` (existing, `backup.go`).
- Produces: `RestoreRule{Host, Path string; Include bool}` (agent's own duplicate type — agent can't import `cmd/policyclient`, same constraint `backup.go`'s header comment already documents for `ObjectFilter`); `restoreTasks(policiesCachePath string, logger *slog.Logger) ([]Policy, bool)`.

- [ ] **Step 1: Extend `cachedPolicy` in `backup.go`**

Add to the `cachedPolicy` struct, near the existing storage-only fields comment:
```go
	// "restore" policy only, zero/empty for every other type.
	Rules []RestoreRule `json:"rules,omitempty"`
```

- [ ] **Step 2: Write the failing tests**

Create `src/cmd/agent/restore_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCachedPolicies(t *testing.T, path string, policies []cachedPolicy) {
	t.Helper()
	data, err := json.Marshal(policies)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func TestRestoreTasks_OneTaskPerRestorePolicy(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPolicies(t, cachePath, []cachedPolicy{
		{
			Name: "web01-emergency", Type: "restore",
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		},
		{Name: "nightly", Type: "backup"}, // must contribute zero restore tasks
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, "restore:web01-emergency", tasks[0].ID)
	assert.Equal(t, "rwfs", tasks[0].Binary)
	assert.True(t, strings.HasPrefix(tasks[0].JobID, "restore:web01-emergency:"), "job id must be stamped with the policy name")
	assert.Equal(t, []string{"verify", "bwfs-1:8080", "--rules-stdin", "--job-id", tasks[0].JobID}, tasks[0].Args)
	assert.True(t, tasks[0].Background)

	var payload struct {
		Rules []RestoreRule `json:"rules"`
	}
	require.NoError(t, json.Unmarshal(tasks[0].Stdin, &payload))
	assert.Equal(t, []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}}, payload.Rules)
}

func TestRestoreTasks_NoDestinationsSkipsWithNoTask(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPolicies(t, cachePath, []cachedPolicy{
		{Name: "dangling", Type: "restore", Rules: []RestoreRule{{Path: "/x", Include: true}}},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	assert.Empty(t, tasks)
}

func TestRestoreTasks_DisabledPolicySkipped(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPolicies(t, cachePath, []cachedPolicy{
		{
			Name: "old", Type: "restore",
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Path: "/x", Include: true}},
			DisabledAt:   time.Now().Add(-time.Hour),
		},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	assert.Empty(t, tasks)
}

func TestRestoreTasks_UnreadableCacheReturnsNotOK(t *testing.T) {
	_, ok := restoreTasks(filepath.Join(t.TempDir(), "missing.json"), testLogger())
	assert.False(t, ok)
}

func TestRestoreTasks_DueUntilFirstSuccessThenNeverAgain(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPolicies(t, cachePath, []cachedPolicy{
		{Name: "x", Type: "restore", Destinations: []string{"bwfs-1:8080"}, Rules: []RestoreRule{{Path: "/x", Include: true}}},
	})
	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)

	now := time.Now()
	assert.True(t, tasks[0].Due(PolicyState{}, now), "never succeeded is due")
	success := now.Add(-time.Minute)
	assert.False(t, tasks[0].Due(PolicyState{LastSuccessAt: &success}, now), "succeeded once is never due again")
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestRestoreTasks -v`
Expected: FAIL to compile — `restoreTasks`/`RestoreRule` undefined.

- [ ] **Step 4: Implement `restore.go`**

```go
// restore.go derives agent's dynamic "restore verification" tasks from
// policies-cache.json -- one task per cached "restore" policy, one-shot:
// due until it succeeds once, never again after. See
// docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// RestoreRule mirrors policyclient's on-disk RestoreRule (cmd/policyclient/
// fetch.go) that agent needs. agent can't import cmd/policyclient directly
// -- Go forbids importing another command's main package -- so this field
// set is duplicated here, the same way backup.go's ObjectFilter already is.
type RestoreRule struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Include bool   `json:"include"`
}

// restoreTaskID is the stable identifier for one restore policy's task in
// agent-state.json -- one task per policy (not per host, unlike backup's
// per-object-filter-path tasks -- a restore policy's rules aren't cleanly
// partitionable by host, since a folder rule can be host-agnostic).
func restoreTaskID(policyName string) string {
	return fmt.Sprintf("restore:%s", policyName)
}

// restoreJobID is the --job-id passed to rwfs verify for one run --
// includes a timestamp so a retry after failure gets a distinct id,
// mirroring backup.go's backupJobID.
func restoreJobID(policyName string, now time.Time) string {
	return fmt.Sprintf("restore:%s:%d", policyName, now.Unix())
}

// rulesStdinPayload is the JSON shape piped to `rwfs verify --rules-stdin`
// -- {"rules": [...]}, matching policy-server's RestorePolicy.Rules field
// name exactly (see docs/superpowers/specs/
// 2026-08-10-restore-policy-verification-design.md's §4).
type rulesStdinPayload struct {
	Rules []RestoreRule `json:"rules"`
}

// restoreTasks derives one Policy per cached "restore" policy from
// policiesCachePath, valid at the instant it's called -- callers that need
// to notice policies-cache.json changing over time (agent serve's
// reconcile loop) must call this fresh every tick, exactly like
// backupTasks/storageTasks.
//
// ok=false mirrors backupTasks's contract: it means this tick's read of
// policiesCachePath failed, and callers must never treat that as "there
// are zero restore tasks."
//
// A policy whose Destinations is empty (its storage policy has no live
// checkins yet, or storage_policy_id is dangling) contributes no task --
// rather than exec'ing rwfs verify against an empty target, which would
// fail loudly anyway but with a less useful error than simply not trying.
// Each skip is logged with the policy name. A disabled policy is skipped
// the same way backup/storage policies already are.
func restoreTasks(policiesCachePath string, logger *slog.Logger) ([]Policy, bool) {
	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []Policy
	for _, p := range cachedPolicies {
		if p.Type != "restore" {
			continue
		}
		if p.disabled(time.Now()) {
			continue
		}
		if len(p.Destinations) == 0 {
			logger.Error("restore policy has no resolved destination, skipping", "policy", restoreTaskID(p.Name))
			continue
		}

		payload, err := json.Marshal(rulesStdinPayload{Rules: p.Rules})
		if err != nil {
			logger.Error("restore policy rules failed to marshal, skipping", "policy", restoreTaskID(p.Name), "error", err)
			continue
		}

		jobID := restoreJobID(p.Name, time.Now())
		tasks = append(tasks, Policy{
			ID:         restoreTaskID(p.Name),
			Binary:     "rwfs",
			JobID:      jobID,
			Args:       []string{"verify", p.Destinations[0], "--rules-stdin", "--job-id", jobID},
			Stdin:      payload,
			Background: true,
			Due: func(s PolicyState, now time.Time) bool {
				return s.LastSuccessAt == nil
			},
		})
	}
	return tasks, true
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go build ./... && go test ./cmd/agent/... -v`
Expected: build succeeds; all tests PASS.

- [ ] **Step 6: Wire into `main.go`**

In `main.go`, replace:
```go
			policiesFunc := func() ([]Policy, bool) {
				tasks, ok := backupTasks(policiesCachePath, logger, conf)
				return append(policies(conf), tasks...), ok
			}
```
with:
```go
			policiesFunc := func() ([]Policy, bool) {
				backupTaskList, backupOk := backupTasks(policiesCachePath, logger, conf)
				restoreTaskList, restoreOk := restoreTasks(policiesCachePath, logger)
				all := append(policies(conf), backupTaskList...)
				all = append(all, restoreTaskList...)
				return all, backupOk && restoreOk
			}
```

And in the `"list-policies"` case, replace:
```go
			backupTaskList, _ := backupTasks(policiesCachePath, silentLogger, conf)
			allPolicies := append(policies(conf), backupTaskList...)
```
with:
```go
			backupTaskList, _ := backupTasks(policiesCachePath, silentLogger, conf)
			restoreTaskList, _ := restoreTasks(policiesCachePath, silentLogger)
			allPolicies := append(policies(conf), backupTaskList...)
			allPolicies = append(allPolicies, restoreTaskList...)
```

- [ ] **Step 7: Run the full agent test suite**

Run: `cd src && go build ./... && go test ./cmd/agent/... -v`
Expected: PASS.

- [ ] **Step 8: Update `docs/components/agent.md`**

Add a new `## Policy-driven restore verification` section, directly after the existing `## Storage-policy supervision` section, mirroring "Policy-driven backup execution"'s structure:

```markdown
## Policy-driven restore verification

Every reconcile tick, alongside backup tasks and storage supervision, `agent` derives one
verification task per cached `"restore"`-typed policy (ID: `restore:<policy-name>`) — unlike a
backup task, there is exactly one task per policy, not one per rule or per host, since a restore
policy's `rules` aren't cleanly partitionable by host (a folder rule can be host-agnostic). A
policy whose `destinations` is empty (its `storage_policy_id` has no live checkins yet, or is
dangling) contributes no task, logged the same way an unresolved backup destination already is.

A restore task is **one-shot**: due until it first succeeds, retried with the same jittered
backoff every other failing policy uses, and never dispatched again afterward for as long as this
exact policy still appears in `policies-cache.json` (a restore policy is deletable — deleting it
removes its task the same way any orphaned task's `agent-state.json` entry is pruned).

When due, `agent` execs `rwfs verify <destinations[0]> --rules-stdin --job-id
restore:<policy>:<timestamp>`, piping the policy's `rules` as `{"rules": [...]}` on the child's
standard input — see [rwfs](./rwfs.md)'s `--rules-stdin` mode for how that's resolved into an
actual pass/fail. `list-policies` shows each restore task as an additional row
(`restore:<policy>`), same columns as everything else; a permanently-succeeded one-shot task's
`NEXT RUN` column reads "due now" even though it will never run again — a known, accepted display
quirk (see [Design: Restore Policy Verification Execution](../superpowers/specs/2026-08-10-restore-policy-verification-design.md)), not a functional bug.
```

Also update this file's intro paragraph (the one listing agent's "two kinds of dynamic work") to mention the third: restore verification.

- [ ] **Step 9: Update `docs/ARCHITECTURE.md`**

In the `agent` component-table row and its longer paragraph below the table (the one starting `` `agent` is a node-level process that wraps `certclient`...``), add a clause mentioning restore-policy verification alongside backup execution and storage supervision, e.g. appending to that paragraph: `` `agent` additionally derives one one-shot verification task per cached `"restore"`-typed policy, executing `rwfs verify` against the resolved source `bwfs` -- see [agent](components/agent.md#policy-driven-restore-verification). ``

- [ ] **Step 10: Commit**

```bash
git add src/cmd/agent/restore.go src/cmd/agent/restore_test.go src/cmd/agent/backup.go src/cmd/agent/main.go docs/components/agent.md docs/ARCHITECTURE.md
git commit -m "feat: derive one-shot restore verification tasks from cached restore policies"
```

---

### Task 13: `rwfs` — `--rules-stdin` flag and hostname-default bypass

**Files:**
- Modify: `src/cmd/rwfs/arguments.go`
- Modify: `src/cmd/rwfs/arguments_test.go`

**Interfaces:**
- Produces: `Arguments.RulesStdin bool` (new field); when true, `Arguments.ServerName`/`.PathFilter` are left `""` regardless of the positional (or lack of one), instead of defaulting `ServerName` to the local hostname.

- [ ] **Step 1: Write the failing tests**

Add to `arguments_test.go`:

```go
func TestParseArguments_VerifyRulesStdinLeavesServerNameEmpty(t *testing.T) {
	withArgs(t, []string{"rwfs", "verify", "localhost:8080", "--rules-stdin"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.True(t, args.RulesStdin)
		assert.Equal(t, "", args.ServerName, "rules-stdin must not default to the local hostname")
		assert.Equal(t, "", args.PathFilter)
	})
}

func TestParseArguments_VerifyWithoutRulesStdinStillDefaultsServerNameToLocalHostname(t *testing.T) {
	withArgs(t, []string{"rwfs", "verify", "localhost:8080"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.False(t, args.RulesStdin)
		assert.NotEqual(t, "", args.ServerName, "existing behavior must be unchanged when the flag is absent")
	})
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestParseArguments_VerifyRulesStdin -v`
Expected: FAIL to compile — `Arguments` has no field `RulesStdin`.

- [ ] **Step 3: Implement**

In `arguments.go`:
- Add `RulesStdin bool // verify only` to the `Arguments` struct, near `Streams`/`Retries`.
- Add `verifyCmd.Flags().BoolVar(&args.RulesStdin, "rules-stdin", false, "Read {\"rules\":[{host,path,include}]} from stdin and verify only matching files")` alongside `verifyCmd`'s other flag registrations.
- Change the hostname-defaulting block near the end of `parseArguments` from:
```go
	serverName, path, err := common.ParseServerPath(args.listPositional)
	if err != nil {
		return nil, fmt.Errorf("positional error: %w", err)
	}
	if serverName == "" {
		serverName = common.GetHostname()
	}
	args.ServerName = serverName
	args.PathFilter = path
```
to:
```go
	serverName, path, err := common.ParseServerPath(args.listPositional)
	if err != nil {
		return nil, fmt.Errorf("positional error: %w", err)
	}
	if serverName == "" && !args.RulesStdin {
		serverName = common.GetHostname()
	}
	args.ServerName = serverName
	args.PathFilter = path
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go build ./... && go test ./cmd/rwfs/... -v`
Expected: build succeeds; all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/rwfs/arguments.go src/cmd/rwfs/arguments_test.go
git commit -m "feat: add rwfs verify --rules-stdin flag"
```

---

### Task 14: `rwfs` — ported rule resolution

**Files:**
- Create: `src/cmd/rwfs/rules.go`
- Create: `src/cmd/rwfs/rules_test.go`

**Interfaces:**
- Produces: `RestoreRule{Host, Path string; Include bool}`; `resolveRestoreFile(rules []RestoreRule, host, path string) bool`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/rwfs/rules_test.go` — these mirror `web/src/utils/restoreRules.spec.js`'s `resolveFile` cases exactly (read that file's test names/cases before writing this, to make sure every case it covers has a Go equivalent here — at minimum):

```go
package main

import "testing"

func TestResolveRestoreFile_NoRulesIsUnselected(t *testing.T) {
	if resolveRestoreFile(nil, "web-01", "/var/log/x") {
		t.Fatal("expected unselected with no rules")
	}
}

func TestResolveRestoreFile_HostAgnosticFolderRuleSelectsDescendant(t *testing.T) {
	rules := []RestoreRule{{Host: "", Path: "/var/log", Include: true}}
	if !resolveRestoreFile(rules, "any-host", "/var/log/nested/x.log") {
		t.Fatal("expected selected: host-agnostic folder rule covers any host")
	}
}

func TestResolveRestoreFile_HostAgnosticFolderRuleDoesNotOverMatchSiblingPath(t *testing.T) {
	rules := []RestoreRule{{Host: "", Path: "/var/log", Include: true}}
	if resolveRestoreFile(rules, "any-host", "/var/log2/x.log") {
		t.Fatal("/var/log2 is not a descendant of /var/log despite the string prefix match")
	}
}

func TestResolveRestoreFile_ExactHostSpecificRuleWinsOverFolderRule(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/var/log", Include: true},
		{Host: "web-01", Path: "/var/log/app.log", Include: false},
	}
	if resolveRestoreFile(rules, "web-01", "/var/log/app.log") {
		t.Fatal("exact file-level exclude must win over the folder-level include")
	}
	if !resolveRestoreFile(rules, "web-02", "/var/log/app.log") {
		t.Fatal("the exclude is scoped to web-01 only; web-02's copy stays included")
	}
}

func TestResolveRestoreFile_LongestMatchingAncestorWins(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/var", Include: true},
		{Host: "", Path: "/var/log", Include: false},
	}
	if resolveRestoreFile(rules, "any-host", "/var/log/x") {
		t.Fatal("the more specific /var/log exclude must win over the /var include")
	}
	if !resolveRestoreFile(rules, "any-host", "/var/lib/x") {
		t.Fatal("/var/lib isn't covered by the /var/log exclude, so the /var include applies")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestResolveRestoreFile -v`
Expected: FAIL to compile — `resolveRestoreFile`/`RestoreRule` undefined.

- [ ] **Step 3: Implement `rules.go`**

```go
// rules.go ports web/src/utils/restoreRules.js's resolveFile (longest-
// matching-ancestor-rule-wins) to Go, so `rwfs verify --rules-stdin` can
// resolve a restore policy's rules against a real ListFiles result without
// policy-server or agent ever needing to interpret rule semantics
// themselves. Kept behaviorally identical to the JS original -- see
// docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md.
package main

import "strings"

// RestoreRule mirrors policy-server's RestoreRule / the restore cart's rule
// shape -- {host, path, include}. Host == "" means host-agnostic.
type RestoreRule struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Include bool   `json:"include"`
}

// splitRestorePath derives (parent, base) from p, mirroring
// cmd/catalog/pathsplit.go's splitPath exactly (duplicated here -- rwfs
// can't import cmd/catalog, another command's main package). Root paths
// keep a trailing separator; "" means "no known parent" (p had no
// separator at all).
func splitRestorePath(p string) (parent, base string) {
	if p == "" {
		return "", ""
	}
	sep := byte('/')
	if isWindowsStyleRestorePath(p) {
		sep = '\\'
		if !strings.ContainsRune(p, '\\') {
			sep = '/'
		}
	}
	idx := strings.LastIndexByte(p, sep)
	if idx < 0 {
		return "", p
	}
	parent, base = p[:idx], p[idx+1:]
	if base == "" {
		return splitRestorePath(p[:idx])
	}
	if parent == "" {
		parent = string(sep)
	} else if isDriveRootRestorePath(parent) {
		parent += string(sep)
	}
	return parent, base
}

func isWindowsStyleRestorePath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 2 && isDriveRootRestorePath(p[:2])
}

func isDriveRootRestorePath(s string) bool {
	return len(s) == 2 && s[1] == ':' && isASCIILetterRestorePath(s[0])
}

func isASCIILetterRestorePath(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ancestorsOrSelfRestorePath returns path's ancestor chain, root first,
// path itself last -- mirrors web/src/utils/pathSplit.js's pathCrumbs, but
// returning only the path strings (this package never needs display
// names).
func ancestorsOrSelfRestorePath(path string) []string {
	var chain []string
	current := path
	for current != "" {
		chain = append(chain, current)
		parent, _ := splitRestorePath(current)
		if parent == current {
			break // true root reached (splitRestorePath returns itself unchanged at a drive/UNC root)
		}
		current = parent
	}
	// reverse into root-first order
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// longestMatchingFolderRule finds the most specific host-agnostic folder
// rule covering path (checking path itself before its ancestors), mirrors
// restoreRules.js's function of the same name.
func longestMatchingFolderRule(rules []RestoreRule, path string) (include bool, found bool) {
	chain := ancestorsOrSelfRestorePath(path)
	for i := len(chain) - 1; i >= 0; i-- {
		for _, r := range rules {
			if r.Host == "" && r.Path == chain[i] {
				return r.Include, true
			}
		}
	}
	return false, false
}

// resolveRestoreFile reports whether (host, path) is selected: an exact
// host-specific rule wins outright; otherwise the longest matching
// host-agnostic ancestor folder rule applies; no match = unselected.
// Mirrors restoreRules.js's resolveFile exactly.
func resolveRestoreFile(rules []RestoreRule, host, path string) bool {
	for _, r := range rules {
		if r.Host == host && r.Path == path {
			return r.Include
		}
	}
	include, found := longestMatchingFolderRule(rules, path)
	return found && include
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestResolveRestoreFile -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/rwfs/rules.go src/cmd/rwfs/rules_test.go
git commit -m "feat: port restore-cart rule resolution to rwfs"
```

---

### Task 15: `rwfs verify` — wire `--rules-stdin` into the verify run

**Files:**
- Modify: `src/cmd/rwfs/verify.go`
- Modify: `src/cmd/rwfs/main.go`
- Create: `src/cmd/rwfs/verify_test.go`
- Modify: `docs/components/rwfs.md`
- Modify: `docs/protocols/restore.md`

**Interfaces:**
- Consumes: `resolveRestoreFile`/`RestoreRule` (Task 14), `Arguments.RulesStdin` (Task 13).
- Produces: `runVerify`'s signature gains a `rulesStdin bool` and a `stdin io.Reader` parameter; a file-level rule (non-empty `Host`) matching zero rows is a reported failure; a folder-level rule (empty `Host`) matching zero rows is not.

- [ ] **Step 1: Write the failing tests**

`applyRulesStdin` (added in Step 3 below) is a pure function — rows in, selected+not-found out —
deliberately factored out of the network-calling `runVerify` so it's directly unit-testable
without a fake gRPC client or an in-process server. Create `src/cmd/rwfs/verify_test.go`:

```go
package main

import (
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyRulesStdin_FileLevelRuleWithNoMatchingRowFails(t *testing.T) {
	rows := []*pb.FileRow{
		{Source: "web-01", Path: "/var/www/index.html", Type: "f", Size: 10, FileUuid: "u1"},
	}
	rules := []RestoreRule{
		{Host: "web-01", Path: "/var/www/index.html", Include: true},
		{Host: "web-01", Path: "/missing.txt", Include: true},
	}
	selected, notFound := applyRulesStdin(rows, rules)
	require.Len(t, selected, 1)
	assert.Equal(t, "u1", selected[0].FileUuid)
	require.Len(t, notFound, 1)
	assert.Equal(t, "/missing.txt", notFound[0].Path)
}

func TestApplyRulesStdin_FolderLevelRuleWithNoMatchingRowIsNotAFailure(t *testing.T) {
	rows := []*pb.FileRow{}
	rules := []RestoreRule{{Host: "", Path: "/empty/folder", Include: true}}
	selected, notFound := applyRulesStdin(rows, rules)
	assert.Empty(t, selected)
	assert.Empty(t, notFound, "a folder rule matching nothing is not itself a failure")
}

func TestApplyRulesStdin_ExcludedRowIsNotSelected(t *testing.T) {
	rows := []*pb.FileRow{{Source: "web-01", Path: "/var/log/app.log", Type: "f", Size: 5, FileUuid: "u2"}}
	rules := []RestoreRule{
		{Host: "", Path: "/var/log", Include: true},
		{Host: "web-01", Path: "/var/log/app.log", Include: false},
	}
	selected, notFound := applyRulesStdin(rows, rules)
	assert.Empty(t, selected)
	assert.Empty(t, notFound)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestApplyRulesStdin -v`
Expected: FAIL to compile — `applyRulesStdin` undefined.

- [ ] **Step 3: Implement**

In `verify.go`, add:

```go
// rulesStdinPayload is the JSON shape read from stdin when --rules-stdin is
// set -- {"rules": [...]}, the same field name policy-server's
// RestorePolicy.Rules and agent's restore.go use.
type rulesStdinPayload struct {
	Rules []RestoreRule `json:"rules"`
}

// notFoundRule records a file-level rule (non-empty Host) that matched no
// row in the ListFiles result -- reported as a verification failure,
// unlike a folder-level rule (empty Host) matching nothing, which is a
// legitimate outcome (an empty or already-fully-excluded folder), not an
// error.
type notFoundRule struct {
	Host string
	Path string
}

// applyRulesStdin resolves rules against rows (a ListFiles result) using
// resolveRestoreFile (rules.go), returning the rows that are actually
// selected for verification and any file-level rule that matched nothing.
func applyRulesStdin(rows []*pb.FileRow, rules []RestoreRule) (selected []*pb.FileRow, notFound []notFoundRule) {
	for _, row := range rows {
		if row.Type == "f" && row.Size > 0 && resolveRestoreFile(rules, row.Source, row.Path) {
			selected = append(selected, row)
		}
	}
	for _, r := range rules {
		if r.Host == "" || !r.Include {
			continue
		}
		found := false
		for _, row := range rows {
			if row.Source == r.Host && row.Path == r.Path {
				found = true
				break
			}
		}
		if !found {
			notFound = append(notFound, notFoundRule{Host: r.Host, Path: r.Path})
		}
	}
	return selected, notFound
}
```

Now wire it into `runVerify`. Change its signature to accept the new mode and an `io.Reader` for stdin (production always passes `os.Stdin`):

```go
func runVerify(logger *slog.Logger, host string, port int, serverName, pathFilter, filter string, rulesStdin bool, stdin io.Reader, streams, retries int, quiet bool, certsDir string) error {
	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	listClient := pb.NewListServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resp, err := listClient.ListFiles(ctx, &pb.ListRequest{
		ServerName: serverName,
		Path:       pathFilter,
		Filter:     filter,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	var rows []*pb.FileRow
	for _, r := range resp.Rows {
		if r.Type == "f" && r.Size > 0 {
			rows = append(rows, r)
		}
	}

	var notFound []notFoundRule
	if rulesStdin {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("read rules from stdin: %w", err)
		}
		var payload rulesStdinPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			return fmt.Errorf("parse rules from stdin: %w", err)
		}
		rows, notFound = applyRulesStdin(rows, payload.Rules)
	}

	if len(rows) == 0 && len(notFound) == 0 {
		logger.Info("summary", "verified", 0, "warnings", 0)
		return nil
	}

	// ...unchanged worker-pool verification loop below (streams/resultCh/wg)...
```

Add `"encoding/json"` and `"io"` to the import block. After the existing `for result := range resultCh { ... }` loop and before its final `logger.Info("summary", ...)`/return, add the not-found accounting:

```go
	for _, nf := range notFound {
		warnings++
		logger.Warn("verification failed", "source", nf.Host, "path", nf.Path, "reason", "not found on this store")
	}

	logger.Info("summary", "verified", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed verification", warnings)
	}
	return nil
}
```
(this replaces the existing tail of the function — the `warnings` variable already exists from the pre-existing loop; just add the `notFound` loop directly before the final summary/return that already reads `warnings`.)

In `main.go`, update the call site that invokes `runVerify` to pass the two new parameters:
```go
		if err := runVerify(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.ServerName, arguments.PathFilter, arguments.Filter, arguments.RulesStdin, os.Stdin, arguments.Streams, arguments.Retries, arguments.Quiet, certsDir); err != nil {
```
(add `"os"` to `main.go`'s imports if not already present — check first, it very likely already is for other flag/exit handling.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go build ./... && go test ./cmd/rwfs/... -v`
Expected: build succeeds; all tests PASS.

- [ ] **Step 5: Update `docs/components/rwfs.md`**

In the `## verify` section, add a new subsection after the existing `### Flags` table:

```markdown
### Restore rule verification (`--rules-stdin`)

```bash
# Verify exactly the files a restore policy's rules select, piped as JSON
echo '{"rules":[{"host":"web-01","path":"/var/www/index.html","include":true}]}' \
  | rwfs verify localhost:8080 --rules-stdin
```

When set, `rwfs verify` ignores the positional `[[server_name:]path]` filter and `--filter`, and
instead reads `{"rules":[{"host","path","include"}, ...]}` from stdin -- the same rule shape
`policy-server`'s `"restore"` policy type and the web restore cart both already use (host-agnostic
folder rules have an empty/omitted `host`; longest-matching-rule wins, exactly like
`.gitignore`). Every file on the server is resolved against the rule set (not filtered by
`server_name`/`path` first) and only included matches are verified.

A **file-level** rule (non-empty `host`, `include: true`) that matches nothing is reported as a
verification failure ("not found on this store") -- it named one specific file, and it wasn't
there. A **folder-level** rule (empty `host`) matching nothing is not a failure -- an empty (or
fully-excluded) folder is a legitimate outcome.

Used by `agent`'s restore-policy verification tasks (see
[agent](./agent.md#policy-driven-restore-verification)) — never combined with `--filter` or the
positional filter in that usage.
```

- [ ] **Step 6: Update `docs/protocols/restore.md`**

In the "CLI → RPC Mapping" section, add directly after the existing `rwfs verify myhost:/var/log ...` example:

```markdown
With `--rules-stdin`, the `ListFiles` call omits `server_name`/`path` entirely (fetches every row
on the server) and the returned rows are instead resolved against the piped rule set client-side,
in `rwfs` itself -- no protocol change; this is purely a different way `rwfs` decides which
`file_uuid`s to call `RestoreFile` for.
```

- [ ] **Step 7: Commit**

```bash
git add src/cmd/rwfs/verify.go src/cmd/rwfs/main.go src/cmd/rwfs/verify_test.go docs/components/rwfs.md docs/protocols/restore.md
git commit -m "feat: verify against piped restore rules in rwfs verify"
```

---

### Task 16: `web` — simplified restore submission

**Files:**
- Modify: `web/src/stores/restoreSubmission.js`
- Modify: `web/src/stores/restoreSubmission.spec.js`

**Interfaces:**
- Consumes: `apiFetch` (existing, `web/src/api/client.js`), `useRestoreCartStore` (existing — `cart.rules`), `useStoragePoliciesStore` (existing — `.list[].id`/`.checkins[].hostname`), `useRestorePoliciesStore.create` (existing, posts to `/restore` — unchanged interface, just a different body shape now).
- Produces: `useRestoreSubmissionStore.submit(destinationHost)` — same public signature, rewritten body.

- [ ] **Step 1: Write the failing tests**

Replace `restoreSubmission.spec.js`'s existing test bodies (keep the file's existing mocking setup for `apiFetch`/the pinia stores if already present; adapt to the new flow):

```js
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestoreSubmissionStore } from './restoreSubmission'
import { useRestoreCartStore } from './restoreCart'
import { useStoragePoliciesStore } from './storagePolicies'
import { useRestorePoliciesStore } from './restorePolicies'
import { apiFetch } from '../api/client'

vi.mock('../api/client')

describe('restoreSubmission', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('creates one restore policy per distinct storage_policy_id touched, sending the full rule list', async () => {
    const cart = useRestoreCartStore()
    cart.rules = [{ host: null, path: '/var/log', include: true }]

    apiFetch.mockImplementation((url) => {
      if (url.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'bwfs-1', count: 3, last_seen: 100 }] })
      }
      throw new Error(`unexpected apiFetch call: ${url}`)
    })

    const storagePolicies = useStoragePoliciesStore()
    storagePolicies.list = [
      { id: 'sp-1', checkins: [{ hostname: 'bwfs-1', last_seen_at: 100 }] },
    ]
    vi.spyOn(storagePolicies, 'fetchAll').mockResolvedValue()

    const restorePolicies = useRestorePoliciesStore()
    vi.spyOn(restorePolicies, 'create').mockResolvedValue({ id: 'r1' })

    const submission = useRestoreSubmissionStore()
    await submission.submit('dest-host')

    expect(restorePolicies.create).toHaveBeenCalledTimes(1)
    expect(restorePolicies.create).toHaveBeenCalledWith(
      expect.objectContaining({
        client_filters: { hostnames: ['dest-host'], labels: {} },
        storage_policy_id: 'sp-1',
        rules: cart.rules,
      })
    )
    expect(submission.results).toEqual([{ storeHost: 'bwfs-1', status: 'success', policy: { id: 'r1' } }])
  })

  it('reports an error for a store with no matching storage policy', async () => {
    const cart = useRestoreCartStore()
    cart.rules = [{ host: null, path: '/var/log', include: true }]
    apiFetch.mockResolvedValue({ data: [{ name: 'bwfs-unknown', count: 1, last_seen: 100 }] })

    const storagePolicies = useStoragePoliciesStore()
    storagePolicies.list = []
    vi.spyOn(storagePolicies, 'fetchAll').mockResolvedValue()

    const submission = useRestoreSubmissionStore()
    await submission.submit('dest-host')

    expect(submission.results).toEqual([
      { storeHost: 'bwfs-unknown', status: 'error', message: expect.stringContaining('bwfs-unknown') },
    ])
  })

  it('sets an error and does not call the backend when the cart has no selections', async () => {
    const submission = useRestoreSubmissionStore()
    await submission.submit('dest-host')
    expect(submission.error).toBe('Nothing selected for restore.')
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: FAIL — current implementation calls `/catalog?...` (paginated) and `groupByStore`/`filterResolved`, not `/catalog/stores`, and posts `source_store`/`config` instead of `storage_policy_id`/`rules`.

- [ ] **Step 3: Rewrite `restoreSubmission.js`**

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { useRestoreCartStore } from './restoreCart'
import { useStoragePoliciesStore } from './storagePolicies'
import { useRestorePoliciesStore } from './restorePolicies'

// distinctPositiveEntries returns cart.entries (the positively-selected
// top-level rules), deduped by (host, path) -- submitting the same
// top-level selection twice would otherwise issue a redundant facet query.
function distinctPositiveEntries(entries) {
  const seen = new Set()
  return entries.filter((e) => {
    const key = `${e.host ?? ''}:${e.path}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function buildStoreFacetsQuery(entry) {
  const params = new URLSearchParams()
  if (entry.host) params.set('source_hosts', entry.host)
  params.set('pattern', entry.path)
  return params.toString()
}

// distinctStoreHosts finds every store_host touched by any of entries'
// patterns -- a cheap facet query (bounded by distinct-store-count, not by
// how many files match), replacing the old full-file-pagination approach.
async function distinctStoreHosts(entries) {
  const hosts = new Set()
  for (const entry of entries) {
    const qs = buildStoreFacetsQuery(entry)
    const body = await apiFetch(`/catalog/stores?${qs}`)
    for (const facet of body.data) hosts.add(facet.name)
  }
  return [...hosts]
}

// storagePolicyIdForHost finds which storage policy's checkins include
// storeHost -- same cross-reference resolveStoreAddress used to do, but
// stopping at the policy id: policy-server finishes the resolution live
// (see server.go's attachDestination), so staying stale is no longer a
// risk the frontend needs to avoid by resolving all the way to an address
// itself.
function storagePolicyIdForHost(storagePolicies, storeHost) {
  for (const policy of storagePolicies) {
    if ((policy.checkins || []).some((c) => c.hostname === storeHost)) return policy.id
  }
  return null
}

export const useRestoreSubmissionStore = defineStore('restoreSubmission', {
  state: () => ({
    submitting: false,
    results: [],
    error: null,
  }),
  actions: {
    async submit(destinationHost) {
      const cart = useRestoreCartStore()
      const storagePolicies = useStoragePoliciesStore()
      const restorePolicies = useRestorePoliciesStore()

      this.submitting = true
      this.results = []
      this.error = null

      try {
        const positiveEntries = distinctPositiveEntries(cart.entries)
        if (positiveEntries.length === 0) {
          this.error = 'Nothing selected for restore.'
          return
        }

        const storeHosts = await distinctStoreHosts(positiveEntries)

        await storagePolicies.fetchAll()
        if (storagePolicies.error) {
          this.error = `Could not look up storage policies: ${storagePolicies.error}`
          return
        }

        const results = []
        for (const storeHost of storeHosts) {
          const storagePolicyId = storagePolicyIdForHost(storagePolicies.list, storeHost)
          if (!storagePolicyId) {
            results.push({
              storeHost,
              status: 'error',
              message: `No storage policy found for ${storeHost}`,
            })
            continue
          }
          try {
            const name = `restore-${new Date().toISOString()}-${storeHost}`
            const policy = await restorePolicies.create({
              name,
              client_filters: { hostnames: [destinationHost], labels: {} },
              storage_policy_id: storagePolicyId,
              rules: cart.rules,
            })
            results.push({ storeHost, status: 'success', policy })
          } catch (err) {
            results.push({ storeHost, status: 'error', message: err.message })
          }
        }
        this.results = results
      } catch (err) {
        this.error = err.message
      } finally {
        this.submitting = false
      }
    },
  },
})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: PASS.

- [ ] **Step 5: Delete now-unused code, if nothing else references it**

Run: `grep -rn "fetchCandidateEntries\|collapseToLatestVersion\|filterResolved\|groupByStore\|resolveStoreAddress" web/src --include=*.js | grep -v '\.spec\.js'`
- If `collapseToLatestVersion`/`filterResolved`/`groupByStore` (from `restoreResolve.js`) and `resolveStoreAddress` (from `storeAddress.js`) are no longer referenced anywhere outside their own spec files, delete those two now-dead utility files and their specs (`web/src/utils/restoreResolve.js`, `.spec.js`, `web/src/utils/storeAddress.js`, `.spec.js`). If anything else still imports them (e.g. a component using `resolveStoreAddress` for display elsewhere), leave them and note in the commit message why.

- [ ] **Step 6: Run the full web test suite**

Run: `cd web && npx vitest run`
Expected: PASS (confirms nothing else broke from the deletions in Step 5, if any were made).

- [ ] **Step 7: Commit**

```bash
git add web/src/stores/restoreSubmission.js web/src/stores/restoreSubmission.spec.js
# if Step 5 deleted files:
git add -u web/src/utils/restoreResolve.js web/src/utils/restoreResolve.spec.js web/src/utils/storeAddress.js web/src/utils/storeAddress.spec.js
git commit -m "feat: submit restore policies with rules instead of an expanded file list"
```

---

### Task 17: Cross-link superseded specs and finish the changelog

**Files:**
- Modify: `docs/superpowers/specs/2026-08-09-restore-policy-type-design.md`
- Modify: `docs/superpowers/specs/2026-08-10-restore-cart-submission-design.md`
- Modify: `docs/protocols/policy-server.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none (docs only).

- [ ] **Step 0: Fix the stale proto code block in `docs/protocols/policy-server.md`**

**Found mid-execution (Task 5's reviewer flagged it):** Task 1 changed the real
`src/api/policyserver.proto`, and Task 5 fixed this file's *prose* (`destinations` wording, the
restore-policy paragraph), but nobody updated this file's own mirrored `.proto`-formatted code
block (~lines 40-119) — it still shows the removed `source_store` fields and stale comments. Fix
it to match the real proto exactly:

- Line 71 (`string storage_policy_id = 15; // backup policy only, required`) → `// "backup" and "restore" policy only, required`.
- Line 73 (`repeated string destinations = 17; // backup and restore policy only, ...`) — already correct (Task 5 already fixed this one), leave as-is.
- Lines 74-75 (`// "restore" policy only. host:port of the source bwfs to restore from.` / `string source_store = 18;`) → replace with:
  ```proto
  reserved 18; reserved "source_store"; // removed 2026-08-10, replaced by storage_policy_id (15) + rules (19) -- see below
  // "restore" policy only.
  repeated RestoreRule rules = 19;
  ```
- Add the `RestoreRule` message definition directly above `message Policy {` (mirroring the real proto):
  ```proto
  message RestoreRule {
    string host    = 1; // "" = host-agnostic, matches every source host
    string path    = 2;
    bool   include = 3;
  }
  ```
- Lines 85-88 (the `CreatePolicyRequest.type` field's comment: `` mixing fields across types is rejected (e.g. a "restore" request must not set object_filters/rpo/ backup_window/storage_policy_id/port). ``) → change the parenthetical to `` (e.g. a "restore" request must not set object_filters/rpo/backup_window/port/config). ``
- Line 94 (`string storage_policy_id = 12; // backup policy only, required`) → `// "backup" and "restore" policy only, required`.
- Lines 95-97 (`// "restore" policy only, required. host:port of the source bwfs to restore from.` / `string source_store = 13;`) → replace with:
  ```proto
  reserved 13; reserved "source_store"; // removed 2026-08-10, see Policy.rules above
  // "restore" policy only, required.
  repeated RestoreRule rules = 14;
  ```

No test for this step (it's a markdown code block, not compiled code) — verify by re-reading the
edited block once and confirming it's byte-identical in shape to the real
`src/api/policyserver.proto`'s `Policy`/`CreatePolicyRequest`/`RestoreRule` definitions.

- [ ] **Step 1: Add a superseding note to the restore-policy-type design**

At the very top of `docs/superpowers/specs/2026-08-09-restore-policy-type-design.md`, directly after its title line, insert:

```markdown
> **Schema superseded 2026-08-10:** `source_store` and `config` as described in this document were
> replaced by `storage_policy_id` (live-resolved, same mechanism as backup) and a typed `rules`
> field. See [Design: Restore Policy Verification Execution](2026-08-10-restore-policy-verification-design.md)
> for the current schema and the reasoning behind the change.
```

- [ ] **Step 2: Add the same note to the restore-cart-submission design**

At the top of `docs/superpowers/specs/2026-08-10-restore-cart-submission-design.md`, directly after its title line, insert:

```markdown
> **Submission flow superseded 2026-08-10:** the file-enumeration-and-`groupByStore` flow this
> document describes was replaced by a `ListStoreFacets`-based lookup and unsplit rule submission.
> See [Design: Restore Policy Verification Execution](2026-08-10-restore-policy-verification-design.md).
```

- [ ] **Step 3: Add the `CHANGELOG.md` entry**

At the top of `CHANGELOG.md`'s entry list (most recent first), add:

```markdown
## 2026-08-10 — restore policy verification

`agent` now picks up `"restore"`-typed policies and verifies (not yet restores — `rwfs restore`
remains unbuilt) the exact files a future restore would need, against the source `bwfs`, via a new
`rwfs verify --rules-stdin` mode. Getting there required revising the restore policy schema itself:
`source_store` (a raw address baked in once at submission time, which could go stale) is replaced by
`storage_policy_id`, resolved live the same way a backup policy's destination already is; and
`config.files` (a client-expanded, size-capped list of every matched file) is replaced by a small
typed `rules` field carrying the restore cart's actual selection rules, resolved against the real
file listing at verify time instead of pre-expanded in the browser. `catalog` gained a
`ListStoreFacets` endpoint so the web UI can cheaply discover which stores a selection touches
without enumerating every file first.
```

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/specs/2026-08-09-restore-policy-type-design.md docs/superpowers/specs/2026-08-10-restore-cart-submission-design.md docs/protocols/policy-server.md CHANGELOG.md
git commit -m "docs: cross-link superseded restore specs and record the changelog entry"
```

---

## Deliberately Deferred: end-to-end integration test

The design doc's "Testing" section calls for an integration test against a real `policy-server` +
`bwfs` + `agent serve`, extending "the existing e2e harness." Checked: `src/e2e/e2e_test.go` (run
via `make test-e2e` against `make demo-up`'s docker-composed lab) currently contains exactly one
test (`TestE2E_WebUIAvailable`) — there is no existing backup- or storage-policy end-to-end test to
mirror, despite an earlier design's testing section calling for one the same way. Writing a real one
from scratch means understanding the demo lab's compose topology and certificate bootstrapping in
enough depth to drive a live `CreatePolicy` call and assert on a live `agent-state.json`/log
outcome — enough unknowns that a task for it here would be underspecified placeholder work, which
this plan's rules forbid. Tasks 1-17's unit/handler-level coverage is the real safety net for this
change; treat a `src/e2e/e2e_test.go` addition as separate, explicitly-scoped follow-up work once
this plan lands, not a step to improvise here.

## Final Verification

After all 17 tasks are committed:

- [ ] Run: `cd src && go build ./... && go test ./...` — expect all packages to build and all tests to pass.
- [ ] Run: `cd web && npx vitest run` — expect all tests to pass.
- [ ] Run: `grep -rn "source_store\|SourceStore" src web docs/api docs/protocols docs/components 2>/dev/null | grep -vi "docs/superpowers/specs\|CHANGELOG"` — expect no hits outside historical spec/changelog text (everything live should be on the new schema).
- [ ] Manually re-read `docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md`'s "Testing" section against this plan's tasks — confirm every bullet there maps to a task above (it should: policy-server validation/ToProto → Task 3/4; catalog facets → Task 6/7; api-server → Task 8/9; web → Task 16; rwfs → Task 13/14/15; agent → Task 11/12).
