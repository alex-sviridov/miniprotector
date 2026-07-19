# clientmanager-admin-api Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a UI in `web` for the seven client-write endpoints `clientmanager-admin-api` added (enroll, re-enroll, revoke/unrevoke, description/attribute/SAN management), matching the existing `/policies` store/list/detail/form pattern.

**Architecture:** Extend `stores/clients.js` with one action per RPC (mirroring `stores/policies.js`'s `create`/`update`/`remove` shape). Add a `/clients/new` enroll form. Extend `ClientDetailView.vue` with Revoke/Unrevoke/Re-enroll actions, a one-time token banner, and two new small reusable components (`KeyValueEditor.vue` for description/attributes, `SanListEditor.vue` for SANs) providing inline add/remove editing gated by a per-section "Update" button. Wrap `ClientsListView.vue`'s table with `simple-datatables`, matching `JobsListView.vue`.

**Tech Stack:** Vue 3 (`<script setup>`), Pinia, Vue Router 4, `simple-datatables`, Vitest + `@vue/test-utils` + `@pinia/testing`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-19-clientmanager-admin-api-frontend-design.md` — every task implements one piece of it.
- No new store, no generic "patch" abstraction: one named action per RPC, following `policies.js`'s exact `loading`/`error`/try-catch-finally shape.
- Enrollment tokens are never written into `list`/`byHostname` (the persistent client cache) — only into the transient `pendingToken` field, read once and cleared.
- All work happens in `web/` (run commands from there): `npm test` runs the full Vitest suite; there is no separate build/typecheck step required by this plan beyond tests passing.
- Match existing conventions exactly: Tailwind utility classes as already used (`border rounded px-2 py-1` for inputs, `bg-blue-600 text-white rounded px-3 py-1` for primary buttons, `data-test="..."` attributes for anything a test needs to target), `<script setup>` throughout, no TypeScript (this project is plain JS).

---

### Task 1: `stores/clients.js` — seven write actions

**Files:**
- Modify: `web/src/stores/clients.js`
- Modify: `web/src/stores/clients.spec.js`

**Interfaces:**
- Produces: state field `pendingToken: { hostname, token } | null` (default `null`); actions `enroll(hostname, sans)`, `reenroll(hostname, sans)`, `revoke(hostname)`, `unrevoke(hostname)`, `updateDescription(hostname, set, unset)`, `updateAttributes(hostname, set, unset)`, `updateSans(hostname, add, remove)`, and a shared `updateCache(client)` helper action — all consumed by Tasks 3 and 6.

- [ ] **Step 1: Write the failing tests**

Append to `web/src/stores/clients.spec.js`, inside the existing `describe('clients store', ...)` block (before its closing `})`):

```js
  it('enroll posts hostname/sans, records a minimal list entry, and sets pendingToken', async () => {
    apiFetch.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })
    const clients = useClientsStore()

    const result = await clients.enroll('node-1', ['alias.internal'])

    expect(apiFetch).toHaveBeenCalledWith('/clients', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ hostname: 'node-1', sans: ['alias.internal'] }),
    })
    expect(result).toEqual({ hostname: 'node-1', token: 'tok-abc' })
    expect(clients.list).toEqual([{ hostname: 'node-1', revoked: false, revoked_at: 0, last_seen_at: 0 }])
    expect(clients.pendingToken).toEqual({ hostname: 'node-1', token: 'tok-abc' })
  })

  it('enroll records an error and rethrows on failure', async () => {
    apiFetch.mockRejectedValue(new Error('client node-1 already enrolled'))
    const clients = useClientsStore()

    await expect(clients.enroll('node-1', [])).rejects.toThrow('client node-1 already enrolled')
    expect(clients.error).toBe('client node-1 already enrolled')
    expect(clients.pendingToken).toBeNull()
  })

  it('reenroll posts sans, sets pendingToken, and does not touch list/byHostname', async () => {
    apiFetch.mockResolvedValue({ hostname: 'node-1', token: 'tok-fresh' })
    const clients = useClientsStore()

    const result = await clients.reenroll('node-1', ['override.internal'])

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/reenroll', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sans: ['override.internal'] }),
    })
    expect(result).toEqual({ hostname: 'node-1', token: 'tok-fresh' })
    expect(clients.pendingToken).toEqual({ hostname: 'node-1', token: 'tok-fresh' })
    expect(clients.list).toEqual([])
  })

  it('revoke posts to the revoke endpoint and updates byHostname and the matching list row', async () => {
    const updated = { hostname: 'node-1', revoked: true, revoked_at: 111, last_seen_at: 0 }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()
    clients.list = [{ hostname: 'node-1', revoked: false, revoked_at: 0, last_seen_at: 0 }]

    const result = await clients.revoke('node-1')

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/revoke', { method: 'POST' })
    expect(result).toEqual(updated)
    expect(clients.byHostname['node-1']).toEqual(updated)
    expect(clients.list[0]).toEqual(updated)
  })

  it('unrevoke posts to the unrevoke endpoint and updates the cache', async () => {
    const updated = { hostname: 'node-1', revoked: false, revoked_at: 0, last_seen_at: 0 }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()
    clients.list = [{ hostname: 'node-1', revoked: true, revoked_at: 111, last_seen_at: 0 }]

    await clients.unrevoke('node-1')

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/unrevoke', { method: 'POST' })
    expect(clients.byHostname['node-1']).toEqual(updated)
    expect(clients.list[0]).toEqual(updated)
  })

  it('updateDescription PATCHes set/unset and updates the cache', async () => {
    const updated = { hostname: 'node-1', descriptions: { owner: 'alice' } }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()

    const result = await clients.updateDescription('node-1', { owner: 'alice' }, ['old'])

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/description', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ set: { owner: 'alice' }, unset: ['old'] }),
    })
    expect(result).toEqual(updated)
    expect(clients.byHostname['node-1']).toEqual(updated)
  })

  it('updateAttributes PATCHes set/unset and updates the cache', async () => {
    const updated = { hostname: 'node-1', attributes: { role: 'db' } }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()

    await clients.updateAttributes('node-1', { role: 'db' }, [])

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/attributes', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ set: { role: 'db' }, unset: [] }),
    })
    expect(clients.byHostname['node-1']).toEqual(updated)
  })

  it('updateSans PATCHes add/remove and updates the cache', async () => {
    const updated = { hostname: 'node-1', sans: ['new.internal'] }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()

    await clients.updateSans('node-1', ['new.internal'], ['old.internal'])

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/sans', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ add: ['new.internal'], remove: ['old.internal'] }),
    })
    expect(clients.byHostname['node-1']).toEqual(updated)
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- clients.spec.js`
Expected: FAIL — `clients.enroll is not a function` (and similarly for the other six)

- [ ] **Step 3: Implement the seven actions**

In `web/src/stores/clients.js`, replace the whole file with:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

export const useClientsStore = defineStore('clients', {
  state: () => ({
    list: [],
    byHostname: {},
    loading: false,
    error: null,
    pendingToken: null,
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
        this.error = null
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
    async enroll(hostname, sans) {
      this.loading = true
      this.error = null
      try {
        const result = await apiFetch('/clients', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hostname, sans }),
        })
        this.list.push({ hostname: result.hostname, revoked: false, revoked_at: 0, last_seen_at: 0 })
        this.pendingToken = { hostname: result.hostname, token: result.token }
        return result
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async reenroll(hostname, sans) {
      this.loading = true
      this.error = null
      try {
        const result = await apiFetch(`/clients/${encodeURIComponent(hostname)}/reenroll`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sans }),
        })
        this.pendingToken = { hostname: result.hostname, token: result.token }
        return result
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async revoke(hostname) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/revoke`, { method: 'POST' })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async unrevoke(hostname) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/unrevoke`, { method: 'POST' })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async updateDescription(hostname, set, unset) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/description`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ set, unset }),
        })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async updateAttributes(hostname, set, unset) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/attributes`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ set, unset }),
        })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async updateSans(hostname, add, remove) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/sans`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ add, remove }),
        })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    // updateCache writes a fresh client record (from revoke/unrevoke/update*
    // responses) into both byHostname and the matching list row, so every
    // view reading either stays in sync without a refetch.
    updateCache(client) {
      this.byHostname[client.hostname] = client
      const idx = this.list.findIndex((c) => c.hostname === client.hostname)
      if (idx !== -1) this.list[idx] = client
    },
  },
})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test -- clients.spec.js`
Expected: PASS (all tests, old and new)

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/clients.js web/src/stores/clients.spec.js
git commit -m "feat(web): add enroll/re-enroll/revoke/unrevoke/description/attribute/SAN actions to the clients store"
```

---

### Task 2: `ClientsListView.vue` — simple-datatables + New Client link

**Files:**
- Modify: `web/src/views/ClientsListView.vue`
- Modify: `web/src/views/ClientsListView.spec.js`

**Interfaces:**
- Consumes: `clients.fetchAll()`, `clients.list` (Task 1, unchanged shape for this task).
- Produces: nothing new consumed elsewhere — `/clients/new` link target is wired up by Task 3.

- [ ] **Step 1: Write the failing tests**

Replace `web/src/views/ClientsListView.spec.js` entirely with:

```js
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientsListView from './ClientsListView.vue'
import { useClientsStore } from '../stores/clients'

const { destroy, DataTable } = vi.hoisted(() => {
  const destroy = vi.fn()
  const DataTable = vi.fn(() => ({ destroy }))
  return { destroy, DataTable }
})

vi.mock('simple-datatables', () => ({ DataTable }))

beforeEach(() => {
  DataTable.mockClear()
  destroy.mockClear()
})

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
    expect(wrapper.find('a[href="/clients/webserver"]').exists()).toBe(true)
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('links to the enroll form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('a[href="/clients/new"]').exists()).toBe(true)
  })

  it('initializes simple-datatables on the rendered table once data loads, and destroys it on unmount', async () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'webserver', revoked: false, last_seen_at: 0 }],
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

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- ClientsListView.spec.js`
Expected: FAIL — no link to `/clients/new` exists yet, `DataTable` never called

- [ ] **Step 3: Implement the changes**

Replace `web/src/views/ClientsListView.vue` entirely with:

```vue
<script setup>
import { onMounted, onBeforeUnmount, nextTick, ref } from 'vue'
import { DataTable } from 'simple-datatables'
import 'simple-datatables/dist/style.css'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'

const clients = useClientsStore()
const tableRef = ref(null)
let dataTable = null

onMounted(async () => {
  await clients.fetchAll()
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
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-semibold">Clients</h1>
      <router-link to="/clients/new" class="bg-blue-600 text-white rounded px-3 py-1">
        New Client
      </router-link>
    </div>
    <p v-if="clients.loading">Loading...</p>
    <p v-else-if="clients.error" class="text-red-600">{{ clients.error }}</p>
    <table v-else ref="tableRef" class="w-full text-left border-collapse">
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
            {{ formatTimestamp(client.last_seen_at) || 'Never' }}
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test -- ClientsListView.spec.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src/views/ClientsListView.vue web/src/views/ClientsListView.spec.js
git commit -m "feat(web): use simple-datatables on the clients list, add a New Client link"
```

---

### Task 3: `/clients/new` — `ClientFormView.vue`

**Files:**
- Create: `web/src/views/ClientFormView.vue`
- Create: `web/src/views/ClientFormView.spec.js`
- Modify: `web/src/router.js`

**Interfaces:**
- Consumes: `clients.enroll(hostname, sans)` (Task 1).
- Produces: route `/clients/new` — no interfaces consumed by later tasks (Task 6 reads `clients.pendingToken` directly from the store, not from this view).

- [ ] **Step 1: Write the failing tests**

Create `web/src/views/ClientFormView.spec.js`:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientFormView from './ClientFormView.vue'
import { useClientsStore } from '../stores/clients'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientFormView, { global: { plugins: [pinia] } })
  return { wrapper, clients: useClientsStore() }
}

describe('ClientFormView', () => {
  afterEach(() => {
    push.mockReset()
  })

  it('submits an enroll request with the entered hostname and navigates to the new detail page', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })

    await wrapper.find('input[name="hostname"]').setValue('node-1')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()

    expect(clients.enroll).toHaveBeenCalledWith('node-1', [])
    expect(push).toHaveBeenCalledWith('/clients/node-1')
  })

  it('adds and removes SAN rows, sending only non-empty trimmed values', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })

    await wrapper.find('[data-test="add-san"]').trigger('click')
    await wrapper.find('[data-test="add-san"]').trigger('click')
    const sanInputs = wrapper.findAll('[data-test="san-input"]')
    await sanInputs[0].setValue('alias.internal')
    await sanInputs[1].setValue('  ')
    await wrapper.find('input[name="hostname"]').setValue('node-1')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()

    expect(clients.enroll).toHaveBeenCalledWith('node-1', ['alias.internal'])
  })

  it('removes a SAN row via its remove button', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })

    await wrapper.find('[data-test="add-san"]').trigger('click')
    await wrapper.find('[data-test="san-input"]').setValue('alias.internal')
    await wrapper.find('[data-test="remove-san"]').trigger('click')
    await wrapper.find('input[name="hostname"]').setValue('node-1')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()

    expect(clients.enroll).toHaveBeenCalledWith('node-1', [])
  })

  it('shows the store error message and keeps the entered hostname on submit failure', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockRejectedValue(new Error('client node-1 already enrolled'))

    await wrapper.find('input[name="hostname"]').setValue('node-1')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()
    clients.error = 'client node-1 already enrolled'
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('client node-1 already enrolled')
    expect(wrapper.find('input[name="hostname"]').element.value).toBe('node-1')
    expect(push).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- ClientFormView.spec.js`
Expected: FAIL — `Failed to resolve component: ClientFormView` / module not found

- [ ] **Step 3: Implement the view**

Create `web/src/views/ClientFormView.vue`:

```vue
<script setup>
import { reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useClientsStore } from '../stores/clients'

const router = useRouter()
const clients = useClientsStore()

const form = reactive({ hostname: '', sans: [] })

function addSan() {
  form.sans.push('')
}
function removeSan(i) {
  form.sans.splice(i, 1)
}

async function submit() {
  const sans = form.sans.map((s) => s.trim()).filter(Boolean)
  try {
    const result = await clients.enroll(form.hostname, sans)
    router.push(`/clients/${result.hostname}`)
  } catch {
    // error already recorded on clients.error by the store
  }
}
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">New Client</h1>
    <p v-if="clients.error" class="text-red-600 mb-4">{{ clients.error }}</p>
    <form @submit.prevent="submit" class="space-y-6 max-w-2xl">
      <div>
        <label class="block font-medium mb-1">Hostname</label>
        <input name="hostname" v-model="form.hostname" required class="w-full border rounded px-2 py-1" />
      </div>

      <div>
        <label class="block font-medium mb-1">SANs (optional)</label>
        <div v-for="(_, i) in form.sans" :key="i" class="flex gap-2 mb-1">
          <input
            data-test="san-input"
            v-model="form.sans[i]"
            class="flex-1 border rounded px-2 py-1"
          />
          <button type="button" data-test="remove-san" @click="removeSan(i)" class="border rounded px-2">
            Remove
          </button>
        </div>
        <button type="button" data-test="add-san" @click="addSan" class="border rounded px-3 py-1">
          Add SAN
        </button>
      </div>

      <button type="submit" class="bg-blue-600 text-white rounded px-4 py-2">Enroll</button>
    </form>
  </div>
</template>
```

- [ ] **Step 4: Wire up the route**

In `web/src/router.js`, add the import:

```js
import ClientFormView from './views/ClientFormView.vue'
```

immediately after the existing `import ClientDetailView from './views/ClientDetailView.vue'` line, and add the route:

```js
{ path: '/clients/new', component: ClientFormView },
```

immediately after `{ path: '/clients', component: ClientsListView },` and before `{ path: '/clients/:hostname', component: ClientDetailView },` — the static `/clients/new` route must be registered before the dynamic `/clients/:hostname` route so Vue Router doesn't match `new` as a `:hostname` param.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd web && npm test -- ClientFormView.spec.js`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add web/src/views/ClientFormView.vue web/src/views/ClientFormView.spec.js web/src/router.js
git commit -m "feat(web): add /clients/new enroll form"
```

---

### Task 4: `KeyValueEditor.vue` — reusable description/attributes editor

**Files:**
- Create: `web/src/components/KeyValueEditor.vue`
- Create: `web/src/components/KeyValueEditor.spec.js`

**Interfaces:**
- Produces: component `KeyValueEditor` — props `modelValue: Object` (a `{key: value}` snapshot), `label: String`, `testPrefix: String`; emits `save` with `{set: Object, unset: Array}`. Consumed by Task 6 (twice: once for description, once for attributes).

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/KeyValueEditor.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import KeyValueEditor from './KeyValueEditor.vue'

function mountEditor(modelValue = {}) {
  return mount(KeyValueEditor, {
    props: { modelValue, label: 'Description', testPrefix: 'description' },
  })
}

describe('KeyValueEditor', () => {
  it('renders existing key/value pairs', () => {
    const wrapper = mountEditor({ owner: 'alice' })
    expect(wrapper.find('[data-test="description-key-input"]').element.value).toBe('owner')
    expect(wrapper.find('[data-test="description-value-input"]').element.value).toBe('alice')
  })

  it('Update button starts disabled with no changes', () => {
    const wrapper = mountEditor({ owner: 'alice' })
    expect(wrapper.find('[data-test="description-update"]').attributes('disabled')).toBeDefined()
  })

  it('Update button enables once a value changes, and emits the correct set/unset diff', async () => {
    const wrapper = mountEditor({ owner: 'alice', old: 'gone' })

    const valueInputs = wrapper.findAll('[data-test="description-value-input"]')
    await valueInputs[0].setValue('bob')
    await wrapper.findAll('[data-test="description-remove"]')[1].trigger('click')

    expect(wrapper.find('[data-test="description-update"]').attributes('disabled')).toBeUndefined()

    await wrapper.find('[data-test="description-update"]').trigger('click')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({ set: { owner: 'bob' }, unset: ['old'] })
  })

  it('adding a new key/value row and clicking Update sends it in set', async () => {
    const wrapper = mountEditor({})

    await wrapper.find('[data-test="description-add"]').trigger('click')
    await wrapper.find('[data-test="description-key-input"]').setValue('role')
    await wrapper.find('[data-test="description-value-input"]').setValue('db')
    await wrapper.find('[data-test="description-update"]').trigger('click')

    expect(wrapper.emitted('save')[0][0]).toEqual({ set: { role: 'db' }, unset: [] })
  })

  it('resets its draft when modelValue prop changes', async () => {
    const wrapper = mountEditor({ owner: 'alice' })
    await wrapper.find('[data-test="description-value-input"]').setValue('bob')
    expect(wrapper.find('[data-test="description-update"]').attributes('disabled')).toBeUndefined()

    await wrapper.setProps({ modelValue: { owner: 'carol' } })

    expect(wrapper.find('[data-test="description-value-input"]').element.value).toBe('carol')
    expect(wrapper.find('[data-test="description-update"]').attributes('disabled')).toBeDefined()
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- KeyValueEditor.spec.js`
Expected: FAIL — module not found

- [ ] **Step 3: Implement the component**

Create `web/src/components/KeyValueEditor.vue`:

```vue
<script setup>
import { reactive, computed, watch } from 'vue'

const props = defineProps({
  modelValue: { type: Object, default: () => ({}) },
  label: { type: String, required: true },
  testPrefix: { type: String, required: true },
})
const emit = defineEmits(['save'])

function toRows(obj) {
  return Object.entries(obj || {}).map(([key, value]) => ({ key, value }))
}

const snapshot = reactive(toRows(props.modelValue))
const draft = reactive(toRows(props.modelValue))

watch(
  () => props.modelValue,
  (newValue) => {
    snapshot.splice(0, snapshot.length, ...toRows(newValue))
    draft.splice(0, draft.length, ...toRows(newValue))
  }
)

function addRow() {
  draft.push({ key: '', value: '' })
}
function removeRow(i) {
  draft.splice(i, 1)
}

function toMap(rows) {
  return Object.fromEntries(rows.map((r) => [r.key.trim(), r.value]).filter(([key]) => key))
}

const dirty = computed(() => JSON.stringify(toMap(draft)) !== JSON.stringify(toMap(snapshot)))

function submit() {
  const draftMap = toMap(draft)
  const snapshotMap = toMap(snapshot)
  const set = {}
  for (const [key, value] of Object.entries(draftMap)) {
    if (snapshotMap[key] !== value) set[key] = value
  }
  const unset = Object.keys(snapshotMap).filter((key) => !(key in draftMap))
  emit('save', { set, unset })
}
</script>

<template>
  <div>
    <label class="block font-medium mb-1">{{ label }}</label>
    <div v-for="(_, i) in draft" :key="i" class="flex gap-2 mb-1">
      <input
        :data-test="`${testPrefix}-key-input`"
        v-model="draft[i].key"
        placeholder="key"
        class="flex-1 border rounded px-2 py-1"
      />
      <input
        :data-test="`${testPrefix}-value-input`"
        v-model="draft[i].value"
        placeholder="value"
        class="flex-1 border rounded px-2 py-1"
      />
      <button type="button" :data-test="`${testPrefix}-remove`" @click="removeRow(i)" class="border rounded px-2">
        Remove
      </button>
    </div>
    <button type="button" :data-test="`${testPrefix}-add`" @click="addRow" class="border rounded px-3 py-1 mr-2">
      Add
    </button>
    <button
      type="button"
      :data-test="`${testPrefix}-update`"
      :disabled="!dirty"
      @click="submit"
      class="bg-blue-600 text-white rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed"
    >
      Update
    </button>
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test -- KeyValueEditor.spec.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src/components/KeyValueEditor.vue web/src/components/KeyValueEditor.spec.js
git commit -m "feat(web): add reusable KeyValueEditor component for description/attribute editing"
```

---

### Task 5: `SanListEditor.vue` — reusable SAN list editor

**Files:**
- Create: `web/src/components/SanListEditor.vue`
- Create: `web/src/components/SanListEditor.spec.js`

**Interfaces:**
- Produces: component `SanListEditor` — props `modelValue: Array` (a SAN string list snapshot); emits `save` with `{add: Array, remove: Array}`. Consumed by Task 6.

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/SanListEditor.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import SanListEditor from './SanListEditor.vue'

describe('SanListEditor', () => {
  it('renders existing SANs', () => {
    const wrapper = mount(SanListEditor, { props: { modelValue: ['old.internal'] } })
    expect(wrapper.find('[data-test="san-input"]').element.value).toBe('old.internal')
  })

  it('Update button starts disabled with no changes', () => {
    const wrapper = mount(SanListEditor, { props: { modelValue: ['old.internal'] } })
    expect(wrapper.find('[data-test="san-update"]').attributes('disabled')).toBeDefined()
  })

  it('adding and removing SANs enables Update and emits the correct add/remove diff', async () => {
    const wrapper = mount(SanListEditor, { props: { modelValue: ['old.internal'] } })

    await wrapper.find('[data-test="san-add"]').trigger('click')
    const inputs = wrapper.findAll('[data-test="san-input"]')
    await inputs[1].setValue('new.internal')
    await wrapper.find('[data-test="san-remove"]').trigger('click')

    expect(wrapper.find('[data-test="san-update"]').attributes('disabled')).toBeUndefined()

    await wrapper.find('[data-test="san-update"]').trigger('click')

    expect(wrapper.emitted('save')[0][0]).toEqual({ add: ['new.internal'], remove: ['old.internal'] })
  })

  it('resets its draft when modelValue prop changes', async () => {
    const wrapper = mount(SanListEditor, { props: { modelValue: ['old.internal'] } })
    await wrapper.find('[data-test="san-add"]').trigger('click')
    expect(wrapper.find('[data-test="san-update"]').attributes('disabled')).toBeUndefined()

    await wrapper.setProps({ modelValue: ['new.internal'] })

    expect(wrapper.findAll('[data-test="san-input"]')).toHaveLength(1)
    expect(wrapper.find('[data-test="san-input"]').element.value).toBe('new.internal')
    expect(wrapper.find('[data-test="san-update"]').attributes('disabled')).toBeDefined()
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- SanListEditor.spec.js`
Expected: FAIL — module not found

- [ ] **Step 3: Implement the component**

Create `web/src/components/SanListEditor.vue`:

```vue
<script setup>
import { reactive, computed, watch } from 'vue'

const props = defineProps({
  modelValue: { type: Array, default: () => [] },
})
const emit = defineEmits(['save'])

const snapshot = reactive([...(props.modelValue || [])])
const draft = reactive([...(props.modelValue || [])])

watch(
  () => props.modelValue,
  (newValue) => {
    snapshot.splice(0, snapshot.length, ...(newValue || []))
    draft.splice(0, draft.length, ...(newValue || []))
  }
)

function addRow() {
  draft.push('')
}
function removeRow(i) {
  draft.splice(i, 1)
}

function normalize(list) {
  return [...new Set(list.map((s) => s.trim()).filter(Boolean))].sort()
}

const dirty = computed(() => JSON.stringify(draft) !== JSON.stringify(snapshot))

function submit() {
  const draftSet = new Set(normalize(draft))
  const snapshotSet = new Set(normalize(snapshot))
  const add = [...draftSet].filter((s) => !snapshotSet.has(s))
  const remove = [...snapshotSet].filter((s) => !draftSet.has(s))
  emit('save', { add, remove })
}
</script>

<template>
  <div>
    <label class="block font-medium mb-1">SANs</label>
    <div v-for="(_, i) in draft" :key="i" class="flex gap-2 mb-1">
      <input data-test="san-input" v-model="draft[i]" class="flex-1 border rounded px-2 py-1" />
      <button type="button" data-test="san-remove" @click="removeRow(i)" class="border rounded px-2">
        Remove
      </button>
    </div>
    <button type="button" data-test="san-add" @click="addRow" class="border rounded px-3 py-1 mr-2">
      Add SAN
    </button>
    <button
      type="button"
      data-test="san-update"
      :disabled="!dirty"
      @click="submit"
      class="bg-blue-600 text-white rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed"
    >
      Update
    </button>
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test -- SanListEditor.spec.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src/components/SanListEditor.vue web/src/components/SanListEditor.spec.js
git commit -m "feat(web): add reusable SanListEditor component for SAN editing"
```

---

### Task 6: `ClientDetailView.vue` — Revoke/Unrevoke, Re-enroll, token banner, and the three editors

**Files:**
- Modify: `web/src/views/ClientDetailView.vue`
- Modify: `web/src/views/ClientDetailView.spec.js`

**Interfaces:**
- Consumes: `clients.revoke`, `clients.unrevoke`, `clients.reenroll`, `clients.pendingToken`, `clients.updateDescription`, `clients.updateAttributes`, `clients.updateSans` (Task 1); `KeyValueEditor` (Task 4); `SanListEditor` (Task 5).

- [ ] **Step 1: Write the failing tests**

Replace `web/src/views/ClientDetailView.spec.js` entirely with:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientDetailView from './ClientDetailView.vue'
import { useClientsStore } from '../stores/clients'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { hostname: 'webserver' } }),
}))

function baseClient(overrides = {}) {
  return {
    hostname: 'webserver',
    revoked: false,
    revoked_at: 0,
    last_seen_at: 123,
    sans: null,
    attributes: null,
    descriptions: null,
    ...overrides,
  }
}

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientDetailView, { global: { plugins: [pinia] } })
  return { wrapper, clients: useClientsStore() }
}

describe('ClientDetailView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('calls fetchOne with the route hostname on mount', () => {
    const { clients } = mountView({ byHostname: {}, loading: false, error: null, pendingToken: null })
    expect(clients.fetchOne).toHaveBeenCalledWith('webserver')
  })

  it('renders the cached client record', () => {
    const { wrapper } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: null,
    })
    expect(wrapper.text()).toContain('webserver')
    expect(wrapper.text()).toContain('No')
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byHostname: {}, loading: false, error: 'client not found', pendingToken: null })
    expect(wrapper.text()).toContain('client not found')
  })

  it('shows a Revoke button for a non-revoked client, and calls revoke after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ revoked: false }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    expect(wrapper.find('[data-test="revoke-button"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="unrevoke-button"]').exists()).toBe(false)

    await wrapper.find('[data-test="revoke-button"]').trigger('click')

    expect(clients.revoke).toHaveBeenCalledWith('webserver')
  })

  it('does not call revoke when the confirm dialog is dismissed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ revoked: false }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    await wrapper.find('[data-test="revoke-button"]').trigger('click')

    expect(clients.revoke).not.toHaveBeenCalled()
  })

  it('shows an Unrevoke button for a revoked client, and calls unrevoke after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ revoked: true }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    expect(wrapper.find('[data-test="unrevoke-button"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="revoke-button"]').exists()).toBe(false)

    await wrapper.find('[data-test="unrevoke-button"]').trigger('click')

    expect(clients.unrevoke).toHaveBeenCalledWith('webserver')
  })

  it('calls reenroll when Re-enroll is clicked', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: null,
    })
    clients.reenroll.mockResolvedValue({ hostname: 'webserver', token: 'tok-fresh' })

    await wrapper.find('[data-test="reenroll-button"]').trigger('click')

    expect(clients.reenroll).toHaveBeenCalledWith('webserver')
  })

  it('shows the token banner when pendingToken matches the route hostname on mount, and clears it', () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: { hostname: 'webserver', token: 'tok-abc' },
    })

    expect(wrapper.find('[data-test="token-banner"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="token-value"]').text()).toBe('tok-abc')
    expect(clients.pendingToken).toBeNull()
  })

  it('does not show the token banner when pendingToken is for a different hostname', () => {
    const { wrapper } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: { hostname: 'other-host', token: 'tok-abc' },
    })

    expect(wrapper.find('[data-test="token-banner"]').exists()).toBe(false)
  })

  it('does not show the token banner when pendingToken is null', () => {
    const { wrapper } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: null,
    })

    expect(wrapper.find('[data-test="token-banner"]').exists()).toBe(false)
  })

  it('calls updateDescription with the KeyValueEditor save payload', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ descriptions: { owner: 'alice' } }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    await wrapper.find('[data-test="description-value-input"]').setValue('bob')
    await wrapper.find('[data-test="description-update"]').trigger('click')

    expect(clients.updateDescription).toHaveBeenCalledWith('webserver', { owner: 'bob' }, [])
  })

  it('calls updateAttributes with the KeyValueEditor save payload', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ attributes: { role: 'db' } }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    await wrapper.find('[data-test="attribute-value-input"]').setValue('web')
    await wrapper.find('[data-test="attribute-update"]').trigger('click')

    expect(clients.updateAttributes).toHaveBeenCalledWith('webserver', { role: 'web' }, [])
  })

  it('calls updateSans with the SanListEditor save payload', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ sans: ['old.internal'] }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    await wrapper.find('[data-test="san-add"]').trigger('click')
    const inputs = wrapper.findAll('[data-test="san-input"]')
    await inputs[1].setValue('new.internal')
    await wrapper.find('[data-test="san-update"]').trigger('click')

    expect(clients.updateSans).toHaveBeenCalledWith('webserver', ['new.internal'], [])
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test -- ClientDetailView.spec.js`
Expected: FAIL — no `[data-test="revoke-button"]` etc. exist yet

- [ ] **Step 3: Implement the view**

Replace `web/src/views/ClientDetailView.vue` entirely with:

```vue
<script setup>
import { onMounted, computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'
import KeyValueEditor from '../components/KeyValueEditor.vue'
import SanListEditor from '../components/SanListEditor.vue'

const route = useRoute()
const clients = useClientsStore()
const hostname = computed(() => route.params.hostname)
const client = computed(() => clients.byHostname[hostname.value])

const showToken = ref(false)
const tokenValue = ref('')

function checkPendingToken() {
  if (clients.pendingToken && clients.pendingToken.hostname === hostname.value) {
    tokenValue.value = clients.pendingToken.token
    showToken.value = true
    clients.pendingToken = null
  }
}

onMounted(async () => {
  try {
    await clients.fetchOne(hostname.value)
  } catch {
    // error already recorded on clients.error by the store
  }
  checkPendingToken()
})

function confirmRevoke() {
  if (window.confirm(`Revoke ${hostname.value}?`)) {
    clients.revoke(hostname.value)
  }
}
function confirmUnrevoke() {
  if (window.confirm(`Unrevoke ${hostname.value}?`)) {
    clients.unrevoke(hostname.value)
  }
}
async function reenroll() {
  try {
    await clients.reenroll(hostname.value)
    checkPendingToken()
  } catch {
    // error already recorded on clients.error by the store
  }
}

async function copyToken() {
  await navigator.clipboard.writeText(tokenValue.value)
}

function saveDescription({ set, unset }) {
  clients.updateDescription(hostname.value, set, unset)
}
function saveAttributes({ set, unset }) {
  clients.updateAttributes(hostname.value, set, unset)
}
function saveSans({ add, remove }) {
  clients.updateSans(hostname.value, add, remove)
}
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">{{ hostname }}</h1>
    <p v-if="clients.loading">Loading...</p>
    <p v-else-if="clients.error" class="text-red-600">{{ clients.error }}</p>
    <template v-else-if="client">
      <div v-if="showToken" data-test="token-banner" class="bg-yellow-50 border border-yellow-400 rounded p-3 mb-4">
        <p class="font-medium">Enrollment token (shown once):</p>
        <code data-test="token-value" class="block bg-white border rounded px-2 py-1 my-1 break-all">{{ tokenValue }}</code>
        <button type="button" @click="copyToken" class="border rounded px-2 py-1 mr-2">Copy</button>
        <button type="button" @click="showToken = false" class="border rounded px-2 py-1">Dismiss</button>
        <p class="text-sm text-gray-600 mt-1">This token won't be shown again — relay it to the node now.</p>
      </div>

      <div class="mb-4 space-x-2">
        <button
          v-if="!client.revoked"
          type="button"
          data-test="revoke-button"
          @click="confirmRevoke"
          class="border rounded px-3 py-1"
        >
          Revoke
        </button>
        <button v-else type="button" data-test="unrevoke-button" @click="confirmUnrevoke" class="border rounded px-3 py-1">
          Unrevoke
        </button>
        <button type="button" data-test="reenroll-button" @click="reenroll" class="border rounded px-3 py-1">
          Re-enroll
        </button>
      </div>

      <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 mb-6">
        <dt class="font-medium">Revoked</dt>
        <dd>{{ client.revoked ? 'Yes' : 'No' }}</dd>
        <dt class="font-medium">Revoked At</dt>
        <dd>{{ formatTimestamp(client.revoked_at) || '—' }}</dd>
        <dt class="font-medium">Last Seen</dt>
        <dd>{{ formatTimestamp(client.last_seen_at) || 'Never' }}</dd>
      </dl>

      <KeyValueEditor
        :model-value="client.descriptions || {}"
        label="Description"
        test-prefix="description"
        class="mb-6"
        @save="saveDescription"
      />
      <KeyValueEditor
        :model-value="client.attributes || {}"
        label="Attributes"
        test-prefix="attribute"
        class="mb-6"
        @save="saveAttributes"
      />
      <SanListEditor :model-value="client.sans || []" @save="saveSans" />
    </template>
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm test -- ClientDetailView.spec.js`
Expected: PASS

- [ ] **Step 5: Run the full web test suite**

Run: `cd web && npm test`
Expected: PASS — every spec in the project, old and new.

- [ ] **Step 6: Commit**

```bash
git add web/src/views/ClientDetailView.vue web/src/views/ClientDetailView.spec.js
git commit -m "feat(web): add revoke/unrevoke/re-enroll actions, token banner, and inline description/attribute/SAN editing to the client detail page"
```

---

### Task 7: Documentation

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update `web.md`'s Pages list**

In `docs/components/web.md`, change:

```markdown
- `/clients` — every enrolled client (hostname, revoked, last seen), linking to:
- `/clients/:hostname` — one client's full record (SANs, attributes, descriptions)
```

to:

```markdown
- `/clients` — every enrolled client (hostname, revoked, last seen), with client-side search/sort
  via `simple-datatables`, linking to:
- `/clients/new` — enroll a new client (hostname + optional SANs); shows the resulting one-time
  enrollment token on the new client's detail page after redirecting
- `/clients/:hostname` — one client's full record (SANs, attributes, descriptions), with actions to
  revoke/unrevoke, re-enroll (shows a fresh one-time token), and inline add/remove editing of
  description, attributes, and SANs, each gated by its own "Update" button that enables only once
  that section has a pending change
```

- [ ] **Step 2: Add the CHANGELOG entry**

At the top of `CHANGELOG.md`, immediately after the `All notable changes...` line, insert:

```markdown
## 2026-07-19 — web: add client enrollment/revocation/metadata management

`web`'s `/clients` pages gain the write surface `clientmanager-admin-api` added: a `/clients/new`
enroll form, Revoke/Unrevoke and Re-enroll actions and a one-time token banner on the client detail
page, and inline add/remove editing for description, attributes, and SANs, each with its own
"Update" button enabled only while that section has an unsaved change. The clients list now uses
`simple-datatables` for client-side search/sort, matching `/jobs` and `/catalog`.
```

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document the client enrollment/revocation/metadata UI"
```

---

## Self-Review

**Spec coverage:**
- Store actions (all 7, `pendingToken`, cache-update behavior) → Task 1.
- `/clients` datatable + New Client link → Task 2.
- `/clients/new` enroll form → Task 3.
- Detail-page Revoke/Unrevoke/Re-enroll/token banner → Task 6.
- Inline description/attribute editing with per-section Update button → Tasks 4 (component) + 6 (wiring).
- Inline SAN editing with its own Update button → Tasks 5 (component) + 6 (wiring).
- Documentation impact list → Task 7.

**Placeholder scan:** no TBD/TODO; every step shows complete code; no "similar to Task N" — each task's test/implementation code is written out in full.

**Type consistency:** `KeyValueEditor`'s emitted `save` payload shape (`{set, unset}`) matches exactly what `ClientDetailView.vue`'s `saveDescription`/`saveAttributes` destructure and pass to `clients.updateDescription`/`updateAttributes` (Task 1's signature: `(hostname, set, unset)`). `SanListEditor`'s `{add, remove}` matches `clients.updateSans(hostname, add, remove)`. `pendingToken`'s shape (`{hostname, token}` or `null`) is identical between Task 1's store definition and Task 6's `checkPendingToken` read/clear logic.
