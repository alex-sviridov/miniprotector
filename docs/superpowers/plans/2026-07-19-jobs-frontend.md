# Jobs Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Jobs list page and a per-job log detail page to the `web` frontend, consuming `api-server`'s existing `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` endpoints.

**Architecture:** Two new Vue views (`JobsListView.vue`, `JobDetailView.vue`) backed by one new Pinia store (`jobs.js`), wired into the existing router/sidebar, following the exact patterns already established by `ClientsListView.vue`/`ClientDetailView.vue`/`clients.js`. The Jobs list table is enhanced post-render by `simple-datatables` for client-side search/sort/pagination, replacing the need for a server-side filter form.

**Tech Stack:** Vue 3 (`<script setup>`), Pinia, Vue Router, Vitest + `@vue/test-utils` + `@pinia/testing`, Tailwind CSS, `simple-datatables` (new dependency).

## Global Constraints

- No filter form — `GET /jobs` is called with no query params (server defaults: last 24h, up to 100, all kinds/hosts/states); narrowing happens client-side via `simple-datatables`'s built-in search/sort. (Spec: Scope, Out of scope)
- No live-tail/polling — `JobDetailView` fetches `GET /jobs/{job_id}/logs` exactly once, on mount, with no query params (server default: last 24h). (Spec: Scope, Out of scope)
- `state` renders as plain text — no color-coded styling. (Spec: Out of scope)
- Log lines render as raw, unparsed text in a monospace block — no `JSON.parse`/pretty-printing. (Spec: Out of scope)
- `simple-datatables` is scoped to the Jobs list table only — `ClientsListView`/`CatalogView` are not touched. (Spec: Out of scope)
- `simple-datatables` version: `^10.2.0` (latest on npm as of this plan).
- `GET /jobs/{job_id}/logs`'s `data` field is `null`, not `[]`, when no lines match (Go's `encoding/json` marshals a nil slice as `null`) — the store must normalize with `body.data ?? []`. (Spec: Error Handling; verified against `src/cmd/api-server/jobs.go`'s `handleGetJobLogs`)
- `GET /jobs/{job_id}/logs`'s `timestamp` field is Loki's raw **nanosecond** unix timestamp (verified against `src/cmd/api-server/jobs.go`'s `logLineDTO`/`handleGetJobLogs` — unlike `GET /jobs`'s `started_at`/`finished_at`, which are already truncated to seconds). The view must divide by `1e9` before calling `formatTimestamp`.
- All `npm` commands run via this repo's existing Docker pattern (no local Node): `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm <args>`, executed from the repository root.

---

## Task 1: Add the `simple-datatables` dependency

**Files:**
- Modify: `web/package.json`
- Modify: `web/package-lock.json` (generated)

**Interfaces:**
- Produces: `simple-datatables` importable as `import { DataTable } from 'simple-datatables'`, with its CSS at `simple-datatables/dist/style.css`, available to later tasks.

- [ ] **Step 1: Install the package via the project's Docker npm pattern**

Run from the repository root:

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm install simple-datatables@^10.2.0
```

Expected: `web/package.json` gains a `"simple-datatables": "^10.2.0"` entry under `dependencies`; `web/package-lock.json` is updated to match.

- [ ] **Step 2: Verify the existing test suite still passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test
```

Expected: all existing tests pass (this step only adds a dependency, no source changes).

- [ ] **Step 3: Commit**

```bash
git add web/package.json web/package-lock.json
git commit -m "chore(web): add simple-datatables dependency"
```

---

## Task 2: `jobs` Pinia store

**Files:**
- Create: `web/src/stores/jobs.js`
- Test: `web/src/stores/jobs.spec.js`

**Interfaces:**
- Consumes: `apiFetch(path)` from `web/src/api/client.js` (existing — returns parsed JSON body or throws `ApiError`, per `web/src/api/client.js:12`).
- Produces: `useJobsStore()` exposing state `{ list, loading, error, logs, logsLoading, logsError }` and actions `fetchAll(): Promise<void>` (sets `list` from `GET /jobs`) and `fetchLogs(jobId: string): Promise<void>` (sets `logs` from `GET /jobs/{job_id}/logs`, normalizing a `null` `data` field to `[]`). Consumed by `JobsListView.vue` and `JobDetailView.vue` in later tasks.

- [ ] **Step 1: Write the failing tests**

Create `web/src/stores/jobs.spec.js`:

```js
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useJobsStore } from './jobs'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('jobs store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll populates the list from the API', async () => {
    apiFetch.mockResolvedValue({ data: [{ job_id: 'backup:x:1' }], truncated: false })
    const jobs = useJobsStore()

    await jobs.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/jobs')
    expect(jobs.list).toEqual([{ job_id: 'backup:x:1' }])
    expect(jobs.loading).toBe(false)
    expect(jobs.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const jobs = useJobsStore()

    await jobs.fetchAll()

    expect(jobs.error).toBe('boom')
    expect(jobs.list).toEqual([])
  })

  it('fetchLogs populates logs from the API', async () => {
    apiFetch.mockResolvedValue({
      data: [{ timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{}' }],
    })
    const jobs = useJobsStore()

    await jobs.fetchLogs('backup:nightly:1752400000')

    expect(apiFetch).toHaveBeenCalledWith('/jobs/backup%3Anightly%3A1752400000/logs')
    expect(jobs.logs).toEqual([
      { timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{}' },
    ])
    expect(jobs.logsLoading).toBe(false)
    expect(jobs.logsError).toBeNull()
  })

  it('fetchLogs treats a null data field as an empty list', async () => {
    apiFetch.mockResolvedValue({ data: null })
    const jobs = useJobsStore()

    await jobs.fetchLogs('backup:nightly:1752400000')

    expect(jobs.logs).toEqual([])
    expect(jobs.logsError).toBeNull()
  })

  it('fetchLogs records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const jobs = useJobsStore()

    await jobs.fetchLogs('backup:nightly:1752400000')

    expect(jobs.logsError).toBe('boom')
    expect(jobs.logs).toEqual([])
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/stores/jobs.spec.js
```

Expected: FAIL — `web/src/stores/jobs.js` does not exist yet (`Cannot find module './jobs'` or similar).

- [ ] **Step 3: Write the implementation**

Create `web/src/stores/jobs.js`:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

export const useJobsStore = defineStore('jobs', {
  state: () => ({
    list: [],
    loading: false,
    error: null,
    logs: [],
    logsLoading: false,
    logsError: null,
  }),
  actions: {
    async fetchAll() {
      this.loading = true
      this.error = null
      try {
        const body = await apiFetch('/jobs')
        this.list = body.data
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async fetchLogs(jobId) {
      this.logsLoading = true
      this.logsError = null
      try {
        const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs`)
        this.logs = body.data ?? []
      } catch (err) {
        this.logsError = err.message
      } finally {
        this.logsLoading = false
      }
    },
  },
})
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/stores/jobs.spec.js
```

Expected: PASS — all 5 tests green.

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/jobs.js web/src/stores/jobs.spec.js
git commit -m "feat(web): add jobs Pinia store"
```

---

## Task 3: `JobsListView.vue`

**Files:**
- Create: `web/src/views/JobsListView.vue`
- Test: `web/src/views/JobsListView.spec.js`

**Interfaces:**
- Consumes: `useJobsStore()` from Task 2 (`jobs.list`, `jobs.loading`, `jobs.error`, `jobs.fetchAll()`); `formatTimestamp(epochSeconds)` from `web/src/utils/format.js:1`; `DataTable` from `simple-datatables` (Task 1).
- Produces: the `/jobs` route's component, routed to in Task 5. Each row links to `/jobs/${job.job_id}`, matching the path Task 4's `JobDetailView` expects at `route.params.job_id`.

- [ ] **Step 1: Write the failing tests**

Create `web/src/views/JobsListView.spec.js`:

```js
import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import JobsListView from './JobsListView.vue'
import { useJobsStore } from '../stores/jobs'

const destroy = vi.fn()
const DataTable = vi.fn(() => ({ destroy }))
vi.mock('simple-datatables', () => ({ DataTable }))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { jobs: state } })
  const wrapper = mount(JobsListView, {
    global: {
      plugins: [pinia],
      stubs: { RouterLink: { template: '<a :href="to"><slot /></a>', props: ['to'] } },
    },
  })
  return { wrapper, jobs: useJobsStore() }
}

describe('JobsListView', () => {
  it('calls fetchAll on mount', () => {
    const { jobs } = mountView({ list: [], loading: false, error: null })
    expect(jobs.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('renders each job with a link to its detail page', () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'backup:nightly:1752400000',
          kind: 'backup',
          source_host: 'database',
          store_host: 'bwfs-east',
          started_at: 1752400000,
          finished_at: 1752400010,
          state: 'success',
        },
      ],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('backup:nightly:1752400000')
    expect(wrapper.find('a').attributes('href')).toBe('/jobs/backup:nightly:1752400000')
  })

  it('renders a dash for a null store_host and finished_at', () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'operating-refresh:1752400500',
          kind: 'operating-refresh',
          source_host: 'webserver',
          store_host: null,
          started_at: 1752400500,
          finished_at: null,
          state: 'in_progress',
        },
      ],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('—')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('initializes simple-datatables on the rendered table once data loads, and destroys it on unmount', async () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'a',
          kind: 'backup',
          source_host: 'h',
          store_host: null,
          started_at: 1,
          finished_at: null,
          state: 'in_progress',
        },
      ],
      loading: false,
      error: null,
    })
    await flushPromises()

    expect(DataTable).toHaveBeenCalledTimes(1)
    expect(DataTable.mock.calls[0][0].tagName).toBe('TABLE')

    wrapper.unmount()
    expect(destroy).toHaveBeenCalledTimes(1)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/views/JobsListView.spec.js
```

Expected: FAIL — `web/src/views/JobsListView.vue` does not exist yet.

- [ ] **Step 3: Write the implementation**

Create `web/src/views/JobsListView.vue`:

```vue
<script setup>
import { onMounted, onBeforeUnmount, nextTick, ref } from 'vue'
import { DataTable } from 'simple-datatables'
import 'simple-datatables/dist/style.css'
import { useJobsStore } from '../stores/jobs'
import { formatTimestamp } from '../utils/format'

const jobs = useJobsStore()
const tableRef = ref(null)
let dataTable = null

onMounted(async () => {
  await jobs.fetchAll()
  await nextTick()
  if (tableRef.value) {
    dataTable = new DataTable(tableRef.value)
  }
})

onBeforeUnmount(() => {
  if (dataTable) {
    dataTable.destroy()
    dataTable = null
  }
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Jobs</h1>
    <p v-if="jobs.loading">Loading...</p>
    <p v-else-if="jobs.error" class="text-red-600">{{ jobs.error }}</p>
    <table v-else ref="tableRef" class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Job ID</th>
          <th class="py-2 pr-4">Kind</th>
          <th class="py-2 pr-4">Source Host</th>
          <th class="py-2 pr-4">Store Host</th>
          <th class="py-2 pr-4">Started At</th>
          <th class="py-2 pr-4">Finished At</th>
          <th class="py-2 pr-4">State</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="job in jobs.list" :key="job.job_id" class="border-b hover:bg-gray-50">
          <td class="py-2 pr-4">
            <router-link :to="`/jobs/${job.job_id}`" class="text-blue-600 hover:underline">
              {{ job.job_id }}
            </router-link>
          </td>
          <td class="py-2 pr-4">{{ job.kind }}</td>
          <td class="py-2 pr-4">{{ job.source_host }}</td>
          <td class="py-2 pr-4">{{ job.store_host || '—' }}</td>
          <td class="py-2 pr-4">{{ formatTimestamp(job.started_at) || '—' }}</td>
          <td class="py-2 pr-4">{{ formatTimestamp(job.finished_at) || '—' }}</td>
          <td class="py-2 pr-4">{{ job.state }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/views/JobsListView.spec.js
```

Expected: PASS — all 5 tests green.

- [ ] **Step 5: Commit**

```bash
git add web/src/views/JobsListView.vue web/src/views/JobsListView.spec.js
git commit -m "feat(web): add JobsListView with simple-datatables search/sort"
```

---

## Task 4: `JobDetailView.vue`

**Files:**
- Create: `web/src/views/JobDetailView.vue`
- Test: `web/src/views/JobDetailView.spec.js`

**Interfaces:**
- Consumes: `useJobsStore()` from Task 2 (`jobs.logs`, `jobs.logsLoading`, `jobs.logsError`, `jobs.fetchLogs(jobId)`); `useRoute()` from `vue-router` (`route.params.job_id`, matching the link Task 3 produces); `formatTimestamp` from `web/src/utils/format.js:1`.
- Produces: the `/jobs/:job_id` route's component, routed to in Task 5.

- [ ] **Step 1: Write the failing tests**

Create `web/src/views/JobDetailView.spec.js`:

```js
import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import JobDetailView from './JobDetailView.vue'
import { useJobsStore } from '../stores/jobs'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { job_id: 'backup:nightly:1752400000' } }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { jobs: state } })
  const wrapper = mount(JobDetailView, { global: { plugins: [pinia] } })
  return { wrapper, jobs: useJobsStore() }
}

describe('JobDetailView', () => {
  it('calls fetchLogs with the route job_id on mount', () => {
    const { jobs } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(jobs.fetchLogs).toHaveBeenCalledWith('backup:nightly:1752400000')
  })

  it('renders the job id as the heading', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(wrapper.find('h1').text()).toBe('backup:nightly:1752400000')
  })

  it('renders each log line with its formatted timestamp, hostname, binary, and raw line', () => {
    const { wrapper } = mountView({
      logs: [
        { timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{"msg":"started"}' },
      ],
      logsLoading: false,
      logsError: null,
    })
    expect(wrapper.text()).toContain('database')
    expect(wrapper.text()).toContain('brfs')
    expect(wrapper.text()).toContain('{"msg":"started"}')
  })

  it('shows an empty-state message when no lines are returned', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(wrapper.text()).toContain('No log lines found')
  })

  it('shows the store error message on failure', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/views/JobDetailView.spec.js
```

Expected: FAIL — `web/src/views/JobDetailView.vue` does not exist yet.

- [ ] **Step 3: Write the implementation**

Create `web/src/views/JobDetailView.vue`:

```vue
<script setup>
import { onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useJobsStore } from '../stores/jobs'
import { formatTimestamp } from '../utils/format'

const route = useRoute()
const jobs = useJobsStore()
const jobId = computed(() => route.params.job_id)

onMounted(async () => {
  await jobs.fetchLogs(jobId.value)
})

// GET /jobs/{job_id}/logs returns Loki's raw nanosecond timestamp (unlike
// GET /jobs's started_at/finished_at, already seconds) -- convert before
// formatTimestamp, which expects epoch seconds.
function formatLineTimestamp(nanos) {
  return formatTimestamp(Math.floor(nanos / 1e9))
}
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">{{ jobId }}</h1>
    <p v-if="jobs.logsLoading">Loading...</p>
    <p v-else-if="jobs.logsError" class="text-red-600">{{ jobs.logsError }}</p>
    <p v-else-if="jobs.logs.length === 0">No log lines found for this job in the last 24h.</p>
    <ul v-else class="font-mono text-sm space-y-1">
      <li v-for="(line, index) in jobs.logs" :key="index">
        <span class="text-gray-500">{{ formatLineTimestamp(line.timestamp) }}</span>
        [{{ line.hostname }}/{{ line.binary }}] {{ line.line }}
      </li>
    </ul>
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/views/JobDetailView.spec.js
```

Expected: PASS — all 5 tests green.

- [ ] **Step 5: Commit**

```bash
git add web/src/views/JobDetailView.vue web/src/views/JobDetailView.spec.js
git commit -m "feat(web): add JobDetailView for per-job log lines"
```

---

## Task 5: Wire routes and sidebar

**Files:**
- Modify: `web/src/router.js`
- Modify: `web/src/components/Sidebar.vue`

**Interfaces:**
- Consumes: `JobsListView` (Task 3), `JobDetailView` (Task 4).
- Produces: `/jobs` and `/jobs/:job_id` become reachable in the running app; the sidebar exposes a "Jobs" link. Nothing downstream depends on this task.

- [ ] **Step 1: Add the routes**

Edit `web/src/router.js` — add the imports and two routes:

```js
import { createRouter, createWebHistory } from 'vue-router'
import HomeView from './views/HomeView.vue'
import ClientsListView from './views/ClientsListView.vue'
import ClientDetailView from './views/ClientDetailView.vue'
import CatalogView from './views/CatalogView.vue'
import PoliciesListView from './views/PoliciesListView.vue'
import PolicyDetailView from './views/PolicyDetailView.vue'
import PolicyFormView from './views/PolicyFormView.vue'
import JobsListView from './views/JobsListView.vue'
import JobDetailView from './views/JobDetailView.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: HomeView },
    { path: '/clients', component: ClientsListView },
    { path: '/clients/:hostname', component: ClientDetailView },
    { path: '/catalog', component: CatalogView },
    { path: '/policies', component: PoliciesListView },
    { path: '/policies/new', component: PolicyFormView },
    { path: '/policies/:id', component: PolicyDetailView },
    { path: '/policies/:id/edit', component: PolicyFormView },
    { path: '/jobs', component: JobsListView },
    { path: '/jobs/:job_id', component: JobDetailView },
  ],
})
```

- [ ] **Step 2: Add the sidebar link**

Edit `web/src/components/Sidebar.vue` — add a "Jobs" link after "Policies":

```vue
<template>
  <nav class="w-48 bg-gray-100 h-screen p-4 space-y-2">
    <router-link to="/clients" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Clients
    </router-link>
    <router-link to="/catalog" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Catalog
    </router-link>
    <router-link to="/policies" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Policies
    </router-link>
    <router-link to="/jobs" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Jobs
    </router-link>
  </nav>
</template>
```

- [ ] **Step 3: Run the full test suite**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test
```

Expected: PASS — every existing and new test file green (this task adds no new test file itself; it's covered by Task 3/4's view tests plus this being a router/template wiring change with no dedicated router spec in this codebase, matching existing convention — see `web/src/router.js`'s lack of a `.spec.js` file).

- [ ] **Step 4: Manual smoke test**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app -p 5173:5173 node:20-alpine npm run dev -- --host
```

With `api-server` running locally (or via `make control-plane-up`), open `http://localhost:5173`, enter the bearer token, click "Jobs" in the sidebar, confirm the table renders with a working search box and sortable columns, click a Job ID, confirm its log lines render. Stop the dev server (Ctrl-C) when done.

- [ ] **Step 5: Commit**

```bash
git add web/src/router.js web/src/components/Sidebar.vue
git commit -m "feat(web): wire up /jobs routes and sidebar link"
```

---

## Task 6: Documentation and changelog

**Files:**
- Modify: `docs/components/web.md`
- Modify: `README.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing (documentation only).
- Produces: nothing consumed by other tasks — this is the last task.

- [ ] **Step 1: Update `docs/components/web.md`**

In the `## Pages` section, add two bullets after the `/policies/:id/edit` line (currently `docs/components/web.md:27`):

```markdown
- `/jobs` — every job across the fleet from the last 24h (job ID, kind, source host, store host,
  started/finished time, state), with client-side search, sort, and pagination via
  `simple-datatables` (the only page in this app not a plain HTML table), linking to:
- `/jobs/:job_id` — one job's raw log lines from the last 24h, fetched once on page load (no
  live-tail/polling)
```

Update the top summary line (currently `docs/components/web.md:3`) from:

```markdown
A small browser UI over [api-server](./api-server.md)'s REST API — lists enrolled clients,
browses catalog entries, and manages backup policies (list/create/edit/delete). **Not a mesh
member:**
```

to:

```markdown
A small browser UI over [api-server](./api-server.md)'s REST API — lists enrolled clients,
browses catalog entries, manages backup policies (list/create/edit/delete), and browses
fleet-wide jobs and their logs. **Not a mesh member:**
```

- [ ] **Step 2: Update `README.md`**

Change the `web` bullet in the component list (currently `README.md:69`) from:

```markdown
- **[web](docs/components/web.md)** - a small browser UI over `api-server`'s read-only REST API - browse enrolled clients and catalog entries
```

to:

```markdown
- **[web](docs/components/web.md)** - a small browser UI over `api-server`'s read-only REST API - browse enrolled clients, catalog entries, and fleet-wide jobs
```

- [ ] **Step 3: Add a `CHANGELOG.md` entry**

Add a new dated heading at the top of the changelog (above the existing `## 2026-07-19 — api-server: add GET /api/v1/jobs...` entry, since this ships after it):

```markdown
## 2026-07-19 — web: add a Jobs page

Adds `/jobs` and `/jobs/:job_id` to the `web` frontend, giving a browser view of `api-server`'s
`GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` endpoints. The jobs table uses
`simple-datatables` for client-side search, sort, and pagination over the fetched batch, rather
than a server-side filter form.
```

- [ ] **Step 4: Commit**

```bash
git add docs/components/web.md README.md CHANGELOG.md
git commit -m "docs: document the web frontend's new Jobs page"
```
