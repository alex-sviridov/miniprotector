# Policy Management UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add full CRUD for backup policies to the `web` frontend — list, view, create, edit, and delete — consuming the `/api/v1/policies` endpoints `api-server` already exposes.

**Architecture:** A new `policies` Pinia store wraps the five REST endpoints; three new views (`PoliciesListView`, `PolicyDetailView`, `PolicyFormView`) follow the exact list/detail pattern already used for `clients`. `PolicyFormView` is shared between create (`/policies/new`) and edit (`/policies/:id/edit`), with mode driven by whether `route.params.id` is set.

**Tech Stack:** Vue 3 (`<script setup>`), Pinia, Vue Router, Vitest + `@vue/test-utils` + `@pinia/testing`, Tailwind CSS. No new dependencies.

## Global Constraints

- Every write action in the `policies` store must reset `error` to `null` at the start and set it from `err.message` in a `catch`, matching `stores/clients.js`'s existing pattern exactly.
- `object_filters`' `include`/`exclude` are edited as comma-separated text and split into arrays only at submit time (`split(',')`, trim, drop empties) — per the approved spec, no nested add/remove UI for these two sub-lists.
- Delete is guarded by the browser's native `confirm()` — no custom modal component is introduced.
- All new views/stores get a matching `.spec.js` file in the same directory, mirroring `clients.spec.js` / `ClientsListView.spec.js` / `ClientDetailView.spec.js` exactly (same mocking style: `vi.mock('../api/client', ...)` for stores, `createTestingPinia({ stubActions: true, ... })` for views).
- Run tests via: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test` (no local `node`/`npm` on this machine; `web/node_modules` is already populated).

---

### Task 1: Extend `apiFetch` to handle `204 No Content`

`DELETE /api/v1/policies/{id}` returns `204` with an empty body. `apiFetch` (`web/src/api/client.js`) currently always calls `response.json()` on a successful response, which throws on an empty body. This must be fixed before the `policies` store's `remove` action can use `apiFetch` safely.

**Files:**
- Modify: `web/src/api/client.js`
- Test: `web/src/api/client.spec.js`

**Interfaces:**
- Produces: `apiFetch(path, options)` now resolves to `null` (instead of throwing) when the response status is `204`.

- [ ] **Step 1: Write the failing test**

Add to `web/src/api/client.spec.js`, inside the existing `describe('apiFetch', ...)` block (after the last `it(...)`):

```js
  it('returns null on a 204 No Content response without parsing a body', async () => {
    global.fetch.mockResolvedValue({
      ok: true,
      status: 204,
      json: async () => {
        throw new Error('should not be called')
      },
    })

    const body = await apiFetch('/policies/abc', { method: 'DELETE' })

    expect(body).toBeNull()
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: FAIL — the new test throws `Error: should not be called` because `apiFetch` still calls `response.json()` unconditionally.

- [ ] **Step 3: Write minimal implementation**

In `web/src/api/client.js`, change the end of `apiFetch`:

```js
  if (response.status === 204) {
    return null
  }

  return response.json()
```

(This block goes immediately before the existing final `return response.json()` line, replacing it — i.e. the function's last two statements become the `if` block above, with no trailing `return response.json()` left duplicated.)

- [ ] **Step 4: Run test to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: PASS — all tests in `client.spec.js` pass, including the new one.

- [ ] **Step 5: Commit**

```bash
git add web/src/api/client.js web/src/api/client.spec.js
git commit -m "fix(web): handle 204 No Content responses in apiFetch"
```

---

### Task 2: `policies` Pinia store

**Files:**
- Create: `web/src/stores/policies.js`
- Test: `web/src/stores/policies.spec.js`

**Interfaces:**
- Consumes: `apiFetch(path, options)` from `web/src/api/client.js` (Task 1's `204` handling).
- Produces: `usePoliciesStore()` returning a store with state `{ list, byId, loading, error }` and actions `fetchAll()`, `fetchOne(id)`, `create(input)`, `update(id, input)`, `remove(id)`. `create`/`update`/`remove` rethrow on failure (same as `clients.fetchOne`). `create`/`update` resolve to the created/updated policy object (which has an `id` field). Later tasks (3, 4, 5, 6) call these by these exact names.

- [ ] **Step 1: Write the failing tests**

Create `web/src/stores/policies.spec.js`:

```js
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { usePoliciesStore } from './policies'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('policies store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll populates the list from the API', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 'p1', name: 'nightly' }] })
    const policies = usePoliciesStore()

    await policies.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/policies')
    expect(policies.list).toEqual([{ id: 'p1', name: 'nightly' }])
    expect(policies.loading).toBe(false)
    expect(policies.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const policies = usePoliciesStore()

    await policies.fetchAll()

    expect(policies.error).toBe('boom')
    expect(policies.list).toEqual([])
  })

  it('fetchOne fetches and caches a policy by id', async () => {
    apiFetch.mockResolvedValue({ id: 'p1', name: 'nightly' })
    const policies = usePoliciesStore()

    const first = await policies.fetchOne('p1')
    const second = await policies.fetchOne('p1')

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(apiFetch).toHaveBeenCalledWith('/policies/p1')
    expect(first).toEqual({ id: 'p1', name: 'nightly' })
    expect(second).toEqual(first)
  })

  it('fetchOne records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('policy not found'))
    const policies = usePoliciesStore()

    await expect(policies.fetchOne('missing')).rejects.toThrow('policy not found')
    expect(policies.error).toBe('policy not found')
  })

  it('create posts the input and adds the result to list and byId', async () => {
    const created = { id: 'p2', name: 'weekly' }
    apiFetch.mockResolvedValue(created)
    const policies = usePoliciesStore()

    const input = { name: 'weekly' }
    const result = await policies.create(input)

    expect(apiFetch).toHaveBeenCalledWith('/policies', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(created)
    expect(policies.list).toEqual([created])
    expect(policies.byId.p2).toEqual(created)
  })

  it('create records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('name is required'))
    const policies = usePoliciesStore()

    await expect(policies.create({ name: '' })).rejects.toThrow('name is required')
    expect(policies.error).toBe('name is required')
    expect(policies.list).toEqual([])
  })

  it('update puts the input and replaces the entry in list and byId', async () => {
    const original = { id: 'p1', name: 'nightly' }
    const updated = { id: 'p1', name: 'nightly-renamed' }
    apiFetch.mockResolvedValueOnce({ data: [original] })
    const policies = usePoliciesStore()
    await policies.fetchAll()

    apiFetch.mockResolvedValueOnce(updated)
    const input = { name: 'nightly-renamed' }
    const result = await policies.update('p1', input)

    expect(apiFetch).toHaveBeenCalledWith('/policies/p1', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(updated)
    expect(policies.list).toEqual([updated])
    expect(policies.byId.p1).toEqual(updated)
  })

  it('update records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('invalid glob pattern'))
    const policies = usePoliciesStore()

    await expect(policies.update('p1', { name: 'x' })).rejects.toThrow('invalid glob pattern')
    expect(policies.error).toBe('invalid glob pattern')
  })

  it('remove deletes and drops the entry from list and byId', async () => {
    apiFetch.mockResolvedValueOnce({ data: [{ id: 'p1', name: 'nightly' }] })
    const policies = usePoliciesStore()
    await policies.fetchAll()
    policies.byId.p1 = { id: 'p1', name: 'nightly' }

    apiFetch.mockResolvedValueOnce(null)
    await policies.remove('p1')

    expect(apiFetch).toHaveBeenCalledWith('/policies/p1', { method: 'DELETE' })
    expect(policies.list).toEqual([])
    expect(policies.byId.p1).toBeUndefined()
  })

  it('remove records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('policy not found'))
    const policies = usePoliciesStore()

    await expect(policies.remove('missing')).rejects.toThrow('policy not found')
    expect(policies.error).toBe('policy not found')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: FAIL — `Cannot find module './policies'` (the store doesn't exist yet).

- [ ] **Step 3: Write minimal implementation**

Create `web/src/stores/policies.js`:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

export const usePoliciesStore = defineStore('policies', {
  state: () => ({
    list: [],
    byId: {},
    loading: false,
    error: null,
  }),
  actions: {
    async fetchAll() {
      this.loading = true
      this.error = null
      try {
        const body = await apiFetch('/policies')
        this.list = body.data
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async fetchOne(id) {
      if (this.byId[id]) {
        this.error = null
        return this.byId[id]
      }
      this.loading = true
      this.error = null
      try {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
        this.byId[id] = policy
        return policy
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async create(input) {
      this.loading = true
      this.error = null
      try {
        const policy = await apiFetch('/policies', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        this.list.push(policy)
        this.byId[policy.id] = policy
        return policy
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async update(id, input) {
      this.loading = true
      this.error = null
      try {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        const idx = this.list.findIndex((p) => p.id === id)
        if (idx !== -1) this.list[idx] = policy
        this.byId[id] = policy
        return policy
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async remove(id) {
      this.loading = true
      this.error = null
      try {
        await apiFetch(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' })
        this.list = this.list.filter((p) => p.id !== id)
        delete this.byId[id]
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

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: PASS — all tests in `policies.spec.js` pass, and no other spec file regresses.

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/policies.js web/src/stores/policies.spec.js
git commit -m "feat(web): add policies store"
```

---

### Task 3: `PoliciesListView`

**Files:**
- Create: `web/src/views/PoliciesListView.vue`
- Test: `web/src/views/PoliciesListView.spec.js`

**Interfaces:**
- Consumes: `usePoliciesStore()` (Task 2) — `list`, `loading`, `error`, `fetchAll()`, `remove(id)`.
- Produces: a `/policies` page; each row links to `/policies/:id`; a "New Policy" link to `/policies/new`; a "Delete" button per row.

- [ ] **Step 1: Write the failing test**

Create `web/src/views/PoliciesListView.spec.js`:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import PoliciesListView from './PoliciesListView.vue'
import { usePoliciesStore } from '../stores/policies'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(PoliciesListView, {
    global: {
      plugins: [pinia],
      stubs: { RouterLink: { template: '<a :href="to"><slot /></a>', props: ['to'] } },
    },
  })
  return { wrapper, policies: usePoliciesStore() }
}

describe('PoliciesListView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('calls fetchAll on mount', () => {
    const { policies } = mountView({ list: [], loading: false, error: null })
    expect(policies.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('renders each policy with a link to its detail page', () => {
    const { wrapper } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup' }],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('nightly-db-backup')
    expect(wrapper.find('a[href="/policies/p1"]').exists()).toBe(true)
  })

  it('links to the create form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('a[href="/policies/new"]').exists()).toBe(true)
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('deletes a policy after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, policies } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup' }],
      loading: false,
      error: null,
    })

    await wrapper.find('button').trigger('click')

    expect(policies.remove).toHaveBeenCalledWith('p1')
  })

  it('does not delete when the confirm dialog is dismissed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { wrapper, policies } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup' }],
      loading: false,
      error: null,
    })

    await wrapper.find('button').trigger('click')

    expect(policies.remove).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: FAIL — `Failed to resolve import "./PoliciesListView.vue"`.

- [ ] **Step 3: Write minimal implementation**

Create `web/src/views/PoliciesListView.vue`:

```vue
<script setup>
import { onMounted } from 'vue'
import { usePoliciesStore } from '../stores/policies'

const policies = usePoliciesStore()

onMounted(() => {
  policies.fetchAll()
})

function confirmDelete(id) {
  if (window.confirm('Delete this policy?')) {
    policies.remove(id)
  }
}
</script>

<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-semibold">Policies</h1>
      <router-link to="/policies/new" class="bg-blue-600 text-white rounded px-3 py-1">
        New Policy
      </router-link>
    </div>
    <p v-if="policies.loading">Loading...</p>
    <p v-else-if="policies.error" class="text-red-600">{{ policies.error }}</p>
    <table v-else class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Name</th>
          <th class="py-2 pr-4">RPO</th>
          <th class="py-2 pr-4">Destination</th>
          <th class="py-2 pr-4"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="policy in policies.list" :key="policy.id" class="border-b hover:bg-gray-50">
          <td class="py-2 pr-4">
            <router-link :to="`/policies/${policy.id}`" class="text-blue-600 hover:underline">
              {{ policy.name }}
            </router-link>
          </td>
          <td class="py-2 pr-4">{{ policy.rpo }}</td>
          <td class="py-2 pr-4">{{ policy.destination }}</td>
          <td class="py-2 pr-4">
            <button @click="confirmDelete(policy.id)" class="border rounded px-2 py-1">Delete</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: PASS — all tests in `PoliciesListView.spec.js` pass.

- [ ] **Step 5: Commit**

```bash
git add web/src/views/PoliciesListView.vue web/src/views/PoliciesListView.spec.js
git commit -m "feat(web): add policies list view"
```

---

### Task 4: `PolicyDetailView`

**Files:**
- Create: `web/src/views/PolicyDetailView.vue`
- Test: `web/src/views/PolicyDetailView.spec.js`

**Interfaces:**
- Consumes: `usePoliciesStore()` (Task 2) — `byId`, `loading`, `error`, `fetchOne(id)`, `remove(id)`; `useRoute()`/`useRouter()` from `vue-router`.
- Produces: a `/policies/:id` page rendering one policy's fields, an "Edit" link to `/policies/:id/edit`, and a "Delete" button that navigates to `/policies` on success.

- [ ] **Step 1: Write the failing test**

Create `web/src/views/PolicyDetailView.spec.js`:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import PolicyDetailView from './PolicyDetailView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 'p1' } }),
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(PolicyDetailView, {
    global: {
      plugins: [pinia],
      stubs: { RouterLink: { template: '<a :href="to"><slot /></a>', props: ['to'] } },
    },
  })
  return { wrapper, policies: usePoliciesStore() }
}

describe('PolicyDetailView', () => {
  afterEach(() => {
    push.mockReset()
    vi.restoreAllMocks()
  })

  it('calls fetchOne with the route id on mount', () => {
    const { policies } = mountView({ byId: {}, loading: false, error: null })
    expect(policies.fetchOne).toHaveBeenCalledWith('p1')
  })

  it('renders the cached policy record', () => {
    const { wrapper } = mountView({
      byId: {
        p1: {
          id: 'p1',
          name: 'nightly-db-backup',
          rpo: '1h',
          destination: 'store:8080',
          client_filters: { hostnames: ['database'], labels: {} },
          object_filters: [{ id: 'f1', path: '/var/lib/dbdata', include: [], exclude: [] }],
          backup_window: ['0 * * * *'],
        },
      },
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('nightly-db-backup')
    expect(wrapper.text()).toContain('1h')
    expect(wrapper.text()).toContain('/var/lib/dbdata')
    expect(wrapper.find('a[href="/policies/p1/edit"]').exists()).toBe(true)
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byId: {}, loading: false, error: 'policy not found' })
    expect(wrapper.text()).toContain('policy not found')
  })

  it('deletes the policy after confirming and navigates to the list', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.remove.mockResolvedValue(undefined)

    await wrapper.find('button').trigger('click')
    await Promise.resolve()

    expect(policies.remove).toHaveBeenCalledWith('p1')
    expect(push).toHaveBeenCalledWith('/policies')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: FAIL — `Failed to resolve import "./PolicyDetailView.vue"`.

- [ ] **Step 3: Write minimal implementation**

Create `web/src/views/PolicyDetailView.vue`:

```vue
<script setup>
import { onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import { formatTimestamp } from '../utils/format'

const route = useRoute()
const router = useRouter()
const policies = usePoliciesStore()
const id = computed(() => route.params.id)

onMounted(async () => {
  try {
    await policies.fetchOne(id.value)
  } catch {
    // error already recorded on policies.error by the store
  }
})

async function confirmDelete() {
  if (window.confirm('Delete this policy?')) {
    await policies.remove(id.value)
    router.push('/policies')
  }
}
</script>

<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-semibold">{{ policies.byId[id]?.name || id }}</h1>
      <div class="flex gap-2">
        <router-link :to="`/policies/${id}/edit`" class="border rounded px-3 py-1">Edit</router-link>
        <button @click="confirmDelete" class="border rounded px-3 py-1">Delete</button>
      </div>
    </div>
    <p v-if="policies.loading">Loading...</p>
    <p v-else-if="policies.error" class="text-red-600">{{ policies.error }}</p>
    <dl v-else-if="policies.byId[id]" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
      <dt class="font-medium">RPO</dt>
      <dd>{{ policies.byId[id].rpo }}</dd>
      <dt class="font-medium">Destination</dt>
      <dd>{{ policies.byId[id].destination }}</dd>
      <dt class="font-medium">Backup Window</dt>
      <dd>{{ (policies.byId[id].backup_window || []).join(', ') || '—' }}</dd>
      <dt class="font-medium">Hostnames</dt>
      <dd>{{ (policies.byId[id].client_filters?.hostnames || []).join(', ') || '—' }}</dd>
      <dt class="font-medium">Labels</dt>
      <dd>{{ JSON.stringify(policies.byId[id].client_filters?.labels || {}) }}</dd>
      <dt class="font-medium">Object Filters</dt>
      <dd>
        <ul>
          <li v-for="f in policies.byId[id].object_filters || []" :key="f.id">
            {{ f.path }}
            <span v-if="f.include?.length"> include: {{ f.include.join(', ') }}</span>
            <span v-if="f.exclude?.length"> exclude: {{ f.exclude.join(', ') }}</span>
          </li>
        </ul>
      </dd>
      <dt class="font-medium">Created</dt>
      <dd>{{ formatTimestamp(policies.byId[id].created_at) || '—' }}</dd>
      <dt class="font-medium">Updated</dt>
      <dd>{{ formatTimestamp(policies.byId[id].updated_at) || '—' }}</dd>
    </dl>
  </div>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: PASS — all tests in `PolicyDetailView.spec.js` pass.

- [ ] **Step 5: Commit**

```bash
git add web/src/views/PolicyDetailView.vue web/src/views/PolicyDetailView.spec.js
git commit -m "feat(web): add policy detail view"
```

---

### Task 5: `PolicyFormView` — core fields, mode detection, submit

Builds the shared create/edit form's skeleton: `name`/`rpo`/`destination`, mode detection from the route, edit-mode pre-population, submit dispatch to `create`/`update`, and error display. `client_filters`, `object_filters`, and `backup_window` are wired into the payload as empty/unedited collections in this task; Task 6 adds the UI to edit them.

**Files:**
- Create: `web/src/views/PolicyFormView.vue`
- Test: `web/src/views/PolicyFormView.spec.js`

**Interfaces:**
- Consumes: `usePoliciesStore()` (Task 2) — `error`, `fetchOne(id)`, `create(input)`, `update(id, input)`; `useRoute()`/`useRouter()` from `vue-router`.
- Produces: `/policies/new` and `/policies/:id/edit` pages. Internal functions `buildPayload()` and `toFormShape(policy)` are extended (not replaced) in Task 6 — their return shapes must keep the same top-level keys (`name`, `client_filters`, `object_filters`, `rpo`, `backup_window`, `destination`).

- [ ] **Step 1: Write the failing tests**

Create `web/src/views/PolicyFormView.spec.js`:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import PolicyFormView from './PolicyFormView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()
let routeParams = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: routeParams }),
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(PolicyFormView, { global: { plugins: [pinia] } })
  return { wrapper, policies: usePoliciesStore() }
}

describe('PolicyFormView', () => {
  afterEach(() => {
    push.mockReset()
    routeParams = {}
  })

  describe('create mode', () => {
    it('does not fetch an existing policy on mount', () => {
      routeParams = {}
      const { policies } = mountView({ error: null })
      expect(policies.fetchOne).not.toHaveBeenCalled()
    })

    it('submits a create request with the entered core fields', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('input[name="name"]').setValue('nightly-db-backup')
      await wrapper.find('input[name="rpo"]').setValue('1h')
      await wrapper.find('input[name="destination"]').setValue('store:8080')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.create).toHaveBeenCalledWith({
        name: 'nightly-db-backup',
        client_filters: { hostnames: [], labels: {} },
        object_filters: [],
        rpo: '1h',
        backup_window: [],
        destination: 'store:8080',
      })
      expect(push).toHaveBeenCalledWith('/policies/p9')
    })
  })

  describe('edit mode', () => {
    it('fetches and pre-populates the existing policy on mount', async () => {
      routeParams = { id: 'p1' }
      const { wrapper, policies } = mountView({ error: null })
      policies.fetchOne.mockResolvedValue({
        id: 'p1',
        name: 'nightly-db-backup',
        rpo: '1h',
        destination: 'store:8080',
        client_filters: { hostnames: [], labels: {} },
        object_filters: [],
        backup_window: [],
      })

      await wrapper.vm.$nextTick()
      await Promise.resolve()
      await wrapper.vm.$nextTick()

      expect(policies.fetchOne).toHaveBeenCalledWith('p1')
      expect(wrapper.find('input[name="name"]').element.value).toBe('nightly-db-backup')
      expect(wrapper.find('input[name="rpo"]').element.value).toBe('1h')
    })

    it('submits an update request addressed by the route id', async () => {
      routeParams = { id: 'p1' }
      const { wrapper, policies } = mountView({ error: null })
      policies.fetchOne.mockResolvedValue({
        id: 'p1',
        name: 'nightly-db-backup',
        rpo: '1h',
        destination: 'store:8080',
        client_filters: { hostnames: [], labels: {} },
        object_filters: [],
        backup_window: [],
      })
      policies.update.mockResolvedValue({ id: 'p1' })

      await wrapper.vm.$nextTick()
      await Promise.resolve()
      await wrapper.vm.$nextTick()

      await wrapper.find('input[name="rpo"]').setValue('2h')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.update).toHaveBeenCalledWith('p1', {
        name: 'nightly-db-backup',
        client_filters: { hostnames: [], labels: {} },
        object_filters: [],
        rpo: '2h',
        backup_window: [],
        destination: 'store:8080',
      })
      expect(push).toHaveBeenCalledWith('/policies/p1')
    })
  })

  it('shows the store error message and keeps entered values on submit failure', async () => {
    routeParams = {}
    const { wrapper, policies } = mountView({ error: null })
    policies.create.mockRejectedValue(new Error('name is required'))

    await wrapper.find('input[name="name"]').setValue('bad-policy')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()
    policies.error = 'name is required'
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('name is required')
    expect(wrapper.find('input[name="name"]').element.value).toBe('bad-policy')
    expect(push).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: FAIL — `Failed to resolve import "./PolicyFormView.vue"`.

- [ ] **Step 3: Write minimal implementation**

Create `web/src/views/PolicyFormView.vue`:

```vue
<script setup>
import { reactive, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'

const route = useRoute()
const router = useRouter()
const policies = usePoliciesStore()

const isEdit = computed(() => !!route.params.id)

function emptyForm() {
  return {
    name: '',
    client_filters: { hostnames: [], labels: [] },
    object_filters: [],
    rpo: '',
    backup_window: [],
    destination: '',
  }
}

function toFormShape(policy) {
  return {
    name: policy.name,
    client_filters: {
      hostnames: [...(policy.client_filters?.hostnames || [])],
      labels: Object.entries(policy.client_filters?.labels || {}).map(([key, value]) => ({ key, value })),
    },
    object_filters: (policy.object_filters || []).map((f) => ({
      path: f.path,
      includeText: (f.include || []).join(', '),
      excludeText: (f.exclude || []).join(', '),
    })),
    rpo: policy.rpo,
    backup_window: [...(policy.backup_window || [])],
    destination: policy.destination,
  }
}

const form = reactive(emptyForm())

onMounted(async () => {
  if (isEdit.value) {
    try {
      const policy = await policies.fetchOne(route.params.id)
      Object.assign(form, toFormShape(policy))
    } catch {
      // error already recorded on policies.error by the store
    }
  }
})

function splitCsv(text) {
  return text.split(',').map((s) => s.trim()).filter(Boolean)
}

function buildPayload() {
  return {
    name: form.name,
    client_filters: {
      hostnames: form.client_filters.hostnames.map((h) => h.trim()).filter(Boolean),
      labels: Object.fromEntries(
        form.client_filters.labels
          .map((l) => [l.key.trim(), l.value.trim()])
          .filter(([key]) => key)
      ),
    },
    object_filters: form.object_filters
      .filter((f) => f.path.trim())
      .map((f) => ({
        path: f.path.trim(),
        include: splitCsv(f.includeText || ''),
        exclude: splitCsv(f.excludeText || ''),
      })),
    rpo: form.rpo,
    backup_window: form.backup_window.map((w) => w.trim()).filter(Boolean),
    destination: form.destination,
  }
}

async function submit() {
  const payload = buildPayload()
  try {
    const policy = isEdit.value
      ? await policies.update(route.params.id, payload)
      : await policies.create(payload)
    router.push(`/policies/${policy.id}`)
  } catch {
    // error already recorded on policies.error by the store
  }
}
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">{{ isEdit ? 'Edit Policy' : 'New Policy' }}</h1>
    <p v-if="policies.error" class="text-red-600 mb-4">{{ policies.error }}</p>
    <form @submit.prevent="submit" class="space-y-6 max-w-2xl">
      <div>
        <label class="block font-medium mb-1">Name</label>
        <input name="name" v-model="form.name" required class="w-full border rounded px-2 py-1" />
      </div>

      <div>
        <label class="block font-medium mb-1">RPO</label>
        <input name="rpo" v-model="form.rpo" placeholder="e.g. 24h" class="w-full border rounded px-2 py-1" />
      </div>

      <div>
        <label class="block font-medium mb-1">Destination</label>
        <input name="destination" v-model="form.destination" placeholder="host:port" class="w-full border rounded px-2 py-1" />
      </div>

      <button type="submit" class="bg-blue-600 text-white rounded px-4 py-2">
        {{ isEdit ? 'Save Changes' : 'Create Policy' }}
      </button>
    </form>
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: PASS — all tests in `PolicyFormView.spec.js` pass.

- [ ] **Step 5: Commit**

```bash
git add web/src/views/PolicyFormView.vue web/src/views/PolicyFormView.spec.js
git commit -m "feat(web): add policy form view (core fields)"
```

---

### Task 6: `PolicyFormView` — dynamic list/map fields

Adds the add/remove-row editing UI for `client_filters.hostnames`, `client_filters.labels`, `backup_window`, and `object_filters` on top of Task 5's skeleton. Extends (does not replace) `buildPayload()`/`toFormShape()` from Task 5 — same top-level payload keys, now actually populated from user input.

**Files:**
- Modify: `web/src/views/PolicyFormView.vue`
- Modify: `web/src/views/PolicyFormView.spec.js`

**Interfaces:**
- Consumes: nothing new — same store actions as Task 5.
- Produces: `buildPayload()` now includes non-empty `client_filters.hostnames`/`labels`, `object_filters`, `backup_window` when the operator has added rows.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/views/PolicyFormView.spec.js`, inside the top-level `describe('PolicyFormView', ...)` block (as a new nested `describe`, alongside the existing `'create mode'`/`'edit mode'` blocks):

```js
  describe('dynamic list fields', () => {
    it('adds and removes hostname rows, sending only non-empty trimmed values', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('[data-test="add-hostname"]').trigger('click')
      await wrapper.find('[data-test="add-hostname"]').trigger('click')
      const hostnameInputs = wrapper.findAll('[data-test="hostname-input"]')
      await hostnameInputs[0].setValue('database')
      await hostnameInputs[1].setValue('  ')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.create).toHaveBeenCalledWith(
        expect.objectContaining({ client_filters: { hostnames: ['database'], labels: {} } })
      )
    })

    it('adds a label row and sends it as a key/value map', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('[data-test="add-label"]').trigger('click')
      await wrapper.find('[data-test="label-key-input"]').setValue('env')
      await wrapper.find('[data-test="label-value-input"]').setValue('prod')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.create).toHaveBeenCalledWith(
        expect.objectContaining({ client_filters: { hostnames: [], labels: { env: 'prod' } } })
      )
    })

    it('adds a backup window row', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('[data-test="add-window"]').trigger('click')
      await wrapper.find('[data-test="window-input"]').setValue('0 2 * * *')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.create).toHaveBeenCalledWith(
        expect.objectContaining({ backup_window: ['0 2 * * *'] })
      )
    })

    it('adds an object filter and splits comma-separated include/exclude into arrays', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('[data-test="add-filter"]').trigger('click')
      await wrapper.find('[data-test="filter-path-input"]').setValue('/var/lib/dbdata')
      await wrapper.find('[data-test="filter-include-input"]').setValue('*.sql, *.dump')
      await wrapper.find('[data-test="filter-exclude-input"]').setValue('*.tmp')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.create).toHaveBeenCalledWith(
        expect.objectContaining({
          object_filters: [{ path: '/var/lib/dbdata', include: ['*.sql', '*.dump'], exclude: ['*.tmp'] }],
        })
      )
    })

    it('removes a row via its remove button', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('[data-test="add-hostname"]').trigger('click')
      await wrapper.find('[data-test="hostname-input"]').setValue('database')
      await wrapper.find('[data-test="remove-hostname"]').trigger('click')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.create).toHaveBeenCalledWith(
        expect.objectContaining({ client_filters: { hostnames: [], labels: {} } })
      )
    })

    it('pre-populates existing object filters and labels in edit mode', async () => {
      routeParams = { id: 'p1' }
      const { wrapper, policies } = mountView({ error: null })
      policies.fetchOne.mockResolvedValue({
        id: 'p1',
        name: 'nightly-db-backup',
        rpo: '1h',
        destination: 'store:8080',
        client_filters: { hostnames: ['database'], labels: { env: 'prod' } },
        object_filters: [{ id: 'f1', path: '/var/lib/dbdata', include: ['*.sql'], exclude: [] }],
        backup_window: ['0 2 * * *'],
      })

      await wrapper.vm.$nextTick()
      await Promise.resolve()
      await wrapper.vm.$nextTick()

      expect(wrapper.find('[data-test="hostname-input"]').element.value).toBe('database')
      expect(wrapper.find('[data-test="label-key-input"]').element.value).toBe('env')
      expect(wrapper.find('[data-test="label-value-input"]').element.value).toBe('prod')
      expect(wrapper.find('[data-test="filter-path-input"]').element.value).toBe('/var/lib/dbdata')
      expect(wrapper.find('[data-test="filter-include-input"]').element.value).toBe('*.sql')
      expect(wrapper.find('[data-test="window-input"]').element.value).toBe('0 2 * * *')
    })
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: FAIL — the new tests can't find `[data-test="add-hostname"]` etc., since the template only has `name`/`rpo`/`destination` inputs so far.

- [ ] **Step 3: Write minimal implementation**

In `web/src/views/PolicyFormView.vue`, add these functions to the `<script setup>` block, placed after `const form = reactive(emptyForm())` / the `onMounted` block and before `splitCsv`:

```js
function addHostname() {
  form.client_filters.hostnames.push('')
}
function removeHostname(i) {
  form.client_filters.hostnames.splice(i, 1)
}
function addLabel() {
  form.client_filters.labels.push({ key: '', value: '' })
}
function removeLabel(i) {
  form.client_filters.labels.splice(i, 1)
}
function addWindow() {
  form.backup_window.push('')
}
function removeWindow(i) {
  form.backup_window.splice(i, 1)
}
function addFilter() {
  form.object_filters.push({ path: '', includeText: '', excludeText: '' })
}
function removeFilter(i) {
  form.object_filters.splice(i, 1)
}
```

Then replace the `<template>` block's `<form>...</form>` contents to insert the new field groups between the "Name" group and the "RPO" group:

```vue
      <div>
        <label class="block font-medium mb-1">Hostnames (glob patterns)</label>
        <div v-for="(_, i) in form.client_filters.hostnames" :key="i" class="flex gap-2 mb-1">
          <input
            data-test="hostname-input"
            v-model="form.client_filters.hostnames[i]"
            class="flex-1 border rounded px-2 py-1"
          />
          <button type="button" data-test="remove-hostname" @click="removeHostname(i)" class="border rounded px-2">
            Remove
          </button>
        </div>
        <button type="button" data-test="add-hostname" @click="addHostname" class="border rounded px-3 py-1">
          Add Hostname
        </button>
      </div>

      <div>
        <label class="block font-medium mb-1">Labels</label>
        <div v-for="(_, i) in form.client_filters.labels" :key="i" class="flex gap-2 mb-1">
          <input
            data-test="label-key-input"
            v-model="form.client_filters.labels[i].key"
            placeholder="key"
            class="flex-1 border rounded px-2 py-1"
          />
          <input
            data-test="label-value-input"
            v-model="form.client_filters.labels[i].value"
            placeholder="value"
            class="flex-1 border rounded px-2 py-1"
          />
          <button type="button" data-test="remove-label" @click="removeLabel(i)" class="border rounded px-2">
            Remove
          </button>
        </div>
        <button type="button" data-test="add-label" @click="addLabel" class="border rounded px-3 py-1">
          Add Label
        </button>
      </div>

      <div>
        <label class="block font-medium mb-1">Object Filters</label>
        <div v-for="(_, i) in form.object_filters" :key="i" class="border rounded p-2 mb-2 space-y-1">
          <input
            data-test="filter-path-input"
            v-model="form.object_filters[i].path"
            placeholder="path"
            class="w-full border rounded px-2 py-1"
          />
          <input
            data-test="filter-include-input"
            v-model="form.object_filters[i].includeText"
            placeholder="include patterns, comma-separated"
            class="w-full border rounded px-2 py-1"
          />
          <input
            data-test="filter-exclude-input"
            v-model="form.object_filters[i].excludeText"
            placeholder="exclude patterns, comma-separated"
            class="w-full border rounded px-2 py-1"
          />
          <button type="button" data-test="remove-filter" @click="removeFilter(i)" class="border rounded px-2">
            Remove Filter
          </button>
        </div>
        <button type="button" data-test="add-filter" @click="addFilter" class="border rounded px-3 py-1">
          Add Object Filter
        </button>
      </div>
```

And between the "RPO" group and the "Destination" group:

```vue
      <div>
        <label class="block font-medium mb-1">Backup Window (cron expressions)</label>
        <div v-for="(_, i) in form.backup_window" :key="i" class="flex gap-2 mb-1">
          <input
            data-test="window-input"
            v-model="form.backup_window[i]"
            placeholder="0 2 * * *"
            class="flex-1 border rounded px-2 py-1"
          />
          <button type="button" data-test="remove-window" @click="removeWindow(i)" class="border rounded px-2">
            Remove
          </button>
        </div>
        <button type="button" data-test="add-window" @click="addWindow" class="border rounded px-3 py-1">
          Add Window
        </button>
      </div>
```

The full field order in the template is now: Name, Hostnames, Labels, Object Filters, RPO, Backup Window, Destination, Submit button.

- [ ] **Step 4: Run tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: PASS — all tests in `PolicyFormView.spec.js` pass, including both the Task 5 and Task 6 tests, and no other spec file regresses.

- [ ] **Step 5: Commit**

```bash
git add web/src/views/PolicyFormView.vue web/src/views/PolicyFormView.spec.js
git commit -m "feat(web): add dynamic list/map fields to policy form"
```

---

### Task 7: Wire up routing and navigation

**Files:**
- Modify: `web/src/router.js`
- Modify: `web/src/components/Sidebar.vue`

**Interfaces:**
- Consumes: `PoliciesListView`, `PolicyDetailView`, `PolicyFormView` (Tasks 3, 4, 5/6).
- Produces: `/policies`, `/policies/new`, `/policies/:id`, `/policies/:id/edit` routes; a "Policies" sidebar link.

Neither `router.js` nor `Sidebar.vue` has a dedicated spec file in this codebase today (only `App.spec.js` checks that `<nav>` renders once authenticated, and it doesn't assert on specific links) — this task is verified by the existing `App.spec.js` continuing to pass plus the manual walkthrough in Task 9, consistent with how `/catalog` and `/clients` were wired up.

- [ ] **Step 1: Add the new routes**

In `web/src/router.js`, add the three new imports after the existing `CatalogView` import:

```js
import PoliciesListView from './views/PoliciesListView.vue'
import PolicyDetailView from './views/PolicyDetailView.vue'
import PolicyFormView from './views/PolicyFormView.vue'
```

Add four new route entries after the existing `{ path: '/catalog', component: CatalogView }` line:

```js
    { path: '/policies', component: PoliciesListView },
    { path: '/policies/new', component: PolicyFormView },
    { path: '/policies/:id', component: PolicyDetailView },
    { path: '/policies/:id/edit', component: PolicyFormView },
```

- [ ] **Step 2: Add the sidebar link**

In `web/src/components/Sidebar.vue`, add a new `router-link` after the existing "Catalog" link, before the closing `</nav>`:

```vue
    <router-link to="/policies" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Policies
    </router-link>
```

- [ ] **Step 3: Run the full test suite**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: PASS — every spec file (including `App.spec.js`) still passes; no test asserts on route/sidebar content that would need updating.

- [ ] **Step 4: Commit**

```bash
git add web/src/router.js web/src/components/Sidebar.vue
git commit -m "feat(web): wire up policy routes and sidebar link"
```

---

### Task 8: Documentation

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update the routes list in `docs/components/web.md`**

Change the file's header line 3 from:

```
A small read-only browser UI over [api-server](./api-server.md)'s REST API — lists enrolled
clients and browses catalog entries. **Not a mesh member:** unlike every other control-plane
```

to:

```
A small browser UI over [api-server](./api-server.md)'s REST API — lists enrolled clients,
browses catalog entries, and manages backup policies (list/create/edit/delete). **Not a mesh
member:** unlike every other control-plane
```

Change the `## Pages` list (currently ending at the `/catalog` bullet) by adding four new bullets after it:

```
- `/policies` — every policy (name, RPO, destination), linking to:
- `/policies/:id` — one policy's full record (client filters, object filters, backup window)
- `/policies/new` — create a new policy
- `/policies/:id/edit` — edit an existing policy
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Read the top of `CHANGELOG.md` first to match its existing entry format (most-recent-first date heading, bullet list under it), then add a new entry dated `2026-07-18` (or the current top entry's date, appending to it if one for today already exists) describing: "web: add policy management UI (list, create, edit, delete)".

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document the policy management UI"
```

---

### Task 9: Manual end-to-end verification

No automated test exercises the real `policy-server` file-backed storage or a real browser DOM — this task is the smoke test the spec calls for, matching how the original web frontend design was manually verified.

- [ ] **Step 1: Bring up the demo lab**

Run: `make demo-up`
Expected: all services (including `policy-server`, `api-server`, `web`) start successfully.

- [ ] **Step 2: Walk through the UI in a browser**

Navigate to the `web` service's published port (see `demo/README.md` for the exact port and bearer token). Enter the bearer token, then:
1. Click "Policies" in the sidebar — confirm the existing demo policies (`database-backup`, `webserver-backup`, `audit-logs`) render.
2. Click "New Policy" — fill in `name`, `rpo`, `destination`, add one hostname, one label, one object filter (with include/exclude), one backup window entry — submit. Confirm it redirects to the new policy's detail page and all entered fields render correctly there.
3. Click "Edit" on that policy — change the `rpo` value — submit. Confirm the detail page reflects the change.
4. Click "Delete" on that policy from the list — confirm the browser's confirm dialog appears, accept it — confirm the policy disappears from the list.

- [ ] **Step 3: Tear down**

Run: `make demo-down` (or the project's equivalent teardown target — check `Makefile` if the exact name differs)

No commit for this task — it's verification only.
