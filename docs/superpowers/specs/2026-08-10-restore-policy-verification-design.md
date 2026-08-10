# Restore Policy Verification Execution — Design

> **Supersedes, in part:** `docs/superpowers/specs/2026-08-09-restore-policy-type-design.md` (the
> `"restore"` policy's `source_store`/`config` fields) and
> `docs/superpowers/specs/2026-08-10-restore-cart-submission-design.md` (the submission flow that
> built them). Both were already shipped; this design revises their schema and submission-flow
> choices in place, for the reasons in "Why revise already-shipped work" below, and adds the piece
> both explicitly deferred — `agent` actually acting on a `"restore"` policy. Still scoped to
> **verification only**, since `rwfs restore` (the write-to-filesystem path) doesn't exist yet (see
> `docs/ARCHITECTURE.md`'s `rwfs` row).

## Problem

`policy-server` can hold a `"restore"`-typed policy and route it to the destination node via
`client_filters`, exactly like backup and storage policies. `agent` already derives dynamic tasks
from cached `"backup"` and `"storage"` policies (`backup.go`, `storage.go`) but has no notion of
`"restore"` at all. Since actual restore can't be executed yet, the useful thing `agent` *can* do
today is prove the requested files are actually intact and retrievable from the source `bwfs` — an
early, real signal that a future restore of this exact policy would succeed, using a capability
(`rwfs verify`) that already exists.

Wiring that up surfaced two structural problems in the already-shipped schema and submission flow
that make the "obvious" implementation (verify against a pre-enumerated file list) the wrong shape
to build on:

1. **`source_store` is resolved once, client-side, and baked into the policy as a raw
   `host:port`.** If the storage node's checked-in address changes between submission and
   whenever `agent` actually gets around to the one-shot verify (could be many minutes, or longer
   if the destination node's `agent` was down), the policy silently points at a stale address.
   `"backup"` policies don't have this problem — `destinations` is computed *live* from checkin
   records every time `GetPolicies` serves the policy, specifically to avoid this.
2. **`config.files` is a fully client-side-expanded list of every individual matched file.** The
   browser has to paginate `GET /api/v1/catalog` to materialize the whole list *before*
   submission, ship it through `CreatePolicy` (which has no override for gRPC's default max
   message size anywhere in this codebase, so this already fails around ~35k–65k file entries in
   one list), have `policy-server` store/re-serve the whole blob on every poll, and have `agent`
   re-parse it every reconcile tick. `"backup"` policies don't have this problem either —
   `object_filters` carries intent (`{path, include, exclude}`), not a pre-walked file list;
   `brfs` resolves it against the real filesystem at execution time.

## Why revise already-shipped work rather than build on top of it

Both problems trace to the same root cause: the restore-policy-type and restore-cart-submission
designs modeled `"restore"` as "collect the final answer once, up front, in the browser" rather
than "carry small intent, resolve it live at the point of use" — the pattern `"backup"` already
uses successfully for both of its own analogous problems (`destinations`, `object_filters`).
Verification is the first real consumer of this policy type; building it against the existing
shape would mean either inheriting a real staleness bug and a real scale ceiling, or immediately
re-deriving the same fix `"backup"` already has. Fixing the shape now, before a second consumer
(the future actual `rwfs restore`) exists, is cheaper than fixing it twice or living with the
inherited bugs.

## Goals

- `source_store` staleness is eliminated: a restore policy references a `"storage"` policy by ID
  (exactly like `"backup"` does) and its dial address is resolved live, every time it's served —
  reusing `policy-server`'s existing `CheckinsForPolicy` mechanism verbatim, not a parallel one.
- `config.files`'s scale ceiling is eliminated: a restore policy carries the cart's actual
  selection rules (small, bounded by user interaction count) instead of a pre-expanded file list.
  Resolution against the real, current file listing happens at verify time, on the node that's
  about to act — the same "walk the real thing at execution time" pattern `object_filters`/`brfs`
  already use.
- A `"restore"` policy's rules get verified against the resolved source `bwfs` via `rwfs verify`,
  automatically, the same "policy-server holds the directive, agent picks it up and acts" pattern
  backup/storage already use.
- Verification is a **one-shot** action per policy: retried with the existing backoff machinery on
  failure, never re-run once it succeeds.
- A file-level rule with nothing matching it is a real, visible failure; a folder-level rule with
  nothing under it is not (an empty folder isn't an error).
- No `bwfs` change, no restore-protocol wire change — `bwfs` already serves everything `rwfs
  verify` needs.

## Non-Goals (this pass)

- **Actual restore.** `rwfs restore` remains unbuilt. This design's output is a pass/fail signal
  an operator can read via `agent list-policies` and logs (already correlatable end-to-end by
  `job-id`, per `docs/components/agent.md`'s "Logging and correlation" — this system's existing,
  general-purpose observability path), nothing more. No new status-reporting channel back through
  `policy-server`/`api-server`/web is added — that's a separate, independent concern already
  covered by log shipping.
- **No new concurrency cap.** Restore-verify tasks share `agent`'s existing background-job
  semaphore (`MaxConcurrentBackupJobs`) rather than getting a dedicated one. Rare, one-shot,
  low-priority relative to scheduled backups; revisit only if this proves to matter in practice.
- **No push-based/low-latency pickup.** A restore policy is still picked up on `agent`'s existing
  poll cycle (up to `PolicyFetchIntervalSec` + one reconcile tick of latency). Agents are
  deliberately outbound-only in this architecture's security model; giving them an inbound surface
  just to shave minutes off a background verification is a much bigger trade-off than the latency
  it removes.
- **No pruning of stale per-task state, no cross-node coordination** — same acceptances
  `2026-07-10-agent-backup-execution-design.md` already made for backup tasks.
- **Rule semantics stay exactly what the cart already defines.** Longest-matching-rule-wins,
  host-agnostic folder rules, host-specific file rules — all unchanged from
  `web/src/utils/restoreRules.js`. This design ports that logic to Go (`rwfs`) and to a Go-side
  facet query (`catalog`); it does not change what a rule means.
- **`list-policies`'s NEXT RUN column will read "due now" for a permanently-succeeded one-shot
  task.** Same known, accepted cosmetic wrinkle as before: `estimatedNextRun`/`formatNextRun`
  (`list.go`) have no "never again" concept. Not worth a shared-display-code change for one
  column's wording on a rare, one-shot task type.

## Architecture

### 1. `policy-server`: `RestorePolicy` schema change

`source_store` (raw `host:port`) is replaced by `storage_policy_id` (string, required, must
reference an existing `"storage"`-typed policy — same validation `BackupPolicy` already applies,
checked in `CreatePolicy` where a live cache is in scope, not in `Validate()` itself, exactly
mirroring backup's existing split). The resolved dial address is served via the *existing*
`destinations` proto field (already defined, currently backup-only) computed the same live way,
via the same `CheckinsForPolicy(ctx, storagePolicyID)` call `server.go` already makes for backup —
literally the same helper, not a parallel implementation. `agent` uses `destinations[0]`, same
"only the freshest is used" convention backup already documents.

`config` (opaque JSON, borrowed from `"storage"`'s backend-config field) is replaced by a proper
structured, typed field: `repeated RestoreRule rules`, mirroring how `object_filters` is a real
typed field for backup rather than opaque JSON — opaque `config` was only ever the right fit for
storage's backend-*pluggable* settings; restore's rules have one fixed, known shape.

`storage_policy_id` is **not** a new field — it already exists on both `Policy` (field 15) and
`CreatePolicyRequest` (field 12), currently set only by backup policies. Restore simply starts
setting/reading it too, the same field, no wire-format change needed there. `source_store` (`Policy`
field 18, `CreatePolicyRequest` field 13, both added 2026-08-09) is removed and must be marked
`reserved` rather than silently dropped, so the numbers are never reassigned to something else
later and collide with any policy JSON/wire data written under the old schema:

```proto
message RestoreRule {
  string host    = 1; // empty = host-agnostic (folder rule, matches every source host)
  string path    = 2;
  bool   include = 3;
}

message Policy {
  // ...existing fields unchanged, including storage_policy_id = 15 (now also used by restore)...
  reserved 18; // was source_store, removed
  repeated RestoreRule rules = 19; // restore policy only
}

message CreatePolicyRequest {
  // ...existing fields unchanged, including storage_policy_id = 12 (now also required by restore)...
  reserved 13; // was source_store, removed
  repeated RestoreRule rules = 14; // restore policy only
}
```

`RestorePolicy.Validate()`: `client_filters` via `validateCommon` (unchanged), `storage_policy_id`
non-empty, `rules` non-empty (at least one), each rule's `path` non-empty. No glob-syntax check —
rules match exact host+path pairs via longest-matching-ancestor resolution, not `path.Match`
globs, so there's nothing glob-shaped to validate.

`RestorePolicy.ToProto`: populates `Destinations` (computed by the caller, same as
`BackupPolicy.ToProto` already does — not computed inside `ToProto` itself) and `Rules` instead of
`SourceStore`/`Config`.

Everything else about the type is unchanged from the 2026-08-09 design: still not updatable, still
targeted via `client_filters`, still lives under `policies/restore/*.json`, still deletable,
`disabled_at` behaves the same.

`write.go`'s cross-type field rejection (`buildPolicyForCreate`) needs three concrete changes,
since `storage_policy_id` moves from backup-exclusive to backup-*and*-restore, and `config`
reverts to storage-exclusive:
- `"a restore policy must not set object_filters/rpo/backup_window/storage_policy_id/port"` loses
  `storage_policy_id` from that list (restore now requires it) and gains `config` (restore no
  longer uses it) — becomes `"...object_filters/rpo/backup_window/port/config"`.
- `"only a restore policy may set source_store"` is deleted along with the field.
- A new check is added: `"only a restore policy may set rules"`, and the storage/backup rejection
  messages each gain `rules` to their forbidden-fields list.

## Existing tests to move, not just extend

`src/cmd/policy-server/restore_policy_test.go`, `server_test.go`'s restore cases, and
`src/cmd/api-server/policies_test.go`'s restore cases all currently assert the `source_store`/
`config` shape from the 2026-08-09 design — these need updating in place (not left alongside new
tests for the new shape), since the old shape no longer exists to test.

### 2. `catalog`: new `ListStoreFacets` RPC

Mirrors the three existing facet RPCs (`ListClientFacets`/`ListJobFacets`/`ListDirectoryFacets`)
exactly — same `ListFacetsRequest`/`ListFacetsResponse` messages, grouping by `store_host` instead
of source host/job/directory. Gives the frontend "which stores does this selection touch,"
bounded by distinct-store-count (typically small, regardless of how many files match), instead of
paginating every matched file just to collect the same set of hostnames.

```proto
service CatalogService {
  // ...existing RPCs unchanged...
  rpc ListStoreFacets(ListFacetsRequest) returns (ListFacetsResponse);
}
```

`api-server`: new `GET /api/v1/catalog/stores` route (`handleListCatalogStores`), same pattern as
the existing `/catalog/clients`/`/catalog/jobs`/`/catalog/directories` routes.

### 3. `web/src/stores/restoreSubmission.js`: simplified submission

Replaces `fetchCandidateEntries` / `collapseToLatestVersion` / `filterResolved` / `groupByStore`
entirely. New flow, per top-level `cart.entries`:

1. Call `GET /api/v1/catalog/stores?source_host=...&pattern=...` to get the distinct `store_host`s
   touched by that entry's pattern.
2. Union the distinct `store_host`s across all entries.
3. For each distinct `store_host`, look up its owning storage policy's `id` from the already-
   fetched `storagePolicies.list` (same cross-reference `resolveStoreAddress` does today, stopping
   at the ID instead of resolving all the way to a dial address — `policy-server` finishes that
   resolution live, per §1).
4. `POST /api/v1/restore` once per distinct `storage_policy_id`, with `client_filters` and the
   **entire, unsplit** `cart.rules` list every time — no client-side per-store splitting. A rule
   that doesn't match anything on a given store simply resolves to zero matches there (a no-op for
   a folder rule, a reported failure for a file rule — see §4); the parallel policy for whichever
   store the file actually lives on handles it.

This removes the browser's need to ever fetch, hold, or dedupe individual file rows — submission
now moves an amount of data proportional to the number of rules the user actually created, not the
number of files those rules resolve to.

### 4. `rwfs verify`: `--rules-stdin` (supersedes the earlier `--files-stdin` idea)

New bool flag. When set, `rwfs verify` reads all of stdin, parses
`{"rules":[{"host","path","include"}, ...]}` (Go port of `restoreRules.js`'s rule shape and
longest-matching-ancestor resolution — same semantics, same tie-breaking, ported not reinvented),
then:

1. Calls `ListFiles` broadly against the resolved store (no `server_name`/`path` positional
   filter — the stdin rules are the filter now; a positional is no longer needed since rules can
   be host-agnostic and aren't cleanly pre-partitionable by host).
2. For every returned row, resolves inclusion via the ported longest-matching-rule logic against
   `(row.Source, row.Path)`.
3. Verifies every row that resolves to included, exactly as today (`RestoreFile` per matched row,
   BLAKE3 + CRC32 checks).
4. Per-rule accounting: a **file-level** rule (`host` set) that matched zero rows is reported as a
   failure (`"not found on this store"`) — it named one specific file, and it wasn't there. A
   **folder-level** rule (`host` empty) that matched zero rows is *not* a failure — a folder with
   nothing left under it (or nothing there in the first place) is a legitimate outcome, not an
   error.

`--filter`/the positional remain available for `rwfs verify`'s original, non-restore use (an
operator manually spot-checking a store) but are mutually exclusive with `--rules-stdin` in
practice — `agent` only ever uses `--rules-stdin`.

### 5. `agent`: restore task derivation (`src/cmd/agent/restore.go`, new)

One task per **restore policy** (not per source host, unlike the file-list version of this design
— rules aren't cleanly host-partitionable once a folder rule is host-agnostic by definition).

- `cachedPolicy` (`backup.go`) and `policyclient`'s on-disk `CachedPolicy` (`fetch.go`) both gain
  `Rules []RestoreRule` (mirroring `ObjectFilter`'s existing passthrough treatment) — `config`'s
  restore-only meaning goes away with the schema change in §1, so nothing is removed that other
  code depends on.
- `restoreTasks(policiesCachePath, logger, conf) ([]Policy, bool)`: for every cached policy with
  `Type == "restore"` (skipping `p.disabled(now)`, same as backup/storage) with a non-empty
  `Destinations` (mirrors backup's existing "no live checkins yet, skip and log" direction for a
  dangling/not-yet-checked-in `storage_policy_id` — exactly the same condition backup already
  handles, reused as-is):
  - `ID`: `restore:<policy-name>`
  - `JobID`: `restore:<policy-name>:<unix-timestamp>`
  - `Binary`: `"rwfs"`; `Args`: `["verify", destinations[0], "--rules-stdin", "--job-id", jobID]`
  - `Stdin`: the policy's `Rules`, marshaled to `{"rules":[...]}`
  - `Background: true`
  - `Due: func(s PolicyState, now time.Time) bool { return s.LastSuccessAt == nil }` — one-shot,
    reusing `isDue`'s existing failure-backoff path for retries, never re-dispatched once
    succeeded, for as long as this policy still exists in the cache.

### `Policy`/`reconcile.go`: threading stdin through

Unchanged from the original version of this design. `runner` becomes `func(ctx, binary, args,
stdin []byte) error`; `realExec` sets `cmd.Stdin` when non-nil; `Policy` gains `Stdin []byte`
(zero-value for every other policy/task, strictly additive).

### Wiring (`main.go`)

`restoreTasksFunc` folds into the same `policiesFunc` closure `backupTasks` already contributes
to, used by both `agent serve` and `agent list-policies`.

## Data Flow (end to end)

```
web: cart.rules = [{host,path,include}, ...]  (small, unchanged shape from today)
  -> GET /api/v1/catalog/stores per entry  -> distinct store_hosts touched
  -> map store_host -> storage_policy_id via already-fetched storagePolicies.list
  -> POST /api/v1/restore once per distinct storage_policy_id
       { client_filters, storage_policy_id, rules: cart.rules }  (full, unsplit)
  -> policy-server ("restore" policy; client_filters targets the executing node)
       |
       v  GetPolicies (agent's existing policy-update job)
policies-cache.json  (rules passthrough; destinations resolved live, same as backup)
       |
       v  agent serve, every reconcile tick
restoreTasks(): one task per restore policy, due until first success
       |
       v  background exec
rwfs verify <destinations[0]> --rules-stdin --job-id ...
  stdin = {"rules":[...]}  (the whole policy's rules, unsplit)
       |
       v  ListFiles (broad) + per-row rule resolution + RestoreFile per included match
bwfs  (existing List/Restore protocol, unchanged)
```

## Configuration

No new config keys.

## Error Handling

- **`policies-cache.json` missing or unparseable**: zero restore tasks derived that tick,
  `ok=false` — same contract `backupTasks`/`storageTasks` already use.
- **`storage_policy_id` dangling or not yet checked in** (`destinations` empty): policy contributes
  no task, logged — identical to backup's existing handling of the same condition, reused as-is.
- **A file-level rule matching nothing on the resolved store**: verification failure for that
  policy's task (see §4); a folder-level rule matching nothing is not a failure.
- **Destination `bwfs` unreachable**: ordinary exec failure, backoff, retried next eligible tick.
- **`rules` empty or malformed** (should be prevented by `Validate()`, but `agent` doesn't trust
  the cache blindly): policy skipped, logged — same fail-safe direction as storage's config
  parsing.
- **Concurrency cap reached**: due task stays due, reconsidered next tick, not recorded as failure.
- **`agent serve` receives `SIGTERM` mid-verify**: in-flight `rwfs verify` terminated via the
  shared shutdown context, same as an in-flight `brfs` backup today.

## Testing

- `policy-server`: `RestorePolicy.Validate()` — rejects empty `storage_policy_id`, empty `rules`,
  a rule with an empty `path`; `CreatePolicy` rejects a dangling `storage_policy_id` (mirrors
  backup's existing test); `ToProto` populates `rules`/`destinations`, omits `source_store`/
  `config`. `GetPolicies`/`ListPolicies` resolve `destinations` for a restore policy the same live
  way as backup (extend existing checkin-resolution tests).
- `catalog`: `ListStoreFacets` — returns distinct `store_host` values with correct counts, same
  table-driven shape as the existing three facet RPCs' tests.
- `api-server`: `handleCreateRestore` composes `CreatePolicyRequest{type:"restore",
  storage_policy_id, rules}`; `handleListCatalogStores` proxies `ListStoreFacets` correctly.
- `web`: `restoreSubmission.spec.js` rewritten for the new flow — store-facet lookup, ID
  cross-reference, one `POST /api/v1/restore` per distinct storage policy with the full rule list.
- `rwfs`: `--rules-stdin` — ported rule-resolution matches `restoreRules.js`'s test cases
  (longest-match, host-agnostic vs host-specific, folder-then-file-exception); a file-level rule
  matching zero rows fails with the right reason and affects exit code; a folder-level rule
  matching zero rows does not.
- `agent`: `restoreTasks` — one task per restore policy; empty `destinations` skips with a logged
  error; malformed/empty `rules` skips; one-shot `Due` semantics (never-succeeded is due, failure
  backs off, success is permanently not-due); `Stdin` threading through both `reconcile.go`
  dispatch paths.
- Integration (extends the existing e2e harness): a real `policy-server` serving a `"restore"`
  policy referencing a real, checked-in `"storage"` policy, with `rules` matching real,
  previously-backed-up files on a real `bwfs`, and a real `agent serve` — confirms `rwfs verify`
  runs, succeeds, and never re-runs. A second case with one file-level rule naming a file that was
  never backed up confirms that policy's task fails and retries with backoff.

## Documentation Impact

Per `.claude/CLAUDE.md`'s protocol-change and feature-change rules:

- **`docs/protocols/policy-server.md`** (exists) — update the `"restore"` type section: `rules`
  replaces `config`, `storage_policy_id` replaces `source_store`, `destinations` resolution now
  applies to restore too. Proto field list updated (`rules` added, `source_store` removed).
- **`docs/components/policy-server.md`** (exists) — same update in the "Policy types and directory
  layout" section.
- **`docs/protocols/catalog.md`** / wherever the facet RPCs are documented — add
  `ListStoreFacets`.
- **`docs/components/api-server.md`** / **`docs/api/rest-v1.md`** — `GET /api/v1/catalog/stores`;
  `POST /api/v1/restore`'s body fields (`storage_policy_id`/`rules` replace `source_store`/
  `config`).
- **`docs/components/agent.md`** (exists) — new "Policy-driven restore verification" section
  mirroring "Policy-driven backup execution."
- **`docs/components/rwfs.md`** (exists) — document `--rules-stdin`.
- **`docs/protocols/restore.md`** (exists) — extend "CLI → RPC Mapping" with the `--rules-stdin`
  case; no wire-protocol change.
- **`docs/superpowers/specs/2026-08-09-restore-policy-type-design.md`** and
  **`2026-08-10-restore-cart-submission-design.md`** — add a note at the top of each pointing to
  this design as the schema's current source of truth for the fields it revises, so a reader
  doesn't implement against the superseded shape.
- **`docs/ARCHITECTURE.md`** — update `agent`'s role line to mention restore-policy verification.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

## Relationship to Prior Work

The 2026-08-09 restore-policy-type and 2026-08-10 restore-cart-submission designs got a working
`"restore"` policy end to end, but modeled it as "resolve everything once, up front, client-side"
rather than the "carry small intent, resolve live at the point of use" pattern `"backup"` already
proved out for the exact same two problems (destination staleness, filter-list scale). This design
is the first real consumer of the policy type (verification), and fixes both inherited problems at
the schema level before a second consumer (actual restore) would otherwise have to fix them again,
or inherit them permanently.
