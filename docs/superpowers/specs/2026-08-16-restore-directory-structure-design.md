# Restore: Directory Structure Phase — Design

> **Builds on:** `docs/superpowers/specs/2026-08-16-restore-execute-log-only-design.md` (the
> log-only `rwfs restore` subcommand, the `ResolveRestoreFiles` consumption pipeline it shares with
> `verify --rules-stdin`, and `restoreDestPath`'s `dest_path` rename logic — all reused unchanged
> here). That design explicitly scoped out any filesystem write; this design is the first to
> actually mutate the destination host's filesystem, for directories only.

## Problem

A real restore needs to recreate a selection's directory structure before any file content can be
written into it. Today nothing in the system can do that: `bwfs` never surfaces a directory as a
queryable, restorable object at all. `brfs` does send a `FileInfo` for every directory it walks
(confirmed live: backing up a 4-file, nested tree produces `filesCount=8` — 4 files + 4 directory
entries), but `bwfs`'s handler treats anything whose type isn't `'f'` as non-transferable
(`src/cmd/bwfs/handler.go:79-88`) — no `FileDataRecord` is ever created for it, only a
`file_version_records` row via `EnsureFileVersion`, capturing its metadata (permissions, owner,
mtime) but nothing queryable by path. Every read surface (`bwfs list`, `ResolveRestoreFiles`)
starts from `file_data_records`, so a directory is invisible to all of them — `ResolveRestoreFiles`
even hardcodes every row it emits as `Type: "f"`. `restoreResolver.Feed`'s existing
`row.GetType() != "f"` filter is dead code today: bwfs has never had a code path that could produce
anything else.

This design makes directories real, queryable, restorable objects, and gives `rwfs restore` a first
phase that actually recreates them on disk — deliberately split from actually writing file content,
which remains unbuilt future work.

## Goals

- A folder rule's resolved selection includes the real, backed-up directories under it (including
  directories with zero files in them), not just an inferred set of file-path ancestors — so an
  empty directory that was actually backed up is actually restored, and the door stays open for a
  later round to restore real captured permissions/ownership from what was actually captured.
- `rwfs restore` gains a real, disk-mutating phase 1: for every resolved directory, check whether it
  exists, create it if not, in parent-before-child order.
- One small, dedicated function does the actual per-directory work (check-exists, create,
  permissions-stub), receiving one directory object — not inlined into the resolution loop.
- Clear, exactly-specified logging: a start line, a detailed error on the first failure (which
  aborts the whole restore job immediately), or a created/reused summary line on full success.
- A pre-existing directory is always safely reused. A pre-existing *non*-directory at the target
  path is always a hard error.

## Non-Goals (this round)

- **Phase 2 — writing file content — is still not built.** File rows continue to be resolved and
  logged exactly as the previous (log-only) design left them; nothing about file handling changes
  in this round.
- **Permission/ownership restoration is a stub.** The per-directory function has the step, but it's
  a no-op — the captured metadata blob isn't even threaded onto the wire this round (no new `bytes`
  field is added to `FileRow`); that's added when the step is actually implemented, the same
  incremental-wiring pattern `dest_path` and `not_before`/`not_after` already followed.
- **Symlinks and every other non-regular-file, non-directory type** (`l`, `p`, `s`, `b`, `c`) are
  still swallowed into `EnsureFileVersion`-only capture with no restore path — this design only
  makes `'d'` real.
- **No `--dry-run` flag or other way to keep previewing without creating directories.** Not
  requested; `mode: "restore"` already means "actually do this" as of the previous design — this
  round is the first consumer for which that distinction has a real, physical effect.
- **No fleet-wide or `catalog`-routed directory resolution** — same acceptance the 2026-08-15
  design already made for files: a restore always targets exactly one `destinations[0]` store,
  resolution stays local to that node.
- **No destination-collision detection between two different rules' directories.** If two distinct
  source directories (different rules, e.g. two folder rules with different `dest_path`s, or a
  host-specific and a host-agnostic rule) resolve to the same `DestPath`, the dedup step in phase 1
  silently collapses them into one create-or-reuse — mechanically harmless (the second one always
  finds "already exists, reuse"), but the design does not warn that two logically distinct source
  trees are being merged into one destination. Same deferral the 2026-08-13 rename design already
  made for `dest_path` collisions generally ("becomes relevant once something actually writes
  files") — this round writes directories, not file content, so the blast radius of a silent
  collision is still just an empty folder, not overwritten data.

## Architecture

### 1. `bwfs` storage: `FileVersionRecord` gains real `SourceHost`/`Path`/`Type` columns

Directories have no `file_data_records` row to decompose into indexed columns (the fix the
2025-08-15 design applied to files) — there is nothing there for a directory. The fix has to live on
`file_version_records` itself, the only table a directory is ever recorded in:

```go
type FileVersionRecord struct {
	Seq        int64  `gorm:"primaryKey;autoIncrement"`
	ObjectID   string `gorm:"uniqueIndex:idx_job_object;index:idx_file_version_object_created,priority:1"`
	JobID      string `gorm:"uniqueIndex:idx_job_object"`
	SourceHost string `gorm:"index:idx_file_version_path_host,priority:2"`
	Path       string `gorm:"index:idx_file_version_path_host,priority:1"`
	Type       string // single char, from FileInfo.GetType() -- 'f', 'd', 'l', ...
	Metadata   []byte
	Ctime      int64
	CreatedAt  time.Time `gorm:"index:idx_file_version_object_created,priority:2"`
}
```

`EnsureFileVersion`'s signature gains three parameters:

```go
// storage/interface.go
EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata []byte, ctime int64) error
```

Both call sites in `src/cmd/bwfs/handler.go` (line ~101, the non-transferable/skip-path branch, and
line ~236, the post-finalize branch for regular files) already have `h.currentFile` — a
`*filesystem.FileInfo` with public `Source()`, `Path()`, `GetType()` accessors — in scope, so this
is pure plumbing: `h.store.EnsureFileVersion(h.jobID, h.currentFile.ID(), h.currentFile.Source(),
h.currentFile.Path(), string(h.currentFile.GetType()), h.currentFile.MetadataBlob(),
h.currentFile.Ctime())`. No re-parsing of the opaque ID string anywhere, unlike `list.go`'s older
`parseFileID` approach.

Existing rows (written before this change) have empty `SourceHost`/`Path`/`Type` — acceptable, since
this store is demo/dev-only with no migration framework yet (same acceptance every prior schema
change in this line has made); a real deployment concern is out of scope here, same as always.

### 2. `bwfs`: `ResolveRestoreFiles` also yields directory rows

`FileRow.type` (proto, already exists) stops being hardcoded to `"f"`. `resolveRestoreFilter`
(`src/cmd/bwfs/resolverestorefiles.go`) gains a sibling function, called only when
`filter.GetPathIsPrefix()` is true (a directory can only ever be selected by a folder rule — a
host-specific, exact-path rule targets a single file by definition, matching how the restore cart
already models selections):

```go
// resolveRestoreDirectoryFilter mirrors resolveRestoreFilter's shape exactly,
// but queries file_version_records directly (WHERE type = 'd') since a
// directory never has a file_data_records row to join through. Reuses
// restoreChildRanges verbatim -- same separator-aware subtree matching,
// same latest-within-window-wins dedup per distinct (source_host, path).
func resolveRestoreDirectoryFilter(store *wfs.Store, filter *pb.RestoreFileFilter, yield func(source, path string) bool) error {
	query := store.RawDB().
		Table("file_version_records").
		Select("source_host, path, MAX(created_at) AS best_version_at").
		Where("type = ?", "d").
		Group("source_host, path").
		Order("source_host ASC, path ASC")

	if filter.GetHost() != "" {
		query = query.Where("source_host = ?", filter.GetHost())
	}
	r := restoreChildRanges(filter.GetPath())
	query = query.Where("path = ? OR (path >= ? AND path < ?) OR (path >= ? AND path < ?)",
		filter.GetPath(), r.Unix.Lower, r.Unix.Upper, r.Windows.Lower, r.Windows.Upper)
	if filter.GetNotBefore() != 0 {
		query = query.Where("created_at >= ?", time.Unix(filter.GetNotBefore(), 0))
	}
	if filter.GetNotAfter() != 0 {
		query = query.Where("created_at <= ?", time.Unix(filter.GetNotAfter(), 0))
	}
	// ... Rows()/Scan() loop, identical shape to resolveRestoreFilter.
}
```

Called from `ResolveRestoreFiles` right after `resolveRestoreFilter` for each `path_is_prefix`
filter, streaming `&pb.FileRow{Source: source, Type: "d", Path: path}` — `FileUuid` left empty (a
directory has no content, nothing ever calls `RestoreFile` against it), `Size`/`Chunks` left zero.

**Known scale caveat, stated plainly:** the host-agnostic case (`filter.GetHost() == ""`) can't use
`idx_file_version_path_host`'s leading column, so it scans across all hosts for a path match — the
same tradeoff every host-agnostic query in this system already accepts, and directory-row volume is
inherently far smaller than file-row volume (one row per directory per backup job, not per file), so
this is a reasonable trade for this round. Worth a follow-up benchmark before a store expected to
reach very large directory counts, exactly the same caveat the 2026-08-15 design already recorded
for its own indexes.

### 3. `rwfs`: `restoreResolver.Feed`'s type gate

`src/cmd/rwfs/resolve.go`'s existing gate (`row.GetType() != "f" || row.GetSize() <= 0` → drop)
becomes: keep when (`Type == "f" && Size > 0`) **or** (`Type == "d"`). Everything else about `Feed`
(precedence tie-break, `dest_path` rule attribution via the returned `ruleIndex`, `filterFoundAny`
tracking for `NotFound`) is untouched and applies uniformly to directory rows — the engine was
already type-agnostic; only the final dispatch gate needed to widen.

### 4. `rwfs restore`: two phases

`runRestoreWithConn` (`src/cmd/rwfs/restore.go`) splits its per-row handling by `row.GetType()` as
rows stream in:

- `"f"` rows: unchanged from the log-only design — logged as a `"resolved"` line
  (`source`/`path`/`dest_path`), counted toward `total`/`resolved`.
- `"d"` rows: no per-row log line (phase 1's own logging covers them collectively — see below); each
  is turned into a `restoreDirectory{DestPath: restoreDestPath(rules[ruleIndex], row.GetPath())}`
  (the exact same rename helper files already use) and appended to a slice.

After the stream ends, `resolver.NotFound()` is checked exactly as today — **any not-found failure
aborts before phase 1 ever starts**, logged the same way, same `"%d file(s) failed resolution"`
error. A restore known to be incomplete shouldn't bother creating directories for it.

If there were no not-found failures, phase 1 runs:

```go
// restoreDirectory is one directory phase 1 must ensure exists at its
// (dest_path-renamed) destination.
type restoreDirectory struct {
	DestPath string
}

// createRestoreDirectory checks whether dir.DestPath exists, creates it if
// not (parent must already exist -- callers create in parent-before-child
// order), and would apply captured permissions/ownership once that
// metadata is threaded through from bwfs (still a stub -- see this
// design's Non-Goals).
func createRestoreDirectory(dir restoreDirectory) (created bool, err error) {
	info, statErr := os.Stat(dir.DestPath)
	switch {
	case statErr == nil && info.IsDir():
		return false, nil
	case statErr == nil:
		return false, fmt.Errorf("path exists and is not a directory: %s", dir.DestPath)
	case !os.IsNotExist(statErr):
		return false, statErr
	}
	if err := os.Mkdir(dir.DestPath, 0o755); err != nil {
		return false, err
	}
	// TODO: apply dir's captured permissions/ownership once FileRow carries
	// the metadata blob -- deferred until that step is actually built.
	return true, nil
}
```

`runRestoreWithConn`'s phase 1 driver: dedupe the collected `restoreDirectory` slice by `DestPath`
(a directory can be reached via more than one rule/filter, same as files can), sort parent-before-
child (reusing `ancestorsOrSelfRestorePath`'s existing separator-aware decomposition — sort key is
chain length, i.e. depth, ascending), then:

```go
logger.Info("creating restored directory structure")
created, reused := 0, 0
for _, dir := range sortedDirs {
	wasCreated, err := createRestoreDirectory(dir)
	if err != nil {
		logger.Error("failed to create restored directory", "path", dir.DestPath, "reason", err)
		return fmt.Errorf("create restored directory %s: %w", dir.DestPath, err)
	}
	if wasCreated {
		created++
	} else {
		reused++
	}
}
logger.Info("restored directory structure created", "created", created, "reused", reused)
```

**Stops at the first failure** (per your answer) — no further directories are attempted, no summary
line is logged, the job fails immediately with a detailed reason attached to the error log line.
The summary line is reached only on full success. `--overwrite` has no effect on any of this — an
existing directory is always reused regardless of the flag; a non-directory occupying the target
path is always the hard error above, never silently replaced.

## Data Flow

```
brfs: walks /tmp/nested, sends FileInfo for every file AND every directory
  -> bwfs handler: type != 'f' -> EnsureFileVersion(jobID, id, host, path, type, metadata, ctime)
       -> file_version_records row, now with real (source_host, path, type) columns

rwfs restore --rules-stdin --overwrite:
  ResolveRestoreFiles(filters) [streaming]
    -> bwfs: resolveRestoreFilter (files, unchanged) + resolveRestoreDirectoryFilter (new, 'd' rows)
    -> rwfs: resolver.Feed per row -- 'f'+size>0 or 'd' both now dispatch
  file rows -> logged "resolved" (unchanged), still nothing written
  'd' rows -> restoreDestPath applied -> collected into []restoreDirectory
  stream ends -> resolver.NotFound() -- any failure aborts here, phase 1 never runs
  phase 1: dedupe + sort parent-first
    -> logger.Info("creating restored directory structure")
    -> createRestoreDirectory per dir: os.Stat -> reuse | os.Mkdir -> create | hard error -> abort
    -> logger.Info("restored directory structure created", created=N, reused=M)
  [job succeeds -- file content restore, phase 2, still unbuilt]
```

## Error Handling

- A file-level rule matching nothing, or a folder-level rule's timeframe excluding a version — same
  behavior as today, and it now aborts the job *before* phase 1 runs at all.
- `os.Stat` finds a non-directory at a directory's destination path — hard error, aborts phase 1
  immediately, detailed in the log line (path + "path exists and is not a directory").
- `os.Mkdir` fails for any other reason (permissions, disk full, etc.) — same immediate abort, error
  wrapped with the path.
- Every other `rwfs restore` error path (stream error, connection error) is unchanged from the
  log-only design.

## Testing

- `storage/filesystem`: `EnsureFileVersion` persists the new `SourceHost`/`Path`/`Type` columns
  correctly; a query filtering `type = 'd'` finds only directory rows.
- `bwfs`: `resolveRestoreDirectoryFilter` — host-specific and host-agnostic folder filters both find
  the right directory rows via `restoreChildRanges`; a host-specific (non-prefix) filter never
  returns a directory row; timeframe windowing and latest-wins dedup match the existing file-query
  tests' shape. `ResolveRestoreFiles` integration: a folder selection streams both file and
  directory rows, directory rows carry `Type: "d"` and empty `FileUuid`.
- `rwfs`: `restoreResolver.Feed` — a `Type: "d"` row now dispatches regardless of `Size`; a `Type:
  "d"` row still correctly marks `filterFoundAny` for `NotFound`. `createRestoreDirectory` —
  existing directory is reused (not recreated); missing directory is created; a non-directory at the
  path is a hard error; parent-before-child ordering is verified end to end against a fake streaming
  `ResolveRestoreFiles` response (a deep nested selection, asserting `os.Mkdir` never fails on a
  missing parent). Not-found failures abort before any directory is touched (assert zero
  `os.Mkdir`/`os.Stat` calls in that case, or equivalent). First-directory-failure aborts before the
  next directory is attempted (ordering-sensitive: use two directories where the first is
  engineered to fail) and never logs the summary line.

## Documentation Impact

Per `.claude/CLAUDE.md`'s protocol-change and feature-change rules:

- **`docs/protocols/list.md`** — `ResolveRestoreFiles` section: `FileRow.type` can now genuinely be
  `"d"`, not just `"f"`; document the directory-row query's semantics and the host-agnostic scale
  caveat.
- **`docs/components/rwfs.md`** — `## restore` section updated: phase 1 (directory creation) is now
  real, disk-mutating behavior; phase 2 (file content) remains log-only/unbuilt. Update the exit-code
  and logging description with the two new log lines and the abort-on-first-failure contract.
- **`docs/components/policy-server.md`** / **`docs/protocols/policy-server.md`** — no change (no
  proto/schema change to the restore policy itself, this round is entirely `bwfs`+`rwfs`).
- **`docs/ARCHITECTURE.md`** — update the `rwfs`/`agent` restore-execution description: directory
  structure creation is now real; file content restore remains the next round.
- **`CHANGELOG.md`** — entry before merge, per the standing rule, explicitly flagging that this is
  the first round where `rwfs restore` actually writes to the destination filesystem.

## Relationship to Prior Work

The 2026-08-16 log-only design deliberately built every piece of restore *resolution* (rule
precedence, timeframe scoping, `dest_path` computation, the streaming `ResolveRestoreFiles`
pipeline) without ever writing anything, explicitly to prove the resolution/dispatch path before
committing to any actual filesystem mutation. This design is the first to spend that groundwork on
a real write — deliberately the smallest possible one (create-if-missing directories, no content, no
permissions yet) — so the write path itself, and its failure/logging contract, can be proven out
before the much larger next round: actually streaming file content into place.
