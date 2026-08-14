# Restore: Split Verify/Restore Actions — Design

> **Builds on:** `docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md`, which
> wired `agent`/`rwfs verify` up to a `"restore"`-typed policy but explicitly scoped out actual
> restore (`rwfs restore` doesn't exist; see that design's Non-Goals). This design prepares the UI
> and `api-server` contract for a real, distinct restore action, ahead of the `policy-server`/`rwfs`
> work that will actually execute it. **This round touches only `web` and `api-server`** —
> `policy-server` and `rwfs` are unchanged, and no request for `mode: "restore"` ever reaches
> `policy-server`.

## Problem

Today, `RestoreView.vue` has a single "Submit restore" button that always creates a `"restore"`-typed
policy — which `agent` only ever verifies (`rwfs verify`), never writes files. There is no way for a
user to express "I actually want files restored" versus "I want to check they're intact," and no way
to say whether an eventual real restore should overwrite files already present at the destination.

We want the UI and API surface for these two distinct actions to exist now, so that when
`policy-server`/`rwfs` gain real restore execution in a later round, only their side needs to change.

## Goals

- Two distinct, separately-clickable actions in the restore UI: **Verify** and **Restore**.
- A **"Overwrite existing files"** checkbox, carried through the API as `overwrite`.
- `POST /api/v1/restore` gains a `mode: "verify" | "restore"` field (default `"verify"`, preserving
  today's behavior for any caller that omits it — including existing e2e tests).
- `mode: "verify"` behaves exactly as today's single button did: forwards to `policy-server`
  unchanged, creates a `"restore"`-typed policy, `agent` verifies it.
- `mode: "restore"` is accepted and validated by `api-server`, but since no execution path exists
  yet, it returns `501 Not Implemented` with a clear message — **without calling `policy-server`**.
- The per-store submission fan-out in `restoreSubmission.js` (facet lookup → group rules by store →
  one `POST /api/v1/restore` per distinct storage policy) is reused unchanged for both modes, since
  that is the same shape real restore will need (one policy per store).

## Non-Goals (this round)

- **No real restore execution.** `rwfs restore` remains unbuilt; `policy-server` is untouched.
- **No proto changes.** `mode: "restore"` never reaches `policy-server`, so no new `Policy`/
  `CreatePolicyRequest` field is needed yet.
- **No new job `kind`.** `validJobKinds`/`kindFromJobID` in `api-server/jobs.go` are unchanged — no
  job is ever created for a rejected `mode: "restore"` request, so `kind=restore` continues to mean
  exactly what it means today (a verify run).
- **No change to how stores/rules are resolved or grouped** — `buildRulesByStore`,
  `storesTouchedByEntry`, `toWireRule` are unchanged.

## Architecture

### 1. `web/src/views/RestoreView.vue`

- Replace the single "Submit restore" `BaseButton` with two: **"Verify"** and **"Restore"**, both
  gated by the existing `canSubmit` computed (cart non-empty, destination host selected, not
  currently submitting).
- Add a checkbox, `data-test="overwrite-checkbox"`, labeled "Overwrite existing files", bound to a
  new local `ref(false)` (default unchecked — the safe default). Always visible and always included
  in the submission payload; it's simplest to send it on every submit rather than conditionally
  show/hide it, and it's inert for `mode: "verify"`.
- Two handlers, `verify()` and `restore()`, calling `submission.submit(destinationHost.value, {
  mode: 'verify', overwrite: overwrite.value })` and `{ mode: 'restore', overwrite: overwrite.value
  }` respectively.
- Results list rendering (`submission.results`) is unchanged structurally — still renders
  `status === 'success'` vs the else branch — but the success copy changes from "Created
  `{policy.name}` from `{storeHost}`" to "Started verification policy `{policy.name}` from
  `{storeHost}`", disambiguating it now that "Restore" is a separate, distinct action. A rejected
  `mode: "restore"` renders through the existing error branch: `"{storeHost}: {message}"`, where
  `message` is the `501` response body's error text.

### 2. `web/src/stores/restoreSubmission.js`

- `submit(destinationHost, { mode, overwrite })` replaces `submit(destinationHost)`.
- The only change inside the per-store loop: the `restorePolicies.create(...)` call body gains
  `mode` and `overwrite` alongside the existing `name`/`client_filters`/`storage_policy_id`/`rules`.
- For `mode: 'restore'`, every per-store call today resolves to a thrown error (api-server's `501`),
  which the existing `try { ... } catch (err) { results.push({ storeHost, status: 'error', message:
  err.message }) }` already handles without modification — `apiFetch` (`web/src/api/client.js`)
  already surfaces a non-2xx JSON `{error: ...}` body as `err.message`, so no client change is
  needed there.

### 3. `src/cmd/api-server/policies.go`

- `restorePolicyInput` gains two fields:
  ```go
  Mode      string `json:"mode"`
  Overwrite bool   `json:"overwrite"`
  ```
- `handleCreateRestore`:
  - Default `mode` to `"verify"` when empty (preserves today's behavior for any existing caller,
    including e2e tests, that doesn't send it).
  - Reject any value other than `"verify"`/`"restore"` with `400`.
  - `mode == "restore"`: return `501` via a new small helper (or `writeJSONError(w,
    http.StatusNotImplemented, "restore execution is not yet implemented; only verification
    (mode=verify) is currently supported")`) **before** constructing or sending any
    `CreatePolicyRequest` — `policy-server` is never called.
  - `mode == "verify"`: unchanged existing path (builds and sends `CreatePolicyRequest` exactly as
    today; `overwrite` is accepted but not forwarded anywhere, since it has no meaning yet).

## Data Flow

```
web: user picks files, checks/unchecks "Overwrite existing files", clicks Verify or Restore
  -> submission.submit(destinationHost, { mode, overwrite })
  -> (unchanged) per-entry store-facet lookup, rules grouped by store
  -> per distinct storage_policy_id: POST /api/v1/restore { ..., mode, overwrite }
       mode=verify   -> api-server forwards to policy-server (unchanged) -> 201, policy created
       mode=restore  -> api-server validates, returns 501 immediately, policy-server never called
  -> results rendered per store (success/error), same shape for both modes
```

## Error Handling

- `mode` missing → treated as `"verify"` (back-compat).
- `mode` present but not `"verify"`/`"restore"` → `400`, `"mode must be 'verify' or 'restore'"`.
- `mode: "restore"` → `501`, friendly message, no backend call, no job/policy created.
- All existing error paths for `mode: "verify"` (invalid JSON, policy-server failure, dangling
  `storage_policy_id`, etc.) are unchanged.

## Testing

- `web`: `RestoreView.spec.js` — both buttons render and are independently clickable; checkbox state
  flows into the call to `submission.submit`; results render the updated "Started verification…"
  copy on success.
- `web`: `restoreSubmission.spec.js` — `mode`/`overwrite` present in each per-store POST body for
  both `verify()`/`restore()` calls; a `501` response from a `mode: "restore"` call becomes an
  error-shaped entry in `results`.
- `api-server`: `policies_test.go` — `mode` omitted and `mode: "verify"` both forward to the policy
  client mock unchanged; `mode: "restore"` returns `501` and asserts the policy client mock was
  **not** called; `mode: "bogus"` returns `400`.
- e2e: existing `restore-verify.spec.js`/`restore-cart.spec.js` continue to pass unmodified (still
  omit `mode`, defaulting to verify) or are updated to click the renamed "Verify" button explicitly;
  optionally add one small case clicking "Restore" and asserting the friendly not-yet-implemented
  message renders in the results list.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule:

- **`docs/api/rest-v1.md`** — `POST /api/v1/restore` section (~line 377): document `mode` (default
  `verify`, values `verify`/`restore`) and `overwrite` fields, and the `501` response for
  `mode: "restore"`.
- **`docs/components/api-server.md`** — `POST /restore` description (~line 67): note the new fields
  and that `mode: "restore"` is accepted but not yet executable.
- **`README.md`** — no change expected (quick-start examples and component list are unaffected); confirm
  during implementation.
- **`docs/ARCHITECTURE.md`** — no change: system topology/data flow are unaffected, since
  `mode: "restore"` never leaves `api-server`.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

No `docs/protocols/` changes — no `.proto` file is touched this round.
