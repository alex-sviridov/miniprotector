# web

A small browser UI over [api-server](./api-server.md)'s REST API — lists enrolled clients,
browses catalog entries, manages backup policies and storage policies (list/create/edit/delete for
each, in separate sections), and browses fleet-wide jobs and their logs. **Not a mesh member:**
unlike every other control-plane component, `web` has no mTLS identity of its own; it's a static Vue
single-page app served by nginx, which reverse-proxies `/api/*` to `api-server` so the browser's
calls stay same-origin (no CORS changes were needed on `api-server`).

## Usage

On first load, the app prompts for `api-server`'s bearer token and stores it in the browser's
`localStorage`; every request thereafter carries `Authorization: Bearer <token>`. No token means
no data — there's no read-only "guest" mode.

## Pages

- `/` — placeholder landing page
- `/clients` — every enrolled client (hostname, revoked, last seen), with client-side search/sort
  via `vue-good-table-next`, linking to:
- `/clients/new` — enroll a new client (hostname + optional SANs); shows the resulting one-time
  enrollment token on the new client's detail page after redirecting
- `/clients/:hostname` — one client's full record (SANs, attributes, descriptions), with actions to
  revoke/unrevoke, re-enroll (shows a fresh one-time token), and inline add/remove editing of
  description, attributes, and SANs, each gated by its own "Update" button that enables only once
  that section has a pending change
- `/catalog` — catalog entries, filterable by real source host, store host (the `bwfs` node that
  replicated the entry), and a path-pattern substring; at least one filter must be filled in before
  Search is enabled, since the catalog has no natural bound the way `/jobs`' 24h window does. On
  search, every matching page is fetched (the catalog API is cursor-paginated) before entries are
  grouped into one row per distinct file (source host + path) and handed to a client-side
  sortable/paginated table (`vue-good-table-next`) — grouping over the complete result set means a
  file's versions are
  never split across a page boundary. Sizes render human-readable (KB/MB/...); a "Versions" count
  on multi-version files opens a modal (click anywhere on that row) listing that file's other
  versions.
- `/policies` — every policy (name, RPO, destination), with a "New backup" action opening a form modal for creating new policies (fields: name, RPO, backup window, client filters, object filters, destination (a required select over `/storage`'s storage policies, replacing free-text host:port entry)) and clickable policy names navigating to each policy's detail view. The modal (`BackupPolicyFormModal` in `components/backup_policies/`) offers two primary actions: "Save" to persist a new or edited policy, or "Run now" to execute the policy's filters immediately as a one-time ad-hoc backup job (the ad-hoc policy auto-sets its `disabled_at` to expire after its configured timeout, 1h by default) and redirects to `/jobs`, where the resulting job(s) can be found and opened for their log lines — same modal-plus-detail-page pattern as `/storage` below. Linking to:
- `/policies/:id` — one policy's full record, in two tabs built on a reusable `Tabs` component
  (`components/ui/Tabs.vue`, active tab synced to `?tab=details`/`?tab=checkins` so either can be
  linked directly): `Details` (the default — client filters, object filters, backup window) and
  `Check-ins` (`components/policies/PolicyCheckins.vue` — every host that has received this policy
  from `policy-server`, each with its most recent check-in time, and a manual Refresh button that
  re-fetches the policy). Edit and Delete buttons sit at the page level, outside the tabs; Edit opens
  `BackupPolicyFormModal` pre-filled with the policy's current values (both "Save" and "Run now" are
  available here). No separate `/policies/new` or `/policies/:id/edit` routes.
- `/storage` — every storage policy (name, target hostname, port, storage type), with a "New Storage
  Policy" action opening `StorageEditModal` (fields: name, target hostname, port, storage type —
  `filesystem` only today — and, when `filesystem` is selected, a filesystem path) and clickable
  policy names navigating to each storage policy's detail view. "Target hostname" submits as
  `client_filters.hostnames` — the same targeting mechanism `/policies` uses, not a separate field.
  Its store (`stores/storagePolicies.js`) is no longer read exclusively by `/storage`: `/policies`'
  form modal also reads it to populate its destination select (see the `/policies` bullet above).
  `/policies` itself still requests only `type=backup` policies, so a storage policy never appears
  in its list. Linking to:
- `/storage/:id` — one storage policy's full record (target hostname, port, storage type, path), in
  the same `Details`/`Check-ins` tab layout as `/policies/:id` above, with Edit (opens
  `StorageEditModal`) and Delete buttons. Editing has moved here from the list — `/storage`'s name
  column now navigates instead of opening the modal directly. Both policy detail pages share their
  component folder for this (`components/policies/`); `/storage` otherwise keeps its own
  (`components/storage/`).
- `/jobs` — every job across the fleet from the last 24h (job ID, kind, source host, store host,
  started/finished time, state), with client-side search, sort, and pagination via
  `vue-good-table-next` (also used on `/catalog`, `/clients`, and `/policies`), linking to:
- `/jobs/:job_id` — one job's log lines from the last 24h, fetched once on page load (no
  live-tail/polling); each line is parsed from its underlying JSON via `LogLine.vue` into a
  level-colored `[LEVEL] time binary@hostname: message` summary, with the remaining fields
  (`job_id`, `event`, `status`, etc.) collapsed behind a click — a line that isn't valid JSON
  falls back to plain text

## Local development

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run dev
```

The dev server proxies `/api` to `http://localhost:8090` — run `api-server` locally (or via
`make control-plane-up`) alongside it.

## Deployment

Ships as the `web` service in `demo/docker-compose.yml`, published at `http://localhost:8091`. Not
yet wired into `deploy/control-plane/`.

## Building

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run build
```

## See Also

- [api-server](./api-server.md) — the backend this UI is a client of
- [REST API v1](../api/rest-v1.md)
- [Design: web frontend](../superpowers/specs/2026-07-18-web-frontend-design.md)
- [Design: web frontend consistency & best-practices refresh](../superpowers/specs/2026-07-20-web-frontend-refresh-design.md)
- [Architecture](../ARCHITECTURE.md)
