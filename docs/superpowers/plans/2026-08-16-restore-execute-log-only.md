# Restore Execution — Log-Only First Slice — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `mode: "restore"` actually reach `policy-server` and dispatch a new `rwfs restore`
subcommand that resolves a restore policy's rules against the live store and **logs** each file's
source path, its renamed destination path, and the policy's `overwrite` setting — writing nothing
to disk yet.

**Architecture:** `Policy`/`CreatePolicyRequest` gain `mode`/`overwrite` fields, threaded verbatim
through `policy-server` → `policyclient`'s on-disk cache → `agent`. `agent` picks the task ID prefix
(`verify:` vs `restore:`) and the `rwfs` subcommand (`verify` vs `restore`) from the policy's
`mode`. The new `rwfs restore` subcommand reuses `verify --rules-stdin`'s exact rule-resolution
pipeline (`ResolveRestoreFiles`, `restoreResolver`, not-found semantics) but replaces the
BLAKE3/CRC32 worker pool with a straight log of `(source, path, dest_path)` per resolved row, where
`dest_path` applies `dest_path`'s existing prefix-substitution rename rule.

**Tech Stack:** Go (`net/http`, `google.golang.org/grpc`, `github.com/spf13/cobra`) for
`policy-server`/`api-server`/`agent`/`rwfs`; Vue 3 + Pinia for `web`; `protoc` for proto
regeneration.

## Global Constraints

- No filesystem write anywhere in this plan — `rwfs restore` only logs. (spec Non-Goals)
- `mode` empty on an existing on-disk restore policy JSON file means `"verify"` — zero on-disk
  migration for restore policies that predate this feature. (spec §1)
- `overwrite` has no validation tie to `mode` — it's accepted and stored unconditionally, since the
  web UI already sends it on every submit regardless of mode. (spec §1, §2)
- `rwfs restore` requires `--rules-stdin`; omitting it is an argument error. (spec §5)
- `rwfs restore` has no `--streams`/`--retries` flags — no per-file network I/O to parallelize or
  retry in this round. (spec Non-Goals, §5)
- Verify task IDs/job IDs move from `restore:<policy>[:ts]` to `verify:<policy>[:ts]`. Restore
  policies (new) use `restore:<policy>[:ts]`. (spec §4)
- Go tests: run via `cd src && go test ./cmd/<pkg>/... -run <TestName> -v`.
- Web tests: run via `cd web && npx vitest run <path/to/spec.js>`.
- Proto regeneration: `make proto` from the repo root (requires `protoc`/`protoc-gen-go`/
  `protoc-gen-go-grpc`, already installed in this environment).

---

### Task 1: Proto — `mode`/`overwrite` fields

**Files:**
- Modify: `src/api/policyserver.proto`
- Regenerate: `src/api/policyserver.pb.go` (via `make proto`)

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `pb.Policy.GetMode() string`, `pb.Policy.GetOverwrite() bool`,
  `pb.CreatePolicyRequest.GetMode() string`, `pb.CreatePolicyRequest.GetOverwrite() bool`. Task 2
  (`policy-server`) and Task 3 (`api-server`) both call these directly.

- [ ] **Step 1: Add the fields to the proto**

In `src/api/policyserver.proto`, inside `message Policy` (find `repeated RestoreRule rules = 19;`,
currently the last field), add immediately after it:

```proto
  // "restore" policy only. "" or "verify" behaves exactly as every restore
  // policy does today (agent runs rwfs verify, writes nothing). "restore"
  // is the log-only-for-now execution path (rwfs restore). A restore
  // policy JSON file written before this field existed has no "mode" key
  // at all and is unaffected -- absent is read as "verify".
  string mode = 20;
  // "restore" policy only. Carried through and logged by rwfs restore;
  // has no effect when mode is "verify" or unset -- the web UI already
  // sends this checkbox unconditionally on every submit (see
  // docs/superpowers/specs/2026-08-14-restore-verify-execute-split-design.md),
  // so it is simply inert for a verify submission.
  bool overwrite = 21;
```

Inside `message CreatePolicyRequest` (find `repeated RestoreRule rules = 14;`, currently the last
field), add immediately after it:

```proto
  // "restore" policy only. See Policy.mode above.
  string mode = 15;
  // "restore" policy only. See Policy.overwrite above.
  bool overwrite = 16;
```

- [ ] **Step 2: Regenerate the Go bindings**

Run: `make proto`
Expected: `src/api/policyserver.pb.go` is rewritten with no errors; `git diff --stat
src/api/policyserver.pb.go` shows changes.

- [ ] **Step 3: Confirm the module still builds**

Run: `cd src && go build ./...`
Expected: PASS (proves the regenerated bindings compile; nothing consumes the new fields yet).

- [ ] **Step 4: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go
git commit -m "feat(api): add mode/overwrite fields to restore policy proto

Next free field numbers on Policy (20/21) and CreatePolicyRequest
(15/16). Nothing reads or writes them yet -- policy-server, api-server,
and agent each pick them up in the following tasks."
```

---

### Task 2: `policy-server` — `RestorePolicy.Mode`/`Overwrite`

**Files:**
- Modify: `src/cmd/policy-server/restore_policy.go`
- Modify: `src/cmd/policy-server/write.go:211-215` (`buildPolicyForCreate`'s restore branch)
- Test: `src/cmd/policy-server/restore_policy_test.go`
- Test: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: Task 1's `pb.Policy.GetMode()`/`GetOverwrite()`, `pb.CreatePolicyRequest.GetMode()`/
  `GetOverwrite()`.
- Produces: `RestorePolicy.Mode string`, `RestorePolicy.Overwrite bool` fields; `ToProto` populates
  `pb.Policy.Mode`/`Overwrite`; `Validate()` rejects a `Mode` outside `{"", "verify", "restore"}`.
  Task 3 (`api-server`) and Task 4 (`agent`, via the wire round-trip) depend on this shape.

- [ ] **Step 1: Write the failing policy-server tests**

Add to `src/cmd/policy-server/restore_policy_test.go`, after `TestRestorePolicy_ToProto_
IncludesTimeframe` (the file's last test):

```go
func TestRestorePolicy_ValidateAcceptsEmptyVerifyOrRestoreMode(t *testing.T) {
	for _, mode := range []string{"", "verify", "restore"} {
		p := &RestorePolicy{
			PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
			StoragePolicyID: "sp-1",
			Rules:           []RestoreRule{{Path: "/a", Include: true}},
			Mode:            mode,
		}
		assert.NoError(t, p.Validate(), "mode %q must be accepted", mode)
	}
}

func TestRestorePolicy_ValidateRejectsUnknownMode(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true}},
		Mode:            "bogus",
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode must be 'verify' or 'restore'")
}

func TestRestorePolicy_ValidateOverwriteWithVerifyModeIsNotAnError(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true}},
		Mode:            "verify",
		Overwrite:       true,
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ToProtoIncludesModeAndOverwrite(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata: Metadata{ID: "r1", Name: "x"},
			Type:     "restore",
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true}},
		Mode:            "restore",
		Overwrite:       true,
	}

	pp := p.ToProto(false)

	assert.Equal(t, "restore", pp.GetMode())
	assert.True(t, pp.GetOverwrite())
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestRestorePolicy_Validate -v`
Expected: FAIL — `RestorePolicy` has no `Mode`/`Overwrite` field yet (compile error).

- [ ] **Step 3: Add `Mode`/`Overwrite` to `RestorePolicy` and wire `Validate`/`ToProto`**

In `src/cmd/policy-server/restore_policy.go`, change the `RestorePolicy` struct:

```go
type RestorePolicy struct {
	PolicyBase
	StoragePolicyID string        `json:"storage_policy_id"`
	Rules           []RestoreRule `json:"rules"`
	// "" or "verify" is today's verify-only behavior (zero on-disk
	// migration for a restore policy written before this field existed).
	// "restore" is the log-only restore-execution path -- see
	// docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md.
	Mode string `json:"mode,omitempty"`
	// Carried through and logged by rwfs restore; has no effect when Mode
	// is "verify" or unset. Not tied to Mode by validation -- the web UI
	// sends this checkbox unconditionally on every submit.
	Overwrite bool `json:"overwrite,omitempty"`
}
```

In `Validate()`, add the mode check right after the `rules must contain at least one entry` check
(before the `for i, r := range p.Rules` loop):

```go
	if p.Mode != "" && p.Mode != "verify" && p.Mode != "restore" {
		return fmt.Errorf("mode must be 'verify' or 'restore'")
	}
```

In `ToProto`, add `Mode` and `Overwrite` to the constructed `&pb.Policy{...}` literal, alongside
the existing `Rules: rules`:

```go
		Rules:           rules,
		Mode:            p.Mode,
		Overwrite:       p.Overwrite,
```

`Clone` needs no change — both new fields are plain scalars, already covered by the
`PolicyBase.clone()`-plus-struct-copy pattern `Clone` already uses (verify by reading the existing
`Clone` method: it constructs a new `&RestorePolicy{...}` by field, so add `Mode: p.Mode,
Overwrite: p.Overwrite,` there too, alongside `StoragePolicyID`/`Rules`).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run TestRestorePolicy -v`
Expected: PASS, all `TestRestorePolicy_*` tests including the new ones.

- [ ] **Step 5: Wire `buildPolicyForCreate` to populate `Mode`/`Overwrite`**

Write the failing test first. Add to `src/cmd/policy-server/write_test.go`, after
`TestCreatePolicy_RestoreRuleTimeframePersistsToDisk`:

```go
func TestCreatePolicy_RestoreModeAndOverwritePersistToDisk(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	storageID := createTestStoragePolicy(t, srv, "bwfs-east", 8080)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:            "actual-restore",
		Type:            "restore",
		ClientFilters:   &pb.ClientFilters{Hostnames: []string{"web-01"}},
		StoragePolicyId: storageID,
		Rules:           []*pb.RestoreRule{{Path: "/var/www", Include: true}},
		Mode:            "restore",
		Overwrite:       true,
	})

	require.NoError(t, err)
	assert.Equal(t, "restore", resp.GetMode())
	assert.True(t, resp.GetOverwrite())

	data, err := os.ReadFile(filepath.Join(dir, "restore", "actual-restore.json"))
	require.NoError(t, err)
	var onDisk struct {
		Mode      string `json:"mode"`
		Overwrite bool   `json:"overwrite"`
	}
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.Equal(t, "restore", onDisk.Mode)
	assert.True(t, onDisk.Overwrite)
}
```

Run: `cd src && go test ./cmd/policy-server/... -run TestCreatePolicy_RestoreModeAndOverwrite -v`
Expected: FAIL — the created policy's `Mode`/`Overwrite` are empty/false, since
`buildPolicyForCreate` doesn't read them from the request yet.

In `src/cmd/policy-server/write.go`, inside `buildPolicyForCreate`'s `if req.GetType() ==
"restore"` branch, add `Mode`/`Overwrite` to the returned `&RestorePolicy{...}` literal (currently
`PolicyBase`, `StoragePolicyID`, `Rules`):

```go
		return &RestorePolicy{
			PolicyBase:      base,
			StoragePolicyID: req.GetStoragePolicyId(),
			Rules:           rules,
			Mode:            req.GetMode(),
			Overwrite:       req.GetOverwrite(),
		}, nil
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `cd src && go test ./cmd/policy-server/... -run TestCreatePolicy_RestoreModeAndOverwrite -v`
Expected: PASS.

- [ ] **Step 7: Run the full policy-server package test suite**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS (confirms nothing else in the package broke).

- [ ] **Step 8: Commit**

```bash
git add src/cmd/policy-server/restore_policy.go src/cmd/policy-server/restore_policy_test.go src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go
git commit -m "feat(policy-server): add mode/overwrite to RestorePolicy

mode defaults to empty (interpreted as verify everywhere it's read --
no on-disk migration needed); Validate rejects anything outside
{'', 'verify', 'restore'}. overwrite has no validation tie to mode."
```

---

### Task 3: `api-server` — forward `mode: "restore"` instead of rejecting it

**Files:**
- Modify: `src/cmd/api-server/policies.go` (`handleCreateRestore`, `policyDTO`, `toPolicyDTO`)
- Test: `src/cmd/api-server/policies_test.go`

**Interfaces:**
- Consumes: Task 2's `RestorePolicy.Mode`/`Overwrite` (via `pb.Policy`/`pb.CreatePolicyRequest`).
- Produces: `POST /api/v1/restore` with `mode: "restore"` now returns `201` and calls
  `s.policy.CreatePolicy`, exactly like `mode: "verify"`. `policyDTO` gains `Mode`/`Overwrite` so
  `GET`/`ListPolicies` round-trip them. Task 9 (`web`) depends on the response actually carrying
  `mode`.

- [ ] **Step 1: Rewrite the now-obsolete 501 test and add new ones**

In `src/cmd/api-server/policies_test.go`, replace
`TestHandleCreateRestore_RestoreModeReturns501AndSkipsBackend` (currently asserting a `501` and a
never-called backend) with:

```go
func TestHandleCreateRestore_RestoreModeForwardsToBackend(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		StoragePolicyId: "sp-1",
		Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		Mode:            "restore",
		Overwrite:       true,
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web01-emergency",
		"storage_policy_id": "sp-1",
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}],
		"mode": "restore",
		"overwrite": true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "restore", fake.lastCreateReq.GetMode())
	assert.True(t, fake.lastCreateReq.GetOverwrite())

	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "restore", respBody["mode"])
	assert.Equal(t, true, respBody["overwrite"])
}
```

Add, immediately after it:

```go
func TestHandleCreateRestore_VerifyModeStillForwardsModeExplicitly(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		StoragePolicyId: "sp-1",
		Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		Mode:            "verify",
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web01-emergency",
		"storage_policy_id": "sp-1",
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "verify", fake.lastCreateReq.GetMode(), "an omitted mode must still forward the defaulted value")
}
```

`TestHandleCreateRestore_ExplicitVerifyModeForwardsToBackend` and
`TestHandleCreateRestore_InvalidModeReturns400` are unchanged — leave them as-is.

- [ ] **Step 2: Run the tests to verify the new/rewritten ones fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleCreateRestore -v`
Expected: FAIL — `TestHandleCreateRestore_RestoreModeForwardsToBackend` gets `501` (today's
behavior), `fake.lastCreateReq` is nil.

- [ ] **Step 3: Update `handleCreateRestore` to forward instead of reject**

In `src/cmd/api-server/policies.go`, remove the `if mode == "restore" { ...501... return }` block
entirely (currently right after the `mode != "verify" && mode != "restore"` check), and add
`Mode`/`Overwrite` to the `CreatePolicyRequest{...}` literal:

```go
	mode := in.Mode
	if mode == "" {
		mode = "verify"
	}
	if mode != "verify" && mode != "restore" {
		writeJSONError(w, http.StatusBadRequest, "mode must be 'verify' or 'restore'")
		return
	}

	rules := make([]*pb.RestoreRule, len(in.Rules))
	for i, ru := range in.Rules {
		rules[i] = &pb.RestoreRule{Host: ru.Host, Path: ru.Path, Include: ru.Include, DestPath: ru.DestPath, NotBefore: ru.NotBefore, NotAfter: ru.NotAfter}
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:            in.Name,
		Type:            "restore",
		ClientFilters:   toProtoClientFiltersInput(in.ClientFilters),
		StoragePolicyId: in.StoragePolicyID,
		Rules:           rules,
		DisabledAt:      disabledAtToProto(in.DisabledAt),
		Mode:            mode,
		Overwrite:       in.Overwrite,
	})
```

Update the function's doc comment (currently describing `mode="restore"` as rejected) to match —
replace the last two sentences with:

```go
// mode distinguishes verification (agent runs rwfs verify against the
// resolved rules, no files written) from restore execution (agent runs
// rwfs restore, which this round only resolves and logs the file list --
// still no files written -- see
// docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md).
// Both modes reach policy-server identically; only the created policy's
// mode field differs.
```

Add `Mode`/`Overwrite` to `policyDTO` (next to `Rules`) and to `toPolicyDTO`'s constructed
`dto := policyDTO{...}` literal (next to `Rules: rules`):

```go
type policyDTO struct {
	// ...existing fields unchanged...
	Rules      []ruleDTO `json:"rules,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	Overwrite  bool      `json:"overwrite,omitempty"`
	DisabledAt int64     `json:"disabled_at,omitempty"`
	Checkins   []checkinDTO `json:"checkins"`
}
```

```go
	dto := policyDTO{
		// ...existing fields unchanged...
		Rules:     rules,
		Mode:      p.GetMode(),
		Overwrite: p.GetOverwrite(),
		Checkins:  checkins,
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleCreateRestore -v`
Expected: PASS, all `TestHandleCreateRestore_*` tests.

- [ ] **Step 5: Run the full api-server package test suite**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): forward mode=restore to policy-server instead of 501

mode=restore now creates a real restore-typed policy, same as verify --
policy-server (Task 2) stores mode/overwrite on it but nothing acts on
mode=restore differently yet below api-server (that's agent's job,
Task 4)."
```

---

### Task 4: `policyclient` + `agent` — cache passthrough and `verify:`/`restore:` task branching

**Files:**
- Modify: `src/cmd/policyclient/fetch.go` (`CachedPolicy`, `toCachedPolicies`)
- Modify: `src/cmd/agent/backup.go` (`cachedPolicy` struct)
- Modify: `src/cmd/agent/restore.go` (rewrite)
- Test: `src/cmd/agent/restore_test.go`
- Test: `src/cmd/agent/reconcile_test.go` (fixture ID cosmetic check only, see Step 6)

**Interfaces:**
- Consumes: Task 2/3's `mode`/`overwrite` reaching `GetPolicies`'s response (`pb.Policy.GetMode()`/
  `GetOverwrite()`).
- Produces: `restoreTaskID(policyName, mode string) string`, `restoreJobID(policyName string, mode
  string, now time.Time) string`, `restoreTasks(...)`'s dispatched `Policy.Args` branching on
  `p.Mode == "restore"`. Task 7/8 (`rwfs`) depend on the exact `Args` shape this task builds
  (`["restore", dest, "--rules-stdin", "--job-id", jobID]` plus `--overwrite`).

- [ ] **Step 1: Add `Mode`/`Overwrite` passthrough to `policyclient`'s `CachedPolicy`**

In `src/cmd/policyclient/fetch.go`, add to the `CachedPolicy` struct (next to `Rules`):

```go
	// "restore" policy only, empty/false for every other type.
	Rules     []RestoreRule `json:"rules,omitempty"`
	Mode      string        `json:"mode,omitempty"`
	Overwrite bool          `json:"overwrite,omitempty"`
```

In `toCachedPolicies`, add `Mode: p.GetMode(), Overwrite: p.GetOverwrite(),` to the constructed
`CachedPolicy{...}` literal, alongside `Rules: rules,`.

There's no dedicated test file assertion to update here — `fetch_test.go` doesn't currently assert
individual `CachedPolicy` field values for restore policies (confirm with `grep -n "Mode\|Overwrite"
src/cmd/policyclient/fetch_test.go` — expect no matches, meaning no existing test needs updating).

- [ ] **Step 2: Add the same fields to `agent`'s duplicated `cachedPolicy`**

In `src/cmd/agent/backup.go`, add to the `cachedPolicy` struct (next to `Rules`):

```go
	// "restore" policy only, empty/false for every other type.
	Rules     []RestoreRule `json:"rules,omitempty"`
	Mode      string        `json:"mode,omitempty"`
	Overwrite bool          `json:"overwrite,omitempty"`
```

- [ ] **Step 3: Write the failing agent tests**

In `src/cmd/agent/restore_test.go`, rename every `"restore:web01-emergency"` /
`"restore:web01-emergency:"` occurrence in the **existing, mode-unset** tests to `"verify:
web01-emergency"` / `"verify:web01-emergency:"` (there are two: in
`TestRestoreTasks_OneTaskPerRestorePolicy`'s `assert.Equal(t, "restore:web01-emergency",
tasks[0].ID)` and its `strings.HasPrefix(tasks[0].JobID, "restore:web01-emergency:")`) — become:

```go
	assert.Equal(t, "verify:web01-emergency", tasks[0].ID)
	assert.Equal(t, "rwfs", tasks[0].Binary)
	assert.True(t, strings.HasPrefix(tasks[0].JobID, "verify:web01-emergency:"), "job id must be stamped with the policy name")
```

Similarly update `TestRestoreTasks_DueUntilFirstSuccessThenNeverAgain`'s cached policy — it doesn't
assert the ID string, so no change needed there. `TestRestoreTasks_NoDestinationsSkipsWithNoTask`,
`TestRestoreTasks_NoRulesSkipsWithNoTask`, `TestRestoreTasks_DisabledPolicySkipped`,
`TestRestoreTasks_UnreadableCacheReturnsNotOK`, `TestRestoreRule_TimeframeRoundTripsThroughJSON` are
unaffected — leave as-is.

Add two new tests, after `TestRestoreTasks_DueUntilFirstSuccessThenNeverAgain`:

```go
func TestRestoreTasks_RestoreModeUsesRestorePrefixAndRestoreSubcommand(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{
			Name: "web01-actual-restore", Type: "restore", Mode: "restore", Overwrite: true,
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, "restore:web01-actual-restore", tasks[0].ID)
	assert.True(t, strings.HasPrefix(tasks[0].JobID, "restore:web01-actual-restore:"))
	assert.Equal(t, []string{"restore", "bwfs-1:8080", "--rules-stdin", "--job-id", tasks[0].JobID, "--overwrite"}, tasks[0].Args)
}

func TestRestoreTasks_RestoreModeWithoutOverwriteOmitsFlag(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{
			Name: "web01-actual-restore", Type: "restore", Mode: "restore", Overwrite: false,
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, []string{"restore", "bwfs-1:8080", "--rules-stdin", "--job-id", tasks[0].JobID}, tasks[0].Args)
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run TestRestoreTasks -v`
Expected: FAIL — `TestRestoreTasks_OneTaskPerRestorePolicy` still gets `restore:web01-emergency`
(unchanged code), and the two new tests fail (unknown `Mode`/`Overwrite` fields on `cachedPolicy` —
compile error until Step 2 above is in place, and wrong `Args` shape until Step 5 below).

- [ ] **Step 5: Rewrite `restore.go`'s task derivation**

Replace `src/cmd/agent/restore.go`'s `restoreTaskID`, `restoreJobID`, and `restoreTasks` with:

```go
// restoreTaskID is the stable identifier for one restore policy's task in
// agent-state.json -- one task per policy (not per host, unlike backup's
// per-object-filter-path tasks -- a restore policy's rules aren't cleanly
// partitionable by host, since a folder rule can be host-agnostic).
// mode == "restore" gets the restore: prefix (rwfs restore, log-only for
// now); every other mode (unset or "verify") gets the verify: prefix
// (rwfs verify, unchanged behavior, renamed from this ID's original
// restore: prefix now that a second kind of restore-policy task exists).
func restoreTaskID(policyName, mode string) string {
	if mode == "restore" {
		return fmt.Sprintf("restore:%s", policyName)
	}
	return fmt.Sprintf("verify:%s", policyName)
}

// restoreJobID is the --job-id passed to the dispatched rwfs subcommand
// for one run -- includes a timestamp so a retry after failure gets a
// distinct id, mirroring backup.go's backupJobID. Same prefix convention
// as restoreTaskID.
func restoreJobID(policyName, mode string, now time.Time) string {
	if mode == "restore" {
		return fmt.Sprintf("restore:%s:%d", policyName, now.Unix())
	}
	return fmt.Sprintf("verify:%s:%d", policyName, now.Unix())
}

// rulesStdinPayload is the JSON shape piped to `rwfs verify --rules-stdin`
// / `rwfs restore --rules-stdin` -- {"rules": [...]}, matching
// policy-server's RestorePolicy.Rules field name exactly (see
// docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md's
// §4).
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
// rather than exec'ing rwfs against an empty target, which would fail
// loudly anyway but with a less useful error than simply not trying.
//
// A policy whose Rules is empty contributes no task either: agent doesn't
// trust the cache blindly. policy-server rejects a rules-less restore
// policy at write time, but a cache file hand-edited or left over from an
// older schema could still carry one, and an empty rule set would select
// zero files and "succeed" -- which this one-shot task would then record
// as permanently done without having done anything. (rwfs rejects that
// payload itself too; see its parseRulesStdin.)
//
// Each skip is logged with the policy name. A disabled policy is skipped
// the same way backup/storage policies already are.
//
// p.Mode == "restore" dispatches `rwfs restore` (this round: resolves and
// logs the file list, writes nothing -- see
// docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md),
// with --overwrite appended iff p.Overwrite. Every other mode (unset or
// "verify") dispatches `rwfs verify`, byte-for-byte what this policy type
// has always run.
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
		taskID := restoreTaskID(p.Name, p.Mode)
		if len(p.Destinations) == 0 {
			logger.Error("restore policy has no resolved destination, skipping", "policy", taskID)
			continue
		}
		if len(p.Rules) == 0 {
			logger.Error("restore policy has no rules, skipping", "policy", taskID)
			continue
		}

		payload, err := json.Marshal(rulesStdinPayload{Rules: p.Rules})
		if err != nil {
			logger.Error("restore policy rules failed to marshal, skipping", "policy", taskID, "error", err)
			continue
		}

		jobID := restoreJobID(p.Name, p.Mode, time.Now())
		args := []string{"verify", p.Destinations[0], "--rules-stdin", "--job-id", jobID}
		if p.Mode == "restore" {
			args = []string{"restore", p.Destinations[0], "--rules-stdin", "--job-id", jobID}
			if p.Overwrite {
				args = append(args, "--overwrite")
			}
		}

		tasks = append(tasks, Policy{
			ID:         taskID,
			Binary:     "rwfs",
			JobID:      jobID,
			Args:       args,
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

The top-of-file package doc comment stays unchanged; only these three functions are replaced.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run TestRestoreTasks -v`
Expected: PASS, all `TestRestoreTasks_*` including the two new ones.

Then check `src/cmd/agent/reconcile_test.go:605`'s `Policy{ID: "restore:x", Binary: "rwfs", Args:
[]string{"verify"}, ...}` fixture inside `TestRun_StdinIsPassedThroughToRunner` — this is a
hand-built `Policy` literal for a stdin-threading test, not a call through `restoreTaskID`, and
doesn't assert anything about the ID naming scheme itself (only that `Stdin` reaches the runner).
Leave it unchanged; confirm by running it:

Run: `cd src && go test ./cmd/agent/... -run TestRun_StdinIsPassedThroughToRunner -v`
Expected: PASS (unaffected by this task).

- [ ] **Step 7: Run the full agent and policyclient package test suites**

Run: `cd src && go test ./cmd/agent/... ./cmd/policyclient/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/agent/backup.go src/cmd/agent/restore.go src/cmd/agent/restore_test.go
git commit -m "feat(agent): branch restore-policy dispatch on mode

mode unset/'verify' keeps rwfs verify under a renamed verify: task/job
ID prefix (was restore:). mode='restore' dispatches the new rwfs
restore subcommand (Task 7) under a restore: prefix, with --overwrite
appended when the policy's overwrite flag is set."
```

---

### Task 5: `api-server` — `verify`/`restore` job-kind classification

**Files:**
- Modify: `src/cmd/api-server/jobs.go`
- Test: `src/cmd/api-server/jobs_test.go`

**Interfaces:**
- Consumes: Task 4's `verify:`/`restore:` job-ID prefixes.
- Produces: `validJobKinds["verify"] == true`; `binariesForKind("verify") == "agent"`; the `400`
  error message on `/api/v1/jobs?kind=<bad>` lists `verify`. No other task depends on this — it's
  the observability-surface consequence of Task 4's rename.

- [ ] **Step 1: Write the failing tests**

In `src/cmd/api-server/jobs_test.go`, extend `TestBinariesForKind` with one new assertion (add the
line, don't remove any existing ones):

```go
func TestBinariesForKind(t *testing.T) {
	assert.Equal(t, "brfs|bwfs", binariesForKind("backup"))
	assert.Equal(t, "agent", binariesForKind("bootstrap-refresh"))
	assert.Equal(t, "agent", binariesForKind("operating-refresh"))
	assert.Equal(t, "agent", binariesForKind("policy-update"))
	assert.Equal(t, "agent", binariesForKind("verify"))
	assert.Equal(t, "agent", binariesForKind("restore"))
	assert.Equal(t, "agent|brfs|bwfs", binariesForKind(""))
}
```

Add two new tests after `TestHandleListJobs_RestoreKindUsesAgentBinaryLabel`:

```go
func TestHandleListJobs_KindVerifyIsAccepted(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=verify", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleListJobs_VerifyKindUsesAgentBinaryLabel(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent"} | event="start"`: {
			{Stream: map[string]string{"hostname": "database"}, Values: []lokiValue{
				{Timestamp: 1752400500000000000, Metadata: map[string]string{"job_id": "verify:e2e-restore-verify:1752400500"}},
			}},
		},
		`{binary=~"agent"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "database"}, Values: []lokiValue{
				{Timestamp: 1752400501000000000, Metadata: map[string]string{"job_id": "verify:e2e-restore-verify:1752400500", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=verify", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	job := data[0].(map[string]any)
	assert.Equal(t, "verify", job["kind"])
	assert.Equal(t, "success", job["state"])
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run "TestBinariesForKind|TestHandleListJobs_.*Verify" -v`
Expected: FAIL — `binariesForKind("verify")` returns `""` (falls to `default`, wrong value); `kind=
verify` returns `400` (not in `validJobKinds`).

- [ ] **Step 3: Update `jobs.go`**

`validJobKinds` gains `"verify": true`:

```go
var validJobKinds = map[string]bool{
	"backup":            true,
	"bootstrap-refresh": true,
	"operating-refresh": true,
	"policy-update":     true,
	"verify":            true,
	"restore":           true,
}
```

`binariesForKind`'s case list gains `"verify"`:

```go
	case "bootstrap-refresh", "operating-refresh", "policy-update", "verify", "restore":
		return "agent"
```

The `400` message (in `handleListJobs`, currently `"kind must be one of backup,
bootstrap-refresh, operating-refresh, policy-update, restore"`) gains `verify`:

```go
		writeJSONError(w, http.StatusBadRequest, "kind must be one of backup, bootstrap-refresh, operating-refresh, policy-update, verify, restore")
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run "TestBinariesForKind|TestHandleListJobs" -v`
Expected: PASS, all matching tests.

- [ ] **Step 5: Run the full api-server package test suite**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go
git commit -m "feat(api-server): classify verify: job ids as kind=verify

Consequence of agent renaming its verify-task id prefix from restore:
to verify: (Task 4) -- restore: now means restore-execution. Both
kinds still route through agent's own wrapper log."
```

---

### Task 6: `rwfs` — `Feed`'s winning-rule index + `restoreDestPath`

**Files:**
- Modify: `src/cmd/rwfs/resolve.go` (`restoreResolver.Feed`)
- Modify: `src/cmd/rwfs/verify.go` (the one `Feed` call site)
- Modify: `src/cmd/rwfs/rules.go` (new `restoreDestPath`)
- Test: `src/cmd/rwfs/resolve_test.go`
- Test: `src/cmd/rwfs/rules_test.go`

**Interfaces:**
- Consumes: nothing new — pure refactor plus one small addition over Task 1-5's unrelated work.
- Produces: `restoreResolver.Feed(row *pb.FileRow, filterIndex int32) (dispatch bool, ruleIndex
  int)`; `restoreDestPath(rule RestoreRule, rowPath string) string`. Task 7 (`rwfs restore`)
  consumes both directly.

- [ ] **Step 1: Write the failing tests**

In `src/cmd/rwfs/resolve_test.go`, change every existing `if !resolver.Feed(row, N) {` / `if
resolver.Feed(row, N) {` call site to capture the new two-value return (they currently discard
nothing since `Feed` returns one value — Go requires the call sites to change or they won't
compile). For example, `TestRestoreResolver_KeepsRowMatchingItsOwnRule` becomes:

```go
func TestRestoreResolver_KeepsRowMatchingItsOwnRule(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	row := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 10}
	dispatch, ruleIndex := resolver.Feed(row, 0)
	if !dispatch {
		t.Fatal("expected the row to be kept")
	}
	if ruleIndex != 0 {
		t.Fatalf("expected the winning rule index to be 0, got %d", ruleIndex)
	}
}
```

Apply the same mechanical change to the other three tests in this file that call `resolver.Feed`:

```go
func TestRestoreResolver_DropsRowShadowedByMoreSpecificRule(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/etc", Include: true, NotBefore: 1, NotAfter: 100},      // filter 0 -- broad
		{Host: "h", Path: "/etc/a", Include: true, NotBefore: 200, NotAfter: 300}, // filter 1 -- specific
	}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	// bwfs resolved /etc/a under BOTH filters (it's under /etc, and it IS
	// /etc/a) -- each with a different version, since their windows differ.
	broadVersionRow := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 10}
	specificVersionRow := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 20}

	if dispatch, _ := resolver.Feed(broadVersionRow, 0); dispatch {
		t.Fatal("the broad rule's row for /etc/a must be dropped: the specific rule (index 1) governs this path")
	}
	dispatch, ruleIndex := resolver.Feed(specificVersionRow, 1)
	if !dispatch {
		t.Fatal("the specific rule's own row for its own path must be kept")
	}
	if ruleIndex != 1 {
		t.Fatalf("expected the winning rule index to be 1, got %d", ruleIndex)
	}
}

func TestRestoreResolver_DropsRowWhoseWinningRuleIsExcluded(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/etc", Include: true},
		{Host: "h", Path: "/etc/secret", Include: false},
	}
	_, filterToRuleIndex := buildRestoreFilters(rules) // only the include rule (index 0) becomes a filter
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	// bwfs resolved /etc/secret under the broad folder filter (filter 0),
	// since the exclude rule never becomes a filter at all.
	row := &pb.FileRow{Source: "h", Path: "/etc/secret", Type: "f", Size: 10}
	if dispatch, _ := resolver.Feed(row, 0); dispatch {
		t.Fatal("the exclude rule governs /etc/secret, so this row must be dropped")
	}
}

func TestRestoreResolver_ZeroByteOrNonFileRowIsFoundButNotKept(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)

	resolver := newRestoreResolver(rules, filterToRuleIndex)
	zeroByte := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 0}
	if dispatch, _ := resolver.Feed(zeroByte, 0); dispatch {
		t.Fatal("a zero-byte row must be found but not selected")
	}
	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a found-but-unselected row must not be reported as not-found, got %v", notFound)
	}

	resolver = newRestoreResolver(rules, filterToRuleIndex)
	dir := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "d", Size: 10}
	if dispatch, _ := resolver.Feed(dir, 0); dispatch {
		t.Fatal("a directory row must be found but not selected")
	}
}
```

In `src/cmd/rwfs/rules_test.go`, add after `TestResolveRestoreFileRule_ReturnsWinningRuleIndex`
(read its current final lines first with `sed -n '55,75p' src/cmd/rwfs/rules_test.go` to append
after, not inside, that test):

```go
func TestRestoreDestPath_NoRenameReturnsRowPathUnchanged(t *testing.T) {
	rule := RestoreRule{Path: "/var/www", DestPath: ""}
	assert.Equal(t, "/var/www/index.html", restoreDestPath(rule, "/var/www/index.html"))

	ruleEqual := RestoreRule{Path: "/var/www", DestPath: "/var/www"}
	assert.Equal(t, "/var/www/index.html", restoreDestPath(ruleEqual, "/var/www/index.html"))
}

func TestRestoreDestPath_FileLevelRuleSwapsExactPath(t *testing.T) {
	rule := RestoreRule{Path: "/etc/nginx/nginx.conf", DestPath: "/etc/nginx/nginx.conf.bak"}
	assert.Equal(t, "/etc/nginx/nginx.conf.bak", restoreDestPath(rule, "/etc/nginx/nginx.conf"))
}

func TestRestoreDestPath_FolderLevelRuleSubstitutesPrefix(t *testing.T) {
	rule := RestoreRule{Path: "/data/photos", DestPath: "/data/photos_recovered"}
	assert.Equal(t, "/data/photos_recovered/vacation.jpg", restoreDestPath(rule, "/data/photos/vacation.jpg"))
	assert.Equal(t, "/data/photos_recovered/2024/beach.jpg", restoreDestPath(rule, "/data/photos/2024/beach.jpg"))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run "TestRestoreResolver|TestRestoreDestPath" -v`
Expected: FAIL — compile errors (`Feed` still returns one value; `restoreDestPath` doesn't exist).

- [ ] **Step 3: Change `Feed`'s signature**

In `src/cmd/rwfs/resolve.go`, change `Feed`'s signature and its two `return false` sites and final
`return true` to also return the winning rule index:

```go
func (r *restoreResolver) Feed(row *pb.FileRow, filterIndex int32) (dispatch bool, ruleIndex int) {
	winningRuleIndex, include, found := resolveRestoreFileRule(r.rules, row.GetSource(), row.GetPath())
	if !found || !include {
		return false, winningRuleIndex
	}
	if winningRuleIndex != r.filterToRuleIndex[filterIndex] {
		return false, winningRuleIndex
	}
	r.filterFoundAny[filterIndex] = true
	if row.GetType() != "f" || row.GetSize() <= 0 {
		return false, winningRuleIndex
	}
	return true, winningRuleIndex
}
```

Update the doc comment's opening line (`// Feed decides whether row ... should be dispatched.`) to
add one sentence: `Also returns the winning rule's index, so a caller (e.g. rwfs restore, which
needs to know which rule's dest_path governs the row) can look it up without a second resolution
pass.`

In `src/cmd/rwfs/verify.go`, the one call site (`if resolver.Feed(resp.GetRow(),
resp.GetFilterIndex()) { workCh <- resp.GetRow() }`) becomes:

```go
				if dispatch, _ := resolver.Feed(resp.GetRow(), resp.GetFilterIndex()); dispatch {
					workCh <- resp.GetRow()
				}
```

- [ ] **Step 4: Add `restoreDestPath` to `rules.go`**

In `src/cmd/rwfs/rules.go`, add at the end of the file:

```go
// restoreDestPath computes row's destination path under rule's dest_path
// rename, if any. Works uniformly for a file-level rule (rowPath ==
// rule.Path exactly, a straight swap) and a folder-level rule (rowPath is
// always a rule.Path-prefixed descendant, by construction of
// longestMatchingFolderRuleIndex's ancestor-chain match) -- the "single
// replacement prefix for the whole folder" semantics
// docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md
// already specified for a future executor to interpret.
func restoreDestPath(rule RestoreRule, rowPath string) string {
	if rule.DestPath == "" || rule.DestPath == rule.Path {
		return rowPath
	}
	return rule.DestPath + strings.TrimPrefix(rowPath, rule.Path)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run "TestRestoreResolver|TestRestoreDestPath" -v`
Expected: PASS.

- [ ] **Step 6: Run the full rwfs package test suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS (confirms `verify.go`'s updated call site didn't break anything else).

- [ ] **Step 7: Commit**

```bash
git add src/cmd/rwfs/resolve.go src/cmd/rwfs/resolve_test.go src/cmd/rwfs/verify.go src/cmd/rwfs/rules.go src/cmd/rwfs/rules_test.go
git commit -m "refactor(rwfs): expose restoreResolver.Feed's winning rule index

Adds restoreDestPath, the dest_path prefix-substitution helper a
future restore command (Task 7) needs to compute each resolved row's
destination path. verify's own dispatch logic is unchanged -- it just
ignores the new second return value."
```

---

### Task 7: `rwfs` — new `restore` subcommand (log-only)

**Files:**
- Create: `src/cmd/rwfs/restore.go`
- Test: `src/cmd/rwfs/restore_test.go`

**Interfaces:**
- Consumes: Task 6's `restoreResolver.Feed` (two-value), `restoreDestPath`; existing
  `parseRulesStdin`, `buildRestoreFilters`, `newRestoreResolver`, `pb.ListServiceClient.
  ResolveRestoreFiles` (all already used by `verify.go`, unchanged). Also reuses three test-only
  helpers already defined in `verify_test.go` — same package (`package main` under
  `src/cmd/rwfs`), so `restore_test.go` calls them directly and must **not** redefine them:
  `expectedCRC32(t, chunks [][]byte) []byte`, `testResolveServer` (a `pb.ListServiceServer`
  stand-in backed by a real `*wfs.Store`), and `recordingRestoreServer` (a `pb.RestoreServiceServer`
  stand-in that records requested `file_uuid`s and always fails).
- Produces: `runRestore(logger *slog.Logger, host string, port int, overwrite bool, stdin io.Reader,
  quiet bool, certsDir, jobID string) error` and `runRestoreWithConn(logger *slog.Logger, conn
  *grpc.ClientConn, overwrite bool, rules []RestoreRule, quiet bool, jobID string) error` (mirroring
  `verify.go`'s `runVerify`/`runVerifyWithConn` split — production entry point vs. a
  bufconn-dialable body tests call directly). Task 8 (`arguments.go`/`main.go`) calls `runRestore`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/rwfs/restore_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// runRestoreWithDialer mirrors verify_test.go's runVerifyWithDialer --
// runRestore always dials via connection.Connect (host/port plus mTLS
// certs), which has no bufconn injection seam, so this duplicates just the
// dial step against lis, then calls runRestoreWithConn, the exact same
// package-level resolution/dispatch logic runRestore itself calls after
// dialing.
func runRestoreWithDialer(t *testing.T, logger *slog.Logger, lis *bufconn.Listener, rulesJSON string, overwrite bool) error {
	t.Helper()

	rules, err := parseRulesStdin(strings.NewReader(rulesJSON))
	require.NoError(t, err)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	return runRestoreWithConn(logger, conn, overwrite, rules, false, "test-job")
}

func TestRunRestore_LogsResolvedFileWithRenamedDestPath(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/photos/vacation.jpg:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/photos/vacation.jpg:1000", expectedCRC32(t, [][]byte{{1, 2, 3, 4}})))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/data/photos/vacation.jpg:1000", JobID: "job1", CreatedAt: time.Unix(5000, 0)}).Error)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	rulesJSON := `{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":"/data/photos_recovered"}]}`

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, true)
	require.NoError(t, err)

	out := logBuf.String()
	assert.Contains(t, out, `source=hosta`)
	assert.Contains(t, out, `path=/data/photos/vacation.jpg`)
	assert.Contains(t, out, `dest_path=/data/photos_recovered/vacation.jpg`)
	assert.Contains(t, out, `overwrite=true`)
	assert.Empty(t, restoreSrv.Requested(),
		"rwfs restore must never call RestoreFile in this round -- it only resolves and logs")
}

func TestRunRestore_FileLevelRuleMatchingNothingFails(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	rulesJSON := `{"rules":[{"host":"hosta","path":"/etc/never-backed-up.conf","include":true}]}`

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 file(s) failed resolution")
	assert.Contains(t, logBuf.String(), `reason="not found on this store"`)
}

func TestRunRestore_FolderLevelRuleMatchingNothingSucceeds(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	rulesJSON := `{"rules":[{"host":"","path":"/empty","include":true}]}`

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
	assert.NoError(t, err)
}
```

Add the missing `"strings"` import to the file (used by `parseRulesStdin(strings.NewReader(...))`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestRunRestore -v`
Expected: FAIL — `runRestoreWithConn` doesn't exist yet (compile error).

- [ ] **Step 3: Implement `restore.go`**

Create `src/cmd/rwfs/restore.go`:

```go
// restore.go implements `rwfs restore` -- this round, a log-only preview
// of a restore policy's resolved file list: for every row
// ResolveRestoreFiles yields that survives restoreResolver.Feed's
// precedence tie-break, it logs the row's source path and its computed
// destination path (restoreDestPath's dest_path rename applied), plus the
// run's overwrite setting once at start. No RestoreFile call, nothing
// written to disk -- see
// docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md.
// Reuses the exact rule-resolution pipeline `rwfs verify --rules-stdin`
// already built (parseRulesStdin, buildRestoreFilters, newRestoreResolver,
// the same not-found semantics) -- only the per-row action differs.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"google.golang.org/grpc"
)

// runRestore resolves --rules-stdin against a remote bwfs store and logs
// what a real restore of this policy would do. jobID rides the
// ResolveRestoreFiles call as outgoing job-id metadata, the same
// convention runVerify uses.
func runRestore(logger *slog.Logger, host string, port int, overwrite bool, stdin io.Reader, quiet bool, certsDir, jobID string) error {
	rules, err := parseRulesStdin(stdin)
	if err != nil {
		return err
	}

	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	return runRestoreWithConn(logger, conn, overwrite, rules, quiet, jobID)
}

// runRestoreWithConn is runRestore's body, parameterized on an
// already-dialed conn -- split out purely so tests can exercise it over a
// bufconn dial without duplicating anything past the transport-level
// connect (runRestore itself is the only production caller). See
// restore_test.go's runRestoreWithDialer.
func runRestoreWithConn(logger *slog.Logger, conn *grpc.ClientConn, overwrite bool, rules []RestoreRule, quiet bool, jobID string) error {
	callCtx := jobid.Outgoing(context.Background(), jobID)

	filters, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	logger.Info("restore starting", "overwrite", overwrite, "rules", len(rules))

	listClient := pb.NewListServiceClient(conn)
	stream, err := listClient.ResolveRestoreFiles(callCtx, &pb.ResolveRestoreFilesRequest{Filters: filters})
	if err != nil {
		return fmt.Errorf("resolve restore files: %w", err)
	}

	total := 0
	warnings := 0
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("resolve restore files: %w", err)
		}

		row := resp.GetRow()
		dispatch, ruleIndex := resolver.Feed(row, resp.GetFilterIndex())
		if !dispatch {
			continue
		}

		total++
		destPath := restoreDestPath(rules[ruleIndex], row.GetPath())
		if !quiet {
			logger.Info("resolved",
				"source", row.GetSource(),
				"path", row.GetPath(),
				"dest_path", destPath,
			)
		}
	}

	for _, nf := range resolver.NotFound() {
		warnings++
		logger.Warn("resolution failed", "source", nf.Host, "path", nf.Path, "reason", nf.Reason)
	}

	logger.Info("summary", "resolved", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed resolution", warnings)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestRunRestore -v`
Expected: PASS.

- [ ] **Step 5: Run the full rwfs package test suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/rwfs/restore.go src/cmd/rwfs/restore_test.go
git commit -m "feat(rwfs): add log-only restore command

Resolves a restore policy's rules against the live store via the same
ResolveRestoreFiles/restoreResolver pipeline verify --rules-stdin
uses, and logs each resolved file's source path, renamed destination
path, and the run's overwrite setting. No RestoreFile call, nothing
written -- CLI wiring lands in the next task."
```

---

### Task 8: `rwfs` — CLI wiring for the `restore` subcommand

**Files:**
- Modify: `src/cmd/rwfs/arguments.go`
- Modify: `src/cmd/rwfs/main.go`
- Test: `src/cmd/rwfs/arguments_test.go`

**Interfaces:**
- Consumes: Task 7's `runRestore`.
- Produces: `rwfs restore <bwfs_host:port> --rules-stdin [--overwrite] [--quiet] [--job-id ID]
  [--debug]` as a real, invocable CLI subcommand. Task 4's `agent` dispatch (already built) now has
  a real binary to exec.

- [ ] **Step 1: Write the failing argument-parsing tests**

Add to `src/cmd/rwfs/arguments_test.go`:

```go
func TestParseArguments_RestoreWithoutRulesStdinErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080"}, func() {
		_, err := parseArguments(testConfig())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "restore requires --rules-stdin")
	})
}

func TestParseArguments_RestoreWithRulesStdinParsesOverwriteFlag(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin", "--overwrite"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "restore", args.Action)
		assert.True(t, args.RulesStdin)
		assert.True(t, args.Overwrite)
	})
}

func TestParseArguments_RestoreOverwriteDefaultsFalse(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.False(t, args.Overwrite)
	})
}
```

This uses the file's existing `withArgs(t, args, fn)` and `testConfig()` helpers (already defined
at the top of `arguments_test.go`, lines 12-22) — the same pattern every other
`TestParseArguments_*` test in the file already follows.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestParseArguments_Restore -v`
Expected: FAIL — `restore` isn't a registered subcommand yet (cobra reports "unknown command").

- [ ] **Step 3: Add the `restore` subcommand**

In `src/cmd/rwfs/arguments.go`, add `Overwrite bool // restore only` to the `Arguments` struct
(next to `RulesStdin`).

After the existing `verifyCmd` block (after `verifyCmd.Flags().StringVar(&args.JobID, ...)` and
before `rootCmd.AddCommand(listCmd)`), add:

```go
	restoreCmd := &cobra.Command{
		Use:   "restore [[server_name:]path] <bwfs_host:port>",
		Short: "Resolve a restore policy's rules and log what a restore would do (no files written yet)",
		Args:  cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, cliArgs []string) {
			args.Action = "restore"
			if len(cliArgs) == 1 {
				args.bwfsTarget = cliArgs[0]
			} else {
				args.listPositional = cliArgs[0]
				args.bwfsTarget = cliArgs[1]
			}
		},
	}
	restoreCmd.Flags().BoolVar(&args.RulesStdin, "rules-stdin", false, "Read {\"rules\":[{host,path,include,dest_path}]} from stdin (required)")
	restoreCmd.Flags().BoolVar(&args.Overwrite, "overwrite", false, "Whether a real restore would overwrite existing destination files (logged only, not yet enforced)")
	restoreCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	restoreCmd.Flags().BoolVar(&args.Quiet, "quiet", false, "Suppress per-file resolved lines (warnings and summary always shown)")
	restoreCmd.Flags().StringVar(&args.JobID, "job-id", "", "Correlation ID for this invocation's logs (auto-generated if omitted); sent to bwfs as job-id metadata")
```

Register it: `rootCmd.AddCommand(restoreCmd)` alongside the existing `rootCmd.AddCommand(listCmd)` /
`rootCmd.AddCommand(verifyCmd)`.

Update the "subcommand is required" error: `return nil, fmt.Errorf("a subcommand is required:
list, verify, restore")`.

Add the required-flag validation, alongside the existing `if args.Action == "verify" { ... }`
block:

```go
	if args.Action == "restore" && !args.RulesStdin {
		return nil, fmt.Errorf("restore requires --rules-stdin")
	}
```

The existing `serverName == "" && !args.RulesStdin` hostname-default line (shared by all three
subcommands) needs no change — `restore` always has `RulesStdin` true by the time it's reached
(enforced by the check just added), so it naturally skips the local-hostname default, exactly like
`verify --rules-stdin` already does.

- [ ] **Step 4: Wire `main.go`**

In `src/cmd/rwfs/main.go`, add a `case "restore":` branch to the `switch arguments.Action` block,
after the existing `case "verify":`:

```go
	case "restore":
		if err := runRestore(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.Overwrite, os.Stdin, arguments.Quiet, certsDir, jobID); err != nil {
			logger.Error("Restore failed", "error", err)
			os.Exit(1)
		}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestParseArguments_Restore -v`
Expected: PASS.

- [ ] **Step 6: Run the full rwfs package test suite and build**

Run: `cd src && go test ./cmd/rwfs/... && go build ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/rwfs/arguments.go src/cmd/rwfs/main.go src/cmd/rwfs/arguments_test.go
git commit -m "feat(rwfs): wire up the restore subcommand's CLI

rwfs restore requires --rules-stdin (argument error otherwise, no
non-rules restore mode exists) and takes --overwrite, --quiet,
--job-id, --debug. No --streams/--retries -- this round does no
per-file network I/O to parallelize."
```

---

### Task 9: `web` — mode-aware success copy

**Files:**
- Modify: `web/src/stores/restoreSubmission.js`
- Modify: `web/src/views/RestoreView.vue`
- Test: `web/src/stores/restoreSubmission.spec.js`
- Test: `web/src/views/RestoreView.spec.js`

**Interfaces:**
- Consumes: Task 3's `POST /api/v1/restore` now succeeding for `mode: "restore"`.
- Produces: each success result in `restoreSubmission`'s `results` array carries `mode`;
  `RestoreView.vue` renders "Started restore policy ..." vs "Started verification policy ..."
  accordingly. Nothing downstream consumes this — terminal UI task.

- [ ] **Step 1: Write the failing web tests**

In `web/src/stores/restoreSubmission.spec.js`, four existing tests assert a `status: 'success'`
result object; each needs `mode: 'verify'` added (all four call `submit(..., { mode: 'verify', ...
})`) since that field doesn't exist on the pushed result yet:

In `'sends the full, unsplit rule list to the one store a folder rule touches'`, change:

```js
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
    ])
```

to:

```js
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' }, mode: 'verify' },
    ])
```

In `'creates one restore policy per distinct store, each carrying only its own file rules'`,
change:

```js
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
      { storeHost: 'store-b', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-b' } },
    ])
```

to:

```js
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' }, mode: 'verify' },
      { storeHost: 'store-b', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-b' }, mode: 'verify' },
    ])
```

In `'reports a per-store error when a store has no matching storage policy, without blocking other
stores'`, change (the error entry is untouched — only `agent`'s own code path pushes `mode` on
success, never on error):

```js
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
      { storeHost: 'store-b', status: 'error', message: 'No storage policy found for store-b' },
    ])
```

to:

```js
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' }, mode: 'verify' },
      { storeHost: 'store-b', status: 'error', message: 'No storage policy found for store-b' },
    ])
```

In `'reports a per-store error when CreatePolicy fails, without blocking other stores'`, change:

```js
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
      { storeHost: 'store-b', status: 'error', message: 'name already exists' },
    ])
```

to:

```js
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' }, mode: 'verify' },
      { storeHost: 'store-b', status: 'error', message: 'name already exists' },
    ])
```

Add a new test, near the other mode/overwrite-propagation tests:

```js
  it('tags each success result with the mode it was submitted under', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 2, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name, mode: 'restore' })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'restore', overwrite: true })

    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: expect.any(String), mode: 'restore' }, mode: 'restore' },
    ])
  })
```

In `web/src/views/RestoreView.spec.js`, add — using the same `mountView({ submission: {...} })`
initial-state pattern the existing `'renders a successful submission result'` test (line 144)
already uses, so no post-mount state mutation or `nextTick` is needed:

```js
  it('renders restore-specific copy for a mode=restore success result', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      submission: { results: [{ storeHost: 'store-a', status: 'success', policy: { name: 'r1' }, mode: 'restore' }] },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain(
      'Started restore policy r1 from store-a'
    )
  })

  it('keeps verification copy for a mode=verify success result', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      submission: { results: [{ storeHost: 'store-a', status: 'success', policy: { name: 'r1' }, mode: 'verify' }] },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain(
      'Started verification policy r1 from store-a'
    )
  })
```

The existing `'renders a successful submission result'` (line 144) and `'keeps submission results
visible after the cart is emptied'` (line 174) tests use a result object with no `mode` field at
all — confirm they still pass unmodified: the template's `result.mode === 'restore' ? 'restore' :
'verification'` falls back to `'verification'` when `mode` is `undefined`, matching what both
tests already assert.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js src/views/RestoreView.spec.js`
Expected: FAIL — `results` entries have no `mode` field yet; the view always renders "Started
verification policy ...".

- [ ] **Step 3: Implement**

In `web/src/stores/restoreSubmission.js`, inside the `submit` action's per-store loop, change:

```js
            results.push({ storeHost, status: 'success', policy })
```

to:

```js
            results.push({ storeHost, status: 'success', policy, mode })
```

In `web/src/views/RestoreView.vue`, replace the success line:

```html
        <span v-if="result.status === 'success'">Started verification policy {{ result.policy.name }} from {{ result.storeHost }}</span>
```

with:

```html
        <span v-if="result.status === 'success'">
          Started {{ result.mode === 'restore' ? 'restore' : 'verification' }} policy {{ result.policy.name }} from {{ result.storeHost }}
        </span>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js src/views/RestoreView.spec.js`
Expected: PASS.

- [ ] **Step 5: Run the full web unit test suite**

Run: `cd web && npm test`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/stores/restoreSubmission.js web/src/views/RestoreView.vue web/src/stores/restoreSubmission.spec.js web/src/views/RestoreView.spec.js
git commit -m "feat(web): distinguish restore vs verify success copy

mode=restore no longer always 501s (Task 3), so a successful restore
submission must say 'Started restore policy', not 'Started
verification policy'."
```

---

### Task 10: Documentation and changelog

**Files:**
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/api-server.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `docs/components/rwfs.md`
- Modify: `docs/protocols/restore.md`
- Modify: `docs/components/agent.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: the final, shipped behavior of Tasks 1-9.
- Produces: nothing consumed by other tasks — terminal documentation task, per
  `.claude/CLAUDE.md`'s protocol-change and feature-change rules (this plan touched `.proto` and
  regenerated `*.pb.go` in Task 1).

- [ ] **Step 1: `docs/protocols/policy-server.md`**

Find the `Policy`/`CreatePolicyRequest` proto blocks (search `repeated RestoreRule rules = 19` and
`= 14`) and add the `mode`/`overwrite` fields shown in Task 1's Step 1, verbatim, to both blocks.
In the `"restore"` policy prose section, add a sentence: `mode` (`"verify"`, the default, or
`"restore"`) selects which action `agent` performs -- `rwfs verify` (unchanged) or the new
log-only `rwfs restore` (resolves and logs the file list, writes nothing yet). `overwrite` is
carried through and logged by `rwfs restore`; it has no effect under `mode: "verify"`.

- [ ] **Step 2: `docs/components/policy-server.md`**

In the "Policy types and directory layout" section's `"restore"` policy paragraph (the one
describing `rules`/`dest_path`/`not_before`/`not_after`), append: `mode` and `overwrite` (see
[Design: Restore Execution — Log-Only First Slice](../superpowers/specs/
2026-08-16-restore-execute-log-only-design.md)) select and parameterize which action `agent`
performs for this policy.

- [ ] **Step 3: `docs/components/api-server.md`**

In the `POST /restore` description (the paragraph documenting `mode`/`overwrite`, added by the
2026-08-14 design), replace the sentence noting `mode: "restore"` returns `501` with: `mode:
"restore"` now creates a real restore-typed policy, exactly like `mode: "verify"` -- `agent` picks
it up and runs the new `rwfs restore` subcommand, which this round only resolves and logs the file
list (see [Design: Restore Execution — Log-Only First Slice](../superpowers/specs/
2026-08-16-restore-execute-log-only-design.md)); no file is written to disk yet.

- [ ] **Step 4: `docs/api/rest-v1.md`**

In the `POST /api/v1/restore` section, replace the `mode: "restore"` paragraph (currently
describing the `501` response) with: `mode: "restore"` creates the policy exactly like `mode:
"verify"` (`201` with the created policy), but `agent` runs `rwfs restore` against it instead of
`rwfs verify` -- this round, `rwfs restore` only resolves the policy's rules against the live store
and logs each file's source path, computed destination path, and the policy's `overwrite` setting;
it writes nothing.

- [ ] **Step 5: `docs/components/rwfs.md`**

Add a new `## restore` section, mirroring the existing `## verify` section's structure (usage
example, flags table, exit-code note), placed after `## verify` and before `## Transport Security`:

```markdown
## restore

Resolves a restore policy's rules against a remote `bwfs` server's file listing and logs what a
real restore of that policy would do -- **this round writes nothing to disk**. Requires
`--rules-stdin` (the only way to select files; there is no plain-listing restore mode).

```bash
# Preview a restore policy's resolved file list
echo '{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":"/data/photos_recovered"}]}' \
  | rwfs restore localhost:8080 --rules-stdin
```

For each resolved file, logs `source`, `path` (original), and `dest_path` (the `dest_path` rename
rule applied -- see [restore protocol](../protocols/restore.md)). Logs the run's `overwrite`
setting once at start; `overwrite` currently has no effect (nothing is written yet, so there is
nothing to overwrite or skip).

### Flags

| Flag | Default | Description |
|------|---------|--------------|
| `--rules-stdin` | | **Required.** Read `{"rules":[...]}` from stdin -- same shape `verify --rules-stdin` uses. |
| `--overwrite` | false | Logged only; not yet enforced. |
| `--quiet` | false | Suppress per-file resolved lines (warnings and summary always shown) |
| `--job-id` | auto-generated UUID | Correlation ID for this invocation's logs; also sent to `bwfs` as `job-id` gRPC metadata |

Exit code follows the same not-found rule `verify --rules-stdin` uses: a file-level rule matching no
row is a failure (non-zero exit); a folder-level rule matching nothing is not.
```

- [ ] **Step 6: `docs/protocols/restore.md`**

In the "CLI → RPC Mapping" section, after the existing `--rules-stdin` paragraph (the one ending
"...like `bootstrap-refresh` below)"), add:

```markdown
`rwfs restore --rules-stdin` calls only `ListService.ResolveRestoreFiles` -- unlike `rwfs verify
--rules-stdin`, it never calls `RestoreFile`, since this round only resolves and logs the file
list without reading any chunk data.
```

- [ ] **Step 7: `docs/components/agent.md`**

Split the "Policy-driven restore verification" section (currently documenting `restore:
<policy-name>` as the task ID for every restore policy) into two: rename it "Policy-driven restore
verification" (updated to say the task ID is now `verify:<policy-name>`, everywhere the section
currently says `restore:<policy>`), and add a new "Policy-driven restore execution" section
immediately after it:

```markdown
## Policy-driven restore execution

A `"restore"`-typed policy whose `mode` is `"restore"` gets a task with a `restore:<policy-name>`
ID instead -- otherwise identical to restore verification above (one task per policy, one-shot,
same failure backoff, `list-policies` row). `agent` execs `rwfs restore <destinations[0]>
--rules-stdin --job-id restore:<policy>:<timestamp>`, with `--overwrite` appended when the policy's
`overwrite` field is set, piping the same `{"rules": [...]}` payload verification uses.

This round, `rwfs restore` only resolves the policy's rules against the live store and logs each
file's source path and renamed destination path -- see [rwfs](./rwfs.md)'s `restore` section. No
file is written to disk yet; a future round adds that. See [Design: Restore Execution — Log-Only
First Slice](../superpowers/specs/2026-08-16-restore-execute-log-only-design.md).
```

In the "Logging and correlation" section, update the job-id prefix list (currently `restore:
<policy>:<timestamp>` for restore-verification tasks) to: `verify:<policy>:<timestamp>` for restore
*verification* tasks, `restore:<policy>:<timestamp>` for restore *execution* tasks.

- [ ] **Step 8: `docs/ARCHITECTURE.md`**

Find `agent`'s role-line description of restore-policy verification and extend it to mention the
new log-only restore-execution path alongside it (one clause, not a new paragraph -- match this
doc's existing terse per-component style).

- [ ] **Step 9: Add a CHANGELOG entry**

In `CHANGELOG.md`, insert a new entry immediately after line 3 (the `All notable changes...` line),
before the current top entry:

```markdown
## 2026-08-16 — restore execution: first slice (log-only)

`mode: "restore"` on `POST /api/v1/restore` now actually creates a restore policy instead of always
returning `501` -- `agent` picks it up and runs a new `rwfs restore` subcommand under a `restore:`
task-id prefix (verification tasks move to a `verify:` prefix, freeing `restore:` for this). This
round, `rwfs restore` only resolves the policy's rules against the live store (reusing `rwfs
verify --rules-stdin`'s exact resolution pipeline) and logs each file's source path, its `dest_path`
rename applied, and the policy's `overwrite` setting -- it writes nothing to disk. A future round
adds the actual write path.

```

- [ ] **Step 10: Verify the doc edits render sensibly**

Run: `git diff docs/ CHANGELOG.md`
Expected: a clean, readable diff -- no broken markdown, no stray blank lines inside the CHANGELOG
entry, every added cross-link path resolves to a file that actually exists (spot-check with `ls
docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md`).

- [ ] **Step 11: Commit**

```bash
git add docs/ CHANGELOG.md
git commit -m "docs: document restore execution's log-only first slice

Per .claude/CLAUDE.md's protocol-change, feature-change, and
changelog rules."
```
