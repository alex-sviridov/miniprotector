# Restore Verify/Execute Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the restore UI's single "Submit restore" action into separate **Verify** and
**Restore** actions with an "overwrite existing files" option, propagated through `api-server`,
without touching `policy-server` or `rwfs`.

**Architecture:** `POST /api/v1/restore` gains a `mode: "verify" | "restore"` field (default
`"verify"`) and an `overwrite: bool` field. `mode: "verify"` forwards to `policy-server` exactly as
today. `mode: "restore"` is validated and rejected with `501` before any `policy-server` call is
made — no execution path exists yet. The web store and view are updated to send these fields and
render two distinct buttons plus a checkbox.

**Tech Stack:** Go (`net/http`, no new deps) for `api-server`; Vue 3 + Pinia + Vitest for `web`.

## Global Constraints

- No `policy-server` or `rwfs` changes — `mode: "restore"` must never reach `policy-server`. (spec
  Non-Goals)
- No new job `kind` — `validJobKinds`/`kindFromJobID` in `src/cmd/api-server/jobs.go` stay
  untouched. (spec Non-Goals)
- No proto changes. (spec Non-Goals)
- `mode` omitted on the wire must behave exactly as `mode: "verify"` (back-compat with existing
  callers/e2e tests). (spec Goals)
- `overwrite` checkbox defaults to unchecked (`false`). (spec Architecture §1)
- Go tests: run via `cd src && go test ./cmd/api-server/... -run <TestName> -v`.
- Web tests: run via `cd web && npx vitest run <path/to/spec.js>`.

---

### Task 1: `api-server` — `mode`/`overwrite` fields on `POST /api/v1/restore`

**Files:**
- Modify: `src/cmd/api-server/policies.go:316-361` (`restorePolicyInput`, `handleCreateRestore`)
- Test: `src/cmd/api-server/policies_test.go` (add near existing `TestHandleCreateRestore_*` tests,
  after line 819)

**Interfaces:**
- Consumes: existing `restorePolicyInput`, `decodeRestorePolicyInput`, `s.policy.CreatePolicy`,
  `writeJSONError`, `writeGRPCError`, `writeJSON`, `toPolicyDTO` — all unchanged signatures, all
  already in `policies.go`/`errors.go`.
- Produces: `POST /api/v1/restore` request body accepts `"mode"` (string, `"verify"` or
  `"restore"`, defaults to `"verify"` when omitted/empty) and `"overwrite"` (bool). `mode: "restore"`
  responds `501` with JSON body `{"error": "restore execution is not yet implemented; only
  verification (mode=verify) is currently supported"}` and never calls `s.policy.CreatePolicy`. An
  unrecognized `mode` value responds `400` with `{"error": "mode must be 'verify' or 'restore'"}`.
  Later tasks (2, 3) call this endpoint with these two fields.

- [ ] **Step 1: Write the failing Go tests**

Add to `src/cmd/api-server/policies_test.go`:

```go
func TestHandleCreateRestore_ExplicitVerifyModeForwardsToBackend(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "r1", Name: "web01-emergency", Type: "restore",
		StoragePolicyId: "sp-1",
		Rules:           []*pb.RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web01-emergency",
		"storage_policy_id": "sp-1",
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}],
		"mode": "verify",
		"overwrite": false
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
}

func TestHandleCreateRestore_RestoreModeReturns501AndSkipsBackend(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "r1"}}
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

	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Nil(t, fake.lastCreateReq, "backend must not be called for mode=restore")

	var respBody map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, "restore execution is not yet implemented; only verification (mode=verify) is currently supported", respBody["error"])
}

func TestHandleCreateRestore_InvalidModeReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web01-emergency",
		"storage_policy_id": "sp-1",
		"rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}],
		"mode": "bogus"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastCreateReq)
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleCreateRestore -v`
Expected: `TestHandleCreateRestore_ExplicitVerifyModeForwardsToBackend` passes already (no `mode`
handling needed yet since it's forwarded regardless), but
`TestHandleCreateRestore_RestoreModeReturns501AndSkipsBackend` and
`TestHandleCreateRestore_InvalidModeReturns400` FAIL — today `handleCreateRestore` always calls
`CreatePolicy` and returns `201`/whatever the fake returns, never `501`/`400` for these bodies.

- [ ] **Step 3: Implement `mode`/`overwrite` handling**

In `src/cmd/api-server/policies.go`, replace the `restorePolicyInput` struct (lines 316-322):

```go
type restorePolicyInput struct {
	Name            string           `json:"name"`
	ClientFilters   clientFiltersDTO `json:"client_filters"`
	StoragePolicyID string           `json:"storage_policy_id"`
	Rules           []ruleDTO        `json:"rules"`
	DisabledAt      int64            `json:"disabled_at,omitempty"`
	Mode            string           `json:"mode"`
	Overwrite       bool             `json:"overwrite"`
}
```

Replace `handleCreateRestore` (lines 332-361) with:

```go
// handleCreateRestore is the sole creation path for "restore"-typed
// policies: POST /api/v1/restore, not POST/PUT /api/v1/restore-policies --
// a restore policy is launched, not managed as a long-lived resource, and
// is never updatable (PUT /api/v1/policies/{id} against one is rejected by
// policy-server itself, see write.go's buildPolicyForUpdate).
//
// mode distinguishes verification (agent runs rwfs verify against the
// resolved rules, no files written) from actual restore (not yet
// implemented anywhere below this layer -- see
// docs/superpowers/specs/2026-08-14-restore-verify-execute-split-design.md).
// mode="restore" is rejected here, before policy-server is ever called, so
// this layer's contract is ready ahead of that future work.
func (s *server) handleCreateRestore(w http.ResponseWriter, r *http.Request) {
	in, err := decodeRestorePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	mode := in.Mode
	if mode == "" {
		mode = "verify"
	}
	if mode != "verify" && mode != "restore" {
		writeJSONError(w, http.StatusBadRequest, "mode must be 'verify' or 'restore'")
		return
	}
	if mode == "restore" {
		writeJSONError(w, http.StatusNotImplemented, "restore execution is not yet implemented; only verification (mode=verify) is currently supported")
		return
	}

	rules := make([]*pb.RestoreRule, len(in.Rules))
	for i, ru := range in.Rules {
		rules[i] = &pb.RestoreRule{Host: ru.Host, Path: ru.Path, Include: ru.Include, DestPath: ru.DestPath}
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

Run: `cd src && go test ./cmd/api-server/... -run TestHandleCreateRestore -v`
Expected: PASS, all `TestHandleCreateRestore_*` tests including the three pre-existing ones.

- [ ] **Step 5: Run the full api-server package test suite**

Run: `cd src && go test ./cmd/api-server/...`
Expected: PASS (confirms nothing else in the package broke).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): add mode/overwrite fields to POST /api/v1/restore

mode=restore is validated and rejected with 501 before policy-server is
ever called -- no execution path exists yet. mode=verify (the default,
for back-compat) is unchanged."
```

---

### Task 2: `web/src/stores/restoreSubmission.js` — thread `mode`/`overwrite` through

**Files:**
- Modify: `web/src/stores/restoreSubmission.js:107-170` (`submit` action)
- Test: `web/src/stores/restoreSubmission.spec.js`

**Interfaces:**
- Consumes: Task 1's `POST /api/v1/restore` contract (`mode`, `overwrite` fields; `501` response on
  `mode: "restore"`). Existing `buildRulesByStore`, `toWireRule`, `distinctPositiveEntries`,
  `storagePolicyIdForHost` — all unchanged.
- Produces: `useRestoreSubmissionStore().submit(destinationHost, { mode, overwrite })` — the second
  parameter is required (no default), an object with `mode` (`"verify"` or `"restore"`) and
  `overwrite` (bool). Every per-store `POST /restore` body gains `mode` and `overwrite` keys after
  `rules`. Task 3's view calls this with `{ mode: 'verify', overwrite: ... }` and
  `{ mode: 'restore', overwrite: ... }`.

- [ ] **Step 1: Update existing tests to pass the new required second argument**

In `web/src/stores/restoreSubmission.spec.js`, every call of the form `submission.submit('web01')`
becomes `submission.submit('web01', { mode: 'verify', overwrite: false })`. This is every occurrence
in the file (12 call sites, including inside `expect(...)` and `const pending = ...`) — replace the
literal substring `submit('web01')` with `submit('web01', { mode: 'verify', overwrite: false })`
everywhere it appears in this file.

Then update the two tests that assert the exact request body to include `mode`/`overwrite`. First,
`'sends the full, unsplit rule list to the one store a folder rule touches'` — change its
`expect(apiFetch).toHaveBeenCalledWith(...)` block to:

```js
    expect(apiFetch).toHaveBeenCalledWith('/restore', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'restore-2026-08-10T00:00:00.000Z-store-a',
        client_filters: { hostnames: ['web01'], labels: {} },
        storage_policy_id: 's1',
        rules: [{ host: null, path: '/var/lib/dbdata', include: true }],
        mode: 'verify',
        overwrite: false,
      }),
    })
```

Second, `'never sends storeHost or size on the wire'` — no change needed (it only asserts
`body.rules[0]` shape, not the whole body).

- [ ] **Step 2: Add a new failing test for mode/overwrite propagation and the 501 case**

Add to `web/src/stores/restoreSubmission.spec.js`, after the `'includes dest_path...'` test:

```js
  it('sends mode and overwrite through on every per-store /restore call', async () => {
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
    await submission.submit('web01', { mode: 'restore', overwrite: true })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.mode).toBe('restore')
    expect(body.overwrite).toBe(true)
  })

  it('reports a per-store error when the backend rejects mode=restore as not implemented', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 2, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.reject(
          new Error('restore execution is not yet implemented; only verification (mode=verify) is currently supported')
        )
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'restore', overwrite: false })

    expect(submission.results).toEqual([
      {
        storeHost: 'store-a',
        status: 'error',
        message: 'restore execution is not yet implemented; only verification (mode=verify) is currently supported',
      },
    ])
  })
```

- [ ] **Step 3: Run the tests to verify the new/updated ones fail**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: FAIL — `submit` doesn't accept a second argument yet, so `mode`/`overwrite` are `undefined`
in every request body; the body-equality assertions and the two new tests fail.

- [ ] **Step 4: Implement `mode`/`overwrite` threading**

In `web/src/stores/restoreSubmission.js`, change the `submit` action signature and the
`restorePolicies.create` call (inside the `actions` block, replacing the current `submit(destinationHost)`):

```js
    async submit(destinationHost, { mode, overwrite }) {
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

        const rulesByStore = await buildRulesByStore(positiveEntries, cart.rules)

        await storagePolicies.fetchAll()
        if (storagePolicies.error) {
          this.error = `Could not look up storage policies: ${storagePolicies.error}`
          return
        }

        const results = []
        for (const [storeHost, rules] of rulesByStore) {
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
              rules: rules.map(toWireRule),
              mode,
              overwrite,
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
```

(Only the `restorePolicies.create` call body and the function signature change; the rest of the
action is unchanged from today.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: PASS, all tests in the file.

- [ ] **Step 6: Commit**

```bash
git add web/src/stores/restoreSubmission.js web/src/stores/restoreSubmission.spec.js
git commit -m "feat(web): thread mode/overwrite through restore submission

Every per-store POST /restore call now carries mode and overwrite,
matching api-server's new contract. mode=restore results in a per-store
error result today (the backend returns 501), same rendering path as
any other per-store failure."
```

---

### Task 3: `web/src/views/RestoreView.vue` — Verify/Restore buttons + overwrite checkbox

**Files:**
- Modify: `web/src/views/RestoreView.vue`
- Test: `web/src/views/RestoreView.spec.js`
- Modify: `web/e2e/restore-verify.spec.js:71,74` (existing Playwright e2e test — references the
  removed `submit-restore` test id and the old "Created" success copy; must be updated in the same
  task or it silently breaks, since it isn't run by `npm test`)

**Interfaces:**
- Consumes: Task 2's `useRestoreSubmissionStore().submit(destinationHost, { mode, overwrite })`.
- Produces: two buttons, `data-test="verify-button"` and `data-test="restore-button"`, and a
  checkbox `data-test="overwrite-checkbox"` (unchecked by default). No other view consumes these.

- [ ] **Step 1: Update `RestoreView.spec.js` for the new buttons and checkbox**

Replace the `'disables submit until the cart has a selection and a destination is chosen'` test with:

```js
  it('disables verify and restore until the cart has a selection and a destination is chosen', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }],
    })
    expect(wrapper.find('[data-test="verify-button"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="restore-button"]').attributes('disabled')).toBeDefined()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')

    expect(wrapper.find('[data-test="verify-button"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="restore-button"]').attributes('disabled')).toBeUndefined()
  })
```

Replace the `'clicking submit calls restoreSubmission.submit with the chosen destination'` test with
three tests:

```js
  it('clicking Verify calls restoreSubmission.submit with mode verify', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }],
    })
    const submission = useRestoreSubmissionStore()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')
    await wrapper.find('[data-test="verify-button"]').trigger('click')

    expect(submission.submit).toHaveBeenCalledWith('web01', { mode: 'verify', overwrite: false })
  })

  it('clicking Restore calls restoreSubmission.submit with mode restore and the checked overwrite flag', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }],
    })
    const submission = useRestoreSubmissionStore()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')
    await wrapper.find('[data-test="overwrite-checkbox"]').setValue(true)
    await wrapper.find('[data-test="restore-button"]').trigger('click')

    expect(submission.submit).toHaveBeenCalledWith('web01', { mode: 'restore', overwrite: true })
  })

  it('the overwrite checkbox defaults to unchecked', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
    })
    expect(wrapper.find('[data-test="overwrite-checkbox"]').element.checked).toBe(false)
  })
```

- [ ] **Step 2: Run the tests to verify the new/updated ones fail**

Run: `cd web && npx vitest run src/views/RestoreView.spec.js`
Expected: FAIL — `[data-test="verify-button"]`/`[data-test="restore-button"]`/
`[data-test="overwrite-checkbox"]` don't exist yet (the view still renders a single
`data-test="submit-restore"` button).

- [ ] **Step 3: Implement the two buttons and checkbox**

In `web/src/views/RestoreView.vue`, add an `overwrite` ref next to `destinationHost` (in the
`<script setup>` block):

```js
const destinationHost = ref('')
const overwrite = ref(false)
```

Replace the `submit` function with:

```js
function verify() {
  submission.submit(destinationHost.value, { mode: 'verify', overwrite: overwrite.value })
}

function restore() {
  submission.submit(destinationHost.value, { mode: 'restore', overwrite: overwrite.value })
}
```

In the template, replace:

```html
      <BaseButton data-test="submit-restore" variant="primary" :disabled="!canSubmit" @click="submit">
        Submit restore
      </BaseButton>
```

with:

```html
      <label class="flex items-center gap-2">
        <input type="checkbox" data-test="overwrite-checkbox" v-model="overwrite" />
        Overwrite existing files
      </label>
      <BaseButton data-test="verify-button" variant="secondary" :disabled="!canSubmit" @click="verify">
        Verify
      </BaseButton>
      <BaseButton data-test="restore-button" variant="primary" :disabled="!canSubmit" @click="restore">
        Restore
      </BaseButton>
```

Update the results list's success copy from "Created" to "Started verification policy", so it reads
correctly now that Restore is a visibly distinct action (today, only a verify submission can ever
succeed — see Task 1's `501` for `mode: "restore"`):

```html
        <span v-if="result.status === 'success'">Started verification policy {{ result.policy.name }} from {{ result.storeHost }}</span>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/views/RestoreView.spec.js`
Expected: PASS, all tests in the file.

- [ ] **Step 5: Run the full web unit test suite**

Run: `cd web && npm test`
Expected: PASS (confirms no other vitest spec references the removed `submit-restore` test id or
the old success copy).

- [ ] **Step 6: Fix the Playwright e2e spec that used the removed button/copy**

`web/e2e/restore-verify.spec.js` isn't run by `npm test` (it's a Playwright test that needs the
docker-compose demo environment — see the file's own comments), so it won't fail loudly here, but it
references both things this task just changed. In `web/e2e/restore-verify.spec.js`, change line 71:

```js
    await page.getByTestId('submit-restore').click()
```

to:

```js
    await page.getByTestId('verify-button').click()
```

and change line 74:

```js
    const policyName = /Created (\S+) from/.exec(resultText)[1]
```

to:

```js
    const policyName = /Started verification policy (\S+) from/.exec(resultText)[1]
```

If the docker-compose demo environment is available, run `npm run test:e2e -- restore-verify` from
`web/` to confirm; otherwise leave this for the standard e2e run this repo already does before
merge.

- [ ] **Step 7: Commit**

```bash
git add web/src/views/RestoreView.vue web/src/views/RestoreView.spec.js web/e2e/restore-verify.spec.js
git commit -m "feat(web): split restore submit into separate Verify/Restore buttons

Adds an overwrite-existing-files checkbox alongside the two buttons.
Restore submits mode=restore, which api-server currently rejects with
501 -- rendered through the existing per-store error path. Updates the
restore-verify e2e spec for the renamed button and success copy."
```

---

### Task 4: Documentation and changelog

**Files:**
- Modify: `docs/api/rest-v1.md:377-405` (`POST /api/v1/restore` section)
- Modify: `docs/components/api-server.md:67-78` (`POST /restore` description)
- Modify: `CHANGELOG.md` (new entry at top, after line 3)

**Interfaces:**
- Consumes: Task 1's final `mode`/`overwrite` contract and `501` response.
- Produces: nothing consumed by other tasks — this is the terminal documentation task.

- [ ] **Step 1: Update `docs/api/rest-v1.md`**

In the `## POST /api/v1/restore` section (currently lines 377-405), update the body example and add
`mode`/`overwrite` documentation. Replace the section from `## POST /api/v1/restore` through the
`disabled_at` paragraph (lines 377-402) with:

```markdown
## `POST /api/v1/restore`

Creates a new `"restore"`-typed policy -- the only way to create one; there is no
`POST /api/v1/restore-policies` and no update endpoint. Body:

```json
{
  "name": "web01-emergency",
  "client_filters": {"hostnames": ["web-01"], "labels": {}},
  "storage_policy_id": "<id of an existing \"storage\" policy>",
  "rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}],
  "mode": "verify",
  "overwrite": false
}
```

`storage_policy_id` must reference an existing `"storage"`-typed policy -- its dial address is
resolved live from that policy's check-ins, exactly like a `"backup"` policy's `destinations`.
`rules` must contain at least one entry; an entry with `"host": null` (or omitted) is
host-agnostic, applying across every source host under `path`. `client_filters` targets the node
that will execute the restore, the same mechanism every other policy type uses.

`mode` is `"verify"` or `"restore"`, defaulting to `"verify"` when omitted (so existing callers that
never set it are unaffected). `mode: "verify"` creates the policy exactly as before: `agent` picks
it up and runs `rwfs verify` against it, writing nothing. `mode: "restore"` is validated but
currently always rejected with `501` (`{"error": "restore execution is not yet implemented; only
verification (mode=verify) is currently supported"}`) -- `policy-server` is never called in this
case, since no execution path exists yet. `overwrite` (bool, default `false`) is accepted alongside
`mode: "restore"` but has no effect yet, for the same reason. `201` with the created policy on
success (`mode: "verify"` only).

`400` if `name` is empty, `storage_policy_id` doesn't reference an existing `"storage"` policy,
`rules` is empty or contains an entry with an empty `path`, or `mode` is neither `"verify"` nor
`"restore"`.

An optional integer `disabled_at` (Unix seconds) may also be included; once that time passes,
`GetPolicies` stops serving the policy. Omit it (or send `0`) for a policy that's never disabled.
```

Leave the remaining two paragraphs (restore policies never updatable; `GET`/`DELETE` behavior,
currently lines 404-405) unchanged.

- [ ] **Step 2: Update `docs/components/api-server.md`**

In the `POST /restore-policies` doesn't exist... paragraph (currently lines 67-78), update the field
list sentence. Change:

```markdown
`POST /restore-policies` doesn't exist -- restore policies have exactly one creation path,
`POST /restore` (fields: `name`/`client_filters`/`storage_policy_id`/`rules`, each rule optionally
carrying `dest_path` to rename that selection's restore target), and no update path at
```

to:

```markdown
`POST /restore-policies` doesn't exist -- restore policies have exactly one creation path,
`POST /restore` (fields: `name`/`client_filters`/`storage_policy_id`/`rules`/`mode`/`overwrite`,
each rule optionally carrying `dest_path` to rename that selection's restore target), and no update
path at
```

And append a sentence to the end of that paragraph (after the two "See [Design: ...]" links,
currently ending line 78), documenting the new fields:

```markdown
`mode` (`"verify"`, the default, or `"restore"`) and `overwrite` (bool) prepare the contract for a
real restore action: `mode: "restore"` is validated and rejected with `501` today, since no
execution path exists below `api-server` yet -- see [Design: Restore Verify/Execute
Split](../superpowers/specs/2026-08-14-restore-verify-execute-split-design.md).
```

- [ ] **Step 3: Add a CHANGELOG entry**

In `CHANGELOG.md`, insert a new entry immediately after line 3 (the `All notable changes...` line),
before the existing `## 2026-08-13 — restore verification gains e2e coverage` entry:

```markdown
## 2026-08-14 — restore UI gains separate Verify/Restore actions

The restore view's single "Submit restore" button is now two buttons, Verify and Restore, plus an
"Overwrite existing files" checkbox. `POST /api/v1/restore` gained `mode` (`verify`/`restore`,
default `verify`) and `overwrite` fields to carry this through. Verify behaves exactly as the old
single button did. Restore is deliberately not wired to anything yet -- `api-server` validates and
rejects it with `501` before ever calling `policy-server`, since real restore execution
(`rwfs restore`) still doesn't exist. This is UI/API groundwork only, ahead of a later change to
`policy-server`/`rwfs` that will make Restore actually work.

```

- [ ] **Step 4: Verify the doc edits render sensibly**

Run: `git diff docs/api/rest-v1.md docs/components/api-server.md CHANGELOG.md`
Expected: a clean, readable diff — no broken markdown (matching code fences, no stray blank lines
inside the CHANGELOG entry).

- [ ] **Step 5: Commit**

```bash
git add docs/api/rest-v1.md docs/components/api-server.md CHANGELOG.md
git commit -m "docs: document mode/overwrite on POST /api/v1/restore

Per .claude/CLAUDE.md's feature-change and changelog rules."
```
