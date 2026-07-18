# Design: web — a small Vue/Pinia frontend for api-server

**Date:** 2026-07-18
**Status:** Approved for planning

## Problem

`api-server` (see [Design: api-server](2026-07-14-api-server-design.md) and
[REST API v1](../api/rest-v1.md)) exposes read-only REST endpoints for enrolled clients and catalog
entries, but the only way to use it today is `curl` or an admin tool with a bearer token. There is no
browser-based way to look at "what clients are enrolled" or "what's in the catalog." This adds a
small single-page frontend, `web`, that is nothing more than a thin UI over the two existing
read-only resource groups api-server already provides.

## Scope

**In scope (v1):**
- A new `web/` app: Vite + Vue 3 (`<script setup>`) + Pinia + Vue Router + Tailwind CSS
- A persistent sidebar with links to the two resource pages (Clients, Catalog)
- `/clients` — list of enrolled clients
- `/clients/:hostname` — single client detail (404 handling)
- `/catalog` — filter form (`source_host`, `pattern`) + paginated results table
- `/` — minimal placeholder/welcome page (no data fetching)
- A one-time bearer-token prompt, stored in `localStorage`, attached to every API request
- `web/Dockerfile` + `nginx.conf` wired into `demo/docker-compose.yml` so `make demo-up` serves the
  built frontend end-to-end, reverse-proxying `/api/*` to `api-server`

**Out of scope (v1):**
- Any write functionality — the frontend only ever issues `GET` requests, matching api-server's
  read-only v1 surface
- Login/user accounts/RBAC — the single shared bearer token is the only auth, matching api-server's
  own model
- CORS changes to `api-server` — the frontend never calls api-server cross-origin; nginx proxies
  same-origin instead (see "API access" below)
- True numbered pagination (page 1, 2, 3...) — the catalog RPC only supports cursor pagination
  (`starting_after` + `has_more`, no total count), so the UI only offers Prev/Next
- Any component beyond `api-server`'s existing `/api/v1/clients` and `/api/v1/catalog` endpoints
  (policy-server, jobs, etc. are future pages, added only once api-server exposes them)
- Deployment beyond the `demo/` compose stack (no `deploy/control-plane/` wiring in v1)

## Architecture

```
 browser --same-origin--> [nginx: serves web/dist, proxies /api/* --> api-server:8090]
```

`web` is a static single-page app built by Vite and served by nginx in its own container. nginx does
two jobs: serve the built `index.html`/JS/CSS bundle for any non-`/api` path, and reverse-proxy
`/api/*` requests through to `api-server` unchanged (including whatever `Authorization` header the
browser attached). This makes the browser's view same-origin, so no CORS headers need to be added to
`api-server`, and the Authorization header set by the frontend passes straight through to api-server's
existing bearer-token check untouched.

## Components

**Layout:** `App.vue` renders a persistent sidebar (links: Clients, Catalog) and a `<router-view>`
for the active page.

**Routes (Vue Router, `web/src/router.js`):**
- `/` — `HomeView.vue`, minimal placeholder/welcome page, no data fetching
- `/clients` — `ClientsListView.vue`
- `/clients/:hostname` — `ClientDetailView.vue`
- `/catalog` — `CatalogView.vue`

**API client (`web/src/api/client.js`):** a thin wrapper around `fetch`, base path `/api/v1`.
Attaches `Authorization: Bearer <token>` (read from the `auth` store) to every request. On `401`,
clears the stored token and routes to the token prompt. Other non-2xx responses throw with the
`{"error": "..."}` message from the body, surfaced inline by the calling view.

**Pinia stores (`web/src/stores/`):**
- `auth.js` — holds the bearer token (persisted to `localStorage`); exposes `setToken`/`clearToken`;
  `App.vue` shows a full-screen token-entry gate whenever no token is stored (first load, or after a
  `clearToken` triggered by a `401`)
- `clients.js` — `fetchAll()` (`GET /clients`), `fetchOne(hostname)` (`GET /clients/{hostname}`,
  caches by hostname so navigating list → detail → list doesn't always refetch)
- `catalog.js` — holds current filter values (`source_host`, `pattern`), a cursor stack (list of
  `starting_after` values visited, enabling Prev), current page's `entries` + `has_more`; exposes
  `search()` (resets cursor stack, fetches page 1), `nextPage()`, `prevPage()`

## Data Flow

1. On app load, `App.vue` checks the `auth` store; if no token, renders the token-entry gate and
   blocks the rest of the UI.
2. Once a token exists, the sidebar and current route render normally. Each view's `onMounted` calls
   its store's fetch action.
3. `ClientsListView` calls `clients.fetchAll()`; each row links to `/clients/:hostname`.
4. `ClientDetailView` calls `clients.fetchOne(route.params.hostname)`; renders `NotFoundView`-style
   inline message on 404.
5. `CatalogView`'s filter form calls `catalog.search()` on submit; Prev/Next buttons call
   `catalog.prevPage()`/`nextPage()`. Next is disabled when `has_more` is false; Prev is disabled when
   the cursor stack is empty (i.e., on page 1).

## Error Handling

- **No/invalid token:** token-entry gate blocks the UI; a `401` from any request clears the stored
  token and re-shows the gate with an inline "invalid token" message.
- **404 (unknown client hostname):** `ClientDetailView` shows an inline "client not found" message
  instead of a blank page.
- **400 (bad catalog query params):** surfaced inline above the filter form; the UI itself only ever
  sends values it validates client-side (numeric `limit`/`starting_after`), so this mainly guards
  against unexpected backend behavior.
- **Network/5xx errors:** surfaced inline as a generic error message with the response body's
  `error` text when available; no retry logic — the user can re-submit/reload.

## Testing

- **Pinia stores:** Vitest unit tests for `clients.js` and `catalog.js` — fetch success, 404/401/500
  handling, and `catalog.js`'s cursor-stack Prev/Next logic (including the has_more/disabled-button
  edge cases) — against a mocked `api/client.js`.
- **Manual/integration:** `make demo-up`, then exercise the full path in a browser: enter the demo
  lab's bearer token, browse `/clients`, drill into a client, browse `/catalog` with filters and
  Prev/Next pagination. This is the end-to-end smoke test; no browser e2e framework is added, matching
  "keep it small and simple."

## Deployment

- `web/Dockerfile` — multi-stage build: `npm ci && npm run build` (Vite) into a final `nginx:alpine`
  stage serving `web/dist`.
- `web/nginx.conf` — serves static files for any path, reverse-proxies `/api/*` to
  `api-server:8090` inside the compose network.
- `demo/docker-compose.yml` — new `web` service (`depends_on: api-server`), built from
  `web/Dockerfile`, exposed on its own host port (e.g. `8091`).
- No changes to `deploy/control-plane/` in v1 (see "Out of scope").

## Documentation

- New `docs/components/web.md` — role, how it fits the mesh (a plain nginx-served static app, not a
  mesh member — it has no mTLS identity of its own), how to run it (`make demo-up`, or
  `npm run dev` against a locally running `api-server` for frontend-only development).
- `README.md` — add `web` to the component list and a short Quick Start note.
- `docs/ARCHITECTURE.md` — add `web`/nginx to the component list and diagram as the system's first
  browser-facing UI, sitting in front of `api-server`.
- `demo/README.md` — document the new `web` service's port and that it needs the same bearer token
  as direct `curl` access.
- `CHANGELOG.md` — one dated entry.

## Out of scope

- No write endpoints or mutating UI anywhere.
- No login/RBAC beyond the single shared bearer token.
- No CORS changes to `api-server`.
- No true numbered pagination.
- No `deploy/control-plane/` wiring (demo-only for v1).
- No pages for components beyond `client-manager`/`catalog` (mirrors api-server's own v1 scope).
