# Restore Destination Rename — Design

## Problem

The restore cart lets an operator select files and folders to restore, but always restores them to
their original path on the destination host. There's no way to restore `/etc/nginx/nginx.conf` to
`/etc/nginx/nginx.conf.bak` instead of overwriting the live file, or to restore `/data/photos` into
`/data/photos_recovered` instead of its original location — both common restore-review needs (avoid
clobbering a file that still exists, or stage a restore somewhere inspectable before promoting it).

## Scope

This design touches **web, api-server, and policy-server only** — proto included, since the new
field flows through it. It does **not** touch `agent`, `rwfs`, `policyclient`, or `bwfs`: those
already implement verification-only restore (`docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md`),
and nothing here changes what they do. This design only extends what a restore policy's `rules` can
*express* — a destination path alongside each selection — so the data exists in the policy for a
future restore-execution consumer to read. No executor reads it yet, exactly like `rules` itself sat
unconsumed by an executor before 2026-08-10.

## Goals

- An operator can give any selected file or folder a different destination path than its source
  path, directly in the restore review screen, before submitting.
- Renaming is per-selection (per cart rule), not global — most selections stay at their original
  path; only the ones explicitly edited get a `dest_path`.
- The restore review screen also surfaces information the operator needs to make that call safely:
  which storage node and source host each file-level selection came from, and its size. Folder-level
  selections (host-agnostic, can span many hosts/stores/sizes) show this as unavailable rather than
  computing an aggregate.
- The schema change is additive and backward compatible: an existing restore policy with no
  `dest_path` on any rule behaves exactly as it does today (restore to the original path).

## Non-Goals

- **No restore execution.** `rwfs restore` remains unbuilt (per the 2026-08-10 design's Non-Goals,
  still true). `dest_path` is stored, not acted on.
- **No rename validation beyond "well-formed and paired with an included rule."** No path-traversal
  sanitization, no collision detection between two rules' destination paths, no filesystem-existence
  check. Meaningful once something actually writes files; premature before that, and out of scope
  per this design's own scope note above.
- **No per-file rename for folder selections.** A folder rule's `dest_path` is a single replacement
  prefix for the whole folder (interpreted by a future restore executor, not by anything in this
  design) — not an editor for renaming individual files inside it. Renaming one specific file inside
  an otherwise-not-renamed folder requires selecting that file individually (already possible today,
  independent of this change — an exact-host rule already overrides its ancestor folder rule).
- **No live catalog expansion of folder selections.** The review table stays at today's rule-level
  granularity (one row per cart selection); it does not enumerate every file a folder rule would
  match. See "Table granularity" under UI below.

## Architecture

### 1. `RestoreRule` gains `dest_path`

`src/api/policyserver.proto`:

```proto
message RestoreRule {
  string host      = 1; // "" = host-agnostic, matches every source host
  string path      = 2;
  bool   include    = 3;
  // Destination path to restore to, if different from path. Empty (or equal
  // to path) means "no rename -- restore to the original path." Only
  // meaningful when include is true; see RestorePolicy.Validate.
  string dest_path = 4;
}
```

Regenerate `src/api/policyserver.pb.go` via `make proto`.

`src/cmd/policy-server/restore_policy.go`:

- `RestoreRule` struct gains `DestPath string \`json:"dest_path,omitempty"\`` alongside `Host`/`Path`/`Include`.
- `RestorePolicy.Validate()` gains, per rule: if `r.DestPath != "" && r.DestPath != r.Path && !r.Include`,
  error `"rules[%d]: dest_path is only valid on an included rule"`. (A rule whose `dest_path` merely
  equals `path` is never an error, included or not — that's indistinguishable from "no rename" and
  costs nothing to allow.)
- `ToProto` passes `DestPath` through to `pb.RestoreRule.DestPath` alongside the existing three
  fields.
- `Clone` already deep-copies the `Rules` slice element-wise (`copy(rules, p.Rules)`); a new plain
  `string` field needs no additional handling there.

### 2. `api-server`: DTO and handler

`src/cmd/api-server/policies.go`:

- `ruleDTO` gains `DestPath string \`json:"dest_path,omitempty"\`` — used both when decoding
  `POST /api/v1/restore`'s body and when encoding a policy back out via `toPolicyDTO` (so a
  `GET`/`ListPolicies` response round-trips the field).
- `handleCreateRestore`'s `rules[i] = &pb.RestoreRule{...}` construction gains `DestPath: ru.DestPath`.
- `toPolicyDTO`'s existing `rules[i] = ruleDTO{Host: ..., Path: ..., Include: ...}` construction
  (line ~66) gains `DestPath: r.GetDestPath()`.

No new REST route. `POST /api/v1/restore`'s existing body shape just accepts one more optional field
per rule.

### 3. `web`: cart, submission, and the review table

**`web/src/utils/restoreRules.js`** — `toggleFile`/`toggleFolder` create new rules with
`destPath: path` (equal by default — "no rename" is represented as equality here, not as an absent
field; the absent-vs-equal distinction only matters on the wire, handled in submission, below).
`resolveFile`/`resolveFolderState`/the pruning logic in `toggleFile`/`toggleFolder` are unchanged —
rename is data carried alongside a rule, not part of what these functions resolve.

**`web/src/stores/restoreCart.js`** — new action:

```js
setDestPath(entry, destPath) {
  const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
  if (rule) rule.destPath = destPath
}
```

Also, `toggleFile(host, path, storeHost, size)` gains two optional trailing params, threaded into
the created rule as `storeHost`/`size` (display-only; see below) when provided. `toggleFolder` is
unchanged — folder rows never carry `storeHost`/`size`.

**`web/src/views/CatalogView.vue`** — `toggleSelection` (the only caller of `toggleFile`) passes
`row.representative?.store_host` and `row.representative?.size` for a file row:

```js
function toggleSelection(row) {
  if (row.isFolder) restoreCart.toggleFolder(row.path)
  else restoreCart.toggleFile(row.sourceHost, row.path, row.representative?.store_host, row.representative?.size)
}
```

**`web/src/stores/restoreSubmission.js`** — `buildRulesByStore`'s per-store rule lists are currently
sent to `restorePolicies.create` as-is. Add a wire-shape mapping step immediately before each
`POST /api/v1/restore` call:

```js
function toWireRule(rule) {
  return {
    host: rule.host,
    path: rule.path,
    include: rule.include,
    ...(rule.destPath && rule.destPath !== rule.path ? { dest_path: rule.destPath } : {}),
  }
}
```

applied via `rules.map(toWireRule)` when building each store's `create()` payload. This strips the
client-only `storeHost`/`size` fields and omits `dest_path` entirely when unchanged — keeping the
wire payload identical to today's for every rule nobody renamed.

**`web/src/views/RestoreView.vue`** — replace the `<ul>` of cart entries with a table, one row per
`restoreCart.entries` item (unchanged granularity from today):

| storage host | source host | source path | destination path | size |
|---|---|---|---|---|
| *(dash if folder row)* | *(dash if folder row, else `entry.host`)* | `entry.path` | click-to-edit, defaults to `entry.path` | *(dash if folder row, else `entry.size`, formatted)* |

- Destination path: rendered as text by default; clicking it swaps in a text input (pre-filled with
  the current `destPath`) that commits on blur or Enter via `restoreCart.setDestPath(entry, value)`,
  and reverts to text display. No separate checkbox or edit-mode toggle.
- Storage host / source host / size: for a file-level row (`entry.host !== null`), read directly
  from `entry.storeHost`/`entry.host`/`entry.size` (captured at selection time, per above). For a
  folder-level row (`entry.host === null`), render `—` for all three — no live lookup, per this
  design's Non-Goals.
- The existing single "Destination host" selector above the table is unchanged — it still applies to
  every row; this design adds no per-row destination-host control.
- The remove button becomes a table column instead of an inline list-item button; behavior
  unchanged.

**Table granularity note:** an earlier version of this design considered expanding folder selections
into one row per matched file (so every row would have real host/size data). That was rejected:
folder selections can be arbitrarily large, and today's rule-level cart model (one row per selection,
however broad) is what the rest of the restore flow — submission, the future verification/execution
consumers of `rules` — already assumes. Keeping the table at that same granularity avoids introducing
a second, inconsistent notion of "what a restore selection is."

## Data Flow

```
CatalogView.vue: user checks a file row
  -> restoreCart.toggleFile(sourceHost, path, storeHost, size)
  -> cart.rules gains {host, path, include: true, destPath: path, storeHost, size}

RestoreView.vue: renders cart.entries as a table
  -> user clicks a destination-path cell, edits it, blurs
  -> restoreCart.setDestPath(entry, newValue) mutates that rule's destPath in place

RestoreView.vue: user clicks "Submit restore"
  -> restoreSubmission.submit(destinationHost)
       -> buildRulesByStore(...) groups cart.rules per store (unchanged from today)
       -> each store's rule list mapped through toWireRule (strips storeHost/size,
          omits dest_path when == path)
       -> POST /api/v1/restore per store: {..., rules: [{host, path, include, dest_path?}]}

api-server: handleCreateRestore decodes ruleDTO (dest_path now included)
  -> pb.CreatePolicyRequest{..., Rules: [...]}
  -> policy-server.CreatePolicy
       -> buildPolicyForCreate: RestoreRule{Host, Path, Include, DestPath}
       -> RestorePolicy.Validate(): rejects dest_path on an excluded rule
       -> written to policies/restore/*.json, dest_path persisted on disk

(nothing downstream of policy-server reads dest_path yet -- agent/rwfs/bwfs
 are unchanged, per Scope)
```

## Validation

- `dest_path` set (non-empty and different from `path`) on a rule with `include: false` →
  `RestorePolicy.Validate()` error, `CreatePolicy` returns `INVALID_ARGUMENT` (existing error-plumbing
  path, no new error-handling code needed beyond the check itself).
- `dest_path` equal to `path`, or empty, on any rule → always valid, treated as "no rename."
- No other constraint. In particular: no check that `dest_path` is absolute, no check that it
  doesn't collide with another rule's `dest_path` or `path`, no path-traversal sanitization. All of
  these become relevant once a restore executor actually writes to `dest_path` — adding them now,
  against a field nothing reads, would be validating against a use case that doesn't exist yet.

## Testing

- **`policy-server`**: `restore_policy_test.go` — `Validate` rejects `dest_path` set on an excluded
  rule; accepts `dest_path` set on an included rule; accepts `dest_path == path`; accepts `dest_path`
  empty. `ToProto` includes `dest_path` in the produced `pb.RestoreRule`. `Clone` still deep-copies
  (extend the existing mutation-independence test to cover `DestPath`).
- **`api-server`**: `policies_test.go` — `handleCreateRestore` passes `dest_path` through from the
  decoded body into the `CreatePolicyRequest`; `toPolicyDTO` round-trips `dest_path` on a policy
  that has one set.
- **`web`**:
  - `restoreRules.spec.js` — `toggleFile`/`toggleFolder` create rules with `destPath` equal to
    `path`.
  - `restoreCart.spec.js` — `setDestPath` updates the matching rule's `destPath` and leaves others
    untouched; `toggleFile` threads `storeHost`/`size` onto the created rule when passed.
  - `restoreSubmission.spec.js` — `toWireRule` omits `dest_path` when unchanged, includes it when
    different, and never sends `storeHost`/`size`.
  - `RestoreView.spec.js` — table renders storage host/source host/size for a file row and dashes
    for a folder row; clicking the destination-path cell allows editing; committing an edit calls
    `setDestPath`; submitting sends the edited value.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change and protocol-change rules:

- **`docs/protocols/policy-server.md`** — `RestoreRule` proto block gains `dest_path`; the
  `"restore"` policy prose section notes that a rule may carry a destination path, valid only when
  `include` is true.
- **`docs/components/policy-server.md`** — restore policy type section gains a short note on
  `dest_path` alongside the existing `rules` description.
- **`docs/components/web.md`** — restore review screen description updated: table columns (storage
  host, source host, source path, destination path, size), click-to-edit rename behavior.
- **`docs/components/api-server.md`** (if it documents `POST /api/v1/restore`'s body shape) —
  `rules[].dest_path` added as an optional field.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

No change to `docs/ARCHITECTURE.md` (no topology/data-flow/component change — same components,
same RPCs, one additional optional field) and no change to `docs/protocols/restore.md` (that
document covers the `bwfs` `RestoreFile` read protocol, which this design doesn't touch).

## Relationship to Prior Work

Builds directly on `docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md`'s
`RestoreRule{host, path, include}` schema and the restore cart's rule-list selection model
(`docs/superpowers/specs/2026-08-09-restore-cart-design.md`). Adds one field and one UI capability;
changes no existing behavior for a rule that doesn't use it.
