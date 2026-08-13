# Restore Destination Rename Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator give any restore-cart selection (file or folder) a different destination path than its source path, and see storage host / source host / size for each selection, in the restore review screen — stored on the `"restore"` policy for a future restore executor to read.

**Architecture:** Add one optional `dest_path` field to the existing `RestoreRule` message/struct/DTO, threaded through proto → policy-server → api-server unchanged in meaning everywhere else. On the web side, the restore cart's rule objects grow two purely client-side display fields (`storeHost`, `size`, captured off the catalog row at selection time) and one persisted field (`destPath`, defaulting to the source `path`); `RestoreView.vue` becomes a table with a click-to-edit destination-path cell, and submission strips the display-only fields and omits `dest_path` on the wire whenever it's unchanged.

**Tech Stack:** Go (policy-server, api-server, protobuf/gRPC), Vue 3 + Pinia (web), Vitest (web tests), Go's `testing` + `testify` (Go tests).

## Global Constraints

- Scope is **web, api-server, and policy-server only** — no changes to `agent`, `rwfs`, `policyclient`, or `bwfs`. Nothing downstream of policy-server reads `dest_path` yet.
- `dest_path` empty, or equal to `path`, always means "no rename." Only ever meaningful on a rule with `include: true`; setting it on an excluded rule is a validation error.
- No new REST route. No new proto RPC. Every change is a field addition to existing messages/structs/DTOs.
- Per `.claude/CLAUDE.md`: update the affected `docs/protocols/`, `docs/components/`, and `CHANGELOG.md` files before this is done — Task 9 covers this.
- Full design reference: `docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md`.

---

### Task 1: `RestoreRule` proto schema — add `dest_path`

**Files:**
- Modify: `src/api/policyserver.proto:52-56`
- Modify (generated): `src/api/policyserver.pb.go` (via `make proto`, not hand-edited)

**Interfaces:**
- Produces: `pb.RestoreRule.DestPath string` / `pb.RestoreRule.GetDestPath() string`, used by Task 2 and Task 3.

- [ ] **Step 1: Add the `dest_path` field to the proto message**

Edit `src/api/policyserver.proto`, replacing the existing `RestoreRule` message (lines 52-56 as currently generated in `docs/protocols/policy-server.md`'s mirrored block — the real proto's `RestoreRule` is the one under `message RestoreRule {` in `src/api/policyserver.proto`):

```proto
// One restore-cart selection rule: host-agnostic (Host == "") folder rules
// and host-specific file rules resolve by longest-matching-path-ancestor,
// exactly like web/src/utils/restoreRules.js's resolveFile. policy-server
// never interprets these beyond the load-time validation in
// RestorePolicy.Validate (non-empty Path, and dest_path only on an included
// rule); resolution happens at verify time, in rwfs.
message RestoreRule {
  string host      = 1; // "" = host-agnostic, matches every source host
  string path      = 2;
  bool   include    = 3;
  // Destination path to restore to, if different from path. Empty (or
  // equal to path) means "no rename -- restore to the original path."
  // Only meaningful when include is true; see RestorePolicy.Validate.
  string dest_path = 4;
}
```

- [ ] **Step 2: Regenerate the Go protobuf code**

Run: `cd /home/alex/miniprotector && make proto`
Expected: `Protobuf code generated in src/api/` printed, `src/api/policyserver.pb.go` modified with a new `DestPath` field and `GetDestPath()` method on the generated `RestoreRule` struct.

- [ ] **Step 3: Verify the module still builds**

Run: `cd /home/alex/miniprotector/src && go build ./...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go
git commit -m "feat(proto): add dest_path to RestoreRule"
```

---

### Task 2: `policy-server` — `RestoreRule.DestPath`, validation, `ToProto`

**Files:**
- Modify: `src/cmd/policy-server/restore_policy.go`
- Test: `src/cmd/policy-server/restore_policy_test.go`

**Interfaces:**
- Consumes: `pb.RestoreRule.DestPath`/`GetDestPath()` (Task 1).
- Produces: `RestoreRule{Host, Path string; Include bool; DestPath string}` (policy-server's own Go type, extended); `RestorePolicy.Validate()` now rejects `dest_path` set on an excluded rule; `RestorePolicy.ToProto` includes `DestPath` in the produced `pb.RestoreRule`. Used by Task 3.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/restore_policy_test.go`:

```go
func TestRestorePolicy_ValidateDestPathOnExcludedRuleFails(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: false, DestPath: "/a-renamed"}},
	}
	assert.Error(t, p.Validate())
}

func TestRestorePolicy_ValidateDestPathEqualToPathOnExcludedRuleSucceeds(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules: []RestoreRule{
			{Path: "/a", Include: true},
			{Path: "/a/secret", Include: false, DestPath: "/a/secret"},
		},
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ValidateDestPathOnIncludedRuleSucceeds(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "x"}},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Path: "/a", Include: true, DestPath: "/a-renamed"}},
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ToProtoIncludesDestPath(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase: PolicyBase{
			Metadata: Metadata{ID: "r1", Name: "x"},
			Type:     "restore",
		},
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true, DestPath: "/var/www/index.html.bak"}},
	}

	pp := p.ToProto(false)

	require.Len(t, pp.GetRules(), 1)
	assert.Equal(t, "/var/www/index.html.bak", pp.GetRules()[0].GetDestPath())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/policy-server/... -run TestRestorePolicy_ValidateDestPath -v && go test ./cmd/policy-server/... -run TestRestorePolicy_ToProtoIncludesDestPath -v`
Expected: `TestRestorePolicy_ValidateDestPathOnExcludedRuleFails` FAILs (no error returned — `DestPath` field doesn't exist yet, so this won't even compile). Expected compile error: `unknown field DestPath in struct literal of type RestoreRule`.

- [ ] **Step 3: Add `DestPath` to the struct, `Validate`, and `ToProto`**

Edit `src/cmd/policy-server/restore_policy.go`:

```go
// RestoreRule is one restore-cart selection rule -- {host, path, include,
// dest_path} mirroring web/src/utils/restoreRules.js's rule shape exactly,
// so the frontend can send its cart.rules through with no reshaping. Host
// == "" means host-agnostic (a folder rule that applies across every
// source host, matching restoreRules.js's `host: null` convention -- a
// JSON null decodes to Go's zero-value "" automatically); a non-empty Host
// scopes the rule to exactly that source. DestPath, if non-empty and
// different from Path, is the path to restore to instead of Path -- only
// meaningful when Include is true (see Validate). policy-server never
// resolves any of this against a real file listing or acts on DestPath --
// resolution happens at verify time, in rwfs, and DestPath is not consumed
// anywhere yet (no restore executor exists). See
// docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md.
type RestoreRule struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	Include  bool   `json:"include"`
	DestPath string `json:"dest_path,omitempty"`
}
```

In `Validate()`, extend the per-rule loop:

```go
	for i, r := range p.Rules {
		if r.Path == "" {
			return fmt.Errorf("rules[%d]: path is required", i)
		}
		if r.DestPath != "" && r.DestPath != r.Path && !r.Include {
			return fmt.Errorf("rules[%d]: dest_path is only valid on an included rule", i)
		}
	}
```

In `ToProto`, pass `DestPath` through:

```go
	rules := make([]*pb.RestoreRule, len(p.Rules))
	for i, r := range p.Rules {
		rules[i] = &pb.RestoreRule{Host: r.Host, Path: r.Path, Include: r.Include, DestPath: r.DestPath}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/policy-server/... -run TestRestorePolicy -v`
Expected: PASS for all `TestRestorePolicy_*` tests, including the four new ones and every pre-existing one (`ValidateValidPolicyReturnsNil`, `ToProtoSetsTypeSpecificFields`, etc. — none of their assertions reference `DestPath`, so a zero-value `""` satisfies them unchanged).

- [ ] **Step 5: Run the full policy-server package tests**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/policy-server/... -v`
Expected: PASS, no regressions in `write_test.go`/`server_test.go`.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/restore_policy.go src/cmd/policy-server/restore_policy_test.go
git commit -m "feat(policy-server): add dest_path to RestoreRule with include-only validation"
```

---

### Task 3: `api-server` — `ruleDTO.DestPath` through create and read paths

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Test: `src/cmd/api-server/policies_test.go`

**Interfaces:**
- Consumes: `pb.RestoreRule.DestPath`/`GetDestPath()` (Task 1), `RestorePolicy`/policy-server's `CreatePolicy` (Task 2, via the existing gRPC client interface).
- Produces: `ruleDTO{Host, Path string; Include bool; DestPath string}`, round-tripped by `toPolicyDTO` and accepted by `decodeRestorePolicyInput`/`handleCreateRestore`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/policies_test.go`:

```go
func TestToPolicyDTO_IncludesDestPathForRestore(t *testing.T) {
	p := &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		StoragePolicyId: "sp-1",
		Rules: []*pb.RestoreRule{
			{Host: "web-01", Path: "/var/www/index.html", Include: true, DestPath: "/var/www/index.html.bak"},
		},
	}

	dto := toPolicyDTO(p)

	require.Len(t, dto.Rules, 1)
	assert.Equal(t, "/var/www/index.html.bak", dto.Rules[0].DestPath)
}

func TestHandleCreateRestore_PassesDestPathThrough(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		StoragePolicyId: "sp-1",
		Rules: []*pb.RestoreRule{
			{Host: "web-01", Path: "/var/www/index.html", Include: true, DestPath: "/var/www/index.html.bak"},
		},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web01-emergency",
		"storage_policy_id": "sp-1",
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true, "dest_path": "/var/www/index.html.bak"}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	require.Len(t, fake.lastCreateReq.GetRules(), 1)
	assert.Equal(t, "/var/www/index.html.bak", fake.lastCreateReq.GetRules()[0].GetDestPath())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/api-server/... -run 'TestToPolicyDTO_IncludesDestPathForRestore|TestHandleCreateRestore_PassesDestPathThrough' -v`
Expected: compile FAIL — `ruleDTO` has no field `DestPath`.

- [ ] **Step 3: Add `DestPath` to `ruleDTO` and thread it through both directions**

Edit `src/cmd/api-server/policies.go`:

```go
type ruleDTO struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	Include  bool   `json:"include"`
	DestPath string `json:"dest_path,omitempty"`
}
```

In `toPolicyDTO`:

```go
	rules := make([]ruleDTO, len(p.GetRules()))
	for i, r := range p.GetRules() {
		rules[i] = ruleDTO{Host: r.GetHost(), Path: r.GetPath(), Include: r.GetInclude(), DestPath: r.GetDestPath()}
	}
```

In `handleCreateRestore`:

```go
	rules := make([]*pb.RestoreRule, len(in.Rules))
	for i, ru := range in.Rules {
		rules[i] = &pb.RestoreRule{Host: ru.Host, Path: ru.Path, Include: ru.Include, DestPath: ru.DestPath}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/api-server/... -run 'TestToPolicyDTO|TestHandleCreateRestore' -v`
Expected: PASS for all matched tests, old and new.

- [ ] **Step 5: Run the full api-server package tests**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/api-server/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): pass dest_path through restore policy create/read"
```

---

### Task 4: `web/src/utils/restoreRules.js` — default `destPath` on newly created rules

**Files:**
- Modify: `web/src/utils/restoreRules.js`
- Test: `web/src/utils/restoreRules.spec.js`

**Interfaces:**
- Produces: `toggleFile(rules, host, path, extra = {})` / `toggleFolder(rules, path, extra = {})` — both now accept an optional fourth-argument (third for `toggleFolder`) object merged onto a *newly created* rule, and both set `destPath: path` on a newly created rule by default. Used by Task 5.

- [ ] **Step 1: Update the failing assertions in `restoreRules.spec.js`**

Edit `web/src/utils/restoreRules.spec.js` — every `toggleFolder`/`toggleFile` test whose expected array contains a **newly created** rule gains `destPath` on that entry (entries that are pre-existing, untouched, or the empty-array case are unaffected — see comments below marking which). Replace the `describe('toggleFolder', ...)` and `describe('toggleFile', ...)` blocks with:

```js
describe('toggleFolder', () => {
  it('adds a wildcard rule when unchecked with no existing rules', () => {
    const result = toggleFolder([], '/etc')
    expect(result).toEqual([{ path: '/etc', host: null, include: true, destPath: '/etc' }])
  })

  it('removes the exact rule when checked via its own rule', () => {
    const rules = [{ path: '/etc', host: null, include: true, destPath: '/etc' }]
    expect(toggleFolder(rules, '/etc')).toEqual([])
  })

  it('adds an exception when checked via an inherited ancestor rule', () => {
    const rules = [{ path: '/', host: null, include: true, destPath: '/' }]
    const result = toggleFolder(rules, '/etc')
    expect(result).toEqual([
      { path: '/', host: null, include: true, destPath: '/' },
      { path: '/etc', host: null, include: false, destPath: '/etc' },
    ])
  })

  it('prunes nested exceptions when re-checking a folder, without a redundant rule', () => {
    const rules = [
      { path: '/var', host: null, include: true, destPath: '/var' },
      { path: '/var/log', host: null, include: false, destPath: '/var/log' },
      { path: '/var/log/nginx', host: null, include: true, destPath: '/var/log/nginx' },
    ]
    // /var/log is indeterminate; checking it should clear everything
    // under it and, since /var already covers it, add nothing new -- the
    // surviving /var rule is untouched, so it keeps its original destPath.
    expect(toggleFolder(rules, '/var/log')).toEqual([{ path: '/var', host: null, include: true, destPath: '/var' }])
  })

  it('prunes nested rules and adds a fresh wildcard when checking an uncovered indeterminate folder', () => {
    const rules = [{ path: '/var/log/access.log', host: 'web01', include: true, destPath: '/var/log/access.log' }]
    expect(toggleFolder(rules, '/var/log')).toEqual([
      { path: '/var/log', host: null, include: true, destPath: '/var/log' },
    ])
  })

  it('prunes a host-specific file exception nested under a newly re-checked folder', () => {
    const rules = [
      { path: '/etc', host: null, include: true, destPath: '/etc' },
      { path: '/etc/hosts', host: 'web01', include: false, destPath: '/etc/hosts' },
    ]
    // Both existing rules at/under /etc are pruned, so the surviving rule
    // is freshly created here (not the original object) and gets a fresh
    // destPath.
    expect(toggleFolder(rules, '/etc')).toEqual([{ path: '/etc', host: null, include: true, destPath: '/etc' }])
  })

  it('threads an extra-properties object onto a newly created rule', () => {
    const result = toggleFolder([], '/etc', { storeHost: 'bwfs-1' })
    expect(result).toEqual([{ path: '/etc', host: null, include: true, destPath: '/etc', storeHost: 'bwfs-1' }])
  })
})

describe('toggleFile', () => {
  it('adds an include rule when unchecked with no existing rules', () => {
    expect(toggleFile([], 'web01', '/etc/hosts')).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' },
    ])
  })

  it('removes the exact rule when checked via its own rule', () => {
    const rules = [{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }]
    expect(toggleFile(rules, 'web01', '/etc/hosts')).toEqual([])
  })

  it('adds a host-specific exception when checked via an inherited ancestor folder rule', () => {
    const rules = [{ path: '/etc', host: null, include: true, destPath: '/etc' }]
    const result = toggleFile(rules, 'web01', '/etc/hosts')
    expect(result).toEqual([
      { path: '/etc', host: null, include: true, destPath: '/etc' },
      { path: '/etc/hosts', host: 'web01', include: false, destPath: '/etc/hosts' },
    ])
  })

  it('removes a host-specific exception to re-check a file, reverting to the ancestor rule', () => {
    const rules = [
      { path: '/etc', host: null, include: true, destPath: '/etc' },
      { path: '/etc/hosts', host: 'web01', include: false, destPath: '/etc/hosts' },
    ]
    expect(toggleFile(rules, 'web01', '/etc/hosts')).toEqual([
      { path: '/etc', host: null, include: true, destPath: '/etc' },
    ])
  })

  it('does not affect other hosts sharing the same path', () => {
    const rules = [{ path: '/etc', host: null, include: true, destPath: '/etc' }]
    const result = toggleFile(rules, 'web01', '/etc/hosts')
    expect(resolveFile(result, 'db02', '/etc/hosts')).toBe(true)
  })

  it('threads an extra-properties object onto a newly created rule', () => {
    const result = toggleFile([], 'web01', '/etc/hosts', { storeHost: 'bwfs-1', size: 4096 })
    expect(result).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts', storeHost: 'bwfs-1', size: 4096 },
    ])
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/utils/restoreRules.spec.js`
Expected: FAIL — actual rules don't include `destPath`/`storeHost`/`size`, and the two new "threads an extra-properties object" tests fail because `toggleFile`/`toggleFolder` don't accept a fourth/third argument yet.

- [ ] **Step 3: Implement `destPath` defaulting and the `extra` param**

Edit `web/src/utils/restoreRules.js`:

```js
// toggleFolder returns a new rule list with path's selection flipped.
// Checked -> unchecked mirrors the exact-rule-removal trick below (a
// state of 'checked' guarantees nothing sits underneath, so no pruning
// is needed there). Unchecked/indeterminate -> checked first prunes
// every rule at-or-under path -- clearing any exceptions or partial
// selections underneath -- then adds a fresh wildcard only if the
// remaining rules don't already cover path via an ancestor (avoiding a
// redundant rule). A newly created rule defaults destPath to path (no
// rename) and merges in any caller-supplied extra display-only
// properties (see restoreCart.js).
export function toggleFolder(rules, path, extra = {}) {
  const state = resolveFolderState(rules, path)
  if (state === 'checked') {
    const exact = rules.find((r) => r.host === null && r.path === path)
    if (exact) return rules.filter((r) => r !== exact)
    return [...rules, { path, host: null, include: false, destPath: path, ...extra }]
  }
  const pruned = rules.filter((r) => r.path !== path && !isStrictDescendantPath(r.path, path))
  if (longestMatchingFolderRule(pruned, path) === true) return pruned
  return [...pruned, { path, host: null, include: true, destPath: path, ...extra }]
}

// toggleFile returns a new rule list with (host, path)'s selection
// flipped. If an exact rule already exists at (host, path), it is
// removed: by the pruning invariant maintained throughout this module,
// a stored rule only ever exists because it overrides its closest
// ancestor, so removing it always flips the resolved state back.
// Otherwise a fresh rule is added with the opposite of the current
// resolved state, destPath defaulting to path (no rename), merged with
// any caller-supplied extra display-only properties.
export function toggleFile(rules, host, path, extra = {}) {
  const exact = rules.find((r) => r.host === host && r.path === path)
  if (exact) return rules.filter((r) => r !== exact)
  const checked = resolveFile(rules, host, path)
  return [...rules, { path, host, include: !checked, destPath: path, ...extra }]
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/utils/restoreRules.spec.js`
Expected: PASS, all tests including `resolveFile`/`resolveFolderState` blocks (unchanged, still passing).

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/restoreRules.js web/src/utils/restoreRules.spec.js
git commit -m "feat(web): default destPath and thread extra props through restore rule toggles"
```

---

### Task 5: `web/src/stores/restoreCart.js` — `setDestPath`, `toggleFile` display-field threading

**Files:**
- Modify: `web/src/stores/restoreCart.js`
- Test: `web/src/stores/restoreCart.spec.js`

**Interfaces:**
- Consumes: `toggleFile(rules, host, path, extra)` / `toggleFolder(rules, path, extra)` (Task 4).
- Produces: `useRestoreCartStore().setDestPath(entry, destPath)`; `useRestoreCartStore().toggleFile(host, path, storeHost, size)` (two new optional trailing params). Used by Task 6 (`CatalogView.vue`) and Task 8 (`RestoreView.vue`).

- [ ] **Step 1: Write the failing tests**

Add to `web/src/stores/restoreCart.spec.js`, inside the existing `describe('restoreCart store', ...)` block:

```js
  it('toggleFile threads storeHost and size onto the created rule when passed', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', 'bwfs-1', 4096)
    expect(cart.rules).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts', storeHost: 'bwfs-1', size: 4096 },
    ])
  })

  it('setDestPath updates the matching rule and leaves others untouched', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    cart.toggleFolder('/var')

    cart.setDestPath({ host: 'web01', path: '/etc/hosts' }, '/etc/hosts.bak')

    expect(cart.rules).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts.bak' },
      { path: '/var', host: null, include: true, destPath: '/var' },
    ])
  })

  it('setDestPath on a folder rule updates its destPath', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')

    cart.setDestPath({ host: null, path: '/var' }, '/var_recovered')

    expect(cart.rules).toEqual([{ path: '/var', host: null, include: true, destPath: '/var_recovered' }])
  })

  it('setDestPath is a no-op when no rule matches', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')

    cart.setDestPath({ host: 'web02', path: '/nope' }, '/renamed')

    expect(cart.rules).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' },
    ])
  })
```

Also update the two pre-existing tests whose expectations now need `destPath`:

```js
  it('toggleFile adds a rule and updates hasSelections/entries', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toEqual([{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }])
    expect(cart.hasSelections).toBe(true)
    expect(cart.entries).toEqual([{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }])
  })
```

```js
  it('toggleFolder adds a wildcard rule', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')
    expect(cart.rules).toEqual([{ path: '/var', host: null, include: true, destPath: '/var' }])
    expect(cart.hasSelections).toBe(true)
  })
```

```js
  it('entries excludes exception (include: false) rules', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/etc')
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toHaveLength(2)
    expect(cart.entries).toEqual([{ path: '/etc', host: null, include: true, destPath: '/etc' }])
  })
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/stores/restoreCart.spec.js`
Expected: FAIL — `setDestPath` doesn't exist (`TypeError: cart.setDestPath is not a function`), and `toggleFile` ignores the third/fourth arguments.

- [ ] **Step 3: Implement `setDestPath` and pass through `toggleFile`'s new params**

Edit `web/src/stores/restoreCart.js`:

```js
import { defineStore } from 'pinia'
import { toggleFile as toggleFileRule, toggleFolder as toggleFolderRule } from '../utils/restoreRules'

export const useRestoreCartStore = defineStore('restoreCart', {
  state: () => ({
    rules: [],
  }),
  getters: {
    hasSelections: (state) => state.rules.length > 0,
    entries: (state) => state.rules.filter((r) => r.include),
  },
  actions: {
    // storeHost/size are optional, display-only (never sent to the API --
    // see restoreSubmission.js's toWireRule) -- captured off the catalog
    // row at selection time since the cart's rule shape otherwise has no
    // way to know either.
    toggleFile(host, path, storeHost, size) {
      this.rules = toggleFileRule(this.rules, host, path, { storeHost, size })
    },
    toggleFolder(path) {
      this.rules = toggleFolderRule(this.rules, path)
    },
    removeEntry(entry) {
      if (entry.host === null) this.toggleFolder(entry.path)
      else this.toggleFile(entry.host, entry.path)
    },
    // setDestPath mutates the exact rule matching (entry.host, entry.path)
    // in place -- a no-op if none matches (e.g. the entry was just
    // removed out from under an in-flight edit).
    setDestPath(entry, destPath) {
      const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
      if (rule) rule.destPath = destPath
    },
  },
})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/stores/restoreCart.spec.js`
Expected: PASS, all tests. (Note: `toggleFile(host, path)` called with no third/fourth argument passes `storeHost`/`size` as `undefined` into `extra`; Vitest's `toEqual` treats an explicit `undefined`-valued property as equal to the property being absent, so the pre-existing two-argument call sites' expectations — which don't mention `storeHost`/`size` — still pass unchanged.)

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/restoreCart.js web/src/stores/restoreCart.spec.js
git commit -m "feat(web): add restoreCart.setDestPath and thread storeHost/size through toggleFile"
```

---

### Task 6: `web/src/views/CatalogView.vue` — capture `storeHost`/`size` at selection time

**Files:**
- Modify: `web/src/views/CatalogView.vue:56-59`
- Test: `web/src/views/CatalogView.spec.js`

**Interfaces:**
- Consumes: `restoreCart.toggleFile(host, path, storeHost, size)` (Task 5).

- [ ] **Step 1: Update the failing assertion**

Edit `web/src/views/CatalogView.spec.js`, the `'clicking a file checkbox calls restoreCart.toggleFile and does not navigate'` test (around line 316-332):

```js
  it('clicking a file checkbox calls restoreCart.toggleFile and does not navigate', async () => {
    const { wrapper, catalog, restoreCart } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db' })],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    await checkbox.trigger('click')
    await checkbox.trigger('change')
    expect(restoreCart.toggleFile).toHaveBeenCalledWith('database', '/var/lib/dbdata/data.db', 'bwfs-east', 8192)
    expect(catalog.navigateTo).not.toHaveBeenCalled()
  })
```

(`'bwfs-east'`/`8192` are the `entry()` test-fixture helper's defaults for `store_host`/`size`, at `web/src/views/CatalogView.spec.js:11-31` — unchanged by this task.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/views/CatalogView.spec.js -t "clicking a file checkbox"`
Expected: FAIL — `toggleFile` called with only `('database', '/var/lib/dbdata/data.db')`, missing the two new arguments.

- [ ] **Step 3: Pass `store_host`/`size` from the row into `toggleFile`**

Edit `web/src/views/CatalogView.vue`:

```js
function toggleSelection(row) {
  if (row.isFolder) restoreCart.toggleFolder(row.path)
  else restoreCart.toggleFile(row.sourceHost, row.path, row.representative?.store_host, row.representative?.size)
}
```

- [ ] **Step 4: Run the full CatalogView spec to verify it passes**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/views/CatalogView.spec.js`
Expected: PASS, all tests (folder-checkbox test at line 364 is unaffected — `toggleFolder` takes no new arguments).

- [ ] **Step 5: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js
git commit -m "feat(web): capture store host and size on the restore cart at file-selection time"
```

---

### Task 7: `web/src/stores/restoreSubmission.js` — wire-shape mapping (`toWireRule`)

**Files:**
- Modify: `web/src/stores/restoreSubmission.js`
- Test: `web/src/stores/restoreSubmission.spec.js`

**Interfaces:**
- Consumes: cart rules now carrying `destPath`/`storeHost`/`size` (Tasks 4-6).
- Produces: `POST /api/v1/restore` bodies whose `rules` entries are `{host, path, include, dest_path?}` — `storeHost`/`size` never sent; `dest_path` present only when the rule's `destPath` differs from its `path`.

- [ ] **Step 1: Update the failing test expectations**

Edit `web/src/stores/restoreSubmission.spec.js`. The one test building its expected `POST /restore` body from `cart.rules` directly needs to build the expected wire-shape explicitly instead, since `cart.rules` now carries `destPath` (equal to `path`, so it must be omitted on the wire) — replace the `'sends the full, unsplit rule list to the one store a folder rule touches'` test's final assertion:

```js
  it('sends the full, unsplit rule list to the one store a folder rule touches', async () => {
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
        return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.error).toBeNull()
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
    ])
    expect(apiFetch).toHaveBeenCalledWith('/restore', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'restore-2026-08-10T00:00:00.000Z-store-a',
        client_filters: { hostnames: ['web01'], labels: {} },
        storage_policy_id: 's1',
        rules: [{ host: null, path: '/var/lib/dbdata', include: true }],
      }),
    })
  })
```

(Every other existing test in this file already asserts against inline `{ path, host, include }` literals via `folderRule`/`rulesByStoreFromCalls()` rather than `cart.rules` directly, so their expectations are already the correct wire shape and need no change — `destPath` defaults to `path` on every rule those tests create, so `toWireRule` omits `dest_path` for all of them, same as today's output.)

Add two new tests at the end of the `describe` block, before the closing `})`:

```js
  it('includes dest_path on the wire only for a rule whose destPath differs from its path', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/nginx/nginx.conf')
    cart.setDestPath({ host: 'web01', path: '/etc/nginx/nginx.conf' }, '/etc/nginx/nginx.conf.bak')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules).toEqual([
      { host: 'web01', path: '/etc/nginx/nginx.conf', include: true, dest_path: '/etc/nginx/nginx.conf.bak' },
    ])
  })

  it('never sends storeHost or size on the wire', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', 'bwfs-1', 4096)

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules[0]).not.toHaveProperty('storeHost')
    expect(body.rules[0]).not.toHaveProperty('size')
    expect(body.rules[0]).not.toHaveProperty('dest_path')
  })
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: FAIL — the updated first test fails because today's code still sends raw `cart.rules` (which now includes `destPath: '/var/lib/dbdata'`, not stripped); the two new tests fail because `dest_path` is never added and `storeHost`/`size` are sent as-is.

- [ ] **Step 3: Implement `toWireRule` and use it when building each store's payload**

Edit `web/src/stores/restoreSubmission.js`, adding the mapping function near the top (after `buildRulesByStore`, before `storagePolicyIdForHost`):

```js
// toWireRule strips the cart's client-only display fields (storeHost,
// size -- see restoreCart.js's toggleFile) and omits dest_path entirely
// when it's unchanged from path (the "no rename" case), so a rule nobody
// renamed produces exactly today's wire shape.
function toWireRule(rule) {
  const wire = { host: rule.host, path: rule.path, include: rule.include }
  if (rule.destPath && rule.destPath !== rule.path) wire.dest_path = rule.destPath
  return wire
}
```

In the `submit` action, map each store's rules through it right before calling `restorePolicies.create`:

```js
          try {
            const name = `restore-${new Date().toISOString()}-${storeHost}`
            const policy = await restorePolicies.create({
              name,
              client_filters: { hostnames: [destinationHost], labels: {} },
              storage_policy_id: storagePolicyId,
              rules: rules.map(toWireRule),
            })
            results.push({ storeHost, status: 'success', policy })
          } catch (err) {
            results.push({ storeHost, status: 'error', message: err.message })
          }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: PASS, all tests.

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/restoreSubmission.js web/src/stores/restoreSubmission.spec.js
git commit -m "feat(web): map restore-cart rules to their wire shape on submit"
```

---

### Task 8: `web/src/views/RestoreView.vue` — review table with click-to-edit rename

**Files:**
- Modify: `web/src/views/RestoreView.vue`
- Test: `web/src/views/RestoreView.spec.js`

**Interfaces:**
- Consumes: `restoreCart.entries` (now `{host, path, include, destPath, storeHost?, size?}`), `restoreCart.setDestPath(entry, destPath)` (Task 5), `formatBytes` (`web/src/utils/format.js`, existing, unchanged).

- [ ] **Step 1: Replace `RestoreView.spec.js`'s cart-list tests with table tests**

Replace the two tests currently at lines 27-35 (`'lists a folder wildcard rule as path/*'`, `'lists a file rule as path (host)'`) and add new ones. The full updated file:

```js
// web/src/views/RestoreView.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RestoreView from './RestoreView.vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'

function mountView({ rules = [], clientsList = [], submission = {} } = {}) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: {
      restoreCart: { rules },
      clients: { list: clientsList },
      restoreSubmission: { submitting: false, results: [], error: null, ...submission },
    },
  })
  return mount(RestoreView, { global: { plugins: [pinia] } })
}

describe('RestoreView', () => {
  it('shows the empty state when the cart has no selections', () => {
    const wrapper = mountView()
    expect(wrapper.text()).toContain('No files selected for restore yet.')
  })

  it('lists a folder wildcard rule\'s source path as path/*', () => {
    const wrapper = mountView({ rules: [{ path: '/var', host: null, include: true, destPath: '/var' }] })
    expect(wrapper.text()).toContain('/var/*')
  })

  it('shows storage host, source host, source path, and size in separate columns for a file rule', () => {
    const wrapper = mountView({
      rules: [
        {
          path: '/etc/hosts',
          host: 'web01',
          include: true,
          destPath: '/etc/hosts',
          storeHost: 'bwfs-1',
          size: 4096,
        },
      ],
    })
    const cells = wrapper.find('[data-test="restore-row-web01:/etc/hosts"]').findAll('td')
    expect(cells[0].text()).toBe('bwfs-1')
    expect(cells[1].text()).toBe('web01')
    expect(cells[2].text()).toBe('/etc/hosts')
    expect(cells[4].text()).toBe('4.0 KB')
  })

  it('shows dashes for storage host, source host, and size on a folder rule', () => {
    const wrapper = mountView({ rules: [{ path: '/var', host: null, include: true, destPath: '/var' }] })
    const cells = wrapper.find('[data-test="restore-row-:/var"]').findAll('td')
    expect(cells[0].text()).toBe('—')
    expect(cells[1].text()).toBe('—')
    expect(cells[4].text()).toBe('—')
  })

  it('omits exception (include: false) rules from the list', () => {
    const wrapper = mountView({
      rules: [
        { path: '/etc', host: null, include: true, destPath: '/etc' },
        { path: '/etc/hosts', host: 'web01', include: false, destPath: '/etc/hosts' },
      ],
    })
    expect(wrapper.text()).toContain('/etc/*')
    expect(wrapper.text()).not.toContain('/etc/hosts')
  })

  it('renders the page breadcrumb', () => {
    const wrapper = mountView()
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Restore')
  })

  it('removing an entry calls restoreCart.removeEntry with that entry', async () => {
    const entry = { path: '/var', host: null, include: true }
    const wrapper = mountView({ rules: [entry] })
    const cart = useRestoreCartStore()

    await wrapper.find('[data-test="remove-:/var"]').trigger('click')

    expect(cart.removeEntry).toHaveBeenCalledWith(entry)
  })

  it('populates the destination select from the clients store', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }, { hostname: 'web02' }],
    })
    const options = wrapper.find('[data-test="destination-select"]').findAll('option')
    expect(options.map((o) => o.element.value)).toEqual(['', 'web01', 'web02'])
  })

  it('disables submit until the cart has a selection and a destination is chosen', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }],
    })
    expect(wrapper.find('[data-test="submit-restore"]').attributes('disabled')).toBeDefined()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')

    expect(wrapper.find('[data-test="submit-restore"]').attributes('disabled')).toBeUndefined()
  })

  it('clicking submit calls restoreSubmission.submit with the chosen destination', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }],
    })
    const submission = useRestoreSubmissionStore()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')
    await wrapper.find('[data-test="submit-restore"]').trigger('click')

    expect(submission.submit).toHaveBeenCalledWith('web01')
  })

  it('renders a successful submission result', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      submission: { results: [{ storeHost: 'store-a', status: 'success', policy: { name: 'restore-x' } }] },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain('restore-x')
  })

  it('renders a per-group submission error', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      submission: {
        results: [
          { storeHost: 'store-b', status: 'error', message: 'No reachable storage node found for store-b' },
        ],
      },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain(
      'No reachable storage node found for store-b'
    )
  })

  it('renders a submission-level error while the cart is empty', () => {
    const wrapper = mountView({ submission: { error: 'Nothing selected for restore.' } })
    expect(wrapper.text()).toContain('No files selected for restore yet.')
    expect(wrapper.find('[data-test="submission-error"]').text()).toBe('Nothing selected for restore.')
  })

  it('keeps submission results visible after the cart is emptied', () => {
    const wrapper = mountView({
      submission: { results: [{ storeHost: 'store-a', status: 'success', policy: { name: 'restore-x' } }] },
    })
    expect(wrapper.text()).toContain('No files selected for restore yet.')
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain('restore-x')
  })

  it('shows the destination path as plain text by default, prefilled to the source path', () => {
    const wrapper = mountView({
      rules: [{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }],
    })
    expect(wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').text()).toBe('/etc/hosts')
    expect(wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]').exists()).toBe(false)
  })

  it('clicking the destination path shows an editable input prefilled with the current value', async () => {
    const wrapper = mountView({
      rules: [{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }],
    })

    await wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').trigger('click')

    const input = wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]')
    expect(input.exists()).toBe(true)
    expect(input.element.value).toBe('/etc/hosts')
  })

  it('committing an edited destination path calls restoreCart.setDestPath and exits edit mode', async () => {
    const entry = { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }
    const wrapper = mountView({ rules: [entry] })
    const cart = useRestoreCartStore()

    await wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]')
    await input.setValue('/etc/hosts.bak')
    await input.trigger('blur')

    expect(cart.setDestPath).toHaveBeenCalledWith(entry, '/etc/hosts.bak')
    expect(wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').exists()).toBe(true)
  })

  it('pressing Enter in the destination path input commits the edit', async () => {
    const entry = { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }
    const wrapper = mountView({ rules: [entry] })
    const cart = useRestoreCartStore()

    await wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]')
    await input.setValue('/etc/hosts.bak')
    await input.trigger('keyup.enter')

    expect(cart.setDestPath).toHaveBeenCalledWith(entry, '/etc/hosts.bak')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/views/RestoreView.spec.js`
Expected: FAIL — no `[data-test="restore-row-*"]` elements, no `dest-path-text-*`/`dest-path-input-*` elements exist yet; the current `<ul>` markup doesn't match any of the new selectors.

- [ ] **Step 3: Implement the table**

Replace `web/src/views/RestoreView.vue` in full:

```vue
<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useClientsStore } from '../stores/clients'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'
import { formatBytes } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import BaseField from '../components/ui/BaseField.vue'
import BaseSelect from '../components/ui/BaseSelect.vue'

const restoreCart = useRestoreCartStore()
const clients = useClientsStore()
const submission = useRestoreSubmissionStore()

const destinationHost = ref('')
// Key of the entry currently being edited (see entryKey), or null when no
// destination-path cell is in edit mode. Only one cell can be edited at a
// time.
const editingKey = ref(null)

onMounted(() => {
  if (clients.list.length === 0) clients.fetchAll()
})

// entryKey matches the (host, path) pairing the restore cart itself keys
// rules by -- also reused as the data-test suffix for a row and its
// destination-path cell.
function entryKey(entry) {
  return `${entry.host ?? ''}:${entry.path}`
}

function sourcePathLabel(entry) {
  return entry.host === null ? `${entry.path}/*` : entry.path
}

function remove(entry) {
  restoreCart.removeEntry(entry)
}

function startEditing(entry) {
  editingKey.value = entryKey(entry)
}

function commitEdit(entry, value) {
  restoreCart.setDestPath(entry, value)
  editingKey.value = null
}

const canSubmit = computed(
  () => restoreCart.hasSelections && destinationHost.value !== '' && !submission.submitting
)

function submit() {
  submission.submit(destinationHost.value)
}
</script>

<template>
  <div>
    <PageHeader title="Restore" :crumbs="[{ label: 'Restore' }]" />
    <StatusMessage :empty="restoreCart.entries.length === 0" empty-text="No files selected for restore yet.">
      <table data-test="restore-table">
        <thead>
          <tr>
            <th>Storage Host</th>
            <th>Source Host</th>
            <th>Source Path</th>
            <th>Destination Path</th>
            <th>Size</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="entry in restoreCart.entries" :key="entryKey(entry)" :data-test="`restore-row-${entryKey(entry)}`">
            <td>{{ entry.storeHost ?? '—' }}</td>
            <td>{{ entry.host ?? '—' }}</td>
            <td>{{ sourcePathLabel(entry) }}</td>
            <td>
              <input
                v-if="editingKey === entryKey(entry)"
                :data-test="`dest-path-input-${entryKey(entry)}`"
                :value="entry.destPath"
                @blur="commitEdit(entry, $event.target.value)"
                @keyup.enter="commitEdit(entry, $event.target.value)"
              />
              <span v-else :data-test="`dest-path-text-${entryKey(entry)}`" @click="startEditing(entry)">
                {{ entry.destPath }}
              </span>
            </td>
            <td>{{ formatBytes(entry.size) }}</td>
            <td>
              <button type="button" :data-test="`remove-${entryKey(entry)}`" @click="remove(entry)">Remove</button>
            </td>
          </tr>
        </tbody>
      </table>
      <BaseField label="Destination host">
        <BaseSelect data-test="destination-select" v-model="destinationHost">
          <option value="" disabled>Select a destination host</option>
          <option v-for="client in clients.list" :key="client.hostname" :value="client.hostname">
            {{ client.hostname }}
          </option>
        </BaseSelect>
      </BaseField>
      <BaseButton data-test="submit-restore" variant="primary" :disabled="!canSubmit" @click="submit">
        Submit restore
      </BaseButton>
    </StatusMessage>
    <!-- Outside StatusMessage on purpose: its slot only renders for a
         non-empty cart, but the error (e.g. "Nothing selected for restore.")
         and the results of an already-submitted restore must stay visible
         even once the cart is empty. -->
    <p v-if="submission.error" data-test="submission-error">{{ submission.error }}</p>
    <ul v-if="submission.results.length" data-test="submission-results">
      <li v-for="result in submission.results" :key="result.storeHost">
        <span v-if="result.status === 'success'">Created {{ result.policy.name }} from {{ result.storeHost }}</span>
        <span v-else>{{ result.storeHost }}: {{ result.message }}</span>
      </li>
    </ul>
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/views/RestoreView.spec.js`
Expected: PASS, all tests.

- [ ] **Step 5: Run the entire web test suite to check for regressions**

Run: `cd /home/alex/miniprotector/web && npx vitest run`
Expected: PASS, no regressions in unrelated specs.

- [ ] **Step 6: Commit**

```bash
git add web/src/views/RestoreView.vue web/src/views/RestoreView.spec.js
git commit -m "feat(web): restore review screen becomes a table with click-to-edit rename"
```

---

### Task 9: Documentation and changelog

**Files:**
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/web.md`
- Modify: `docs/components/api-server.md`
- Modify: `CHANGELOG.md`

**Interfaces:** None (docs only).

- [ ] **Step 1: Update `docs/protocols/policy-server.md`'s `RestoreRule` block**

Replace the `RestoreRule` message block (currently at lines 52-56):

```proto
message RestoreRule {
  string host    = 1; // "" = host-agnostic, matches every source host
  string path    = 2;
  bool   include = 3;
}
```

with:

```proto
message RestoreRule {
  string host      = 1; // "" = host-agnostic, matches every source host
  string path      = 2;
  bool   include    = 3;
  string dest_path = 4; // destination to restore to if renamed; "" or == path means no rename; only valid when include is true
}
```

- [ ] **Step 2: Update `docs/components/policy-server.md`'s restore-policy prose**

In the `"restore"` policy paragraph (currently at lines 86-88), change:

```
uses) names the source `bwfs` to restore from, and `rules` (required, at least one entry —
`{host, path, include}`, mirroring the web restore cart's own rule shape; an empty/omitted `host`
means the rule applies across every source host) says what to restore. It has no `object_filters`,
```

to:

```
uses) names the source `bwfs` to restore from, and `rules` (required, at least one entry —
`{host, path, include, dest_path}`, mirroring the web restore cart's own rule shape; an
empty/omitted `host` means the rule applies across every source host; `dest_path`, if set and
different from `path`, is the path to restore to instead — empty or equal to `path` means no
rename, and it's a validation error to set it on a rule with `include: false`) says what to
restore. It has no `object_filters`,
```

Add a link after the existing "See" sentence at the end of that paragraph (currently pointing only to the 2026-08-10 design):

```
See
[Design: Restore Policy Verification Execution](../superpowers/specs/2026-08-10-restore-policy-verification-design.md)
and
[Design: Restore Destination Rename](../superpowers/specs/2026-08-13-restore-destination-rename-design.md).
```

- [ ] **Step 3: Update `docs/components/web.md`'s `/restore` bullet**

In the `/restore` bullet (currently starting at line 50: `` - `/restore` — lists everything currently staged in the restore cart (folder selections as `path/*`, file selections as `path (host)`), each with a Remove button that unstages it...``), change the opening clause describing the review list's rendering from:

```
- `/restore` — lists everything currently staged in the restore cart (folder selections as
  `path/*`, file selections as `path (host)`), each with a Remove button that unstages it (toggles
  the same rule back off, via `restoreCart.removeEntry`). Picking a destination host (from the
```

to:

```
- `/restore` — a table, one row per cart selection, listing storage host, source host, source
  path (folder selections shown as `path/*`), a destination path, and size (file rows only --
  storage host, source host, and size are `—` on a folder row, since a folder selection can span
  many of each). The destination path defaults to the source path; clicking it swaps in a text
  input (`restoreCart.setDestPath`) to rename that selection's restore target, whether a file or a
  folder -- purely client-side data at this point, sent as `dest_path` on the submitted rule only
  when it differs from the source path (see
  [Design: Restore Destination Rename](../superpowers/specs/2026-08-13-restore-destination-rename-design.md));
  nothing yet reads it back out (no restore executor exists). Each row also has a Remove button
  that unstages it (toggles the same rule back off, via `restoreCart.removeEntry`). Picking a
  destination host (from the
```

(Leave the remainder of that bullet, describing the submission grouping mechanism, unchanged — it is unaffected by this feature.)

- [ ] **Step 4: Update `docs/components/api-server.md`'s restore body-shape note**

Change (currently line 68):

```
`POST /restore` (fields: `name`/`client_filters`/`storage_policy_id`/`rules`), and no update path at
```

to:

```
`POST /restore` (fields: `name`/`client_filters`/`storage_policy_id`/`rules`, each rule optionally
carrying `dest_path` to rename that selection's restore target), and no update path at
```

- [ ] **Step 5: Add the `CHANGELOG.md` entry**

Add at the top of `CHANGELOG.md`, above the existing `## 2026-08-10 — restore policy verification` entry:

```markdown
## 2026-08-13 — restore destination rename

The restore review screen (`/restore`) is now a table showing each selection's storage host,
source host, source path, destination path, and size, instead of a plain list. The destination
path defaults to the source path and is click-to-edit, letting an operator restore a file or
folder to a different path than it came from (e.g. to avoid clobbering a file that still exists).
This threads a new optional `dest_path` field through `RestoreRule` end to end -- proto,
`policy-server`'s schema and validation (a rule may only set `dest_path` when `include` is true),
and `api-server`'s REST DTO -- stored on the `"restore"` policy alongside the existing
`host`/`path`/`include` fields. Nothing consumes it yet: no restore executor exists (`rwfs restore`
remains unbuilt, per the 2026-08-10 restore-verification entry above), so this only makes the data
available for one to read in the future.

```

- [ ] **Step 6: Commit**

```bash
git add docs/protocols/policy-server.md docs/components/policy-server.md docs/components/web.md docs/components/api-server.md CHANGELOG.md
git commit -m "docs: document the restore destination rename feature"
```

---

## Final Verification

- [ ] Run the full Go test suite: `cd /home/alex/miniprotector/src && go test ./...` — expect PASS.
- [ ] Run `go vet`: `cd /home/alex/miniprotector/src && go vet ./...` — expect no findings.
- [ ] Run the full web test suite: `cd /home/alex/miniprotector/web && npx vitest run` — expect PASS.
- [ ] Manually confirm no other file references the old `ruleDTO`/`RestoreRule` three-field shape in a way that would now be stale (`grep -rn "Host, Path, Include" src/ web/` — should only match places already updated by Tasks 2-3, e.g. no leftover struct literals missing `DestPath`/`dest_path`).
