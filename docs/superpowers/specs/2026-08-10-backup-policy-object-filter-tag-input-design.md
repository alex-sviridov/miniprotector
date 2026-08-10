# Design: chip/tag input for backup policy object filter patterns

**Date:** 2026-08-10
**Status:** Approved for planning

## Problem

`BackupPolicyFormModal.vue`'s object filters section lets an operator add any number of filters, each
with a `path` plus `include`/`exclude` glob patterns. Today `include`/`exclude` are single free-text
inputs holding a comma-separated string (`includeText`/`excludeText` in the form model), split into
`string[]` only at submit time via `splitCsv`. This has no per-pattern structure while editing: no
visual separation between patterns, no way to remove one pattern without editing the raw text, and no
feedback on whether a pattern is even syntactically valid until the server rejects it on save.

Separately, nothing stops an operator from adding two overlapping patterns to the same list — e.g.
`/var/log` and `/var/log/app` in the same `include` array — which is always redundant (the child is
already covered by the parent).

## Approach

Replace the two free-text inputs per filter row with a new reusable chip/tag input component, backed
by a JS validator that mirrors the backend's glob syntax check, plus a same-list parent/child overlap
check. No new dependency — the project has no tag-input library today (`web/package.json` only carries
`@vuepic/vue-datepicker` and `vue-good-table-next` for their specific complex widgets); every other
form control (`BaseInput`, `BaseSelect`, `RepeatableFieldList`) is hand-rolled, so this follows the
same convention.

## `web/src/utils/globPattern.js` (new)

Two pure functions, no component dependency:

```js
// validateGlobPattern mirrors path.Match's syntax-only check (Go's
// path.Match(pattern, "") -- see backup_policy.go's Validate()): parses the
// pattern for balanced [...] classes, valid \-escapes, and no trailing
// backslash, without matching against any real path. Ported by hand since
// there's no shared Go<->JS validation surface; kept in lockstep with
// path.Match's grammar, not approximated with a looser regex, so a pattern
// the UI accepts never bounces off the server and vice versa.
export function validateGlobPattern(pattern) // -> { valid: boolean, error?: string }

// findParentChildConflict returns the first entry in `patterns` that is a
// plain-string parent, child, or duplicate of `pattern`, using a
// trailing-slash-normalized prefix check in both directions so "/var/log"
// doesn't false-positive against "/var/logs" (shares a text prefix but is a
// different directory). Comparison is plain string prefix, not glob-aware --
// deliberately simple, per product decision.
export function findParentChildConflict(patterns, pattern) // -> string | undefined
```

`findParentChildConflict`'s normalization: append `/` to both sides if not already present, then check
`startsWith` each direction. `"/var/log"` → `"/var/log/"` is a prefix of `"/var/log/app"` →
`"/var/log/app/"` → conflict. `"/var/log"` vs `"/var/logs"` → `"/var/log/"` vs `"/var/logs/"` → neither
prefixes the other → no conflict. Exact duplicates conflict trivially (both normalize identically).

## `web/src/components/ui/TagInput.vue` (new)

A generic chip/tag input, `v-model` on a plain `string[]` — matching `include`/`exclude`'s wire shape
exactly, which also eliminates the CSV join/split round trip entirely (not just a display change).

- A single text box; committing (Enter, comma, or blur) turns the current text into a chip and clears
  the box. Empty/whitespace-only text is ignored, matching today's `splitCsv` filtering.
- Backspace on an empty text box removes the last chip. Each chip has its own `×` remove button.
- Validation runs **on commit only**, not per keystroke, against the array of patterns already in
  *this* component's own list (this is also why the "only within the same list" scoping — include
  never checked against exclude — falls out for free: each `TagInput` instance only ever sees its own
  array, never its sibling's):
  1. Invalid glob syntax (`validateGlobPattern`) → chip added, flagged invalid, message from the
     validator.
  2. Otherwise, `findParentChildConflict` against the existing array → chip added, flagged invalid,
     message naming the conflicting pattern (e.g. `overlaps with "/var/log"`).
  3. Otherwise → valid chip.
- Invalid chips (either reason) render with a red outline and inline message.
- Each chip is keyed by a stable id assigned at creation (a local counter or `crypto.randomUUID()`),
  not array index — removing a chip from the middle of the list must not cause Vue to remount or
  misattribute a sibling chip's DOM node or drop input focus.
- Exposes a method (e.g. `isValid()`) the parent form's `validate()` calls to check whether any chip in
  this instance is currently flagged.

No dedup beyond the parent/child check above — an operator can still add the exact same pattern twice
in unrelated ways only if it doesn't trigger the overlap check, which it always will (duplicates
normalize identically), so in practice duplicates are already covered by the conflict check, not a
separate rule.

## Integration in `BackupPolicyFormModal.vue`

- `toFormShape`: each object filter's `include`/`exclude` map straight from `policy.object_filters[].include`/`.exclude`
  (already `string[]`) instead of `.join(', ')` into `includeText`/`excludeText`.
- `buildPayload`: `include`/`exclude` pass through directly; `splitCsv` and the `includeText`/
  `excludeText` intermediate shape are removed entirely.
- Template: each filter row's two text `<input>`s (`filter-include-input`, `filter-exclude-input`)
  become two `<TagInput>` instances, `v-model` on `form.object_filters[index].include` /
  `.exclude`.
- `validate()`: alongside the existing Name check, collects a ref per `TagInput` rendered and blocks
  submit (same mechanism Name already uses — `errors.message` set, `false` returned) if any reports
  invalid.

## Out of scope

- Any change to backend validation (`BackupPolicy.Validate()` in `backup_policy.go`) — the client-side
  checks are purely a faster feedback loop; the server's `path.Match`-based check remains the source of
  truth and is unchanged.
- A visual path/file browser for picking real paths (considered and explicitly deferred — object
  filters apply prospectively, before a new client necessarily has any catalog data to browse).
- Any change to `hostnames`, `labels`, or `backup_window`, which already use `RepeatableFieldList`
  one-row-per-item rather than a CSV-in-one-field pattern — not the shape this design addresses.
- Cross-filter-row conflict checking (a pattern in one object filter's `include` vs. another filter
  row's `include`) — each `TagInput` instance only ever validates within its own array.
- Deduplication as a distinct feature — subsumed by the parent/child conflict check, which already
  catches exact duplicates.

## Testing plan

- **`globPattern.spec.js`** (new): `validateGlobPattern` parity cases against Go's `path.Match` syntax
  rules — unterminated `[`, trailing `\`, empty pattern, `*`, `?`, `[a-z]`, `[^a]`, escaped `\*`.
  `findParentChildConflict` cases — parent-of, child-of, unrelated-shared-text-prefix (no conflict),
  exact duplicate.
- **`TagInput.spec.js`** (new): commit via Enter and via comma; remove via `×` and via backspace on
  empty text; invalid-syntax pattern flagged and blocks `isValid()`; overlapping pattern flagged and
  blocks `isValid()`; unrelated patterns coexist without false-flagging.
- **`BackupPolicyFormModal.spec.js`** (update): existing include/exclude tests migrate from typing a
  CSV string into the old text input to chip-entry interactions against `TagInput`; add a case where an
  invalid or overlapping pattern blocks submit.

## Documentation

- `docs/components/web.md` — updated to describe the chip-based include/exclude entry and the
  client-side validation/conflict feedback on the backup policy form.
- `CHANGELOG.md` — one dated entry: backup policy object filter patterns are now entered as individual
  chips with live syntax and parent/child-overlap validation, instead of a comma-separated text field.
