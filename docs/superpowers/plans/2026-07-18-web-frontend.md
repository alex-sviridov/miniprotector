# Web Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `web/`, a small Vue 3 + Pinia + Vue Router + Tailwind CSS single-page app that lets
someone browse `api-server`'s two read-only resources (enrolled clients, catalog entries) in a
browser, deployed alongside the rest of the stack in `demo/docker-compose.yml`.

**Architecture:** A static SPA built by Vite, served by an nginx container that also reverse-proxies
`/api/*` to `api-server:8090` (keeping the browser same-origin, so `api-server` needs no CORS
changes). A Pinia `auth` store holds a bearer token entered once via a UI gate and persisted to
`localStorage`; `clients` and `catalog` stores wrap `api-server`'s two resource groups behind a thin
`fetch` client. Three routed pages (`/clients`, `/clients/:hostname`, `/catalog`) plus a placeholder
`/` sit behind a persistent sidebar.

**Tech Stack:** Vite, Vue 3 (`<script setup>`), Pinia, Vue Router 4, Tailwind CSS v4
(`@tailwindcss/vite`), Vitest + `@vue/test-utils` + `@pinia/testing` for tests, `node:20-alpine` +
`nginx:1.27-alpine` for the Docker build.

## Global Constraints

- All new frontend code lives under `web/`. Only `demo/docker-compose.yml`, `demo/README.md`,
  `README.md`, `docs/ARCHITECTURE.md`, `CHANGELOG.md`, `.gitignore`, and a new
  `docs/components/web.md` are touched outside `web/` — no Go code changes anywhere.
- No local Node.js toolchain is installed on the dev host. Every `npm`/`vite`/`vitest` command in
  this plan runs inside Docker via:
  `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine <cmd>`
  Run all such commands from the repository root (`/home/alex/miniprotector`).
- Read-only frontend: only `GET` requests are ever issued to `api-server`. No write endpoints exist
  to call.
- No CORS headers are added to `api-server`; nginx reverse-proxies `/api/*` so the browser sees a
  same-origin API.
- Pagination is Prev/Next only, driven by a client-side stack of `starting_after` cursors — no page
  numbers (the catalog API has no total count).
- The bearer token is entered once via a UI prompt, stored in `localStorage`, and attached to every
  API request. No login/RBAC.
- No changes to `deploy/control-plane/` — this plan only wires `web` into `demo/docker-compose.yml`.
- Spec: `docs/superpowers/specs/2026-07-18-web-frontend-design.md`.

---

### Task 1: Scaffold the Vite + Vue + Pinia + Router + Tailwind app

**Files:**
- Create: `web/package.json`
- Create: `web/vite.config.js`
- Create: `web/vitest.config.js`
- Create: `web/index.html`
- Create: `web/src/main.js`
- Create: `web/src/style.css`
- Create: `web/src/App.vue`
- Create: `web/src/router.js`
- Create: `web/src/views/HomeView.vue`
- Modify: `.gitignore`

**Interfaces:**
- Produces: `web/src/router.js` exports `router` (a Vue Router instance), imported by
  `web/src/main.js` and extended by later tasks (Tasks 6-8 add routes to its `routes` array).
- Produces: `web/src/App.vue`, replaced wholesale by Task 9 once `TokenGate`/`Sidebar` exist.

- [ ] **Step 1: Write `web/package.json`**

```json
{
  "name": "miniprotector-web",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "test": "vitest run"
  },
  "dependencies": {
    "vue": "^3.5.0",
    "vue-router": "^4.4.0",
    "pinia": "^2.2.0"
  },
  "devDependencies": {
    "@vitejs/plugin-vue": "^5.1.0",
    "@tailwindcss/vite": "^4.0.0",
    "tailwindcss": "^4.0.0",
    "vite": "^6.0.0",
    "vitest": "^2.1.0",
    "@vue/test-utils": "^2.4.0",
    "@pinia/testing": "^0.1.7",
    "jsdom": "^25.0.0"
  }
}
```

- [ ] **Step 2: Write `web/vite.config.js`**

```js
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  server: {
    proxy: {
      '/api': 'http://localhost:8090',
    },
  },
})
```

- [ ] **Step 3: Write `web/vitest.config.js`**

```js
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  test: {
    environment: 'jsdom',
    globals: true,
  },
})
```

- [ ] **Step 4: Write `web/index.html`**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Miniprotector</title>
  </head>
  <body>
    <div id="app"></div>
    <script type="module" src="/src/main.js"></script>
  </body>
</html>
```

- [ ] **Step 5: Write `web/src/style.css`**

```css
@import "tailwindcss";
```

- [ ] **Step 6: Write `web/src/views/HomeView.vue`**

```vue
<template>
  <div>
    <h1 class="text-xl font-semibold">Miniprotector</h1>
    <p class="text-gray-600">Select a page from the sidebar.</p>
  </div>
</template>
```

- [ ] **Step 7: Write `web/src/router.js`**

```js
import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [{ path: '/', component: HomeView }],
})
```

- [ ] **Step 8: Write `web/src/App.vue`**

```vue
<template>
  <router-view />
</template>
```

- [ ] **Step 9: Write `web/src/main.js`**

```js
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import './style.css'

createApp(App).use(createPinia()).use(router).mount('#app')
```

- [ ] **Step 10: Add frontend build artifacts to `.gitignore`**

Append to `.gitignore`:

```
# Frontend
web/node_modules/
web/dist/
```

- [ ] **Step 11: Install dependencies and verify the build**

Run from the repository root:

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm install
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run build
```

Expected: `npm install` creates `web/node_modules/` and `web/package-lock.json`; `npm run build`
exits 0 and creates `web/dist/index.html` plus JS/CSS bundles.

- [ ] **Step 12: Commit**

```bash
git add web/package.json web/package-lock.json web/vite.config.js web/vitest.config.js \
  web/index.html web/src/main.js web/src/style.css web/src/App.vue web/src/router.js \
  web/src/views/HomeView.vue .gitignore
git commit -m "feat(web): scaffold Vite + Vue + Pinia + Tailwind app"
```

---

### Task 2: `auth` store

**Files:**
- Create: `web/src/stores/auth.js`
- Test: `web/src/stores/auth.spec.js`

**Interfaces:**
- Produces: `useAuthStore()` (Pinia store id `auth`) with state `{ token: string | null }`, getter
  `isAuthenticated: boolean`, actions `setToken(token: string)`, `clearToken()`. Persists `token` to
  `localStorage` under the key `mp_api_token`. Consumed by Task 3's API client and Task 9's
  `TokenGate`/`App.vue`.

- [ ] **Step 1: Write the failing test**

Create `web/src/stores/auth.spec.js`:

```js
import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useAuthStore } from './auth'

describe('auth store', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('starts with no token when localStorage is empty', () => {
    const auth = useAuthStore()
    expect(auth.token).toBeNull()
    expect(auth.isAuthenticated).toBe(false)
  })

  it('reads an existing token from localStorage on init', () => {
    localStorage.setItem('mp_api_token', 'existing-token')
    const auth = useAuthStore()
    expect(auth.token).toBe('existing-token')
    expect(auth.isAuthenticated).toBe(true)
  })

  it('setToken stores the token in state and localStorage', () => {
    const auth = useAuthStore()
    auth.setToken('new-token')
    expect(auth.token).toBe('new-token')
    expect(localStorage.getItem('mp_api_token')).toBe('new-token')
  })

  it('clearToken removes the token from state and localStorage', () => {
    const auth = useAuthStore()
    auth.setToken('new-token')
    auth.clearToken()
    expect(auth.token).toBeNull()
    expect(localStorage.getItem('mp_api_token')).toBeNull()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- auth.spec.js
```

Expected: FAIL — `web/src/stores/auth.js` does not exist yet.

- [ ] **Step 3: Write `web/src/stores/auth.js`**

```js
import { defineStore } from 'pinia'

const STORAGE_KEY = 'mp_api_token'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    token: localStorage.getItem(STORAGE_KEY) || null,
  }),
  getters: {
    isAuthenticated: (state) => !!state.token,
  },
  actions: {
    setToken(token) {
      this.token = token
      localStorage.setItem(STORAGE_KEY, token)
    },
    clearToken() {
      this.token = null
      localStorage.removeItem(STORAGE_KEY)
    },
  },
})
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- auth.spec.js
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/auth.js web/src/stores/auth.spec.js
git commit -m "feat(web): add auth store for the bearer token"
```

---

### Task 3: API client

**Files:**
- Create: `web/src/api/client.js`
- Test: `web/src/api/client.spec.js`

**Interfaces:**
- Consumes: `useAuthStore()` from Task 2 (`auth.token`, `auth.clearToken()`).
- Produces: `apiFetch(path: string, options？: RequestInit): Promise<any>` — GETs
  `/api/v1${path}`, attaches `Authorization: Bearer <token>` when a token is set, returns parsed
  JSON on 2xx, and `ApiError` (exported class, `{ status: number, message: string }`) on non-2xx
  (clearing the auth token first on a 401). Consumed by Task 4's `clients` store and Task 5's
  `catalog` store.

- [ ] **Step 1: Write the failing test**

Create `web/src/api/client.spec.js`:

```js
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useAuthStore } from '../stores/auth'
import { apiFetch, ApiError } from './client'

describe('apiFetch', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    global.fetch = vi.fn()
  })

  it('attaches the bearer token from the auth store', async () => {
    const auth = useAuthStore()
    auth.setToken('secret-token')
    global.fetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ data: [] }) })

    await apiFetch('/clients')

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/v1/clients',
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: 'Bearer secret-token' }),
      })
    )
  })

  it('does not attach an Authorization header when no token is set', async () => {
    global.fetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ data: [] }) })

    await apiFetch('/clients')

    const [, options] = global.fetch.mock.calls[0]
    expect(options.headers.Authorization).toBeUndefined()
  })

  it('returns parsed JSON on a 2xx response', async () => {
    global.fetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ data: [{ hostname: 'x' }] }) })

    const body = await apiFetch('/clients')

    expect(body).toEqual({ data: [{ hostname: 'x' }] })
  })

  it('throws an ApiError with the backend message on a non-2xx response', async () => {
    global.fetch.mockResolvedValue({ ok: false, status: 404, json: async () => ({ error: 'client not found' }) })

    await expect(apiFetch('/clients/unknown')).rejects.toMatchObject({
      status: 404,
      message: 'client not found',
    })
  })

  it('clears the stored token on a 401 response', async () => {
    const auth = useAuthStore()
    auth.setToken('stale-token')
    global.fetch.mockResolvedValue({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) })

    await expect(apiFetch('/clients')).rejects.toBeInstanceOf(ApiError)
    expect(auth.token).toBeNull()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- client.spec.js
```

Expected: FAIL — `web/src/api/client.js` does not exist yet.

- [ ] **Step 3: Write `web/src/api/client.js`**

```js
import { useAuthStore } from '../stores/auth'

const BASE_URL = '/api/v1'

export class ApiError extends Error {
  constructor(status, message) {
    super(message)
    this.status = status
  }
}

export async function apiFetch(path, options = {}) {
  const auth = useAuthStore()
  const headers = { ...(options.headers || {}) }
  if (auth.token) {
    headers.Authorization = `Bearer ${auth.token}`
  }

  const response = await fetch(`${BASE_URL}${path}`, { ...options, headers })

  if (response.status === 401) {
    auth.clearToken()
  }

  if (!response.ok) {
    let message = `Request failed with status ${response.status}`
    try {
      const body = await response.json()
      if (body && body.error) message = body.error
    } catch {
      // non-JSON error body; keep the default message
    }
    throw new ApiError(response.status, message)
  }

  return response.json()
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- client.spec.js
```

Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/api/client.js web/src/api/client.spec.js
git commit -m "feat(web): add the api-server fetch client"
```

---

### Task 4: `clients` store

**Files:**
- Create: `web/src/stores/clients.js`
- Test: `web/src/stores/clients.spec.js`

**Interfaces:**
- Consumes: `apiFetch(path)` from Task 3.
- Produces: `useClientsStore()` (id `clients`) with state
  `{ list: Client[], byHostname: Record<string, Client>, loading: boolean, error: string | null }`,
  actions `fetchAll(): Promise<void>` (GET `/clients`) and
  `fetchOne(hostname: string): Promise<Client>` (GET `/clients/{hostname}`, cached by hostname).
  Consumed by Task 6 (`ClientsListView`) and Task 7 (`ClientDetailView`).

- [ ] **Step 1: Write the failing test**

Create `web/src/stores/clients.spec.js`:

```js
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useClientsStore } from './clients'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('clients store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll populates the list from the API', async () => {
    apiFetch.mockResolvedValue({ data: [{ hostname: 'webserver' }] })
    const clients = useClientsStore()

    await clients.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/clients')
    expect(clients.list).toEqual([{ hostname: 'webserver' }])
    expect(clients.loading).toBe(false)
    expect(clients.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const clients = useClientsStore()

    await clients.fetchAll()

    expect(clients.error).toBe('boom')
    expect(clients.list).toEqual([])
  })

  it('fetchOne fetches and caches a client by hostname', async () => {
    apiFetch.mockResolvedValue({ hostname: 'webserver', revoked: false })
    const clients = useClientsStore()

    const first = await clients.fetchOne('webserver')
    const second = await clients.fetchOne('webserver')

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(apiFetch).toHaveBeenCalledWith('/clients/webserver')
    expect(first).toEqual({ hostname: 'webserver', revoked: false })
    expect(second).toEqual(first)
  })

  it('fetchOne records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('client not found'))
    const clients = useClientsStore()

    await expect(clients.fetchOne('missing')).rejects.toThrow('client not found')
    expect(clients.error).toBe('client not found')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- clients.spec.js
```

Expected: FAIL — `web/src/stores/clients.js` does not exist yet.

- [ ] **Step 3: Write `web/src/stores/clients.js`**

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

export const useClientsStore = defineStore('clients', {
  state: () => ({
    list: [],
    byHostname: {},
    loading: false,
    error: null,
  }),
  actions: {
    async fetchAll() {
      this.loading = true
      this.error = null
      try {
        const body = await apiFetch('/clients')
        this.list = body.data
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async fetchOne(hostname) {
      if (this.byHostname[hostname]) {
        return this.byHostname[hostname]
      }
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}`)
        this.byHostname[hostname] = client
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
  },
})
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- clients.spec.js
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/clients.js web/src/stores/clients.spec.js
git commit -m "feat(web): add clients store"
```

---

### Task 5: `catalog` store

**Files:**
- Create: `web/src/stores/catalog.js`
- Test: `web/src/stores/catalog.spec.js`

**Interfaces:**
- Consumes: `apiFetch(path)` from Task 3.
- Produces: `useCatalogStore()` (id `catalog`) with state
  `{ filters: { sourceHost: string, pattern: string }, cursorStack: number[], entries: Entry[],
  hasMore: boolean, loading: boolean, error: string | null }`, getter `canGoPrev: boolean`, actions
  `search(filters: { sourceHost: string, pattern: string }): Promise<void>`,
  `nextPage(): Promise<void>`, `prevPage(): Promise<void>`. Consumed by Task 8 (`CatalogView`).

- [ ] **Step 1: Write the failing test**

Create `web/src/stores/catalog.spec.js`:

```js
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useCatalogStore } from './catalog'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('catalog store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('search resets the cursor stack and fetches page 1 with filters', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: 'database', pattern: 'dbdata' })

    expect(apiFetch).toHaveBeenCalledWith('/catalog?source_host=database&pattern=dbdata')
    expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }])
    expect(catalog.hasMore).toBe(true)
    expect(catalog.canGoPrev).toBe(false)
  })

  it('nextPage requests starting_after the last entry id and pushes the cursor stack', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', pattern: '' })

    apiFetch.mockResolvedValue({ data: [{ id: 3 }, { id: 4 }], has_more: false })
    await catalog.nextPage()

    expect(apiFetch).toHaveBeenLastCalledWith('/catalog?starting_after=2')
    expect(catalog.entries).toEqual([{ id: 3 }, { id: 4 }])
    expect(catalog.canGoPrev).toBe(true)
  })

  it('prevPage pops the cursor stack and refetches the prior page', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', pattern: '' })
    apiFetch.mockResolvedValue({ data: [{ id: 3 }, { id: 4 }], has_more: false })
    await catalog.nextPage()

    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    await catalog.prevPage()

    expect(apiFetch).toHaveBeenLastCalledWith('/catalog')
    expect(catalog.canGoPrev).toBe(false)
  })

  it('nextPage does nothing when has_more is false', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }], has_more: false })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', pattern: '' })

    apiFetch.mockClear()
    await catalog.nextPage()

    expect(apiFetch).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- catalog.spec.js
```

Expected: FAIL — `web/src/stores/catalog.js` does not exist yet.

- [ ] **Step 3: Write `web/src/stores/catalog.js`**

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

function buildQuery(filters, startingAfter) {
  const params = new URLSearchParams()
  if (filters.sourceHost) params.set('source_host', filters.sourceHost)
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  return params.toString()
}

export const useCatalogStore = defineStore('catalog', {
  state: () => ({
    filters: { sourceHost: '', pattern: '' },
    cursorStack: [],
    entries: [],
    hasMore: false,
    loading: false,
    error: null,
  }),
  getters: {
    canGoPrev: (state) => state.cursorStack.length > 0,
  },
  actions: {
    async _fetchPage(startingAfter) {
      this.loading = true
      this.error = null
      try {
        const qs = buildQuery(this.filters, startingAfter)
        const body = await apiFetch(`/catalog${qs ? `?${qs}` : ''}`)
        this.entries = body.data
        this.hasMore = body.has_more
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async search(filters) {
      this.filters = { ...filters }
      this.cursorStack = []
      await this._fetchPage(undefined)
    },
    async nextPage() {
      if (!this.hasMore || this.entries.length === 0) return
      const lastId = this.entries[this.entries.length - 1].id
      this.cursorStack.push(lastId)
      await this._fetchPage(lastId)
    },
    async prevPage() {
      if (this.cursorStack.length === 0) return
      this.cursorStack.pop()
      const prevCursor = this.cursorStack[this.cursorStack.length - 1]
      await this._fetchPage(prevCursor)
    },
  },
})
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- catalog.spec.js
```

Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/catalog.js web/src/stores/catalog.spec.js
git commit -m "feat(web): add catalog store with cursor-stack pagination"
```

---

### Task 6: `ClientsListView`

**Files:**
- Create: `web/src/views/ClientsListView.vue`
- Test: `web/src/views/ClientsListView.spec.js`
- Modify: `web/src/router.js`

**Interfaces:**
- Consumes: `useClientsStore()` from Task 4.
- Produces: route `/clients` → `ClientsListView`, linking each row to `/clients/:hostname` (consumed
  by Task 7's route and Task 9's `Sidebar`).

- [ ] **Step 1: Write the failing test**

Create `web/src/views/ClientsListView.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientsListView from './ClientsListView.vue'
import { useClientsStore } from '../stores/clients'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientsListView, {
    global: {
      plugins: [pinia],
      stubs: { RouterLink: { template: '<a :href="to"><slot /></a>', props: ['to'] } },
    },
  })
  return { wrapper, clients: useClientsStore() }
}

describe('ClientsListView', () => {
  it('calls fetchAll on mount', () => {
    const { clients } = mountView({ list: [], loading: false, error: null })
    expect(clients.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('renders each client with a link to its detail page', () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'webserver', revoked: false, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('webserver')
    expect(wrapper.find('a').attributes('href')).toBe('/clients/webserver')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- ClientsListView.spec.js
```

Expected: FAIL — `web/src/views/ClientsListView.vue` does not exist yet.

- [ ] **Step 3: Write `web/src/views/ClientsListView.vue`**

```vue
<script setup>
import { onMounted } from 'vue'
import { useClientsStore } from '../stores/clients'

const clients = useClientsStore()

onMounted(() => {
  clients.fetchAll()
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Clients</h1>
    <p v-if="clients.loading">Loading...</p>
    <p v-else-if="clients.error" class="text-red-600">{{ clients.error }}</p>
    <table v-else class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Hostname</th>
          <th class="py-2 pr-4">Revoked</th>
          <th class="py-2 pr-4">Last Seen</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="client in clients.list" :key="client.hostname" class="border-b hover:bg-gray-50">
          <td class="py-2 pr-4">
            <router-link :to="`/clients/${client.hostname}`" class="text-blue-600 hover:underline">
              {{ client.hostname }}
            </router-link>
          </td>
          <td class="py-2 pr-4">{{ client.revoked ? 'Yes' : 'No' }}</td>
          <td class="py-2 pr-4">
            {{ client.last_seen_at ? new Date(client.last_seen_at * 1000).toLocaleString() : 'Never' }}
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- ClientsListView.spec.js
```

Expected: PASS (3 tests).

- [ ] **Step 5: Add the `/clients` route**

Edit `web/src/router.js`:

```js
import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import ClientsListView from './views/ClientsListView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/clients', component: ClientsListView },
  ],
})
```

- [ ] **Step 6: Verify the full build still succeeds**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run build
```

Expected: exits 0.

- [ ] **Step 7: Commit**

```bash
git add web/src/views/ClientsListView.vue web/src/views/ClientsListView.spec.js web/src/router.js
git commit -m "feat(web): add clients list view"
```

---

### Task 7: `ClientDetailView`

**Files:**
- Create: `web/src/views/ClientDetailView.vue`
- Test: `web/src/views/ClientDetailView.spec.js`
- Modify: `web/src/router.js`

**Interfaces:**
- Consumes: `useClientsStore()` from Task 4 (`fetchOne`, `byHostname`, `loading`, `error`), Vue
  Router's `useRoute()` for `route.params.hostname`.
- Produces: route `/clients/:hostname` → `ClientDetailView`.

- [ ] **Step 1: Write the failing test**

Create `web/src/views/ClientDetailView.spec.js`:

```js
import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientDetailView from './ClientDetailView.vue'
import { useClientsStore } from '../stores/clients'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { hostname: 'webserver' } }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientDetailView, { global: { plugins: [pinia] } })
  return { wrapper, clients: useClientsStore() }
}

describe('ClientDetailView', () => {
  it('calls fetchOne with the route hostname on mount', () => {
    const { clients } = mountView({ byHostname: {}, loading: false, error: null })
    expect(clients.fetchOne).toHaveBeenCalledWith('webserver')
  })

  it('renders the cached client record', () => {
    const { wrapper } = mountView({
      byHostname: {
        webserver: {
          hostname: 'webserver',
          revoked: false,
          revoked_at: 0,
          last_seen_at: 123,
          sans: null,
          attributes: null,
          descriptions: null,
        },
      },
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('webserver')
    expect(wrapper.text()).toContain('No')
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byHostname: {}, loading: false, error: 'client not found' })
    expect(wrapper.text()).toContain('client not found')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- ClientDetailView.spec.js
```

Expected: FAIL — `web/src/views/ClientDetailView.vue` does not exist yet.

- [ ] **Step 3: Write `web/src/views/ClientDetailView.vue`**

```vue
<script setup>
import { onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useClientsStore } from '../stores/clients'

const route = useRoute()
const clients = useClientsStore()
const hostname = computed(() => route.params.hostname)

onMounted(async () => {
  try {
    await clients.fetchOne(hostname.value)
  } catch {
    // error already recorded on clients.error by the store
  }
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">{{ hostname }}</h1>
    <p v-if="clients.loading">Loading...</p>
    <p v-else-if="clients.error" class="text-red-600">{{ clients.error }}</p>
    <dl v-else-if="clients.byHostname[hostname]" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
      <dt class="font-medium">Revoked</dt>
      <dd>{{ clients.byHostname[hostname].revoked ? 'Yes' : 'No' }}</dd>
      <dt class="font-medium">Revoked At</dt>
      <dd>{{ clients.byHostname[hostname].revoked_at || '—' }}</dd>
      <dt class="font-medium">Last Seen</dt>
      <dd>{{ clients.byHostname[hostname].last_seen_at || 'Never' }}</dd>
      <dt class="font-medium">SANs</dt>
      <dd>{{ (clients.byHostname[hostname].sans || []).join(', ') || '—' }}</dd>
      <dt class="font-medium">Attributes</dt>
      <dd>{{ JSON.stringify(clients.byHostname[hostname].attributes || {}) }}</dd>
      <dt class="font-medium">Descriptions</dt>
      <dd>{{ JSON.stringify(clients.byHostname[hostname].descriptions || {}) }}</dd>
    </dl>
  </div>
</template>
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- ClientDetailView.spec.js
```

Expected: PASS (3 tests).

- [ ] **Step 5: Add the `/clients/:hostname` route**

Edit `web/src/router.js`:

```js
import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import ClientsListView from './views/ClientsListView.vue'
import ClientDetailView from './views/ClientDetailView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/clients', component: ClientsListView },
    { path: '/clients/:hostname', component: ClientDetailView },
  ],
})
```

- [ ] **Step 6: Verify the full build still succeeds**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run build
```

Expected: exits 0.

- [ ] **Step 7: Commit**

```bash
git add web/src/views/ClientDetailView.vue web/src/views/ClientDetailView.spec.js web/src/router.js
git commit -m "feat(web): add client detail view"
```

---

### Task 8: `CatalogView`

**Files:**
- Create: `web/src/views/CatalogView.vue`
- Test: `web/src/views/CatalogView.spec.js`
- Modify: `web/src/router.js`

**Interfaces:**
- Consumes: `useCatalogStore()` from Task 5 (`search`, `nextPage`, `prevPage`, `entries`,
  `hasMore`, `canGoPrev`, `loading`, `error`).
- Produces: route `/catalog` → `CatalogView`.

- [ ] **Step 1: Write the failing test**

Create `web/src/views/CatalogView.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'

function mountView(state) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: { catalog: { cursorStack: [], ...state } },
  })
  const wrapper = mount(CatalogView, { global: { plugins: [pinia] } })
  return { wrapper, catalog: useCatalogStore() }
}

describe('CatalogView', () => {
  it('calls search with empty filters on mount', () => {
    const { catalog } = mountView({ entries: [], hasMore: false, loading: false, error: null })
    expect(catalog.search).toHaveBeenCalledWith({ sourceHost: '', pattern: '' })
  })

  it('submits the filter form via search', async () => {
    const { wrapper, catalog } = mountView({ entries: [], hasMore: false, loading: false, error: null })
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('database')
    await inputs[1].setValue('dbdata')
    await wrapper.find('form').trigger('submit.prevent')
    expect(catalog.search).toHaveBeenLastCalledWith({ sourceHost: 'database', pattern: 'dbdata' })
  })

  it('disables Next when hasMore is false and Prev when canGoPrev is false', () => {
    const { wrapper } = mountView({
      entries: [{ id: 1, path: '/x', source_host: 'h', size: 1, mode: '-rw', mod_time: 0 }],
      hasMore: false,
      loading: false,
      error: null,
    })
    const buttons = wrapper.findAll('button')
    const next = buttons.find((b) => b.text() === 'Next')
    const prev = buttons.find((b) => b.text() === 'Prev')
    expect(next.attributes('disabled')).toBeDefined()
    expect(prev.attributes('disabled')).toBeDefined()
  })

  it('clicking Next calls catalog.nextPage', async () => {
    const { wrapper, catalog } = mountView({
      entries: [{ id: 1, path: '/x', source_host: 'h', size: 1, mode: '-rw', mod_time: 0 }],
      hasMore: true,
      loading: false,
      error: null,
    })
    const next = wrapper.findAll('button').find((b) => b.text() === 'Next')
    await next.trigger('click')
    expect(catalog.nextPage).toHaveBeenCalledTimes(1)
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- CatalogView.spec.js
```

Expected: FAIL — `web/src/views/CatalogView.vue` does not exist yet.

- [ ] **Step 3: Write `web/src/views/CatalogView.vue`**

```vue
<script setup>
import { onMounted, reactive } from 'vue'
import { useCatalogStore } from '../stores/catalog'

const catalog = useCatalogStore()
const form = reactive({ sourceHost: '', pattern: '' })

function submit() {
  catalog.search({ ...form })
}

onMounted(() => {
  catalog.search({ ...form })
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Catalog</h1>
    <form @submit.prevent="submit" class="flex gap-2 mb-4">
      <input v-model="form.sourceHost" placeholder="source host" class="border rounded px-2 py-1" />
      <input v-model="form.pattern" placeholder="path pattern" class="border rounded px-2 py-1" />
      <button type="submit" class="bg-blue-600 text-white rounded px-3 py-1">Search</button>
    </form>
    <p v-if="catalog.loading">Loading...</p>
    <p v-else-if="catalog.error" class="text-red-600">{{ catalog.error }}</p>
    <table v-else class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Path</th>
          <th class="py-2 pr-4">Source Host</th>
          <th class="py-2 pr-4">Size</th>
          <th class="py-2 pr-4">Mode</th>
          <th class="py-2 pr-4">Modified</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="entry in catalog.entries" :key="entry.id" class="border-b">
          <td class="py-2 pr-4">{{ entry.path }}</td>
          <td class="py-2 pr-4">{{ entry.source_host }}</td>
          <td class="py-2 pr-4">{{ entry.size }}</td>
          <td class="py-2 pr-4">{{ entry.mode }}</td>
          <td class="py-2 pr-4">{{ new Date(entry.mod_time * 1000).toLocaleString() }}</td>
        </tr>
      </tbody>
    </table>
    <div class="flex gap-2 mt-4">
      <button :disabled="!catalog.canGoPrev" @click="catalog.prevPage()" class="border rounded px-3 py-1 disabled:opacity-50">
        Prev
      </button>
      <button :disabled="!catalog.hasMore" @click="catalog.nextPage()" class="border rounded px-3 py-1 disabled:opacity-50">
        Next
      </button>
    </div>
  </div>
</template>
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- CatalogView.spec.js
```

Expected: PASS (4 tests).

- [ ] **Step 5: Add the `/catalog` route**

Edit `web/src/router.js`:

```js
import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import ClientsListView from './views/ClientsListView.vue'
import ClientDetailView from './views/ClientDetailView.vue'
import CatalogView from './views/CatalogView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/clients', component: ClientsListView },
    { path: '/clients/:hostname', component: ClientDetailView },
    { path: '/catalog', component: CatalogView },
  ],
})
```

- [ ] **Step 6: Verify the full build still succeeds**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run build
```

Expected: exits 0.

- [ ] **Step 7: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js web/src/router.js
git commit -m "feat(web): add catalog browser view"
```

---

### Task 9: `TokenGate`, `Sidebar`, and final `App.vue` wiring

**Files:**
- Create: `web/src/components/TokenGate.vue`
- Create: `web/src/components/TokenGate.spec.js`
- Create: `web/src/components/Sidebar.vue`
- Modify: `web/src/App.vue`
- Create: `web/src/App.spec.js`

**Interfaces:**
- Consumes: `useAuthStore()` from Task 2; routes `/clients`, `/catalog` from Tasks 6, 8 (for
  `Sidebar`'s links).
- Produces: the final `App.vue` shell — token gate blocks the UI until a token is set, then renders
  `Sidebar` + `<router-view>`.

- [ ] **Step 1: Write the failing test for `TokenGate`**

Create `web/src/components/TokenGate.spec.js`:

```js
import { describe, it, expect, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { useAuthStore } from '../stores/auth'
import TokenGate from './TokenGate.vue'

describe('TokenGate', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('renders the token form when no token is stored', () => {
    const wrapper = mount(TokenGate)
    expect(wrapper.find('form').exists()).toBe(true)
  })

  it('hides the token form once a token is set', () => {
    const auth = useAuthStore()
    auth.setToken('secret')
    const wrapper = mount(TokenGate)
    expect(wrapper.find('form').exists()).toBe(false)
  })

  it('submitting the form stores the entered token', async () => {
    const auth = useAuthStore()
    const wrapper = mount(TokenGate)
    await wrapper.find('input').setValue('typed-token')
    await wrapper.find('form').trigger('submit.prevent')
    expect(auth.token).toBe('typed-token')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- TokenGate.spec.js
```

Expected: FAIL — `web/src/components/TokenGate.vue` does not exist yet.

- [ ] **Step 3: Write `web/src/components/TokenGate.vue`**

```vue
<script setup>
import { ref } from 'vue'
import { useAuthStore } from '../stores/auth'

const auth = useAuthStore()
const input = ref('')

function submit() {
  if (input.value.trim()) {
    auth.setToken(input.value.trim())
    input.value = ''
  }
}
</script>

<template>
  <div v-if="!auth.isAuthenticated" class="fixed inset-0 flex items-center justify-center bg-gray-900/80">
    <form @submit.prevent="submit" class="bg-white p-6 rounded shadow w-80 space-y-3">
      <h2 class="text-lg font-semibold">Enter API token</h2>
      <input
        v-model="input"
        type="password"
        placeholder="Bearer token"
        class="w-full border rounded px-2 py-1"
      />
      <button type="submit" class="w-full bg-blue-600 text-white rounded py-1">Continue</button>
    </form>
  </div>
</template>
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- TokenGate.spec.js
```

Expected: PASS (3 tests).

- [ ] **Step 5: Write `web/src/components/Sidebar.vue`**

```vue
<template>
  <nav class="w-48 bg-gray-100 h-screen p-4 space-y-2">
    <router-link to="/clients" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Clients
    </router-link>
    <router-link to="/catalog" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Catalog
    </router-link>
  </nav>
</template>
```

- [ ] **Step 6: Write the failing test for `App.vue`**

Create `web/src/App.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import App from './App.vue'

function mountApp(token) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { auth: { token } } })
  return mount(App, {
    global: {
      plugins: [pinia],
      stubs: { RouterView: true, RouterLink: true },
    },
  })
}

describe('App', () => {
  it('shows only the token gate when unauthenticated', () => {
    const wrapper = mountApp(null)
    expect(wrapper.find('form').exists()).toBe(true)
    expect(wrapper.find('nav').exists()).toBe(false)
  })

  it('shows the sidebar and content once authenticated', () => {
    const wrapper = mountApp('secret')
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.find('nav').exists()).toBe(true)
  })
})
```

- [ ] **Step 7: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- App.spec.js
```

Expected: FAIL — `App.vue` still only renders `<router-view />`, so no token gate/sidebar exist.

- [ ] **Step 8: Rewrite `web/src/App.vue`**

```vue
<script setup>
import Sidebar from './components/Sidebar.vue'
import TokenGate from './components/TokenGate.vue'
import { useAuthStore } from './stores/auth'

const auth = useAuthStore()
</script>

<template>
  <TokenGate />
  <div v-if="auth.isAuthenticated" class="flex">
    <Sidebar />
    <main class="flex-1 p-6">
      <router-view />
    </main>
  </div>
</template>
```

- [ ] **Step 9: Run the tests to verify they pass**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test
```

Expected: PASS — every spec file in `web/src` (all tasks so far) passes.

- [ ] **Step 10: Verify the full build still succeeds**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run build
```

Expected: exits 0.

- [ ] **Step 11: Commit**

```bash
git add web/src/components/TokenGate.vue web/src/components/TokenGate.spec.js \
  web/src/components/Sidebar.vue web/src/App.vue web/src/App.spec.js
git commit -m "feat(web): add token gate, sidebar, and final app shell"
```

---

### Task 10: Dockerize and wire into `demo/docker-compose.yml`

**Files:**
- Create: `web/Dockerfile`
- Create: `web/nginx.conf`
- Create: `web/.dockerignore`
- Modify: `demo/docker-compose.yml`
- Modify: `demo/README.md`

**Interfaces:**
- Produces: a `web` demo-compose service, reachable at `http://localhost:8091`, whose `/api/*`
  requests are reverse-proxied to `api-server:8090` inside the compose network.

- [ ] **Step 1: Write `web/.dockerignore`**

```
node_modules
dist
```

- [ ] **Step 2: Write `web/Dockerfile`**

```dockerfile
# web/Dockerfile
FROM node:20-alpine AS build
WORKDIR /app
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM nginx:1.27-alpine
COPY web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
```

- [ ] **Step 3: Write `web/nginx.conf`**

```nginx
server {
    listen 80;
    root /usr/share/nginx/html;
    index index.html;

    location /api/ {
        proxy_pass http://api-server:8090/api/;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

- [ ] **Step 4: Add the `web` service to `demo/docker-compose.yml`**

Add this service (after the `api-server` service, before `policy-server`) to `demo/docker-compose.yml`:

```yaml
  web:
    build:
      context: ..
      dockerfile: web/Dockerfile
    depends_on:
      - api-server
    ports:
      - "8091:80"
    restart: unless-stopped
```

- [ ] **Step 5: Bring up the full demo stack and verify end-to-end**

```bash
make demo-up
```

Expected: builds all eight images (including `web`) and enrolls every node as before.

```bash
curl -s http://localhost:8091/ | grep -o '<div id="app">'
curl -s -H "Authorization: Bearer dev-placeholder-token-change-me" http://localhost:8091/api/v1/clients
curl -s -H "Authorization: Bearer dev-placeholder-token-change-me" http://localhost:8090/api/v1/clients
```

Expected: the first `curl` finds `<div id="app">` in the served SPA shell; the second and third
`curl` calls return identical JSON (nginx's proxy and a direct call to `api-server` agree).

- [ ] **Step 6: Document the new service in `demo/README.md`**

In `demo/README.md`, update the "Bring it up" section's image count from "seven" to "eight", and add
a new section after "Try it":

```markdown
## Browser UI

`web` serves a small Vue frontend over `api-server`'s read-only REST API, published at
`http://localhost:8091`. On first load it prompts for a bearer token — use the demo lab's
placeholder token, `dev-placeholder-token-change-me` (see `demo/local.conf`). From there, browse
`/clients` and `/catalog`.
```

- [ ] **Step 7: Commit**

```bash
git add web/Dockerfile web/nginx.conf web/.dockerignore demo/docker-compose.yml demo/README.md
git commit -m "feat(web): dockerize and wire the frontend into the demo lab"
```

---

### Task 11: Documentation

**Files:**
- Create: `docs/components/web.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:** None — documentation only.

- [ ] **Step 1: Write `docs/components/web.md`**

```markdown
# web

A small read-only browser UI over [api-server](./api-server.md)'s REST API — lists enrolled
clients and browses catalog entries. **Not a mesh member:** unlike every other control-plane
component, `web` has no mTLS identity of its own; it's a static Vue single-page app served by
nginx, which reverse-proxies `/api/*` to `api-server` so the browser's calls stay same-origin (no
CORS changes were needed on `api-server`).

## Usage

On first load, the app prompts for `api-server`'s bearer token and stores it in the browser's
`localStorage`; every request thereafter carries `Authorization: Bearer <token>`. No token means
no data — there's no read-only "guest" mode.

## Pages

- `/` — placeholder landing page
- `/clients` — every enrolled client (hostname, revoked, last seen), linking to:
- `/clients/:hostname` — one client's full record (SANs, attributes, descriptions)
- `/catalog` — catalog entries, filterable by source host and a path-pattern substring,
  paginated with Prev/Next (the catalog API only supports cursor pagination — no total count, so
  there's no page-number jump)

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
```

- [ ] **Step 2: Add `web` to `README.md`'s component list**

`README.md`'s "Components" list (`README.md:57-68`) is already missing several existing
components (`clientmanager-api`, `api-server`) — out of scope to backfill here. Add `web` as a new
line directly after the last entry, `log-gateway` (`README.md:68`):

```markdown
- **[web](docs/components/web.md)** - a small browser UI over `api-server`'s read-only REST API — browse enrolled clients and catalog entries
```

- [ ] **Step 3: Add `web` to `docs/ARCHITECTURE.md`**

In `docs/ARCHITECTURE.md`'s "Components" table, add a row after `api-server`:

```markdown
| web | Static Vue frontend over `api-server`'s REST API — this system's first browser UI; served by nginx, no mTLS identity of its own | Implemented |
```

- [ ] **Step 4: Add a `CHANGELOG.md` entry**

Add this entry at the top of `CHANGELOG.md`, above the `2026-07-14` entry, dated `2026-07-18`:

```markdown
## 2026-07-18 — web: a small Vue/Pinia frontend for api-server

`web` is a new static single-page app (Vite + Vue 3 + Pinia + Vue Router + Tailwind CSS) providing
a browser UI over `api-server`'s two read-only resources: an enrolled-clients list/detail view and
a filterable, cursor-paginated catalog browser. It's served by nginx, which reverse-proxies `/api/*`
to `api-server` so the browser's requests stay same-origin — no CORS changes were needed on
`api-server` itself. A one-time bearer-token prompt (stored in `localStorage`) is the only auth,
matching `api-server`'s existing model. Wired into `demo/docker-compose.yml` as a new `web` service
on `localhost:8091`; not yet added to `deploy/control-plane/`.
```

- [ ] **Step 5: Commit**

```bash
git add docs/components/web.md README.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document the web frontend"
```
