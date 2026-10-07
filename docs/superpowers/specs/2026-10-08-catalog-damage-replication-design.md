# Catalog: replicate the damaged status and show it in the web UI

Date: 2026-10-08. Branch: `storfs-reliability-perf`. Components: `src/storage/filesystem` (replica reader), `src/cmd/catalogsync`, `src/api/catalog.proto`, `src/cmd/catalog`, `src/storage/catalog`, `src/cmd/api-server`, `web/`.

Follows `2026-10-07-damaged-data-reporting-design.md`, which flags `FileDataRecord.damaged_at` in `bwfs` and listed this as out of scope.

## Facts that shape the design

- A catalog row is per version, keyed `(store_node, job_id, object_id)`; `object_id` equals the bwfs `file_id`, which is what `bwfs` flags. One damaged `file_id` matches every catalog row with that `object_id`; no mapping is needed.
- `catalogsync` follows seq cursors over `file_version_records` / `file_version_deletions`. A damaged flag is an UPDATE on an old `file_data_records` row, which a seq cursor never sees.
- Damage is not permanent: a healthy re-upload of the same `file_id` heals it, and vacuum removes damaged rows once no version references the `file_id`. The catalog must be able to clear the flag.
- The restore cart does not resolve through the catalog (`rwfs` resolves on `bwfs` at restore time). Catalog damage is a UI warning, never a restore gate.

## Decision: state snapshot, not an event log

`catalogsync` periodically sends the set of file ids that are *currently* damaged; the catalog replaces its set for that store node atomically. "Absent" means healthy, so heals, vacuum and pre-existing damage (backfill) need no extra machinery: no new log table, cursor file, prune job or healed-event. Damage is rare, so the payload is small.

"Currently damaged" means: a `file_data_records` row with `damaged_at IS NOT NULL` exists for the `file_id` AND no row exists for it with `checksum IS NOT NULL AND damaged_at IS NULL`. (An in-flight re-upload with a NULL checksum is not healthy yet.)

## Units

1. **bwfs replica reader.** `FileDataRecord.DamagedAt` gets a plain index (`damaged_at`; almost all NULL). `ReplicaReader.DamagedFileIDs(ctx, after string, limit int) ([]string, error)` returns distinct damaged file ids per the definition above, ordered by `file_id`, keyset-paged by `after`. Read-only connection unchanged.
2. **Protocol.** `rpc ReportDamagedFiles(stream DamagedFilesChunk) returns (ReportDamagedFilesResponse)`; `message DamagedFilesChunk { repeated string object_ids = 1; }`; `message ReportDamagedFilesResponse {}`. Role `store` (authz table). `Entry` gets `bool damaged = <next free number>;`. Additive only.
3. **Catalog.** New table `catalog_damaged_files(store_node, object_id)` with a composite primary key, registered in `openDBs` Models. `ReportDamagedFiles` handler: store node from the mTLS peer (never the payload); read the whole stream, then `ReplaceDamagedFiles(storeNode, ids)` = delete that node's rows and insert the ids (batched) in ONE transaction, only on a clean end of stream; a stream error changes nothing. Known trade-off: the single writer is held for the replace; fine for a rare, small set. `ListEntries` sets `damaged` per entry with an EXISTS on `(store_node, object_id)` (works even if the damage arrives before the version row).
4. **catalogsync.** A third pass after the version and deletion passes, every `CatalogSyncDamageIntervalSec` (new key, default 60; follow the existing config key conventions, docs and validation). It pages `DamagedFileIDs` (`CatalogSyncBatchSize` per message) into one `ReportDamagedFiles` stream. Stateless (no cursor file). Skip the send only if the set is empty AND the previous successful send in this process was also empty (in-memory flag; after a restart it sends once, which clears stale catalog rows). Failures use the loop's existing exponential backoff and never advance anything. `Sender` gains `SendDamaged`; `LoggingSender` logs the count.
5. **api-server.** `entryDTO` / `toEntryDTO` gain `Damaged bool json:"damaged"`; `docs/api/rest-v1.md` documents it. No new endpoint.
6. **Web UI.** Staleness up to about one interval plus a poll, so all wording is a warning: "Backup data for this version is damaged; restore will fail." A damaged version row in `VersionsModal` shows a red `Badge` (variant `bad`) with that tooltip. `CatalogView` file rows show the badge when the representative (latest) version is damaged. `restoreCart` captures a display-only `damaged` flag in `toggleFile` (like `storeHost`/`size`); `RestoreView` cart rows show the badge for file rules; `RestoreConfirmModal` shows an amber warning line (like `confirm-overwrite`) naming how many selected files are damaged. Folder rules cannot be checked (resolved at restore time) and get no warning. Restore is never blocked.

## Failure behaviour

- Catalog or network down: the pass fails and retries with backoff; the UI keeps showing the last known set.
- Truncated/aborted stream: no change in the catalog.
- bwfs heals a file: the next pass omits it; the catalog clears it within one interval.
- Damage arrives before the version row is synced: the EXISTS annotation still shows it once the row appears.

## Testing (TDD)

- reader: damaged-only id returned; healed (healthy re-upload) id hidden; in-flight unfinalized re-upload does not hide it; paging by `after`; read-only store.
- catalog: replace is atomic (error mid-stream leaves old rows), replaces only that store node's rows, empty stream clears; authz requires `store`; `ListEntries` marks damaged, and marks it when the damage row predates the version row; peer identity taken from the cert.
- catalogsync: sends damaged set after versions/deletions; skip-when-empty-twice rule; empty send after non-empty clears; backoff on failure; config default/validation.
- api-server: DTO carries `damaged`.
- web (vitest): badge in `VersionsModal`, file row marker, cart row badge, confirm warning shown only with damaged file rules, hidden otherwise. No Playwright test: the demo lab cannot create real damage; note this in the docs/changelog.
- Docs per `.claude/CLAUDE.md`: `docs/protocols/catalog-sync.md` (service block, new RPC and messages, `Entry` field, authz), `README.md` Documentation links, `docs/components/{catalog,catalogsync,bwfs,api-server,web}.md`, `docs/api/rest-v1.md`, `docs/ARCHITECTURE.md` (table rows and mermaid edges), `CHANGELOG.md`, remove the `backlog.md` item, and update the "out of scope" note in the 2026-10-07 spec.
