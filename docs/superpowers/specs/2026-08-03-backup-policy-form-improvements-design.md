# Design: backup policy form improvements

**Date:** 2026-08-03
**Status:** Approved for planning

## Problem

`BackupPolicyFormModal.vue` (`web/src/components/backup_policies/`) is the shared create/edit form
for backup policies, with independent "Save" and "Run now" actions. Three things about it are worth
fixing now:

1. Every label/input pair in this form (and in `StorageEditModal.vue`, the storage-policy form) is
   hand-rolled Tailwind markup repeated field by field, with no shared component underneath.
2. The form's "Destination" field is a free-text `host:port` input an operator has to type by hand,
   even though `storagePolicies.js` already has a Pinia store holding every storage policy the
   operator has configured — each one already *is* a concrete `host:port` target.
3. Beyond HTML5 `required` on the name field, the form has no guardrails: a whitespace-only name
   passes, and a malformed RPO string is accepted silently.

This branch (`backup-policy-form-improvements`, forked from `backup-policy-storage-link`) lands
after that branch's backend work: `policy-server`/`api-server` now expose a `storage_policy_id`
field on backup policies — a real reference to a storage policy — with `destination` server-derived
from it. That backend change removes what would otherwise be the hard part of item 2 here (guessing
which storage policy a raw `destination` string came from); this design is now mostly a frontend
component-and-UX exercise.

## Approach

### A. Reusable form primitives (`web/src/components/ui/`)

Three new thin wrapper components, replacing the repeated `<label class="block font-medium
mb-1">...</label>` / `<input class="w-full border rounded px-2 py-1">` pattern in both
`BackupPolicyFormModal.vue` and `StorageEditModal.vue`:

- **`BaseField.vue`** — a label (with an optional required-asterisk) wrapping a `<slot>` for the
  control.
- **`BaseInput.vue`** — wraps `<input>`. Uses Vue 3.5's `defineModel()` for `v-model` and
  `defineOptions({ inheritAttrs: false })` + `v-bind="$attrs"` so `type`, `placeholder`, `required`,
  `pattern`, `name`, `data-test`, etc. pass through untouched, with the shared input styling baked
  in once.
- **`BaseSelect.vue`** — same shape as `BaseInput` but for `<select>`; callers still write native
  `<option>` children in the default slot (no invented options-array abstraction — it stays a thin
  wrapper, not a new dropdown framework).

`BaseButton.vue` and `RepeatableFieldList.vue` are already reusable and untouched. `StorageEditModal.vue`
is refactored to use `BaseField`/`BaseInput`/`BaseSelect` in place of its own hand-rolled markup;
its existing validation and behavior (name/hostname/port checks, `errors.message`) are unchanged.

### B. Storage-driven destination

`BackupPolicyFormModal.vue` gets `const storagePolicies = useStoragePoliciesStore()`.

- `onMounted`: if `storagePolicies.list.length === 0`, call `fetchAll()`. A "Reload" `BaseButton`
  next to the destination field always calls `fetchAll()` directly, disabled while
  `storagePolicies.loading`.
- The destination field becomes a required `BaseSelect` bound via `v-model="form.storage_policy_id"`
  (native `required` covers the "must pick one" guardrail). Options are every entry in
  `storagePolicies.list`, labeled `"{name} ({hostname}:{port})"` — or `"{name} (incomplete)"` when
  the storage policy is missing its hostname (`client_filters.hostnames` empty) and/or its port
  (falsy `port`). Incomplete storage policies are shown, not filtered out, so an operator can see
  and fix one rather than have it silently vanish from the list.
- `toFormShape(policy)` sets `storage_policy_id: policy?.storage_policy_id || ''` directly — no
  matching heuristic. The backend already knows which storage policy a backup policy references;
  the form just displays it.
- `buildPayload()` sends `storage_policy_id: form.storage_policy_id`. `destination` is never part of
  the payload — it isn't writable on the API (`policyInput` has no `Destination` field as of the
  backend branch this forks from).
- The policy detail view (`BackupPolicyView.vue`) still shows the resolved `destination` string
  (read-only, from `policy.destination`) in its detail list — that's unaffected by this form change.

### C. Guardrails

- **Name:** keep native `required`, and add the existing-pattern JS check
  `if (!form.name.trim())` (mirrors `StorageEditModal.vue` today) so a whitespace-only name is
  rejected — HTML5 `required` alone doesn't trim.
- **Destination:** native `required` on the new select (see B) — nothing else needed, since an
  empty selection is the only invalid state.
- **RPO:** stays optional, but if filled in must match a `time.ParseDuration`-shaped pattern via the
  input's `pattern` attribute (e.g. `24h`, `30m`, `1h30m`), enforced through the existing
  `formEl.reportValidity()` call already used in `submit()`/`runNow()` — no new JS branch needed.

## File-level plan

- `web/src/components/ui/BaseField.vue` — new
- `web/src/components/ui/BaseInput.vue` — new
- `web/src/components/ui/BaseSelect.vue` — new
- `web/src/components/backup_policies/BackupPolicyFormModal.vue` — rewritten: `storagePolicies`
  store wiring, `storage_policy_id` replacing `destination` in form state/payload, new components,
  guardrails
- `web/src/components/storage/StorageEditModal.vue` — refactored to use the new components; no
  behavior change
- `web/src/components/backup_policies/BackupPolicyFormModal.spec.js` — updated for the new field
  and select-based destination
- `web/src/components/storage/StorageEditModal.spec.js` — updated only if selectors change from the
  `BaseInput`/`BaseSelect` refactor (`data-test` attributes are preserved via `$attrs` passthrough,
  so most assertions should be unaffected)
- New: `web/src/components/ui/BaseField.spec.js`, `BaseInput.spec.js`, `BaseSelect.spec.js`

## Out of scope

- Any further change to `api-server`/`policy-server` — this branch is frontend-only, consuming the
  schema `backup-policy-storage-link` already shipped.
- A "create a new storage policy" shortcut from within the backup-policy form (e.g. an inline
  "+ New Storage Policy" option in the select) — an operator creates the storage policy on `/storage`
  first, same as today.
- Showing the resolved `destination` string anywhere inside the form itself — the select's own
  option labels already show `host:port`, so a separate read-only echo would be redundant.

## Testing plan

- **`BaseField.spec.js` / `BaseInput.spec.js` / `BaseSelect.spec.js`**: label rendering + required
  asterisk; `v-model` round-trips a value; arbitrary attrs (`data-test`, `required`, `pattern`) pass
  through to the underlying element.
- **`BackupPolicyFormModal.spec.js`**: destination select is required (native validity blocks
  submit when unset); selecting a storage policy sets `form.storage_policy_id` and the payload's
  `storage_policy_id`; edit mode pre-selects the policy's existing `storage_policy_id`; "Reload"
  calls the store's `fetchAll`; auto-load fires on mount only when the store starts empty; name
  guardrail rejects whitespace-only; RPO pattern rejects a malformed value and accepts a valid one
  (`24h`, `1h30m`) or an empty one.
- **`StorageEditModal.spec.js`**: existing assertions continue to pass against the refactored
  markup (same `data-test` attributes, same validation messages).

## Documentation

- `docs/components/web.md` — the `/policies` bullet currently reads "...opening a form modal for
  creating new policies (fields: name, RPO, backup window, client filters, object filters,
  destination)..."; change `destination` in that field list to `storage policy` (a select over
  `/storage`'s policies, not free text) to match this change.
- No protocol, API, or architecture changes — no other doc updates required.
- `CHANGELOG.md` — one dated entry once this branch is ready to merge (alongside
  `backup-policy-storage-link`'s own entry, since neither is useful to operators without the other).
