# Catalog damage replication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replicate the set of currently damaged file ids from bwfs to the catalog by state snapshot and show it in the web UI.

**Architecture:** `ReplicaReader.DamagedFileIDs` -> `catalogsync` third pass streams the set via new `CatalogService.ReportDamagedFiles` -> catalog replaces the store node's set atomically and annotates `Entry.damaged` -> api-server DTO -> web badges and cart warning.

**Tech Stack:** Go 1.26, gRPC/protobuf (`make proto`, protoc is installed), GORM + SQLite, Vue 3 + vitest.

**Spec:** `docs/superpowers/specs/2026-10-08-catalog-damage-replication-design.md` (binding; read first).

## Global Constraints

- Additive proto changes only: new RPC, two new messages, `Entry.damaged` with the next free field number. Regenerate with `make proto` and commit the `*.pb.go`.
- "Currently damaged" = damaged row exists AND no `checksum IS NOT NULL AND damaged_at IS NULL` row exists for the same `file_id`.
- Store node always from the mTLS peer, never from the payload. Role `store` for `ReportDamagedFiles`.
- The catalog replaces a store node's set in ONE transaction, only on a clean end of stream.
- No cursor file for damage; skip the send only if the set is empty and the previous successful send in this process was empty.
- Restore is never blocked in the UI; UI wording is a warning.
- TDD for every behaviour. Before each commit: `cd src && go build ./... && go vet ./storage/... ./cmd/... ./api/... ./common/... && go test -race ./storage/... ./cmd/catalog/... ./cmd/catalogsync/... ./cmd/api-server/... ./cmd/bwfs/... ./common/...` and `go test -race -tags integration ./cmd/catalogsync/... ./cmd/catalog/... ./storage/...`; web tasks also `cd web && npm test -- --run` (check package.json for the exact command) and the lint/format scripts the repo uses.
- Docs in the same task as the code (listed per task). Plain readable code; comments explain why. Do not reformat unrelated files.
- Commit trailer (every commit): `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_0113q6eVXiibbytqwNC4cCSr`.

---

### Task 1: bwfs replica reader: DamagedFileIDs

**Files:** Modify `src/storage/filesystem/models.go` (index on `damaged_at`), `src/storage/filesystem/replicareader.go`; Test `src/storage/filesystem/replicareader_test.go` (+ `damaged_test.go` helpers); Docs `docs/components/bwfs.md` (one paragraph: the index and that catalogsync reads the damaged set).

**Produces:** `func (r *ReplicaReader) DamagedFileIDs(ctx context.Context, after string, limit int) ([]string, error)` — distinct file ids, ascending, `file_id > after`, at most `limit`.

- [ ] Tests (real store + `OpenReplicaReader`): damaged-only id returned; a healthy finalized re-upload of the same file_id hides it; an in-flight (checksum NULL) re-upload does not hide it; two damaged rows of one file_id give one id; paging with `after`/`limit` returns everything exactly once; none damaged -> empty slice; the query uses the `damaged_at` index (EXPLAIN QUERY PLAN assertion like `TestOpenDB_BackfillProbeUsesPathIndex`).
- [ ] Implement, run the gate, commit `feat(storfs): replica reader lists currently damaged file ids`.

---

### Task 2: Protocol and catalog service

**Files:** Modify `src/api/catalog.proto` (+ regenerated `catalog.pb.go`, `catalog_grpc.pb.go`), `src/storage/catalog/{models,db,store}.go`, `src/cmd/catalog/{server,authz}.go`; Tests alongside (`store_test.go`, `server_test.go`, `authz_test.go`); Docs `docs/protocols/catalog-sync.md`, `docs/components/catalog.md`, `README.md` (Documentation section link/summary if the protocol doc entry needs it).

**Consumes:** nothing from Task 1. **Produces:** `CatalogService.ReportDamagedFiles(stream DamagedFilesChunk) returns (ReportDamagedFilesResponse)`; `Entry.damaged`; catalog store method `ReplaceDamagedFiles(ctx, storeNode string, objectIDs []string) error`; table `catalog_damaged_files`.

- [ ] Tests: `ReplaceDamagedFiles` replaces only that node's rows, empty clears, is atomic on error (inject a failing insert), large sets (>32,766 ids; batch inserts so the SQLite variable limit is never hit — test with a lowered limit or a bulk path); handler reads a whole stream then replaces once, uses the mTLS peer for the node, aborted stream (client cancels / Recv error) leaves old rows; authz requires role `store` and rejects others; `ListEntries` sets `damaged` per entry via EXISTS on `(store_node, object_id)` including when the damaged row was reported before the version row; entries of other nodes unaffected.
- [ ] Implement (`make proto`), run the gate, commit `feat(catalog): ReportDamagedFiles and Entry.damaged`.
- [ ] Docs: protocol doc (service block, messages, authz, `Entry.damaged`, replace-set semantics), catalog component doc (table, RPC), README cross-link per .claude/CLAUDE.md.

---

### Task 3: catalogsync damage pass

**Files:** Modify `src/cmd/catalogsync/{sync,sender,sender_select,main}.go` as needed, `src/common/config/config.go` (+ tests/docs for the new key), Test `src/cmd/catalogsync/*_test.go`; Docs `docs/components/catalogsync.md`, `docs/ARCHITECTURE.md` (table rows + mermaid edges), `docs/protocols/catalog-sync.md` (client behaviour paragraph), `docs/components/bwfs.md` only if needed.

**Consumes:** `ReplicaReader.DamagedFileIDs` (Task 1), `CatalogService.ReportDamagedFiles` (Task 2). **Produces:** config key `CatalogSyncDamageIntervalSec` (default 60); `Sender.SendDamaged(ctx, ids <batched source>) error` on both senders.

- [ ] Tests (fake reader + fake sender, existing patterns): pass runs after versions and deletions; not before the interval elapses (fake clock or injected interval); streams batches of `CatalogSyncBatchSize`; empty+empty skip rule, empty after non-empty is sent; a failed send backs off and is retried, versions/deletions cursors unaffected; `LoggingSender` logs the count; config default and validation like the sibling keys.
- [ ] Implement, run the gate, commit `feat(catalogsync): replicate the damaged file set`.
- [ ] Docs as listed.

---

### Task 4: api-server field

**Files:** Modify `src/cmd/api-server/catalog.go` (`entryDTO`, `toEntryDTO`), Test `src/cmd/api-server/catalog_test.go`; Docs `docs/api/rest-v1.md`, `docs/components/api-server.md`.

**Consumes:** `Entry.damaged` (Task 2). **Produces:** JSON `damaged` boolean on `GET /api/v1/catalog` entries.

- [ ] Test: DTO carries `damaged` true/false from the proto entry; existing golden/JSON tests updated for the new field.
- [ ] Implement, gate, commit `feat(api-server): expose damaged on catalog entries`; docs in the same commit.

---

### Task 5: Web UI

**Files:** Modify `web/src/components/VersionsModal.vue`, `web/src/views/CatalogView.vue` (+ `web/src/utils/catalogGrouping.js` if the representative flag belongs there), `web/src/stores/restoreCart.js`, `web/src/views/RestoreView.vue`, `web/src/components/RestoreConfirmModal.vue`; specs `*.spec.js` beside each; Docs `docs/components/web.md`, `CHANGELOG.md` (dated 2026-10-08 entry, most recent first), `backlog.md` (remove the item "Show damaged versions in the catalog and web UI"), `docs/superpowers/specs/2026-10-07-damaged-data-reporting-design.md` (update the out-of-scope note to point at the new spec).

**Consumes:** JSON `damaged` on catalog entries (Task 4). Use existing `components/ui/Badge.vue` variant `bad`.

- [ ] Specs: versions modal shows a Damaged badge (with the warning tooltip) only for damaged versions; file row shows the badge only when the latest version is damaged; `toggleFile` stores a display-only `damaged` flag (persisted cart shape stays backward compatible with carts saved before this change); cart row shows the badge for file rules; the confirm modal shows the amber warning with the number of damaged files only when file rules with damage are selected, and never blocks submit; folder rules never warn.
- [ ] Implement, run web tests and lint, commit `feat(web): show damaged backup data`.
- [ ] Docs, changelog (what/why: damaged flag replicated by snapshot, UI warns; note no Playwright test because the demo lab cannot create damage), backlog and spec note.
