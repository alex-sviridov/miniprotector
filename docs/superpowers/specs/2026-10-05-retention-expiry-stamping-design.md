# Design: Retention Expiry Stamping (Retention, Part 2)

> Part 2 of a two-part retention feature. This part stamps every new `file_version` with an
> `expire_at`. **Part 1** (separate spec, later) adds the `retention` policy type, its management API
> and the reorderable web UI. **Cleanup** (acting on `expire_at`) is out of scope for both.

## Problem

`bwfs` never deletes a successful `file_version`, so versions accumulate forever. The chunk-reclaim
half already exists (`Store.Vacuum` cascades from unreferenced `file_version_records` down to chunk
files), but nothing records *how long* a version must be kept. Before any cleanup can exist, every
version needs an expiry decided at backup time by operator-controlled rules.

## Decisions

- **Rules live in a separate `retention` policy type** (Part 1), not on the backup policy. They match
  clients by host/attributes exactly as backup policies do, plus backup type (only `filesystem`
  today) and a path rule. Ordered by an explicit integer `priority` (lower first); **first match
  wins**.
- **Path rule** = `prefix` (required, segment-boundary match) + optional `include` globs (same glob
  syntax as backup `object_filters`: `*`, `?`, `[...]`, no `**`).
- **A built-in default is always the last row**: `prefix=/`, no include, keep =
  `RetentionDefaultDays` (agent config, default 7). Nothing is ever unmatched.
- **Retention is resolved per file.** `agent` resolves a *rule matrix* once at job start; `brfs`
  evaluates it per file.
- **`expire_at` is an absolute unix timestamp computed by `brfs`** (`now + keep`) and stored as
  received. **Assumption: node clocks are synced** (stated, not enforced).
- `keep_seconds = 0` in a rule means *never expire* (`expire_at = 0`). `0` therefore also means
  "no `expire_at` sent" (hand-run `brfs`); both are stored as NULL and both mean never auto-deleted.

## 1. Matrix resolution (`agent`)

A matrix row is `{prefix, include[], keep_seconds}`, in priority order, ending with the default row.

At job start, for the task `(host, type=filesystem, root path)`, `agent`:

1. Takes cached `retention` policies in `priority` order (none exist in Part 2, so the matrix is the
   default row alone; Part 1 supplies real rows with no change to this flow).
2. Drops rows whose `prefix` cannot overlap the job root (neither is a segment-boundary prefix of the
   other), and rewrites surviving prefixes relative to the root, so `brfs` matches the same
   relative paths it walks.
3. Truncates after the first row that has no `include` and whose prefix covers the whole root —
   nothing after it can ever win. This keeps the matrix short.

`agent`:

- logs the matrix once under the job's `job_id` as structured event `retention_matrix` (the rows as
  attributes);
- writes the identical JSON to `<state-dir>/retention/<task>.json` (one file per task, overwritten on each run, so the directory stays bounded);
- passes `--retention-file <path>` to `brfs`.

Because `agent` is the only place that resolves rules, the logged matrix is exactly what `brfs`
applies.

## 2. Per-file stamping (`brfs`)

- New flag `--retention-file <path>`; loaded once at start. Without it, no `expire_at` is sent (a
  hand-run `brfs` has no policy; "unset" is not "expired").
- Compiled once per job: each row becomes a segment-boundary prefix check plus precompiled glob
  matchers, immutable and shared by all `--streams` workers (no locking).
- Per file: first-match scan over the (short) matrix, then `expire_at = now + keep` (or 0).
- Sent in a new field on the per-file `FileInfo` message — it must travel there, not in stream
  metadata, because the file-level `SEND_FILE`/`SKIP_FILE` decision happens right after it and a
  skipped (unchanged) file still records a version for the job.

```proto
message FileInfo {
  string file_id = 1;
  bytes attributes = 2;
  int64 expire_at = 3; // unix seconds; 0 = unset/never. New in retention part 2.
}
```

## 3. Storage (`bwfs`) and replication

- `file_version_records` gains a nullable `expire_at` (unix seconds) via the existing schema
  migration. Existing rows stay NULL, meaning "no retention recorded"; no code treats NULL as
  expired.
- `EnsureFileVersion` takes `expire_at` and stores it at both handler call sites (new file and
  skipped file). First observation per `(job_id, object_id)` still wins.
- `catalogsync` carries it in `FileVersionEntry` (`int64 expire_at = 7`); `catalog` persists it.

## Error handling

- Unreadable or malformed `--retention-file`: `brfs` exits non-zero before opening any stream (a job
  must not silently run unprotected). `agent` treats that as an ordinary task failure with backoff.
- A rule with an invalid glob is rejected when the matrix is compiled (`brfs` start) and when
  `agent` builds it, with the offending row named in the error.

## Testing

- `agent`: matrix builder unit tests (overlap pruning, relative rewrite, truncation, default row,
  zero retention policies); the `retention_matrix` log event; `--retention-file` appears on the
  `brfs` command line.
- `brfs`: matcher tests (segment boundary: `/data/tmp` must not match `/data/tmpfile`; include
  globs; first-match order; keep 0); malformed-file failure; no-flag sends 0.
- `bwfs`: `EnsureFileVersion` stores `expire_at` for both new and skipped files; migration leaves old
  rows NULL.
- `catalogsync`/`catalog`: round-trip of `expire_at`.
- Benchmark: per-file match cost with a 10-row matrix.

## Documentation (per CLAUDE.md)

`docs/protocols/backup.md` and `catalog-sync.md` (proto changes); `docs/components/` for `agent`,
`brfs`, `bwfs`, `catalog`, `catalogsync`; `docs/ARCHITECTURE.md` only if the data flow description
changes; README unchanged; `CHANGELOG.md` entry before merge.

## Out of scope

The `retention` policy type, its API and UI (Part 1); any deletion driven by `expire_at`, including
how cleanup is configured and scheduled; propagating deletions to the catalog; enforcing clock sync.
