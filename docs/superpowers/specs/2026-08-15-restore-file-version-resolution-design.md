# Restore: Versioned File Resolution & Scoped Query — Design

> **Builds on:** `docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md` (`RestoreRule`
> schema, `resolveRestoreFile` precedence), `docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md`
> (`dest_path` — the precedent this design's `not_before`/`not_after` fields follow), and
> `docs/superpowers/specs/2026-08-14-restore-verify-execute-split-design.md` (mode/overwrite split). This
> design covers how a restore policy's rules become an actual, timeframe-scoped list of files to act on —
> the shared foundation both `rwfs verify` (today) and a future `rwfs restore` (unbuilt) will consume. It
> does **not** implement `rwfs restore` itself.

## Problem

Two problems, both surfaced from tracing today's rule→file-list pipeline:

1. **No version selection.** `bwfs`'s `file_id` embeds mtime (`fs://host:type:path:mtime`), so a path
   backed up at multiple points in its history produces multiple distinct rows in `ListFiles`. Today's
   rule resolution (`resolveRestoreFile`, `rules.go`) matches purely on `(host, path)` and has no concept
   of time — a matching rule selects *every* historical version of a file, not just one. There's also no
   way for an operator to say "restore this as it looked around a certain date" — verify (and any future
   restore) always implicitly means "whatever's currently in the store."

2. **Full-store scans.** `rwfs verify --rules-stdin` calls `ListFiles` with no `server_name`/`path`
   filter, because rules can span arbitrary hosts (a host-agnostic folder rule matches every source). This
   is a full dump of the destination store's entire catalog, filtered client-side. At the scale
   `docs/protocols/list.md` was written for ("thousands of entries per host") this is fine; it does not
   hold at large single-node scale, and a single `bwfs` node's own store can reach billions of rows.

## Goals

- A restore rule can pin its selection to a timeframe: "use the latest backup inside this window, ignore
  anything outside it" — **per-rule**, not global, so different files in one restore can pin different
  points in time.
- "Latest backup inside the window" is evaluated correctly even when a file's content hasn't changed: a
  file re-attested by many backup jobs without any content change must still be found as "present as of"
  any date in that unchanged streak, not just its original upload date.
- Rule resolution against a destination store is proportional to what the rules actually touch, not the
  size of the whole store — including for host-agnostic folder rules, which today can't be bounded at
  all.
- The resolution pipeline that produces "the list of files a policy governs" is shared, unchanged in
  outward shape, by both `verify` (existing) and a future `restore` executor — this design changes what
  that pipeline can express and how efficiently it runs, not its contract with whichever executor calls
  it.

## Non-Goals

- **No `rwfs restore` (actual write-to-disk).** Still unbuilt, per every prior restore design's
  Non-Goals. This design only changes how the *file list* is built; the two consumers of that list are
  `rwfs verify` (updated to use it) and, later, a restore executor.
- **No UI for setting a rule's timeframe.** `RestoreRule.not_before`/`not_after` need to reach `rwfs`
  somehow, so the wire-shape plumbing through `policy-server`/`api-server`/`web` is covered (mirroring
  `dest_path`'s precedent exactly), but the actual review-screen control (date pickers, click-to-edit UI)
  is left for a follow-on design — the same way `dest_path`'s wire plumbing (2026-08-13) preceded any
  executor consuming it.
- **No change to `bwfs`'s backup ingestion protocol** beyond populating the new columns this design adds
  — chunking, dedup, and `docs/protocols/backup.md` are untouched.
- **No fleet-wide/`catalog`-routed resolution.** A restore always targets exactly one `destinations[0]`
  store (`agent`'s existing per-policy dispatch); resolution stays local to that one `bwfs` node.
  `catalog` is a control-plane, asynchronously-replicated mirror — routing through it would trade
  correctness (it can lag behind `bwfs`) for no benefit, since the RPC still has to hit `bwfs` directly for
  `file_uuid`s regardless.

## Architecture

### 1. `RestoreRule` gains `not_before` / `not_after`

Same shape and placement as `dest_path` (2026-08-13 design): unix seconds, `0` = unbounded on that side,
meaningful only on an included rule.

```proto
message RestoreRule {
  string host       = 1;
  string path       = 2;
  bool   include     = 3;
  string dest_path   = 4;
  int64  not_before  = 5; // unix seconds; 0 = unbounded. Only meaningful when include is true.
  int64  not_after   = 6; // unix seconds; 0 = unbounded.
}
```

`RestorePolicy.Validate()` gains: `not_before != 0 && not_after != 0 && not_after < not_before` → error.
The same "only valid on an included rule" check `dest_path` already has, extended to these two fields.
Threaded through the same chain `dest_path` was: `policy-server`'s `RestoreRule`/`ToProto`/`Clone` →
`api-server`'s `ruleDTO` (decode on `POST /api/v1/restore`, round-trip on `toPolicyDTO`) → `agent`'s
duplicated `RestoreRule` struct (`restore.go`) and the `rulesStdinPayload` it marshals → `rwfs`'s own
duplicated `RestoreRule` (`rules.go`).

### 2. `bwfs` storage: decomposed, indexed columns

`file_data_records` gains real columns instead of relying on parsing the opaque `file_id` string at query
time:

```go
type FileDataRecord struct {
	UUID       string `gorm:"primaryKey"`
	FileID     string `gorm:"index"` // retained for uniqueness/display; no longer parsed on the query path
	SourceHost string `gorm:"index:idx_path_host,priority:2"`
	Path       string `gorm:"index:idx_path_host,priority:1"`
	Mtime      int64
	Size       int64
	Checksum   []byte
	ChunkCount int
	CreatedAt  time.Time
}
```

`(path, source_host)` as a composite index, `path`-leading: a host-agnostic folder rule's recursive
subtree query (`path >= '/etc/' AND path < '/etc0'` — the standard prefix-range trick, since `0` is the
next ASCII byte after `/`) becomes a real B-tree range scan; a host-specific rule adds an equality filter
on the second column for free. `brfs`'s write path (`CreateFileData`) populates `SourceHost`/`Path`/
`Mtime` at ingest time — it already has all three, they're just not persisted as columns today.

`file_version_records` gains a composite index on `(object_id, created_at)` — the version-window join
below needs a range scan per candidate `file_id`, and today's only index is the `(job_id, object_id)`
uniqueness constraint, which doesn't serve this access pattern.

### 3. New RPC: `ListService.ResolveRestoreFiles`

Only `rwfs verify --rules-stdin` (and its future `restore` sibling) calls it. `ListFiles`/`ListRequest`
are untouched — `bwfs list`/`rwfs list`'s flat scalar-filter, "dump everything matching" contract stays
exactly as-is.

```proto
service ListService {
  rpc ListFiles(ListRequest) returns (ListResponse);
  rpc ResolveRestoreFiles(ResolveRestoreFilesRequest) returns (stream ResolveRestoreFilesResponse);
}

message RestoreFileFilter {
  string host           = 1; // "" = host-agnostic (folder rule)
  string path           = 2;
  bool   path_is_prefix = 3; // true = folder rule (recursive subtree), false = exact file rule
  int64  not_before     = 4; // 0 = unbounded
  int64  not_after      = 5; // 0 = unbounded
}

message ResolveRestoreFilesRequest {
  repeated RestoreFileFilter filters = 1;
}

message ResolveRestoreFilesResponse {
  FileRow row          = 1;
  int32   filter_index = 2; // index into the request's filters -- which filter resolved this row
}
```

One response message per resolved row — genuinely server-streaming, not unary-with-a-repeated-field, so
`bwfs` never has to materialize a whole folder-rule match in memory before sending anything.

**Only included rules become filters.** Excluded rules never need file content — they exist purely to
carve exceptions out of a broader include for `rwfs`'s ancestor-precedence walk, which needs no store
data at all. `rwfs` still sends its complete original rule list (includes and excludes) through the
unchanged `rules.go` precedence logic; only the *candidate-row lookup* moves server-side.

**Per-filter resolution in `bwfs`:** for each filter, find candidate `file_id`s matching
`(source_host, path)` (exact match, or prefix range scan when `path_is_prefix`), join to
`file_version_records` on `object_id = file_id`, keep only versions with `created_at` inside
`[not_before, not_after]` (open bound when 0), and for each distinct `(source_host, path)` keep the
`file_id` whose latest in-window version `created_at` is greatest. A `(source_host, path)` with zero
versions in the window contributes no row — never a stale fallback from outside the requested timeframe.

### 4. `rwfs`: streaming consumption + precedence tie-break

`runVerify` replaces its `ListFiles` call (in `--rules-stdin` mode) with `ResolveRestoreFiles`, built
from the parsed rules' included entries (`path_is_prefix = rule.Host == ""`, matching the existing
folder/file convention `notFoundRule`'s doc comment already establishes).

Consumption becomes a pipeline, not buffer-then-process. For each `ResolveRestoreFilesResponse` as it
streams in:

1. Resolve which rule *should* govern this row's path. `resolveRestoreFile` today returns only a bool
   (included/excluded); it needs extending to also return the winning rule's index into the rule list
   (or a sentinel for "no rule matched"), since the tie-break below needs that identity, not just the
   include/exclude outcome. The longest-ancestor-wins walk itself is unchanged, over the small,
   human-authored rule list — cost bounded by rule count, never by store size.
2. If the winning rule is excluded (or nothing matched), drop the row — today's existing behavior,
   unchanged. If the winning rule is included, compare *its* index against the response's `filter_index`
   (recall filters are built only from included rules, in rule-list order, so the indices correspond
   1:1); a mismatch means this row came from a broader rule a more specific included rule shadows, and its
   window-resolved version isn't the one that should be used — drop it. This is the one genuinely new
   piece of logic: two different include rules can each resolve a row for the same path with *different*
   versions when their timeframes differ, and only the most specific rule's version should win — the same
   "most specific rule wins" guarantee `dest_path` already relies on now also covers *which version* gets
   restored.
3. Otherwise, hand the row straight to the existing worker pool (`workCh`) — no full-slice buffering
   first. `workCh`'s current `len(rows)`-sized buffer and the full upfront `rows` slice go away; a small
   fixed-size buffered channel (or unbuffered, gated by the streaming pace) replaces them.

**Not-found semantics**, extending today's `notFoundRule` scan: a file-level filter
(`path_is_prefix = false`) that resolves to zero rows is a failure, distinguished from today's generic
"not found on this store" with a more specific reason — `"no version in timeframe"` — since the file may
well exist, just not within the requested window, and that's a diagnosably different problem than a
typo'd path. A folder-level filter resolving to zero rows remains a legitimate, non-error empty outcome,
unchanged.

## Data Flow

```
web: cart entry gets a timeframe (follow-on UI design, mirrors dest_path's click-to-edit)
  -> restoreCart rule gains {notBefore, notAfter}
  -> restoreSubmission's toWireRule includes not_before/not_after when set
  -> POST /api/v1/restore { rules: [{host, path, include, dest_path?, not_before?, not_after?}] }

api-server: handleCreateRestore decodes ruleDTO -> CreatePolicyRequest
  -> policy-server: RestorePolicy.Validate() (not_after >= not_before; only on included rules)
       -> written to policies/restore/*.json

agent: restoreTasks marshals p.Rules (now carrying not_before/not_after) into rulesStdinPayload
  -> execs `rwfs verify <destinations[0]> --rules-stdin --job-id ...`, payload piped to stdin

rwfs verify --rules-stdin:
  parse rules -> build RestoreFileFilter list from included rules
  -> ResolveRestoreFiles(filters) [streaming]
  -> bwfs: per filter, decomposed-column + version-window query, streams winning rows tagged with filter_index
  -> rwfs: per arriving row, precedence tie-break (drop if shadowed), dispatch to worker pool
  -> RestoreFile(file_uuid) per selected row [unchanged from today] -> hash-verify, discard (or, later: write)
```

## Error Handling

- `not_before`/`not_after` set on an excluded rule → `RestorePolicy.Validate()` rejects, same
  error-plumbing path `dest_path` uses.
- `not_after < not_before` (both non-zero) → rejected at the same validation point.
- A file-level rule with no version in its window → verification failure, reason
  `"no version in timeframe"`.
- A folder-level rule matching nothing in its window → legitimate empty result, no warning (unchanged
  from today's empty-folder-match behavior).
- `bwfs` stream error mid-`ResolveRestoreFiles` → `rwfs` fails the whole run (consistent with today's
  `list files: %w` unrecoverable-error treatment; no partial-result retry, matching `RestoreFile`'s own
  "client retries the whole call" stance in `docs/protocols/restore.md`).

## Performance Notes

- Resolution cost is now proportional to *matched* rows, not total store size — the fix specifically
  targets host-agnostic folder rules, which had no bound at all today.
- This does not make restoring/verifying a folder rule matching hundreds of millions of files fast in
  absolute terms — that's inherently bounded by chunk transfer I/O, not query planning. The goal is
  eliminating wasted full scans, not the underlying bulk-transfer cost.
- New indexes add write-side cost to backup ingestion (`EnsureFileVersion` maintains one more index per
  insert, on a table that already grows once per file per job). Not measured in this design; worth a
  follow-up benchmark before rollout on a store expected to reach the billions.
- `bwfs`'s query implementation must use a real streaming DB cursor (e.g. `Rows()` + iterate +
  `stream.Send()` per row), not `Scan(&results)` into a full slice — the existing `queryFileRows` pattern
  that `ResolveRestoreFiles` must not inherit.

## Testing

- `storage/filesystem`: new columns populated correctly by `CreateFileData`; a query matching a
  host-agnostic path prefix returns correct rows via the new index; version-window join picks the correct
  `file_id` among multiple mtimes when their in-window latest versions differ; zero-versions-in-window
  returns no row.
- `bwfs`: `ResolveRestoreFiles` integration test — streaming response shape, `filter_index` correctly
  attributes rows to their originating filter, a large-result-set test asserts memory doesn't scale with
  match count (e.g. via a row-count ceiling enforced through a small in-test channel/buffer).
- `rwfs`: precedence tie-break — two overlapping include rules with different timeframes on the same
  path; only the more specific rule's resolved version survives. Not-found-in-timeframe produces the
  distinguished reason string. Streaming consumption dispatches to the worker pool without full buffering
  (test via a fake streaming client yielding rows lazily).
- `policy-server`: `Validate()` rejects `not_before`/`not_after` on an excluded rule and
  `not_after < not_before`; `ToProto`/`Clone` carry the new fields correctly (extends the existing
  `dest_path` tests' pattern).
- `api-server`: `handleCreateRestore`/`toPolicyDTO` round-trip `not_before`/`not_after` (extends the
  existing `dest_path` tests' pattern).

## Documentation Impact

Per `.claude/CLAUDE.md`'s protocol-change and feature-change rules (this round touches `.proto` files and
regenerated `*.pb.go`):

- **`docs/protocols/list.md`** — new `ResolveRestoreFiles` RPC section: request/response shape, streaming
  rationale, filter semantics, the `filter_index` tie-break contract. Existing `ListFiles` section
  unchanged.
- **`docs/protocols/restore.md`** — CLI→RPC mapping section updated: `rwfs verify --rules-stdin` now calls
  `ResolveRestoreFiles` instead of unscoped `ListFiles`; remove the "unbounded and unpaginated" caveat
  this design fixes.
- **`docs/protocols/policy-server.md`** — `RestoreRule` proto block gains `not_before`/`not_after`,
  alongside the existing `dest_path` note.
- **`docs/components/rwfs.md`** — update the `--rules-stdin` section (currently documents the
  unbounded-`ListFiles` limitation this design removes).
- **`docs/components/policy-server.md`**, **`docs/components/api-server.md`** — short notes alongside the
  existing `dest_path` mentions.
- **`docs/ARCHITECTURE.md`** — no topology change (same components, same node), but if the restore
  data-flow description mentions `ListFiles` today, it should note the new RPC.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

## Relationship to Prior Work

Builds on `2026-08-10-restore-policy-verification-design.md`'s `RestoreRule{host,path,include}` and
`resolveRestoreFile` precedence (unchanged), and follows `2026-08-13-restore-destination-rename-design.md`'s
exact pattern for adding a per-rule field (`dest_path` then, `not_before`/`not_after` now) end-to-end
through the same chain of components. Precedes and is required by any future `rwfs restore` design (still
unbuilt) — that design can assume a resolution pipeline that already picks exactly one correct version per
selected file, at a cost proportional to what the restore actually touches.
