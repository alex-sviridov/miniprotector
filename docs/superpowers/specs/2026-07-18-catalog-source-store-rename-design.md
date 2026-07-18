# Catalog: rename "source" → "store", add real source host

## Problem

In the catalog, `source_node`/`source_seq`/`source_created_at`/`source_host` all currently refer
to the **storage node** that sent a sync batch — the `bwfs` node's CA-verified mTLS hostname (see
`mtls.PeerHostname`). Elsewhere in Miniprotector, "source" means the **backup source**: the
machine whose files were backed up (e.g. `brfs`'s target host). These two meanings collide under
the same name, and the catalog has no way to distinguish them today.

The real originating host is already available — it's embedded in `object_id`
(`fs://<host>:<type>:<path>:<mtime>`, produced by `filesystem.FileInfo.ID()`) and recoverable via
the already-exported `filesystem.FileInfo.Source()` — but it's never extracted or exposed.

## Goals

1. Rename every catalog field that means "the sending storage node" from `Source*` to `Store*`, so
   the name matches what it actually identifies.
2. Add a new, persisted, indexed `source_host` that captures the *real* originating host, derived
   once at sync time (not decoded on every read).
3. Expose both `source_host` and `store_host` through the gRPC API, REST API, and the web
   frontend's Catalog view — as both display columns and independent exact-match filters.

## Non-goals

- No data migration for existing `catalog.db` files. This is dev/demo data (regenerated per
  demo/e2e run, no migration framework in place today) — AutoMigrate will add the new/renamed
  columns as new columns, and any pre-existing `catalog.db` should simply be deleted before running
  the updated binary. This is an accepted, explicit trade-off, not an oversight.
- No change to the `(store_node, job_id, object_id)` idempotency key — `source_host` is an
  additional indexed column, not part of identity.
- No change to non-filesystem workload handling — `source_host` derivation reuses the same
  `filesystem.DecodeFileInfo` call and tolerant-failure pattern (`toProtoEntry` in
  `src/cmd/catalog/server.go` and its equivalent in the sync path) that already exists for
  `path`/`size`/`mode`/etc.; a decode failure leaves `source_host` empty rather than failing the
  batch.

## Design

### 1. Rename "source" → "store" (sending storage node)

| Location | Before | After |
|---|---|---|
| `src/storage/catalog/models.go` (`EntryRecord`) | `SourceNode`, `SourceSeq`, `SourceCreatedAt` | `StoreNode`, `StoreSeq`, `StoreCreatedAt` |
| `src/storage/catalog/models.go` (unique index) | `idx_source_job_object` | `idx_store_job_object` |
| `src/storage/catalog/store.go` (`Entry`, `ListEntriesFilter`) | `SourceNode` | `StoreNode` |
| `src/storage/catalog/store.go` (`OnConflict` columns) | `source_node` | `store_node` |
| `src/cmd/catalog/server.go` | local var `sourceNode`, log field `source_node` | `storeNode`, `store_node` |
| `src/api/catalog.proto` `FileVersionEntry.source_seq` (field 5) | `source_seq` | `store_seq` |
| `src/api/catalog.proto` `Entry.source_host` (field 2) | `source_host` | `store_host` |
| `src/api/catalog.proto` `Entry.source_created_at` (field 6) | `source_created_at` | `store_created_at` |
| `src/api/catalog.proto` `ListEntriesRequest.source_host` (field 1) | `source_host` | `store_host` |
| `src/cmd/api-server/catalog.go` (`entryDTO`) | `SourceHost` (json `source_host`), `SourceCreatedAt` (json `source_created_at`) | `StoreHost` (json `store_host`), `StoreCreatedAt` (json `store_created_at`) |
| `src/cmd/api-server/catalog.go` (query param) | `source_host` | `store_host` |

`FileVersionEntry.created_at` (field 6, client → server) keeps its name — it was never prefixed
with "source" and is unambiguous as-is.

### 2. Add real `source_host`, derived at sync time

- `EntryRecord` gains `SourceHost string `gorm:"index"``, following the existing single-column
  index convention (`src/storage/filesystem/models.go`'s `FileID`).
- `storage/catalog.Entry` (the store package's transport struct) and `ListEntriesFilter` each gain
  a `SourceHost string` field.
- In `catalogServer.SyncFileVersions` (`src/cmd/catalog/server.go`), before calling
  `store.EnsureEntries`, decode each entry's `Metadata` via `filesystem.DecodeFileInfo` and call
  `.Source()` to populate `SourceHost`. On decode failure, leave it `""` — the batch still
  persists; this mirrors the existing per-row tolerance in `toProtoEntry`.
- `Store.ListEntries` gains a `SourceHost` filter clause (`WHERE source_host = ?`), symmetric with
  the existing `StoreNode` filter.
- Since `source_host` is now a plain persisted column, `toProtoEntry` reads it directly from
  `EntryRecord` instead of re-decoding metadata — cheaper than today's per-read decode, and the
  only reason the read-time decode still exists is for `path`/`size`/`mode`/`owner`/`group`/`mod_time`.

### 3. API surface

- `src/api/catalog.proto`:
  - `ListEntriesRequest` gains `string source_host = 5;` (exact match; empty = no filter).
  - `Entry` gains `string source_host = 14;` (display).
- `src/cmd/api-server/catalog.go`:
  - `entryDTO` gains `SourceHost string `json:"source_host"`.
  - `handleListCatalog` reads a new `source_host` query param and passes it through alongside the
    renamed `store_host`.
- `docs/api/rest-v1.md`'s `GET /api/v1/catalog` section gets a `source_host` filter param row and
  both `source_host`/`store_host` in the example response.

### 4. Frontend

- `web/src/stores/catalog.js`: `filters` gains `sourceHost` alongside the renamed `storeHost`;
  `buildQuery` sends both `source_host` and `store_host` query params.
- `web/src/views/CatalogView.vue`: filter form gets a second input ("Store Host" alongside
  "Source Host"); results table gets a second column so both are visible per row.

### 5. Documentation

Per the project's gRPC/feature-change documentation rules:

- `docs/protocols/catalog-sync.md` — update field tables and the "Identity" section to describe
  `store_node`/`store_seq`/`store_created_at` and the new `source_host` (both its persistence and
  its filter semantics).
- `docs/components/catalog.md` — update "How It Works" to describe the `store_node` identity key
  and the new `source_host` derivation-at-sync-time step.
- `docs/components/catalogsync.md` — update field-name references (`source_node` →
  `store_node` in the idempotency-key explanation).
- `docs/api/rest-v1.md` — update the `GET /api/v1/catalog` param table and example (see above).
- `CHANGELOG.md` — entry added before merging to `main`, summarizing the rename and the new field.

## Testing

- `src/storage/catalog/store_test.go` — update existing assertions for renamed fields; add a case
  covering the `SourceHost` filter and its index.
- `src/cmd/catalog/server_test.go` — add coverage for `SourceHost` derivation from `Metadata` on
  `SyncFileVersions`, including the decode-failure-leaves-it-empty case.
- `src/cmd/catalogsync/grpcsender_test.go` — update renamed field references (`SourceSeq` →
  `StoreSeq`) in whatever fixtures/assertions touch it.
- `src/e2e/catalog_test.go`, `src/e2e/catalog_validate.go` — update `catalogEntryRow`'s
  `SourceNode` → `StoreNode`; extend the validation to assert `SourceHost` matches the backed-up
  client's hostname.
- Frontend: no test suite currently exists for `CatalogView.vue`/`catalog.js` beyond what's noted
  in the original web-frontend spec (Pinia store unit tests) — extending those, if present, to
  cover the two independent filters is in scope but not blocking.
