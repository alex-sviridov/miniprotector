# Catalog View Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix `web`'s `/catalog` page — dual competing filter/pagination UIs, a versions modal that
can't open, opaque source-host values in the demo, and raw byte counts — by making the custom
filter form the sole search mechanism, fetching every matching page before grouping so versions are
never split across a page boundary, wiring row-click through `simple-datatables`' own event system,
and adding human-readable sizes.

**Architecture:** `catalog.js`'s `search()` action loops the existing keyset-paginated `/catalog`
endpoint (limit=500) until exhausted, replacing cursor-based Prev/Next. `CatalogView.vue` requires a
filter before enabling Search, groups the complete result set, and hands it to `simple-datatables`
with `searchable: false` so DataTable's own pagination is the only pager. Because DataTable owns row
DOM after mount, the versions modal opens via DataTable's `datatable.selectrow` event (mapped back
to `groups` by index) instead of a template `@click`. The modal itself is extracted into
`VersionsModal.vue`.

**Tech Stack:** Vue 3 `<script setup>`, Pinia (options-style stores), `simple-datatables`, Vitest +
`@vue/test-utils` + `@pinia/testing`.

## Global Constraints

- No backend, API, or `.proto` changes in this plan — the spec's Out of Scope explicitly excludes
  them; a separate follow-up design covers `bwfs`/`catalog` source-host trust.
- Every feature change must update `docs/components/web.md` and add a dated `CHANGELOG.md` entry
  before merge (this repo's `.claude/CLAUDE.md` "Feature Changes"/"Changelog" rules) — Task 6.
- Follow this codebase's existing conventions: `<script setup>` SFCs, options-style Pinia stores,
  `simple-datatables` for list rendering, Vitest + `@vue/test-utils` + `@pinia/testing` for tests,
  real child components mounted in parent specs (not stubbed/found by name) with assertions against
  rendered DOM — see `ClientDetailView.spec.js` for the pattern this plan's Task 4 tests follow.

---

### Task 1: `formatBytes` utility

**Files:**
- Modify: `web/src/utils/format.js`
- Test: `web/src/utils/format.spec.js` (new file)

**Interfaces:**
- Produces: `formatBytes(bytes: number | null | undefined): string` — binary units (B/KB/MB/GB/TB),
  `null`/`undefined` → `'—'`, `0` → `'0 B'`, otherwise one decimal place beyond bytes. Consumed by
  Task 3 and Task 4.

- [ ] **Step 1: Write the failing test**

Create `web/src/utils/format.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { formatBytes } from './format'

describe('formatBytes', () => {
  it('renders 0 bytes as "0 B"', () => {
    expect(formatBytes(0)).toBe('0 B')
  })

  it('renders null as an em dash', () => {
    expect(formatBytes(null)).toBe('—')
  })

  it('renders undefined as an em dash', () => {
    expect(formatBytes(undefined)).toBe('—')
  })

  it('renders sub-1024 byte counts with no unit conversion', () => {
    expect(formatBytes(512)).toBe('512 B')
  })

  it('renders exactly 1024 bytes as 1.0 KB', () => {
    expect(formatBytes(1024)).toBe('1.0 KB')
  })

  it('renders a fractional KB value to one decimal place', () => {
    expect(formatBytes(8192)).toBe('8.0 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
  })

  it('renders exactly 1024*1024 bytes as 1.0 MB', () => {
    expect(formatBytes(1024 * 1024)).toBe('1.0 MB')
  })

  it('renders large multi-unit values in GB', () => {
    expect(formatBytes(3 * 1024 ** 3)).toBe('3.0 GB')
  })

  it('caps at TB for enormous values instead of introducing a new unit', () => {
    expect(formatBytes(5 * 1024 ** 5)).toBe('5120.0 TB')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- format.spec.js`
Expected: FAIL — `formatBytes` is not exported from `./format`.

- [ ] **Step 3: Write minimal implementation**

In `web/src/utils/format.js`, append below the existing `formatTimestamp`:

```js
const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

export function formatBytes(bytes) {
  if (bytes === null || bytes === undefined) return '—'
  if (bytes === 0) return '0 B'
  let exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), BYTE_UNITS.length - 1)
  let value = bytes / 1024 ** exponent
  if (exponent > 0 && Number(value.toFixed(1)) >= 1024 && exponent < BYTE_UNITS.length - 1) {
    exponent += 1
    value = bytes / 1024 ** exponent
  }
  return `${exponent === 0 ? value : value.toFixed(1)} ${BYTE_UNITS[exponent]}`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- format.spec.js`
Expected: PASS (9 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/format.js web/src/utils/format.spec.js
git commit -m "feat(web): add formatBytes for human-readable catalog sizes"
```

---

### Task 2: Rewrite the catalog store to fetch every matching page

**Files:**
- Modify: `web/src/stores/catalog.js`
- Test: `web/src/stores/catalog.spec.js` (full rewrite)

**Interfaces:**
- Produces: `useCatalogStore()` with state `{ filters, entries: Array, loading: boolean, error:
  string|null }` and action `search(filters: { sourceHost, storeHost, pattern }): Promise<void>`
  that populates `entries` with every matching row across all server pages. `cursorStack`,
  `hasMore`, `canGoPrev`, `nextPage`, `prevPage` are removed. Consumed by Task 4.

- [ ] **Step 1: Write the failing test**

Replace the entire contents of `web/src/stores/catalog.spec.js`:

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

  it('fetches a single page with filters and a limit of 500 when has_more is false', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: false })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: 'database', storeHost: 'bwfs-a', pattern: 'dbdata' })

    expect(apiFetch).toHaveBeenCalledWith(
      '/catalog?source_host=database&store_host=bwfs-a&pattern=dbdata&limit=500'
    )
    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }])
    expect(catalog.loading).toBe(false)
    expect(catalog.error).toBe(null)
  })

  it('loops starting_after until has_more is false, concatenating every page', async () => {
    apiFetch
      .mockResolvedValueOnce({ data: [{ id: 1 }, { id: 2 }], has_more: true })
      .mockResolvedValueOnce({ data: [{ id: 3 }, { id: 4 }], has_more: true })
      .mockResolvedValueOnce({ data: [{ id: 5 }], has_more: false })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })

    expect(apiFetch).toHaveBeenCalledTimes(3)
    expect(apiFetch).toHaveBeenNthCalledWith(1, '/catalog?limit=500')
    expect(apiFetch).toHaveBeenNthCalledWith(2, '/catalog?starting_after=2&limit=500')
    expect(apiFetch).toHaveBeenNthCalledWith(3, '/catalog?starting_after=4&limit=500')
    expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }, { id: 3 }, { id: 4 }, { id: 5 }])
  })

  it('stops looping when a page returns zero rows even if has_more is true', async () => {
    apiFetch.mockResolvedValue({ data: [], has_more: true })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(catalog.entries).toEqual([])
  })

  it('discards everything collected so far and sets error when a later page fails', async () => {
    apiFetch
      .mockResolvedValueOnce({ data: [{ id: 1 }], has_more: true })
      .mockRejectedValueOnce(new Error('boom'))
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })

    expect(catalog.entries).toEqual([])
    expect(catalog.error).toBe('boom')
    expect(catalog.loading).toBe(false)
  })

  it('sets loading true while the fetch loop is in flight', async () => {
    let resolveFirst
    apiFetch.mockReturnValue(
      new Promise((resolve) => {
        resolveFirst = resolve
      })
    )
    const catalog = useCatalogStore()

    const promise = catalog.search({ sourceHost: '', storeHost: '', pattern: '' })
    expect(catalog.loading).toBe(true)
    resolveFirst({ data: [], has_more: false })
    await promise
    expect(catalog.loading).toBe(false)
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- stores/catalog.spec.js`
Expected: FAIL — the current implementation never sends `limit=500` and exposes `hasMore`/
`cursorStack` instead of looping internally.

- [ ] **Step 3: Write minimal implementation**

Replace the entire contents of `web/src/stores/catalog.js`:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

const MAX_PAGE_LIMIT = 500

function buildQuery(filters, startingAfter, limit) {
  const params = new URLSearchParams()
  if (filters.sourceHost) params.set('source_host', filters.sourceHost)
  if (filters.storeHost) params.set('store_host', filters.storeHost)
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  params.set('limit', String(limit))
  return params.toString()
}

export const useCatalogStore = defineStore('catalog', {
  state: () => ({
    filters: { sourceHost: '', storeHost: '', pattern: '' },
    entries: [],
    loading: false,
    error: null,
  }),
  actions: {
    async search(filters) {
      this.filters = { ...filters }
      this.loading = true
      this.error = null
      const collected = []
      try {
        let startingAfter
        for (;;) {
          const qs = buildQuery(this.filters, startingAfter, MAX_PAGE_LIMIT)
          const body = await apiFetch(`/catalog?${qs}`)
          collected.push(...body.data)
          if (!body.has_more || body.data.length === 0) break
          startingAfter = body.data[body.data.length - 1].id
        }
        this.entries = collected
      } catch (err) {
        this.error = err.message
        this.entries = []
      } finally {
        this.loading = false
      }
    },
  },
})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- stores/catalog.spec.js`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/catalog.js web/src/stores/catalog.spec.js
git commit -m "refactor(web): fetch every matching catalog page instead of cursor pagination"
```

---

### Task 3: Extract `VersionsModal.vue`

**Files:**
- Create: `web/src/components/VersionsModal.vue`
- Test: `web/src/components/VersionsModal.spec.js` (new file)

**Interfaces:**
- Consumes: `formatBytes(bytes)` and `formatTimestamp(epochSeconds)` from
  `web/src/utils/format.js` (Task 1).
- Produces: `VersionsModal` component — props `{ group: { path: string, sourceHost: string,
  versions: Array<{ id, store_created_at, size, mode, mod_time, job_id, store_host }> } }`
  (required), emits `close` (no payload) on Close-button click, backdrop click, or Escape.
  Consumed by Task 4.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/VersionsModal.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import VersionsModal from './VersionsModal.vue'

function group(overrides) {
  return {
    sourceHost: 'database',
    path: '/var/lib/dbdata/data.db',
    versions: [
      {
        id: 2,
        store_created_at: 1752400000,
        size: 8192,
        mode: '-rw-r--r--',
        mod_time: 1752400000,
        job_id: 'backup:daily-db-backup:2',
        store_host: 'bwfs-east',
      },
      {
        id: 1,
        store_created_at: 1752300000,
        size: 8004,
        mode: '-rw-r--r--',
        mod_time: 1752300000,
        job_id: 'backup:daily-db-backup:1',
        store_host: 'bwfs-east',
      },
    ],
    ...overrides,
  }
}

describe('VersionsModal', () => {
  it('renders the heading and every version newest-first', () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })

    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('8.0 KB')
    expect(rows[0].text()).toContain('backup:daily-db-backup:2')
    expect(rows[1].text()).toContain('backup:daily-db-backup:1')
  })

  it('renders a dash for a zero timestamp instead of the literal value', () => {
    const wrapper = mount(VersionsModal, {
      props: {
        group: group({
          versions: [
            {
              id: 1,
              store_created_at: 0,
              size: 100,
              mode: '-rw-r--r--',
              mod_time: 0,
              job_id: 'j',
              store_host: 'bwfs-east',
            },
          ],
        }),
      },
    })
    const cells = wrapper.findAll('tbody td')
    expect(cells[0].text()).toBe('—')
    expect(cells[3].text()).toBe('—')
  })

  it('emits close when the Close button is clicked', async () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on a backdrop click', async () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })
    await wrapper.find('.fixed').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('does not emit close when clicking inside the modal body', async () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })
    await wrapper.find('table').trigger('click')
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('emits close on Escape, and stops listening after unmount', () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(1)

    wrapper.unmount()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- VersionsModal.spec.js`
Expected: FAIL — `web/src/components/VersionsModal.vue` does not exist yet.

- [ ] **Step 3: Write minimal implementation**

Create `web/src/components/VersionsModal.vue`:

```vue
<script setup>
import { onMounted, onBeforeUnmount } from 'vue'
import { formatBytes, formatTimestamp } from '../utils/format'

const props = defineProps({
  group: { type: Object, required: true },
})
const emit = defineEmits(['close'])

function close() {
  emit('close')
}

function onKeydown(event) {
  if (event.key === 'Escape') close()
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-2xl w-full max-h-[80vh] overflow-auto">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">
          Versions of {{ group.path }} on {{ group.sourceHost }}
        </h2>
        <button type="button" class="text-gray-500 hover:text-gray-800" @click="close">Close</button>
      </div>
      <table class="w-full text-left border-collapse">
        <thead>
          <tr class="border-b">
            <th class="py-2 pr-4">Captured</th>
            <th class="py-2 pr-4">Size</th>
            <th class="py-2 pr-4">Mode</th>
            <th class="py-2 pr-4">Modified</th>
            <th class="py-2 pr-4">Job ID</th>
            <th class="py-2 pr-4">Store Host</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="version in group.versions" :key="version.id" class="border-b">
            <td class="py-2 pr-4">{{ formatTimestamp(version.store_created_at) || '—' }}</td>
            <td class="py-2 pr-4">{{ formatBytes(version.size) }}</td>
            <td class="py-2 pr-4">{{ version.mode }}</td>
            <td class="py-2 pr-4">{{ formatTimestamp(version.mod_time) || '—' }}</td>
            <td class="py-2 pr-4">{{ version.job_id }}</td>
            <td class="py-2 pr-4">{{ version.store_host }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- VersionsModal.spec.js`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/VersionsModal.vue web/src/components/VersionsModal.spec.js
git commit -m "feat(web): extract VersionsModal from CatalogView"
```

---

### Task 4: Rewrite `CatalogView.vue`

**Files:**
- Modify: `web/src/views/CatalogView.vue`
- Test: `web/src/views/CatalogView.spec.js` (full rewrite)

**Interfaces:**
- Consumes: `useCatalogStore` (Task 2) — `{ entries, loading, error }` + `search(filters)`;
  `formatBytes`/`formatTimestamp` (Task 1); `VersionsModal` (Task 3) — prop `group`, event `close`;
  `groupEntriesByFile(entries)` from `web/src/utils/catalogGrouping.js` (unchanged) returning
  `Array<{ sourceHost, path, versions, representative }>`.

- [ ] **Step 1: Write the failing test**

Replace the entire contents of `web/src/views/CatalogView.spec.js`:

```js
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'

const { destroy, on, DataTable } = vi.hoisted(() => {
  const destroy = vi.fn()
  const on = vi.fn()
  const DataTable = vi.fn(() => ({ destroy, on }))
  return { destroy, on, DataTable }
})

vi.mock('simple-datatables', () => ({ DataTable }))

beforeEach(() => {
  DataTable.mockClear()
  destroy.mockClear()
  on.mockClear()
})

function entry(overrides) {
  return {
    id: 1,
    source_host: 'database',
    store_host: 'bwfs-east',
    job_id: 'backup:daily-db-backup:1',
    object_id: 'fs://database:f:/var/lib/dbdata/data.db:1752400000',
    ctime: 1752400000,
    store_created_at: 1752400000,
    received_at: 1752400010,
    path: '/var/lib/dbdata/data.db',
    size: 8192,
    mode: '-rw-r--r--',
    owner: 999,
    group: 999,
    mod_time: 1752400000,
    ...overrides,
  }
}

function mountView(state) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: { catalog: { entries: [], loading: false, error: null, ...state } },
  })
  const wrapper = mount(CatalogView, { global: { plugins: [pinia] } })
  return { wrapper, catalog: useCatalogStore() }
}

function selectRow(rowIndex) {
  const call = on.mock.calls.find(([event]) => event === 'datatable.selectrow')
  call[1](rowIndex)
}

async function search(wrapper, sourceHost = 'database') {
  await wrapper.findAll('input')[0].setValue(sourceHost)
  await wrapper.find('form').trigger('submit.prevent')
  await flushPromises()
}

describe('CatalogView', () => {
  it('does not fetch on mount', async () => {
    const { catalog } = mountView({})
    await flushPromises()
    expect(catalog.search).not.toHaveBeenCalled()
  })

  it('shows a prompt before any search has been run', () => {
    const { wrapper } = mountView({})
    expect(wrapper.text()).toContain('Enter a filter and search.')
  })

  it('disables Search when every filter field is empty', () => {
    const { wrapper } = mountView({})
    expect(wrapper.find('button[type="submit"]').attributes('disabled')).toBeDefined()
  })

  it('enables Search once a filter field has a value', async () => {
    const { wrapper } = mountView({})
    await wrapper.findAll('input')[0].setValue('database')
    expect(wrapper.find('button[type="submit"]').attributes('disabled')).toBeUndefined()
  })

  it('does not call search when submitted with every field empty', async () => {
    const { wrapper, catalog } = mountView({})
    await wrapper.find('form').trigger('submit.prevent')
    expect(catalog.search).not.toHaveBeenCalled()
  })

  it('submits the filter form via search once a field is filled', async () => {
    const { wrapper, catalog } = mountView({})
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('database')
    await inputs[1].setValue('bwfs-a')
    await inputs[2].setValue('dbdata')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()
    expect(catalog.search).toHaveBeenLastCalledWith({
      sourceHost: 'database',
      storeHost: 'bwfs-a',
      pattern: 'dbdata',
    })
  })

  it('shows a no-results message after a search returns nothing', async () => {
    const { wrapper } = mountView({})
    await search(wrapper)
    expect(wrapper.text()).toContain('No entries match this filter.')
  })

  it('groups entries sharing source_host and path into a single row with a version count', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [
      entry({ id: 1, store_created_at: 1752300000, size: 8004 }),
      entry({ id: 2, store_created_at: 1752400000, size: 8192 }),
    ]
    await search(wrapper)

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('/var/lib/dbdata/data.db')
    expect(rows[0].text()).toContain('8.0 KB')
    expect(rows[0].text()).toContain('2')
  })

  it('renders a single-version file without a version count', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [entry({ id: 1 })]
    await search(wrapper)

    const rows = wrapper.findAll('tbody tr')
    const cells = rows[0].findAll('td')
    expect(cells[cells.length - 1].text()).toBe('')
  })

  it('constructs simple-datatables with search disabled and a 25-row page size, and destroys it on unmount', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [entry({ id: 1 })]
    await search(wrapper)

    expect(DataTable).toHaveBeenCalledTimes(1)
    expect(DataTable.mock.calls[0][0].tagName).toBe('TABLE')
    expect(DataTable.mock.calls[0][1]).toEqual({ searchable: false, perPage: 25 })

    wrapper.unmount()
    expect(destroy).toHaveBeenCalledTimes(1)
  })

  it('opens the versions modal when a multi-version row is selected', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [
      entry({ id: 1, store_created_at: 1752300000 }),
      entry({ id: 2, store_created_at: 1752400000 }),
    ]
    await search(wrapper)

    selectRow(0)
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.fixed').exists()).toBe(true)
    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
  })

  it('does not open the versions modal when a single-version row is selected', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [entry({ id: 1 })]
    await search(wrapper)

    selectRow(0)
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('closes the versions modal via its Close button', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [
      entry({ id: 1, store_created_at: 1752300000 }),
      entry({ id: 2, store_created_at: 1752400000 }),
    ]
    await search(wrapper)

    selectRow(0)
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(true)

    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Close')
      .trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- CatalogView.spec.js`
Expected: FAIL — the current view fetches on mount, has no `hasSearched` gate, constructs
`DataTable` with no options, and has no `datatable.selectrow` handler.

- [ ] **Step 3: Write minimal implementation**

Replace the entire contents of `web/src/views/CatalogView.vue`:

```vue
<script setup>
import { computed, onBeforeUnmount, nextTick, reactive, ref } from 'vue'
import { DataTable } from 'simple-datatables'
import 'simple-datatables/dist/style.css'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'
import VersionsModal from '../components/VersionsModal.vue'

const catalog = useCatalogStore()
const form = reactive({ sourceHost: '', storeHost: '', pattern: '' })
const tableRef = ref(null)
const groups = ref([])
const hasSearched = ref(false)
const selectedGroup = ref(null)
let dataTable = null

const canSearch = computed(() => Boolean(form.sourceHost || form.storeHost || form.pattern))

function destroyTable() {
  if (dataTable) {
    dataTable.destroy()
    dataTable = null
  }
}

async function renderTable() {
  groups.value = groupEntriesByFile(catalog.entries)
  destroyTable()
  await nextTick()
  if (tableRef.value) {
    dataTable = new DataTable(tableRef.value, { searchable: false, perPage: 25 })
    dataTable.on('datatable.selectrow', (rowIndex) => {
      const group = groups.value[rowIndex]
      if (group && group.versions.length > 1) {
        selectedGroup.value = group
      }
    })
  }
}

async function submit() {
  if (!canSearch.value) return
  hasSearched.value = true
  await catalog.search({ ...form })
  await renderTable()
}

onBeforeUnmount(() => {
  destroyTable()
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Catalog</h1>
    <form @submit.prevent="submit" class="flex gap-2 mb-4">
      <input v-model="form.sourceHost" placeholder="source host" class="border rounded px-2 py-1" />
      <input v-model="form.storeHost" placeholder="store host" class="border rounded px-2 py-1" />
      <input v-model="form.pattern" placeholder="path pattern" class="border rounded px-2 py-1" />
      <button
        type="submit"
        :disabled="!canSearch"
        class="bg-blue-600 text-white rounded px-3 py-1 disabled:opacity-50"
      >
        Search
      </button>
    </form>
    <p v-if="catalog.loading">Loading...</p>
    <p v-else-if="catalog.error" class="text-red-600">{{ catalog.error }}</p>
    <p v-else-if="!hasSearched" class="text-gray-500">Enter a filter and search.</p>
    <p v-else-if="groups.length === 0" class="text-gray-500">No entries match this filter.</p>
    <!-- simple-datatables replaces this subtree's DOM internally; wrapping it in its own div ensures Vue's v-if/v-else unmount removes the whole thing cleanly on every re-search, instead of leaving the library's injected wrapper orphaned as a sibling of the form above. -->
    <div v-else>
      <table ref="tableRef" class="w-full text-left border-collapse">
        <thead>
          <tr class="border-b">
            <th class="py-2 pr-4">Path</th>
            <th class="py-2 pr-4">Source Host</th>
            <th class="py-2 pr-4">Store Host</th>
            <th class="py-2 pr-4">Size</th>
            <th class="py-2 pr-4">Mode</th>
            <th class="py-2 pr-4">Modified</th>
            <th class="py-2 pr-4">Versions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="group in groups" :key="`${group.sourceHost}|${group.path}`" class="border-b">
            <td class="py-2 pr-4">{{ group.path }}</td>
            <td class="py-2 pr-4">{{ group.sourceHost }}</td>
            <td class="py-2 pr-4">{{ group.representative.store_host }}</td>
            <td class="py-2 pr-4">{{ formatBytes(group.representative.size) }}</td>
            <td class="py-2 pr-4">{{ group.representative.mode }}</td>
            <td class="py-2 pr-4">{{ formatTimestamp(group.representative.mod_time) || '—' }}</td>
            <td class="py-2 pr-4">{{ group.versions.length > 1 ? group.versions.length : '' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <VersionsModal v-if="selectedGroup" :group="selectedGroup" @close="selectedGroup = null" />
  </div>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- CatalogView.spec.js`
Expected: PASS (14 tests)

- [ ] **Step 5: Run the full web test suite**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test`
Expected: PASS — every test in `web/src` (including Tasks 1-3's new files and unrelated existing
specs like `catalogGrouping.spec.js`), confirming this rewrite hasn't broken anything else.

- [ ] **Step 6: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js
git commit -m "refactor(web): rewrite CatalogView filtering, pagination, and versions modal wiring"
```

---

### Task 5: Demo hostname stopgap

**Files:**
- Modify: `demo/docker-compose.yml`

**Interfaces:** none — standalone config change.

- [ ] **Step 1: Add explicit hostnames**

In `demo/docker-compose.yml`, add a `hostname:` line as the first key under both the `database` and
`webserver` services (immediately above their existing `build:` key), so each container's OS
hostname — and therefore `brfs`'s reported `source_host` — reads as the intended name instead of
Docker's autogenerated container ID:

```yaml
  database:
    hostname: database
    build:
      context: ..
      dockerfile: demo/backup-host/Dockerfile
```

```yaml
  webserver:
    hostname: webserver
    build:
      context: ..
      dockerfile: demo/backup-host/Dockerfile
```

- [ ] **Step 2: Verify the resolved config**

Run: `docker compose -f demo/docker-compose.yml config | grep -A1 "^  database:\|^  webserver:"`
Expected output includes:
```
  database:
    hostname: database
--
  webserver:
    hostname: webserver
```

- [ ] **Step 3: Commit**

```bash
git add demo/docker-compose.yml
git commit -m "fix(demo): set explicit hostname on backup-host containers

Stopgap so the demo's catalog source_host values read as intended
instead of Docker's autogenerated container ID; the real fix (bwfs
passing through its already mTLS-validated peer hostname) is tracked
separately."
```

---

### Task 6: Documentation

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none — prose only.

- [ ] **Step 1: Update the `/catalog` bullet in `docs/components/web.md`**

Replace this existing bullet (lines 27-34):

```markdown
- `/catalog` — catalog entries, filterable by real source host, store host (the `bwfs` node that
  replicated the entry), and a path-pattern substring, paginated with Prev/Next (the catalog API
  only supports cursor pagination — no total count, so there's no page-number jump). Entries within
  the currently loaded page are grouped into one row per distinct file (source host + path), using
  `simple-datatables` (as `/jobs` does) for client-side search/sort over that page; a "Versions"
  count opens a modal listing that file's other versions. Grouping is scoped to the loaded page —
  versions of the same file split across a Prev/Next page boundary appear as separate
  single-version rows on their own pages.
```

with:

```markdown
- `/catalog` — catalog entries, filterable by real source host, store host (the `bwfs` node that
  replicated the entry), and a path-pattern substring; at least one filter must be filled in before
  Search is enabled, since the catalog has no natural bound the way `/jobs`' 24h window does. On
  search, every matching page is fetched (the catalog API is cursor-paginated) before entries are
  grouped into one row per distinct file (source host + path) and handed to `simple-datatables` for
  client-side sort/pagination — grouping over the complete result set means a file's versions are
  never split across a page boundary. Sizes render human-readable (KB/MB/...); a "Versions" count
  on multi-version files opens a modal (click anywhere on that row) listing that file's other
  versions.
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Insert at the top of `CHANGELOG.md`, immediately after the `# Changelog` header and its intro line:

```markdown
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
```

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document the catalog view rewrite"
```
