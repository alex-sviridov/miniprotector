# web

A small browser UI over [api-server](./api-server.md)'s REST API — lists enrolled clients,
browses catalog entries, manages backup policies (list/create/edit/delete), and browses
fleet-wide jobs and their logs. **Not a mesh member:** unlike every other control-plane
component, `web` has no mTLS identity of its own; it's a static Vue single-page app served by
nginx, which reverse-proxies `/api/*` to `api-server` so the browser's calls stay same-origin (no
CORS changes were needed on `api-server`).

## Usage

On first load, the app prompts for `api-server`'s bearer token and stores it in the browser's
`localStorage`; every request thereafter carries `Authorization: Bearer <token>`. No token means
no data — there's no read-only "guest" mode.

## Pages

- `/` — placeholder landing page
- `/clients` — every enrolled client (hostname, revoked, last seen), with client-side search/sort
  via `simple-datatables`, linking to:
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
  grouped into one row per distinct file (source host + path) and handed to `simple-datatables` for
  client-side sort/pagination — grouping over the complete result set means a file's versions are
  never split across a page boundary. Sizes render human-readable (KB/MB/...); a "Versions" count
  on multi-version files opens a modal (click anywhere on that row) listing that file's other
  versions.
- `/policies` — every policy (name, RPO, destination), linking to:
- `/policies/:id` — one policy's full record (client filters, object filters, backup window)
- `/policies/new` — create a new policy
- `/policies/:id/edit` — edit an existing policy
- `/jobs` — every job across the fleet from the last 24h (job ID, kind, source host, store host,
  started/finished time, state), with client-side search, sort, and pagination via
  `simple-datatables` (also used on `/catalog`), linking to:
- `/jobs/:job_id` — one job's raw log lines from the last 24h, fetched once on page load (no
  live-tail/polling)

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
- [Architecture](../ARCHITECTURE.md)
