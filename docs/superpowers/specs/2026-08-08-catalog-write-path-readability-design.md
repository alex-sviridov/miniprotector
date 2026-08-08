# Design: Catalog Write-Path Atomicity and Readability

## Problem

Spec 2 of the catalog/api-server reliability review (Spec 1, the storage connection foundation,
shipped separately). Four issues remain, all local to `storage/catalog` and `cmd/catalog`/
`cmd/api-server`'s catalog handlers:

1. **`SyncFileVersions`'s write path isn't atomic.** It calls `Store.EnsureEntries` then
   `Store.EnsureDirectories` as two independent writes. If the second fails after the first
   succeeds, entries exist with no directory-tree rows for them. `catalogsync` retries the whole
   batch on any RPC error (its cursor only advances on success), so this self-heals if the failure
   is transient — but a batch that fails permanently on the directory side wedges forever while its
   entries already look ingested, and there's a window where readers can observe entries with an
   incomplete directory tree even when nothing ever fails.
2. **Sync-time metadata decode failures are silent.** `decodeSourceHost`/`decodePathParts` each
   independently call `filesystem.DecodeFileInfo` on the same blob and swallow errors into `""`.
   Nothing distinguishes "this host doesn't back up to a named source" from "metadata is
   malformed" — a systemic decode bug would silently degrade filtering/browsing with zero
   operational signal, and the same blob is decoded twice for no reason.
3. **`storage/catalog/store.go`'s three `List*Facets` methods duplicate ~30 lines of aggregation
   logic each** (map-by-name, order-preserving slice, count, last-seen-max), differing only in how
   each row's `Name` is derived. A stale comment already claims `policyNameFromJobID` lives in a
   `facets.go` that doesn't exist.
4. **`cmd/api-server/catalog.go`'s 5 handlers repeat the same 8-line `received_after`/
   `received_before` parse-or-400 block** verbatim.

## Goals

- `SyncFileVersions`'s persisted state is always either "this batch's entries and their directory
  ancestors are both durable" or "neither is" — no partial-write window.
- A sync-time metadata decode failure is visible in logs with enough context (`job_id`,
  `object_id`) to find the offending entry, without failing the rest of the batch.
- One shared aggregation implementation backs all three facet methods.
- One shared date-range-parsing implementation backs all 5 api-server catalog handlers.

## Non-goals

- No behavior change to what any RPC or REST endpoint returns — this is write-path correctness and
  internal DRY-up, not a feature change.
- Not doing the SQL-side facet aggregation rewrite the existing code comments flag as a possible
  future optimization (`strftime()`-based `GROUP BY`) — explicitly deprioritized per the project's
  stated priority order (reliability, then clean code, then performance) and the code's own
  "revisit if measured hot path" framing. Nothing in this spec makes that harder to do later.
- Not touching `catalogsync`, `ListEntries`, `ListDirectoryChildren`, or anything already fixed in
  Spec 1 (connection pooling, context propagation).

## Design

### Write-path atomicity

`Store` gains one new method:

```go
// SyncBatch persists entries and their directory ancestors atomically: both
// commit, or neither does. Replaces SyncFileVersions's previous two
// independent EnsureEntries/EnsureDirectories calls, closing the window
// where a batch's entries could be durable with no corresponding directory
// tree.
func (s *Store) SyncBatch(ctx context.Context, entries []Entry, directories []DirectoryAncestor) error {
	return s.writeDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureEntries(tx, entries); err != nil {
			return fmt.Errorf("ensure entries: %w", err)
		}
		if err := ensureDirectories(tx, directories); err != nil {
			return fmt.Errorf("ensure directories: %w", err)
		}
		return nil
	})
}
```

`EnsureEntries`/`EnsureDirectories` keep their existing public signatures and behavior — every
existing test that calls them directly keeps passing unchanged — but their bodies move into
unexported functions taking a plain `*gorm.DB` (satisfied by both `s.writeDB.WithContext(ctx)` and
a `Transaction` callback's `tx`):

```go
func (s *Store) EnsureEntries(ctx context.Context, batch []Entry) error {
	return ensureEntries(s.writeDB.WithContext(ctx), batch)
}

func ensureEntries(db *gorm.DB, batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	// ... existing body, unchanged, operating on db instead of s.writeDB.WithContext(ctx)
}
```

(Same shape for `EnsureDirectories`/`ensureDirectories`.) `cmd/catalog/server.go`'s
`SyncFileVersions` replaces its two calls with one: `s.store.SyncBatch(ctx, batch, directories)`,
and its single error-log line stays informative because `SyncBatch`'s two wrapped errors name which
phase failed.

### Decode visibility and dedup

`decodeSourceHost` and `decodePathParts` (`cmd/catalog/server.go`) are removed. `SyncFileVersions`'s
per-entry loop decodes once:

```go
for i, e := range entries {
	var sourceHost, parentDir, shortName string
	if fi, err := filesystem.DecodeFileInfo(e.GetMetadata()); err != nil {
		s.logger.Error("SyncFileVersions: metadata decode failed, entry stored without derived fields",
			"job_id", e.GetJobId(), "object_id", e.GetObjectId(), "error", err)
	} else {
		sourceHost = fi.Source()
		parentDir, shortName = splitPath(fi.Path())
	}
	batch[i] = catalogstore.Entry{
		StoreNode:       storeNode,
		JobID:           e.GetJobId(),
		ObjectID:        e.GetObjectId(),
		Metadata:        e.GetMetadata(),
		Ctime:           e.GetCtime(),
		StoreSeq:        e.GetStoreSeq(),
		StoreCreatedAt:  time.Unix(e.GetCreatedAt(), 0).UTC(),
		SourceHost:      sourceHost,
		ParentDirectory: parentDir,
		ShortFilename:   shortName,
	}
	for _, a := range decodeDirectoryAncestors(parentDir) {
		directoriesByPath[a.Path] = a
	}
}
```

Behavior on decode failure is unchanged (the entry still persists, with `SourceHost`/
`ParentDirectory`/`ShortFilename` blank, same as today) — the only differences are one decode
instead of two, and an `Error`-level log line naming the offending `job_id`/`object_id`. `Error`,
not `Warn`, matches this codebase's existing convention for a tolerated-but-should-be-visible
per-item failure (e.g. `policy-server`'s `attachDestination`/`attachCheckins`, and this same
`SyncFileVersions`'s own batch-failure logs) — the batch still succeeds overall, only this one
entry's derived fields are degraded. One log line per bad entry, no batching or rate-limiting:
batches are capped at 500 and decode failures are expected to be rare (a real, if previously
invisible, bug — not routine), so per-entry logging is the simplest correct choice; revisit only if
this proves noisy in practice. `decodeDirectoryAncestors`
is unchanged — it already operates on the derived `parentDir` string. Doc comments elsewhere in
`storage/catalog` that name `decodeSourceHost`/`decodePathParts` (in `store.go`/`facets.go`) get
updated to describe this single decode step instead.

### Facet aggregation DRY (new `facets.go`)

New file `storage/catalog/facets.go` receives, moved verbatim from `store.go`: `FacetFilter`,
`Facet`, `FacetFilter.applyCommon`, `jobNamesWhere`, `policyNameFromJobID`, and the three
`List*Facets` methods — fixing the stale comment that already (incorrectly) claims
`policyNameFromJobID` lives there. A new shared type and function replace each method's duplicated
~30-line aggregation block:

```go
// facetRow is one (name, receivedAt) pair scanned from a facet query, before grouping.
type facetRow struct {
	Name       string
	ReceivedAt time.Time
}

// aggregateFacets groups rows by Name -- counting occurrences and tracking
// the max ReceivedAt per name, in first-seen order -- dropping rows with an
// empty Name. Shared by ListClientFacets/ListJobFacets/ListDirectoryFacets,
// which derive Name differently (raw source_host, policyNameFromJobID(job_id),
// raw parent_directory) but aggregate identically once Name is known.
func aggregateFacets(rows []facetRow) []Facet {
	byName := make(map[string]*Facet)
	var order []string
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		f, ok := byName[r.Name]
		if !ok {
			f = &Facet{Name: r.Name}
			byName[r.Name] = f
			order = append(order, r.Name)
		}
		f.Count++
		if r.ReceivedAt.After(f.LastSeen) {
			f.LastSeen = r.ReceivedAt
		}
	}
	facets := make([]Facet, 0, len(order))
	for _, name := range order {
		facets = append(facets, *byName[name])
	}
	return facets
}
```

Each `List*Facets` method keeps its own query construction (this is where the three genuinely
differ: `ListClientFacets` selects `source_host` and excludes empty at the SQL level,
`ListJobFacets` selects `job_id` and derives `Name` via `policyNameFromJobID`, `ListDirectoryFacets`
selects `parent_directory`), then maps its scanned rows to `[]facetRow` and calls
`aggregateFacets`:

```go
func (s *Store) ListJobFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	// ... unchanged query construction ...
	var rows []struct {
		JobID      string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: policyNameFromJobID(r.JobID), ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
}
```

(`ListClientFacets`/`ListDirectoryFacets` are the same shape, with `Name: r.SourceHost` /
`Name: r.ParentDirectory` respectively.) The "why Go, not SQL" rationale currently repeated on each
method moves to a single doc comment on `aggregateFacets`; each method's own comment keeps only its
dimension-specific filtering semantics.

`store.go` keeps `Entry`, `DirectoryAncestor`, `DirectoryChild`, `ListEntriesFilter`,
`EnsureEntries`/`EnsureDirectories`/`SyncBatch`, `ListEntries`, `ListDirectoryChildren`, `Count`,
`Close` — the write path plus the two non-facet read paths. Rough size after the split: `store.go`
~350 lines (down from 520), `facets.go` ~170 lines.

### api-server date-range parsing DRY

`cmd/api-server/catalog.go` gains one function:

```go
// parseDateRange parses received_after/received_before from q, writing a
// 400 response and returning ok=false if either is malformed. Callers must
// return immediately when ok is false -- the response is already written.
func parseDateRange(w http.ResponseWriter, q url.Values) (after, before int64, ok bool) {
	after, ok = parseUnixParam(q.Get("received_after"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_after must be a non-negative integer")
		return 0, 0, false
	}
	before, ok = parseUnixParam(q.Get("received_before"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_before must be a non-negative integer")
		return 0, 0, false
	}
	return after, before, true
}
```

All 5 handlers (`handleListCatalog`, `handleListCatalogClients`, `handleListCatalogJobs`,
`handleListCatalogDirectories`, `handleListCatalogDirectoryChildren`) replace their 8-line inline
block with:

```go
receivedAfter, receivedBefore, ok := parseDateRange(w, q)
if !ok {
	return
}
```

Adds `"net/url"` to the file's imports (for `url.Values`). No behavior change: same two error
messages, same `400` status, written once instead of five times.

## Testing

- `SyncBatch`: a new test proving atomicity. Both `EnsureEntries`/`EnsureDirectories` use
  `ON CONFLICT DO NOTHING`, so no ordinary bad input makes either insert fail with a real SQL error
  — a different technique is needed to force phase 2 to fail while phase 1 would have succeeded
  standalone: in the test, drop `catalog_directories` via a raw `Exec("DROP TABLE
  catalog_directories")` against the store's DB before calling `SyncBatch` with a valid,
  non-empty `entries` batch and a non-empty `directories` batch. The entries insert has everything
  it needs and would succeed standalone; the directories insert now hits a genuine "no such table"
  error. Assert `SyncBatch` returns an error wrapping "ensure directories", and that `Count()`
  afterward is `0` — proving the entries insert was rolled back, not just skipped. Existing
  `EnsureEntries`/`EnsureDirectories` tests are unaffected (same public signatures, same behavior).
- Decode-failure logging: a `cmd/catalog` test asserting a malformed-metadata entry still persists
  (with blank derived fields, matching today) and that an `Error`-level log line was emitted naming
  its `job_id`/`object_id`. `newTestCatalogServer` (`server_test.go`) builds its logger with
  `slog.NewTextHandler(os.Stderr, ...)` — output goes to stderr, not anywhere a test can assert
  against. This test needs its own server built directly (not via `newTestCatalogServer`) with a
  logger writing to a `bytes.Buffer` via `slog.NewTextHandler`, so the test can assert the buffer's
  content contains the expected `job_id`/`object_id`/log level after calling `SyncFileVersions`.
- `aggregateFacets`: direct unit tests (empty input, single name, multiple names with ties on
  `LastSeen`, empty-`Name` rows dropped) — new coverage that the three `List*Facets` methods didn't
  have in isolation before (their behavior was only exercised end-to-end).
- `parseDateRange`: direct unit tests (both valid, one malformed, both malformed) plus the existing
  5 handler tests continuing to pass unchanged (same request/response contract).

## Risks

- The `facets.go` split is a pure move + one shared-helper extraction — no query logic changes — so
  risk is limited to import/compile mechanics, caught immediately by `go build`.
- `SyncBatch`'s transaction wraps two `Create(...).Clauses(clause.OnConflict{DoNothing: true})`
  calls that were already individually idempotent; wrapping them in one transaction doesn't change
  that idempotency, only when partial failure becomes visible.
