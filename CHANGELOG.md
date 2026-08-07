# Changelog

All notable changes to this project are documented here, most recent first.

## 2026-08-07 — issuer: fix self-cert-refresh leaving a permanently mismatched identity

`issuer`'s daily self-cert-refresh wrote `client.crt` and `client.key` with two independent
`os.WriteFile` calls. Since `common/mtls` reloads both files from disk on every TLS handshake (not
just at startup), any interruption between the two writes — disk pressure, a permission error, the
process being killed mid-write — could leave `client.crt` holding the new certificate while
`client.key` still held the old private key, permanently failing every subsequent handshake until a
full restart. `mintSelfIdentity` now stages both files into temp files first and commits them via
two adjacent renames, so a failure while writing data never touches a live file.

## 2026-08-06 — e2e: add client lifecycle test (revoke/reissue, policy, job, catalog)

Adds `TestE2E_ClientLifecycle` alongside the existing demo-web-UI smoke test: revokes and reissues
an enrolled client's certificate (confirming `certclient operating-refresh` is refused while
revoked and succeeds again after unrevoke), creates a 1-minute recurring backup policy, and
confirms both a real backup job (`GET /api/v1/jobs`) and its replicated catalog entry
(`GET /api/v1/catalog`) appear. Runs against the already-running demo lab, same precondition as the
original smoke test. `make test-e2e`'s timeout grows from 30s to 120s to accommodate the new test's
real reconcile/policy-fetch/catalog-sync wait times.

## 2026-08-05 — catalog: date-range and job/policy filtering with cross-filtering

The catalog UI and API grow two new filter dimensions — a date range (on `received_at`) and
job/policy (matched by the policy name embedded in `job_id`) — alongside the existing client and
path filters. `CatalogService` gains `ListClientFacets`/`ListJobFacets`, two aggregate RPCs that
back new `GET /api/v1/catalog/clients`/`GET /api/v1/catalog/jobs` endpoints and full cross-filtering
between clients and policies in the web filter bar: each facet list excludes its own dimension, so
selecting a client narrows the policy list and vice versa without ever narrowing itself out. All
proto/API additions are additive; the web catalog view's filter bar and store internals are
rewritten (no external consumers), now defaulting to and auto-fetching the last 7 days on load. The
old free-text store-host filter input is also removed from the catalog page — the underlying API
field is untouched, just no longer exposed in this UI.

## 2026-08-04 — policy-server: resolve backup destinations from storage checkins

A backup policy's destination was resolved from `client_filters.hostnames[0]` — a glob-matching
pattern meant for targeting, not an address — silently breaking for any storage policy with a
wildcard or more than one matching host. It's now resolved from the storage policy's live checkin
list instead: one `host:port` entry per host that has actually checked in against it, ordered
freshest-first. `Policy.destination` (a single string) is replaced outright by `Policy.destinations`
(a list) across the wire, `policyclient`'s on-disk cache, `agent` (which uses the first entry when
execing `brfs`), `api-server`'s REST responses, and the web admin views — a breaking change with no
compatibility shim, since every consumer is inside this repo.

## 2026-08-04 — web: navigation shell and visual consistency polish

The sidebar now carries a small brand mark and one icon per section, with a clearer accent-bordered
active state, instead of plain text links on a flat background. Every list and detail page shows a
breadcrumb trail above its title (`PageHeader`'s new `crumbs` prop), closing the orientation gap on
pages reached directly rather than via the sidebar. Boolean/state table columns (a client's Revoked
column, a job's State column) render as a colored badge instead of plain text, and the shared
`DataTable` component now overrides `vue-good-table-next`'s default theme to match the rest of the
app's borders and colors. `BaseButton` gained an optional `to` prop so link-styled actions (like
Clients' "New Client") go through the same component as `<button>`-styled ones, closing the one
spot that still hardcoded its own Tailwind classes. Same routes, same data, same API — a visual and
navigational pass, not a new feature.

## 2026-08-03 — web: unified policy detail tabs with check-in visibility

Both `/policies/:id` and the new `/storage/:id` now share a `Details`/`Check-ins` tabbed layout,
built on a reusable `Tabs` component (`components/ui/Tabs.vue`) that syncs the active tab to the
URL. The `Check-ins` tab (`components/policies/PolicyCheckins.vue`) surfaces the check-in data
`policy-server`/`api-server` started tracking earlier today -- every host that has received a
policy, with its most recent check-in time and a manual Refresh button. Storage policies get a
details page for the first time, closing the gap with backup policies: the `/storage` list's name
column now navigates there instead of opening the edit modal inline.

## 2026-08-03 — policy-server: check-in tracking and cleanup

`policy-server` now records, in a local SQLite database, the most recent time each host received
each policy from `GetPolicies` -- one upserted row per `(policy, hostname)` pair, covering both
backup and storage policy types. A check-in write failure fails the whole `GetPolicies` call rather
than being silently dropped. `ListPolicies` (and therefore `api-server`'s `GET /api/v1/policies` /
`GET /api/v1/policies/{id}`) now returns each policy's current check-in list; `GetPolicies` itself
never echoes it back. A background routine, ticking every fixed 1 minute, purges check-ins older than
the new `CheckinRetentionSec` config key (default 24h), so a host that stops polling a policy ages out
of its check-in list on its own.

## 2026-08-03 — web: destination select and form guardrails on the backup policy modal

`BackupPolicyFormModal.vue`'s destination field is no longer a free-text `host:port` box — it's now a required select populated from `useStoragePoliciesStore()`, listing the operator's configured storage policies by name (with their resolved `hostname:port`, or "(incomplete)" for a storage policy still missing one, shown rather than hidden). Picking an option sets the backup policy's `storage_policy_id` directly; the modal never types or computes a destination itself, closing out the web side of the storage-policy link built server-side today (see the entry directly below). A "Reload" button next to the select re-fetches the storage policies list on demand, and the modal also auto-loads it on mount if the store is still empty, so an operator opening the form fresh doesn't have to remember to visit `/storage` first. The modal picked up basic guardrails along the way -- a non-blank name check, and an RPO input that, when filled in, must match a `time.ParseDuration`-shaped pattern -- and a set of reusable form primitives (`BaseField`, `BaseInput`, `BaseSelect` in `web/src/components/ui/`, built on Vue 3.5's `defineModel()`) that both this modal and `StorageEditModal.vue` now render through, the latter as a pure markup refactor with no behavior change.

## 2026-08-03 — policy-server/api-server: link backup policies to storage policies by id

A backup policy's `destination` (the target `bwfs`, `"host:port"`) is no longer typed in by hand — it's now derived live from a required `storage_policy_id` reference to a storage policy, resolved to that storage policy's `client_filters.hostnames[0]:port` on every read. This removes the drift risk of a free-text destination silently going stale after a storage policy's hostname or port changes, and gives the (separately planned) web form a real value to select from instead of guessing via string-matching. `CreatePolicy`/`UpdatePolicy` now require `storage_policy_id` to reference an existing storage policy; `DeletePolicy` refuses to remove a storage policy still referenced by any backup policy. This is a breaking change: `destination` is no longer accepted as create/update input (for either policy type), and every on-disk backup policy JSON file needs a `storage_policy_id` — the three demo fixtures under `demo/policy-server/policies/backup/` are migrated as part of this change; no backward-compatibility path is provided for hand-maintained files elsewhere.

## 2026-08-02 — web: rewrite backup policies view with modal form and Run now action

The backup policies list and detail views now share a single form modal for creating, editing, and immediately running policies. Previously, `/policies/new` and `/policies/:id/edit` were separate full-page routes; now a "New backup" action on the list and "Edit" button on the detail view both open a shared `BackupPolicyFormModal`, reducing duplication and improving maintainability. The modal also offers a "Run now" action — operators can now execute a policy's filters immediately as a one-time ad-hoc backup job (composed with a `disabled_at` auto-set to expire after 1h) and are immediately redirected to `/jobs` to monitor the resulting job's log lines, eliminating a round-trip through the policies list. The old `/policies/new` and `/policies/:id/edit` routes are removed entirely. This change pairs with the new `POST /api/v1/policies/adhoc` endpoint (committed separately today).

## 2026-08-02 — api-server: adhoc (one-time) backup policy endpoint

`POST /api/v1/policies/adhoc` creates a one-time backup policy from the same fields as an ordinary
create (name, client filters, object filters, destination) -- `api-server` composes `backup_window`
(every minute), `rpo`, and `disabled_at` itself from a new `AdhocPolicyTimeoutSec` config value
(default 1h), so a caller never hand-crafts those three fields to get a "run once on every matched
node, then expire" policy. Also fixes a gap the prior `disabled_at` work flagged: `PUT
/api/v1/policies/{id}` and `PUT /api/v1/storage-policies/{id}` now round-trip `disabled_at` --
previously any edit through either endpoint silently cleared it.

## 2026-08-02 — policy-server: generic disabled_at field on every policy type

Policies of any type (`"backup"` or `"storage"`) can now carry a `disabled_at` timestamp. Once it
passes, `policy-server`'s `GetPolicies` stops serving that policy and `agent` stops acting on it
(deriving no backup task, supervising no `bwfs`/`catalogsync` process) -- checked live, no restart or
manual reload needed. `ListPolicies` still shows a disabled policy for admin visibility. This is a
generic primitive with no "adhoc" concept baked in anywhere; it's the foundation a future one-time/ad
hoc backup capability (an ordinary backup policy with a near-future `disabled_at`, composed by a
planned `api-server` convenience endpoint) will build on.

## 2026-07-31 — agent: supervise catalogsync alongside bwfs; demo drops its process-sequencing shell script

`agent` now supervises a `catalogsync` process the same way it already supervises `bwfs` for a
`"storage"`-typed policy: two fully independent ensure-running tasks per policy, with no ordering or
coordination between them — a `catalogsync` that starts before `bwfs` has created its database
simply fails cleanly and gets crash-restarted, like any other transient exec failure.

The demo's `backup-host` containers (`database`, `webserver`, `store`) no longer run a shell script
that hand-starts and sequences multiple processes around cert-readiness and startup-ordering races;
both hazards it existed for are gone once `agent` owns the whole lifecycle, so the entrypoint is now
just "bootstrap a certificate, then run `agent serve`." `store`'s `bwfs`/`catalogsync` now come up
via a `"storage"`-typed policy (`demo/policy-server/policies/storage/store.json`) instead of an
env-var-gated branch in that script.

## 2026-07-28 — agent: supervise bwfs for storage policies

`agent` is now the first consumer of the `"storage"` policy type added earlier today: every
reconcile tick it derives an ensure-running task (not a scheduled job) for each cached storage
policy targeting this node, and starts/crash-restarts/stops a `bwfs server` process accordingly —
`agent list-policies` shows each one alongside the three static policies and backup tasks.

**Breaking change:** `StoragePolicy.Hostname` (`policy-server`) is removed. Targeting which node
runs a storage policy is now `client_filters` — the same mechanism a backup policy already uses —
not a separate field; the corresponding proto field numbers are retired (`reserved`), not reused.

Also fixes `bwfs`, the one gRPC server in this repo that never wired `signal.NotifyContext`: a
`SIGTERM` previously killed it immediately instead of triggering the existing `GracefulStop()`
path, which matters now that `agent` routinely sends it one.

## 2026-07-28 — api-server/web: storage policy create/edit support

`api-server`'s `GET /policies` now accepts a `?type=` filter, and two new endpoints —
`POST /storage-policies` / `PUT /storage-policies/{id}` — let a caller create and edit `"storage"`
-typed policies, which `policy-server` has supported since the previous entry but which nothing
above it could write until now. The web UI gained a dedicated `Storage` section (`/storage`) —
list, create, and edit via a modal — kept fully separate from the existing backup-only `Policies`
section, which now requests `?type=backup` explicitly so it never renders a storage policy's blank
`rpo`/`destination` fields.

## 2026-07-28 — policy-server: add storage policy type

`policy-server` now supports a second policy type, `"storage"` (`hostname`, `port`, and an opaque
`config` JSON blob) alongside the existing `"backup"` type, for a future storage server to read.
Internally, `Policy` changed from one flat struct into an interface implemented by `BackupPolicy`
and `StoragePolicy`, each with its own schema, validation, and wire conversion — adding a further
type going forward is now a matter of writing one more such type and registering its parser.
`CreatePolicy` requires a `type` selector and writes into the matching `policies/<type>/`.

**Breaking change:** a `policies/<subfolder>/` whose name isn't a registered type (`"backup"` or
`"storage"`) is now skipped and logged at load time, rather than loaded generically as an earlier
design allowed — there's no schema to parse an unrecognized type's files into anymore.

## 2026-07-27 — web: parse and render job log lines; fix Vector line encoding

`/jobs/:job_id` now parses each line's underlying slog JSON instead of showing it raw: a
level-colored `[LEVEL] time binary@hostname: message` summary per line, with the remaining fields
(`job_id`, `event`, `status`, `duration`, `error`, etc.) collapsed behind a click. Lines that aren't
valid JSON still render as plain text. New `LogLine.vue` component and `utils/logLine.js` parser
take over rendering that was previously inlined in `JobDetailView.vue`.

This also fixes the underlying cause of the raw display: agent's Vector config shipped logs to
Loki with `encoding.codec: json`, which serializes the whole Vector event (host, file,
source_type, plus the `binary`/`job_id`/`event`/`status` fields the `add_binary_label` transform
attaches) as the stored line — burying the app's actual slog JSON one level deeper, double-encoded
inside a `message` string, so nothing on the frontend could sensibly parse it. Switched to
`encoding.codec: text`, which stores only the event's `message` field (the app's original log
line) since `binary`/`hostname`/`job_id`/`event`/`status` are already carried as Loki
labels/structured metadata.

## 2026-07-27 — E2E test suite rewrite

Removed all three existing e2e-tagged test suites (`src/e2e`'s Docker-built brfs/bwfs backup
flow, `cmd/issuer`'s real step-ca test, and `cmd/log-gateway`'s real Loki test) and replaced them
with a single minimal smoke test that requires the demo lab (`make demo-up`) to already be
running and checks its web UI responds. The old suites were slow (~3 min) and duplicated
infrastructure the repo already has in `demo/`; the new test trades that coverage for a fast,
simple check that the demo stack is genuinely reachable end-to-end.

## 2026-07-27 — build: docker-consolidation follow-up fixes

Three small fixes to the demo/control-plane Docker consolidation: `demo/local.conf` was missing
`clientmanager_admin_api_host`, which crash-looped `api-server` (and `web`, downstream of it) on
every fresh demo stack — added, mirroring the working control-plane config.
`deploy/build/Dockerfile`'s `ca` stage is renamed to `ca-demo`, matching the `issuer-demo`/
`issuer-controlplane` naming convention, since it's demo-only but was previously named ambiguously
enough that wiring the control-plane's `step-ca` service to it would have silently installed the
wrong entrypoint. `demo/README.md`'s stale "eight images" is corrected to 11, the current count of
build-based demo services. A fourth planned fix — adding `--mount=type=cache` to `src/e2e/Dockerfile`
— was dropped: that Dockerfile is built by `src/e2e/docker.go` via the classic, non-BuildKit Docker
Engine API, which rejects that syntax outright, and reworking `docker.go`'s build-response parsing
to support BuildKit was judged out of scope for this otherwise-small fix set.

## 2026-07-27 — build: consolidate demo/control-plane Dockerfiles

The 9 separate per-image Dockerfiles under `demo/` and `deploy/control-plane/` are replaced by one
`deploy/build/Dockerfile` with a single shared `builder` stage and nine final runtime stages,
selected via Compose's `build.target`. Previously, six of those Dockerfiles each ran their own
`make <binary list>`, and because each list differed, Docker's layer cache never matched between
them — `agent`, `certclient`, and `policyclient` were recompiled from scratch in up to six separate
builder stages on every `make demo-up` or control-plane build, despite producing byte-identical
binaries each time. The shared stage also gains persistent `--mount=type=cache` mounts for Go's
build and module caches, so even a source-code change only recompiles the affected packages instead
of the whole stage. The `control-plane-up` Makefile target now exports `COMPOSE_BAKE=true` before
its single `docker compose up -d`, so Compose builds the shared stage once and fans the six
control-plane images out in parallel. `demo/up.sh` still builds one service at a time (a
deliberate constraint from an earlier fix for OOM on memory-constrained hosts, unrelated to this
change) — it still benefits from the shared `builder` stage compiling once and being reused via
Docker's local layer cache across the eleven sequential builds, just without Bake's parallel
fan-out. No runtime behavior changes: every final image's installed packages, users, and entrypoint
are unchanged from before this refactor.

## 2026-07-20 — policy-server: policy type subfolders

Policies now live under a per-type subfolder — `$MP_CONFIG_PATH/policies/backup/*.json` today,
tagged `type: "backup"` — instead of flat under `policies/`. A policy's type is derived purely from
the name of the subfolder it's loaded from, never read from or written to the file itself, so a
future second policy type is just a new subfolder name with no schema migration for existing files.
`agent`'s backup-task derivation now skips any cached policy whose type isn't `"backup"`, laying the
groundwork for a future non-backup policy type to coexist without being misinterpreted as one. This
is a breaking on-disk layout change with no migration path: existing flat `policies/*.json` files
must be moved into `policies/backup/` before upgrading. `CreatePolicy`/`UpdatePolicy` are unchanged
otherwise — no `type` parameter yet, since there's nothing to choose between until a second type
exists. Policy and object-filter IDs are now derived from the type-qualified path (`<type>/<basename>`)
rather than the basename alone, so every existing policy's ID rotates on the next reload after this
change — same effect as the layout move itself, no migration by design — meaning each node's first
post-upgrade backup for a given path runs as a "never succeeded before" run.

## 2026-07-20 — web: consistency and best-practices refresh

`web` gains a small shared `components/ui/` layer (`BaseButton`, `PageHeader`, `StatusMessage`,
`DetailList`, `RepeatableFieldList`, `DataTable`) used across every view, replacing markup that had
been hand-copied and drifted slightly between pages. `simple-datatables` — a DOM-manipulating
library that sat outside Vue's reactivity — is replaced by `vue-good-table-next` everywhere it was
used (`/clients`, `/policies`, `/jobs`, `/catalog`); this also fixes a real bug in `/catalog`, where
clicking a row after sorting a column could open the wrong file's version modal, because the old
integration correlated a post-sort row index back into the pre-sort data. Every Pinia store's
repeated `loading`/`error`/try-catch boilerplate collapses onto one `withRequest` helper. Routing
switches from eagerly-imported, string-path routes to lazy-loaded, named routes, so internal links
no longer depend on hand-built path strings scattered across templates.

## 2026-07-20 — web: rewrite the catalog view's filtering, pagination, and versions modal

`/catalog` previously layered a custom server-side filter form and cursor-based Prev/Next
underneath `simple-datatables`'s own default search box and pagination, so the DataTable's search
only ever covered whichever page was currently loaded — looking broken to an operator searching for
an entry on a different page. It also relied on Vue click handlers inside a table that
`simple-datatables` owns and regenerates after mount, so the "Versions" button never opened its
modal. The view now requires a filter before searching, fetches every matching page before grouping
(so a file's versions are never split across a page boundary), uses `simple-datatables`'s own
pagination as the only pager (search disabled to avoid a second, narrower search box), opens the
versions modal via a row click wired through `simple-datatables`'s `datatable.selectrow` event, and
renders sizes human-readable. The modal is now its own `VersionsModal.vue` component. As a stopgap,
`demo/docker-compose.yml`'s `database`/`webserver` services now set an explicit `hostname:` so the
demo's `source_host` values read as intended instead of Docker's autogenerated container ID; the
underlying fix (trusting `bwfs`'s already mTLS-validated peer hostname instead of a self-reported
one) is tracked separately.

## 2026-07-19 — web: add client enrollment/revocation/metadata management

`web`'s `/clients` pages gain the write surface `clientmanager-admin-api` added: a `/clients/new`
enroll form, Revoke/Unrevoke and Re-enroll actions and a one-time token banner on the client detail
page, and inline add/remove editing for description, attributes, and SANs, each with its own
"Update" button enabled only while that section has an unsaved change. The clients list now uses
`simple-datatables` for client-side search/sort, matching `/jobs` and `/catalog`.

## 2026-07-19 — clientmanager-admin-api: network-reachable client enrollment/revocation/metadata writes

Added `clientmanager-admin-api`, a new gRPC daemon holding the CA provisioner password directly and
exposing the write operations `client-manager`'s CLI already had (issue/re-enroll enrollment tokens,
revoke/unrevoke, description/attribute/SAN management) over the network for the first time, via seven
new `api-server` REST endpoints under `/api/v1/clients`. Packaged in the same container as the
existing (unchanged, still read-only) `clientmanager-api` to avoid a second mesh enrollment, keeping
the two as separate processes for isolation. `client-manager`'s CLI remains available unchanged for
direct, on-host admin access.

## 2026-07-19 — web: group catalog entries into file versions

`/catalog` now groups entries within each loaded page into one row per distinct file (source host +
path), using `simple-datatables` for client-side search and sort, matching the Jobs page. A
"Versions" count on multi-version files opens a modal listing that file's other versions (capture
time, size, mode, mod time, job ID, store host). Grouping is scoped to the currently loaded page —
versions split across a Prev/Next page boundary aren't merged.

## 2026-07-19 — web: add a Jobs page

Adds `/jobs` and `/jobs/:job_id` to the `web` frontend, giving a browser view of `api-server`'s
`GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` endpoints. The jobs table uses
`simple-datatables` for client-side search, sort, and pagination over the fetched batch, rather
than a server-side filter form.

## 2026-07-19 — api-server: add GET /api/v1/jobs and /api/v1/jobs/{job_id}/logs

Added `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` to `api-server`, giving a fleet-wide
view of every job kind (backups, cert-refresh, policy-fetch) with start/end/source/state, plus
near-real-time per-job log tailing. Both are backed by Loki rather than a new database: `bwfs`,
`brfs`, and `agent` now tag each job's lifecycle boundary lines with `event`/`status`, and `agent`'s
bundled Vector lifts `job_id`/`event`/`status` into Loki structured metadata rather than plain
labels, avoiding the per-job stream-cardinality problem a naive `job_id` label would cause.
`log-gateway` gained a matching read-only proxy route onto Loki's query API alongside its existing
push proxy.

## 2026-07-18 — catalog: rename source_* to store_*, add a real source_host

The catalog's `source_node`/`source_seq`/`source_created_at`/`source_host` fields all actually
identified the `bwfs` node that replicated a batch, not the machine whose files were backed up —
confusing given "source" means the backup source everywhere else in the system. They're renamed to
`store_node`/`store_seq`/`store_created_at`/`store_host`. A new `source_host` is added in their
place: the real originating host, decoded once from each entry's metadata at sync time and
persisted as an indexed column, so it's independently filterable from `store_host`. Both are now
exposed through `ListEntries`, `GET /api/v1/catalog`, and the web frontend's Catalog view. No data
migration — existing `catalog.db` files should be deleted before running the updated binary.

## 2026-07-18 — policy-server: an admin write API for policies, proxied through api-server

`policy-server` gains `ListPolicies` (an unfiltered admin view, distinct from the existing
identity-scoped `GetPolicies`) and `CreatePolicy`/`UpdatePolicy`/`DeletePolicy` — each validates its
input the same way `parsePolicyFile` already does, atomically writes or removes the policy file, and
synchronously reloads its own in-memory cache before responding. `api-server` proxies all five as
`GET/POST/PUT/DELETE /api/v1/policies[/{id}]`, so backup policies can be listed and edited from a
browser instead of hand-editing JSON files on `policy-server`'s host. Policies remain flat files on
disk — no new database, no new persistent actor.

## 2026-07-18 — web: a small Vue/Pinia frontend for api-server

`web` is a new static single-page app (Vite + Vue 3 + Pinia + Vue Router + Tailwind CSS) providing
a browser UI over `api-server`'s two read-only resources: an enrolled-clients list/detail view and
a filterable, cursor-paginated catalog browser. It's served by nginx, which reverse-proxies `/api/*`
to `api-server` so the browser's requests stay same-origin — no CORS changes were needed on
`api-server` itself. A one-time bearer-token prompt (stored in `localStorage`) is the only auth,
matching `api-server`'s existing model. Wired into `demo/docker-compose.yml` as a new `web` service
on `localhost:8091`; not yet added to `deploy/control-plane/`.

## 2026-07-18 — web: add policy management UI (list, create, edit, delete)

The `web` frontend gains a policy management interface: a browseable list of all policies, detail
views for each policy's configuration (client filters, object filters, backup window), and forms
to create new policies or edit existing ones. All operations flow through `api-server`'s proxied
`/api/v1/policies[/{id}]` endpoints (POST, GET, PUT, DELETE), backed by `policy-server`'s in-memory
cache and atomic file writes. The Pinia `policies` store handles CRUD state, three new view
components (`PoliciesListView`, `PolicyDetailView`, `PolicyFormView`) render each page, and
Vue Router wires `/policies`, `/policies/:id`, `/policies/new`, and `/policies/:id/edit` routes.

## 2026-07-14 — api-server: unified read-only REST API for clients and catalog

`api-server` exposes a REST API in front of the control plane's client and catalog data — the first
REST surface in a system that's otherwise entirely gRPC-over-mTLS. `GET /api/v1/clients[/{hostname}]`
and `GET /api/v1/catalog` (filterable by source host and a path-pattern substring, cursor-paginated)
are backed by two gRPC additions: a new `clientmanager-api` daemon (mirroring `issuer`'s existing
pattern of opening `client-manager`'s SQLite file directly, rather than adding a network surface to
`client-manager` itself, which was a deliberate security property) and a new `ListEntries` RPC on
`catalog` (previously write-only). REST access is guarded by a single shared bearer token — no RBAC
yet, matching this phase's scope.

## 2026-07-13 — Deterministic IDs for policies and object filters

`policy-server` now computes a deterministic ID for every policy and every object filter within
it, derived from the policy file's name and each filter's position — never read from or written to
the policy JSON files themselves. `agent` uses the object-filter ID to make its backup task/job IDs
collision-proof: two object filters sharing a `path` within one policy (e.g. one with `include`,
one with `exclude`, both scoped to the same root) previously shared one `agent-state.json` entry
and one in-flight-run slot; each now gets its own. Upgrading resets every existing backup task's
history once (last-success/backoff tracking), since the task-ID format changed — a one-time,
low-cost consequence of fixing the underlying collision.

## 2026-07-13 — Include/exclude glob patterns on object filters

Backup policies' `object_filters` entries (and `brfs` itself) can now carry `include`/`exclude`
glob-pattern lists alongside `path`, letting a policy narrow what gets backed up instead of always
sweeping a path recursively. `brfs` gained `--include`/`--exclude` flags and applies them during
its own directory walk — excludes prune whole matched subtrees, includes act as a files-only
whitelist. `policy-server`, `policyclient`, and `agent` all carry the new fields through
end-to-end; see `docs/process/filesystem-backup.md` for the full flow.

## 2026-07-11 — Policy server consumer wiring and demo content

`policy_server_host` — missing from every `local.conf` since `policy-update` shipped, silently
failing that job every reconcile cycle everywhere — is now set in `demo/local.conf` and both
`deploy/control-plane/catalog/local.conf` and `deploy/control-plane/policy-server/local.conf`. The
demo lab's single generic `client` node is renamed to `database` and a new `webserver` node
(labeled `role=web`) joins it; `policy-server` ships with three example policies covering every
`client_filters` selection mechanism (explicit multi-hostname, single hostname, label) against
themed fixture content (`/var/log/audit`, `/var/lib/dbdata`, `/var/www/html`).

## 2026-07-11 — Agent backup state hygiene: pruning and last-error tracking

`agent-state.json` entries for backup tasks whose policy or path has been removed from
`policies-cache.json` are now pruned automatically, gated on a confirmed-good cache read so a
transient unreadable or corrupt cache file can never be mistaken for "everything was removed" and
wipe live backoff/RPO history. `PolicyState` also gains `LastError`, the most recent failure's
message, cleared on the next success and surfaced as a new `ERROR` column in `agent list-policies`.

## 2026-07-11 — Agent acts on cached backup policies

`agent` closes the loop left open by the `policy-update` job: it now derives one backup task per
`(cached policy, object_filters path)` pair and runs `brfs` for each, scheduled by that policy's
`backup_window` (cron) and `rpo` (staleness) — both must hold, a slot must be open and the path
must actually be stale. `policy-server`'s policy schema gains a `destination` field (the target
`bwfs`), threaded through `policyclient`'s cache unchanged in shape otherwise. Backup execs run in
background goroutines bounded by a new `MaxConcurrentBackupJobs` config key, so a long backup never
delays credential refresh; a `SIGTERM` cleanly terminates in-flight backups rather than orphaning
them.

## 2026-07-10 — Agent policy-update job

`agent` gains a third standard job, `policy-update`, alongside its existing credential-refresh
policies: a new `policyclient fetch` binary pulls this node's applicable backup policies from
`policy-server` and atomically caches them as `policies-cache.json`, authenticated with the node's
existing operating credential. A new shared `common/atomicfile` helper backs the atomic write
(temp file + rename), replacing what had been a copy of the same logic private to `agent`. A
failed fetch leaves the previous cache untouched, the same fail-safe direction used everywhere
else in this codebase. Deliberately stops at fetching and caching — nothing yet reads the cache to
schedule or run a backup; that remains separate, later work.

## 2026-07-10 — Deploy policy-server as an enrolled control-plane node

`policy-server` (added earlier this same day) now has a real deployment story in both
`deploy/control-plane/` and `demo/`, following `catalog`'s existing pattern exactly: its own
Docker image bundling `agent`/`certclient`, enrolled through the same bootstrap-token flow every
node uses, with continuously-refreshed bootstrap and operating credentials rather than a one-shot
identity. It differs from `catalog` in one structural way — no local database, so no
`STORAGE_PATH`/positional CLI argument, and its entrypoint needs no directory-creation step since
`policy-server` already creates its own `policies/` directory on startup. Still no client-side
consumer of `GetPolicies` in either environment — that remains separate, later work.

## 2026-07-10 — Backup policy serving (policy-server)

Added `policy-server`, a new control-plane binary that serves backup policies — static,
operator-authored JSON files under `$MP_CONFIG_PATH/policies/` — filtered to whatever a requesting
client's hostname and attribute labels match. It holds no database and calls no other service: a
client's attribute labels are read directly off its own mTLS certificate, since `issuer` already
embeds them there as a custom X.509 extension on every operating certificate it mints. Policies are
cached in memory and hot-reloaded as a single atomic swap whenever an operator touches a
`policies/.changed` sentinel file after editing one or more policy files. A client-side consumer
(`agent` fetching and acting on policies) is deliberately deferred to later, separate work.

## 2026-07-06 — Self-contained demo lab environment, updated for the current architecture

The 2026-07-03 demo lab design predated `issuer`, the two-tier bootstrap/operating credential
split, and `client-manager` (it assumed the now-retired `certrequest`) — it could no longer be run
as written. `demo/` now stands up `ca`, `issuer`, `catalog`, and two backup-capable nodes
(`client`, `store`) with one command (`make demo-up`), fully self-contained: no host ports
published, no host bind-mounts of secrets, and no dependency on `deploy/control-plane`'s own
compose file or volumes. `catalog`'s image is reused directly rather than duplicated; the CA's
leaf template is read straight from `deploy/control-plane/ca/templates/leaf.tpl` at build time so
the two deployments can't silently drift apart. Building this as a genuine, fully-automated cold
start (rather than a hand-run walkthrough) surfaced and fixed several previously-unknown gaps that
never showed up in the unit/e2e suites or manual deployments: `issuer`'s self-mint requesting a
longer certificate duration than step-ca's default provisioner claims allow (`deploy/control-plane/ca/entrypoint.sh`
has the identical gap — no `--x509-max-dur` flag on its provisioner — so a genuinely fresh
control-plane deployment would hit the same 90-day-request-vs-24h1m-default rejection; not fixed by
this change); `issuer` running as root and corrupting shared SQLite file ownership on a cold boot; `ConnectionTimeOutSec` and
`FileLockTimeoutSec` both lacking defaults in `config.ParseConfig`, silently zeroing out
connection/file-lock timeouts when a deployment's `local.conf` doesn't set them explicitly (as
`deploy/control-plane`'s own config files also don't — a latent gap there too, not fixed by this
change). `deploy/control-plane/catalog/Dockerfile` also gains the `sqlite3` CLI, which its image
never actually had despite being the documented way to inspect its database directly.

## 2026-07-05 — Bootstrap credentials can no longer reach bwfs/catalog

`common/mtls` trusted any CA-signed certificate regardless of which of the two credential tiers
issued it — a leaked bootstrap credential (whose only intended use is authenticating to `issuer`)
could authenticate to `bwfs`/`catalog` exactly as well as an operating credential, something
`docs/SECURITY.md` already flagged as a known, unenforced gap. Bootstrap certificates now carry
`extKeyUsage: ["clientAuth"]` only plus a custom Extended Key Usage marker (`EKUIssuerCaller`, OID
`1.3.6.1.4.1.61183.1.3`); `common/mtls.LoadServerCredentials` (used by `bwfs`/`catalog`) rejects any
peer certificate carrying that marker, and a new `mtls.LoadIssuerServerCredentials` (used only by
`issuer`) rejects any peer certificate that doesn't. Certificates issued before this change lack
the marker and won't pass either check — the demo lab (`deploy/control-plane`) needs its CA and
client-manager volumes wiped and the enroll walkthrough re-run after upgrading.

## 2026-07-05 — Attributes now land in the certificate, not just the Sign request

`issuer` has passed `attribute` key/value pairs to the CA via `TemplateData` on every `Sign` call
since the operating-certificate work landed, but step-ca's default template ignored the field
entirely — the data reached the wire and was silently dropped. A new CA-side template
(`deploy/control-plane/ca/templates/leaf.tpl`, wired in by `ca/entrypoint.sh`) now embeds those
attributes as a real, non-critical X.509 extension (OID `1.3.6.1.4.1.61183.1.1`, JSON-encoded,
present only when a client has attributes set), closing the gap phase 2's design explicitly
deferred. Nothing yet reads or enforces this extension — that remains separate, later work — but
the round-trip from `client-manager attribute set` to an actual certificate field now provably
works end to end, per a new Docker-backed e2e assertion in `src/cmd/issuer/e2e_test.go`.

## 2026-07-05 — Fixed the control-plane docker-compose demo; issuer self-mints its own identity (phase 2d)

Phase 2c's `certclient`/`agent` split broke `deploy/control-plane`'s docker-compose demo: `catalog`
crash-looped, since its container invoked bare `certclient` the old, single-shot way, which no
longer matched the new two-tier bootstrap/operating-refresh model. Fixing this properly meant
closing a gap phase 2c left open — `issuer` itself had no mTLS identity of its own, and couldn't
get one the normal way, since obtaining one via `certclient`/`agent` would mean either a second
daemon running on the CA host or `issuer` depending on itself. `issuer` now mints and signs its own
server certificate directly at startup, reusing the CA provisioner access it already holds for
`RequestOperatingCert`, and re-mints it on an internal ticker while running
(`IssuerSelfCertTTLSec`/`IssuerSelfCertRefreshIntervalSec`, defaulting to a 90-day certificate
refreshed daily); a startup failure is fatal, but a refresh failure just logs and keeps the
existing certificate. `catalog`'s image now bundles `agent` (not just `certclient`), so it runs as
an ordinary `agent`-managed enrolled node with continuously-refreshed bootstrap and operating
credentials for as long as its container runs, instead of a one-shot bootstrap redeemed only at
container start. `docker-compose.yml` gained an `issuer` service and a real, persistent, shared
`client-manager` database volume, so the demo's enrollment commands actually persist across runs
instead of writing to a throwaway container filesystem. `deploy/control-plane/README.md` was
rewritten around a real, verified enroll→connect→revoke smoke test, replacing the stale
instructions that had gone unnoticed as broken.

## 2026-07-05 — Agent-driven operating-certificate refresh (phase 2c)

`agent` now obtains and refreshes operating certificates through `issuer` on a schedule, closing
the loop phase 2's design opened: revocation, live attributes, and SAN changes now actually reach
a node automatically, end to end, without an operator re-enrolling it. This required splitting a
node's mTLS identity into a two-tier credential model — a long-lived bootstrap credential
(`bootstrap.crt`/`bootstrap.key`, obtained/renewed via `certclient bootstrap`/`renew`) used only to
authenticate to `issuer`, and the short-lived operating credential (`client.crt`/`client.key`,
everything else's mTLS identity) obtained fresh via the new `certclient operating-refresh`
subcommand. `agent` accordingly runs two independent-cadence, config-driven policies —
`bootstrap-refresh` and `operating-refresh` — instead of its previous single `cert-refresh` policy.
While wiring this, a design gap surfaced and was closed: step-ca's OTT provisioner validates a
CSR's requested SANs against the signing token's authorized set with an exact match, not a subset,
so a new `issuer.DescribeSANs` RPC was added for a node to learn its own current SAN alias list
before building its CSR — without it, SAN propagation silently failed to actually reach issued
certificates. Also added: `docs/SECURITY.md`, a canonical reference for the mTLS/two-tier-credential/
revocation model, consolidating what had been scattered across dated design docs.

## 2026-07-05 — Operating-certificate issuance (issuer)

Added `issuer`, a new CA-host-local binary that mints short-lived "operating certificates" for
already-enrolled nodes and enforces revocation on every issuance, refusing outright for a revoked
or unknown hostname, by sharing `client-manager`'s SQLite database directly rather than querying it
over the network. Attributes are embedded via the sign request's `TemplateData` field rather than
custom JWT claims, since the CA client library's signing key is unexported and inaccessible outside
its own package. `client-manager`'s `list`/`show` now display real `last_seen` data instead of a
placeholder. Agent-side integration (actually calling `issuer` on a schedule) and a CA-side custom
certificate template (to actually bake attributes into certificate extensions) are deliberately
deferred to a later, separate piece of work.

## 2026-07-03 — Node agent v1 (embedded cert-refresh reconciliation)

Added `agent`, a node-level process intended to replace the bare cron entry for `certclient` with a
small reconcile loop: on a configurable interval it checks whether the (currently single,
compiled-in) `cert-refresh` policy is due, execs `certclient` if so, and records the outcome to a
local JSON cache — failures back off with jittered delays instead of retrying every tick. `agent
list-policies` reads that same cache to show each policy's health and estimated next run without
needing a running daemon. Also added `var_path` to `common/config`, a general directory for this
kind of runtime/variable data, defaulting to the running binary's own directory when unset. This
is the first concrete slice of a broader `agent` design that will later add queue-dispatched and
policy-server-fetched work on top of the same reconcile primitives.

## 2026-07-03 — Backup catalog service (catalog)

Added `catalog`, the receiving end of `catalogsync`'s replication pipeline: a standalone gRPC
service that persists replicated `bwfs` file-version batches to its own SQLite database, keyed by
`(source_node, job_id, object_id)` — `source_node` comes from the CA-verified mTLS client
certificate, never the payload, so a single catalog can safely receive from a fleet of `bwfs`
nodes. `catalogsync` gained a real `GrpcSender` (config-gated by `catalog_host`/`catalog_port`),
replacing the `LoggingSender` stand-in whenever a catalog is configured and reachable. `catalog`
ships its own `docker compose` deployment (`catalog/`), using the same `certclient`-bootstrapped
mTLS identity every other node uses. Also fixed a pre-existing gap in `common/mtls`: server and
client identity certificates are now re-read from disk on every new connection instead of once at
startup, so a certificate renewed by a scheduled `certclient` run is picked up without restarting
the long-running process — this benefits `bwfs`/`brfs`/`rwfs` too, not just this new pair.

## 2026-07-02 — Async catalog replication (catalogsync)

Added `catalogsync`, a new standalone component that tails a `bwfs` node's `file_versions` table
and forwards new rows to a future backup catalog, independently of `bwfs`'s own availability.
`catalogsync` opens `bwfs`'s SQLite database strictly read-only and tracks its own replication
progress in a small local cursor file, retrying with backoff whenever the catalog (represented
today by a logging stand-in `Sender`) is unreachable — nothing is marked replicated until a batch
is confirmed sent, so an outage or restart never loses data. This required replacing
`file_versions`' synthetic `UUID` primary key with a real `INTEGER PRIMARY KEY AUTOINCREMENT`
`seq` column (immune to the row-number reuse a bare SQLite `rowid` allows after a failed job's
rows are purged) and its natural `(job_id, object_id)` identity for external consumers.

## 2026-07-02 — Backup job completion verification

`bwfs` no longer treats a job as finished just because its streams closed. Added a `BackupCommit`
RPC: after a backup run's streams close, `brfs` submits a hash of the files it believes it sent,
and `bwfs` independently recomputes the same hash from its own catalog before marking the job
`success` — a mismatch marks it `failure` and purges that job's incomplete catalog entries. A
background watchdog now fails jobs that go silent past a configurable timeout (`JobTimeoutSec`,
default 30s), and `bwfs` reconciles any jobs left `in_progress` by an unclean shutdown on restart.
`backup_jobs` gained an explicit `status` column (`in_progress`/`success`/`failure`) as the source
of truth for job outcome.
