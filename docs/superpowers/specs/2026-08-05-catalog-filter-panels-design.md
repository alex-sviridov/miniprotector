# Design: catalog filter bar with date-range, client, and job/policy panels

**Date:** 2026-08-05
**Status:** Approved for planning

## Problem

The catalog UI (`web/src/views/CatalogView.vue:13,51-58`) exposes exactly three filters, all
exact-or-substring text inputs: `sourceHost`, `storeHost`, `pattern`. A user has to already know a
hostname or path fragment to search at all — there's no way to browse "what got backed up recently"
or "what did this policy back up," and nothing narrows by date. This mirrors the API one level down:
`ListEntriesRequest` (`src/api/catalog.proto:27-33`) and `ListEntriesFilter`
(`src/storage/catalog/store.go:74-80`) carry the same three fields end to end, with no date range and
no notion of job/policy at all — `job_id` is stored on every `EntryRecord`
(`src/storage/catalog/models.go:14`) but never queryable.

`2026-07-20-catalog-view-rewrite-design.md:60` explicitly scoped that rewrite as not touching filters
or the `/catalog` response shape — this design picks that up.

## Approach

Add two filter dimensions — date range (on `received_at`) and job/policy — as a faceted-search UI: a
three-row filter bar where each row's chip expands a panel below it, and each panel's own list is
scoped by every *other* active filter (never itself), so multi-selecting inside one dimension never
shrinks its own list. An interactive mockup validated this layout; see "UI layout" below for the
agreed structure.

### Why `received_at`, not `ctime`

`EntryRecord` carries two timestamps: `Ctime` (the source file's own metadata change time) and
`ReceivedAt` (when the catalog server persisted the row, `store.go:56`). The date filter uses
`ReceivedAt` — it's server-assigned and monotonic, so "last 7 days" means the same thing regardless of
a client's clock skew, whereas `Ctime` is only as trustworthy as the originating host's clock.

### Why job/policy is a name match, not a foreign key

`job_id` is a per-run string, not a stable policy identifier: `backupJobID`
(`src/cmd/agent/backup.go:177`) builds it as `backup:<policyName>:<path-slug>:<filterID>:<unixTimestamp>`,
freshly generated every scheduled run (`backup.go:240`). One policy therefore produces many distinct
`job_id` values over its lifetime, and there is no join table back to `policy-server`'s `Policy.id`
(`src/api/policyserver.proto:57-90`) — that `id` is a UUIDv5 computed independently
(`src/cmd/policy-server/policy.go:21-25`). The only existing job_id parsing precedent,
`kindFromJobID` (`src/cmd/api-server/jobs.go:34-39`), only splits off the *first* colon segment
("backup") for an unrelated Loki-backed jobs view — no code anywhere extracts the policy-name segment
today. This design adds that parsing inside `storage/catalog` (see "Store layer" below), and the
"Job / Policy" filter matches by that parsed name, grouping many runs under one label — an explicit,
documented departure from exact-ID filtering, not an oversight.

### UI layout (validated by mockup)

Three filter rows, one shared panel underneath the whole bar (not per-column):

- **Row 1 (full width):** date-range chip, defaulting to "last 7 days."
- **Row 2 (split):** Clients chip | Job/Policy chip.
- **Row 3 (full width):** path substring input (existing `pattern` filter, unchanged).

Clicking a chip swaps the panel content: the date chip shows a range picker with presets; the
Clients/Job chips each show a searchable, checkbox-selectable table of facet rows (`name`, `count`,
`last seen`), scoped as described above. Only one panel is open at a time.

### Faceted cross-filtering

Given full cross-filtering is wanted (client selection narrows the policy list and vice versa), each
facet list's query includes every active filter *except its own dimension*:

- Clients facet list: filtered by date range + path + selected job/policy names.
- Job/policy facet list: filtered by date range + path + selected client hostnames.
- The results table itself: filtered by all four dimensions together.

### Proto (`src/api/catalog.proto`)

```proto
message ListEntriesRequest {
  string store_host     = 1;
  string pattern        = 2;
  int32  limit           = 3;
  int64  starting_after  = 4;
  string source_host    = 5;
  // New, additive — old singular fields (1-5) keep their current exact-match
  // behavior for any existing caller; the new repeated fields are OR-matched
  // and combined with everything else via AND, same as the old fields.
  int64  received_after  = 6; // unix seconds; 0 = no lower bound
  int64  received_before = 7; // unix seconds; 0 = no upper bound
  repeated string source_hosts = 8; // OR-matched; empty = no filter
  repeated string job_names    = 9; // OR-matched against the policy name embedded in job_id
}

message Facet {
  string name       = 1; // hostname, or policy name
  int64  count       = 2; // matching entries in the current scope
  int64  last_seen   = 3; // unix seconds, max(received_at) in scope
}

message ListFacetsRequest {
  int64  received_after  = 1;
  int64  received_before = 2;
  string pattern         = 3;
  repeated string source_hosts = 4; // ignored by ListClientFacets (own dimension)
  repeated string job_names    = 5; // ignored by ListJobFacets (own dimension)
}

message ListFacetsResponse {
  repeated Facet facets = 1;
}

service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
  rpc ListClientFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListJobFacets(ListFacetsRequest) returns (ListFacetsResponse);
}
```

One shared `ListFacetsRequest`/`Facet` message pair for both new RPCs — the query shape is identical,
only which column is grouped on differs, so two messages would be pure duplication.

### Store layer (`src/storage/catalog/store.go`, `models.go`)

`ListEntriesFilter` gains `ReceivedAfter`, `ReceivedBefore time.Time`, `SourceHosts []string`,
`JobNames []string`, applied in `ListEntries` alongside the existing conditions (`store.go:101-113`);
`SourceHosts`/`JobNames` become `WHERE source_host IN (...)` / an OR of `job_id LIKE 'backup:<name>:%'`
clauses when non-empty, additive to the existing singular `SourceHost`/`Pattern` conditions.

Two new methods, following the `Group(...)` + `Scan(...)` precedent already established in
`src/cmd/bwfs/list.go:52-59` for aggregate queries against this same GORM setup:

```go
type Facet struct {
	Name     string
	Count    int64
	LastSeen time.Time
}

// ListClientFacets groups matching entries by SourceHost. Excludes any
// SourceHosts filter on the input — a client facet list is never narrowed
// by its own dimension's current selection.
func (s *Store) ListClientFacets(filter FacetFilter) ([]Facet, error) {
	// SELECT source_host AS name, COUNT(*) AS count, MAX(received_at) AS last_seen
	// FROM entry_records WHERE ... GROUP BY source_host
}

// ListJobFacets groups matching entries by the policy name parsed from
// job_id. Unlike ListClientFacets, grouping happens in Go, not SQL: job_id's
// colon-delimited format isn't fixed-width, and this codebase's existing
// precedent for decoding a similar composite ID (bwfs/list.go's parseFileID)
// already favors parsing in Go over SQL substr/instr.
func (s *Store) ListJobFacets(filter FacetFilter) ([]Facet, error) {
	// fetch (job_id, received_at) rows matching filter (excluding JobNames),
	// aggregate policyNameFromJobID(job_id) -> {count, max(received_at)} in a map
}

// policyNameFromJobID extracts the second colon-delimited segment of a
// job_id (e.g. "nightly-db" from "backup:nightly-db:var-www:abcd1234:...").
// Returns "" for a job_id with fewer than two segments (never errors) —
// mirrors bwfs/list.go's parseFileID tolerance for malformed/foreign IDs.
func policyNameFromJobID(jobID string) string
```

`FacetFilter` is `ReceivedAfter`, `ReceivedBefore time.Time`, `Pattern string`, plus exactly one of
`SourceHosts`/`JobNames` depending on which method is called — the caller (the gRPC server) omits its
own dimension when building the struct for each RPC, per the mockup's exclusion rule above.

Both methods drop rows where the grouping key is empty — `SourceHost` can be `""` when
`decodeSourceHost` (`cmd/catalog/server.go:63-69`) failed at sync time, and `policyNameFromJobID` can
return `""` for a malformed `job_id` — rather than surfacing a blank-named facet row in the UI.

**Indexes:** today only `SourceHost` and the `(StoreNode, JobID, ObjectID)` uniqueness key are indexed
(`models.go:13-23`) — there is no index on `ReceivedAt`, which every new query (date range, and both
new facet aggregates) filters or sorts on, and no index on `JobID` alone (it's only the second column
of a composite key, unusable for a standalone `LIKE` scan). Add `gorm:"index"` to `ReceivedAt` and a
new standalone index on `JobID`; `db.AutoMigrate` (`db.go:41`) picks both up with no separate
migration file, matching how the table is already managed.

### gRPC server (`src/cmd/catalog/server.go`)

`ListEntries` (`server.go:71-89`) passes the four new fields into `ListEntriesFilter` alongside the
existing five. Two new handlers, `ListClientFacets`/`ListJobFacets`, each a thin
`ListFacetsRequest` → `FacetFilter` → store call → `ListFacetsResponse` translation, following the
same shape as `ListEntries` itself.

### api-server (`src/cmd/api-server/catalog.go`, `server.go:79`)

`handleListCatalog` gains four new query params (`received_after`, `received_before`,
`source_hosts` comma-separated, `job_names` comma-separated) parsed the same way `starting_after`
already is, and passed into the existing `pb.ListEntriesRequest` construction.

Two new unpaginated handlers — `handleListCatalogClients` → `GET /api/v1/catalog/clients`,
`handleListCatalogJobs` → `GET /api/v1/catalog/jobs` — following `handleListPolicies`
(`policies.go:82-94`), the codebase's existing precedent for a small, unpaginated `{"data": [...]}`
list response, rather than the cursor-paginated style `/catalog` itself uses: facet lists return at
most a few dozen rows.

### Vue frontend (`web/src`)

Composition API, `<script setup>` throughout, matching the existing store/view split.

**Components:**
- `CatalogView.vue` — owns `activePanel: 'date' | 'clients' | 'jobs' | null`; renders the three chip
  rows, the path input (unchanged), and the results `DataTable`.
- `components/catalog/DateRangePanel.vue` — wraps `@vuepic/vue-datepicker` in range mode, using its
  built-in `preset-dates` prop for "Today / Last 7 days / Last 30 days / This month" instead of
  hand-rolled preset buttons. New dependency, added to `web/package.json`.
- `components/catalog/ClientsPanel.vue`, `JobsPanel.vue` — each fetches its facet list on relevant
  filter changes and renders `<DataTable :rows="facets" selectable @selection-change="...">`. No
  custom table markup.
- `components/ui/DataTable.vue` gains a `selectable` prop (default `false`) forwarding to
  `vue-good-table-next`'s existing `select-options` (`{ enabled: true, selectOnCheckboxOnly: true }`,
  confirmed supported by the installed v0.2.2 package) and re-emitting its `on-selected-rows-change`
  as `selection-change` — additive, `CatalogView.vue:67`'s existing non-selectable usage is unaffected.

**State** (`stores/catalog.js`): `filters` grows from `{ sourceHost, storeHost, pattern }` to
`{ receivedAfter, receivedBefore, sourceHosts: [], jobNames: [], pattern }`, with
`receivedAfter`/`receivedBefore` defaulting to "last 7 days" at store creation — `storeHost` (the
bwfs-node filter) is unused by this design and stays as-is, unrelated to client/job filtering. Two new
actions, `fetchClientFacets()`/`fetchJobFacets()`, each building their query from `filters` *excluding
their own dimension* (mirrors the store-layer exclusion above) via a `buildFacetQuery` helper
alongside the existing `buildQuery` (`catalog.js:7-15`).

**Reactivity:** watch `[receivedAfter, receivedBefore, pattern, jobNames]` → `fetchClientFacets`, and
the symmetric watch (substituting `sourceHosts`) → `fetchJobFacets`; debounce only on `pattern` (free
text), immediate otherwise. Both are prefetched (not lazy on panel open) since facet lists are small
and unpaginated — keeps chip-switching instant.

**Behavior change:** today `CatalogView.vue`'s `canSearch`/`hasSearched` gate (`CatalogView.vue:17,59`)
requires the user to type something before any request fires. Since the date range now always has a
default value, the results table (and both facet lists) fetch automatically on mount instead of
waiting for an explicit non-empty filter — closer to `JobsListView`'s existing default-window-on-load
pattern than to the current catalog page's empty-state-first behavior.

## Out of scope

- File-attribute filters (size, owner/group, mode) and better path matching (prefix/glob) — both
  raised during design but deferred; only date range and job/policy are being added this round.
- Any change to `storeHost`/bwfs-node filtering — untouched by this design.
- Retroactively backfilling `ReceivedAt`/`JobID` index creation performance on an already-large
  existing table — `AutoMigrate`'s `CREATE INDEX` cost on first deploy is accepted as-is, no
  online-migration tooling added.
- Making `ListEntries`' new repeated fields replace the old singular `source_host`/`pattern` fields —
  both old and new coexist; no deprecation in this pass.

## Testing plan

- **`storage/catalog/store_test.go`**: `ListClientFacets`/`ListJobFacets` — date-range boundaries
  (inclusive/exclusive edges), multi-value `job_names`/`source_hosts` OR-matching, empty-result case,
  and that each method ignores a filter value passed for its own dimension. `policyNameFromJobID`
  table-driven test including a malformed/short job_id (no false split).
- **`storage/catalog/models_test.go`** (or migration test): confirm `AutoMigrate` creates the new
  `ReceivedAt`/`JobID` indexes.
- **`cmd/catalog/server_test.go`**: `ListClientFacets`/`ListJobFacets` RPC handlers translate request
  fields into the right `FacetFilter`; `ListEntries` passes through the four new fields.
- **`cmd/api-server/catalog_test.go`**: new handlers parse comma-separated `source_hosts`/`job_names`
  and reject malformed `received_after`/`received_before` the same way `starting_after` is validated
  today; existing `/catalog` tests extended for the new query params.
- **Vue (`vitest`)**: `catalog.js` store — `buildFacetQuery` excludes the right dimension per facet;
  default date range is set on store creation. `CatalogView.vue`/panel components — chip click swaps
  the active panel; selecting a facet row updates `filters` and triggers the expected re-fetch of the
  *other* facet list (cross-filter behavior) without re-fetching itself.

## Documentation

- `docs/protocols/catalog-sync.md` — document the four new `ListEntriesRequest` fields, the new
  `ListClientFacets`/`ListJobFacets` RPCs, `Facet`/`ListFacetsRequest`/`ListFacetsResponse`, and the
  "excludes its own dimension" cross-filter contract.
- `docs/components/catalog.md` — describe the new filter bar (date range, clients, job/policy),
  faceted cross-filtering, and the `received_at`-not-`ctime` and name-match-not-ID-match decisions.
- `docs/api/rest-v1.md` — new `GET /api/v1/catalog/clients`/`GET /api/v1/catalog/jobs` endpoints;
  updated `/api/v1/catalog` param table with `received_after`/`received_before`/`source_hosts`/
  `job_names`.
- `README.md` — cross-link the updated protocol doc if its summary line changes.
- `docs/ARCHITECTURE.md` — no changes; no new component or topology/data-flow change, only new
  RPCs/fields on the existing `CatalogService`.
- `CHANGELOG.md` — one dated entry: the catalog UI/API gains date-range and job/policy filtering with
  full cross-filtering between clients and policies, backed by two new `CatalogService` facet RPCs;
  additive to the existing `/catalog` and `ListEntries` contract, no breaking changes.
