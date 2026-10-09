# Restore Workflow UI Clarity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the restore cart/submission/job-status flow easy to understand: no storage-host
grouping, a pre-restore confirmation step, per-row live submission status, a human-readable job
summary banner, and unified file/folder version picking defaulted to the current catalog filter
window.

**Architecture:** Entirely frontend (`web/src/**`). No backend/API/protocol changes — `job_id` is
already returned by `POST /restore`, `not_before`/`not_after` are already accepted by
`RestoreRule` and already resolved correctly by `rwfs`/`bwfs`, `last_seen_at` is already on every
client, and a folder's own version history is already reachable through the existing `/catalog`
search endpoint (nothing in `catalogsync`/`api-server` filters by entry type). The work is: thread
a version window through the cart's rule model, generalize the read-only version modal into an
interactive picker shared by files and folders, track per-cart-entry submission status instead of
a flat results list, and add a human-readable banner to the job detail log view.

**Tech Stack:** Vue 3 (`<script setup>`), Pinia, Vitest + `@vue/test-utils`, Playwright (e2e).

## Global Constraints

- No backend, protocol, or database changes anywhere in this plan — verified feasible without them
  during design (see `docs/superpowers/specs/2026-08-27-restore-ui-clarity-design.md`).
- Never render or reference which storage host (`store_host`) any selection lives on, anywhere in
  `RestoreView.vue` or its confirmation modal — this is the central point of the redesign.
- `Overwrite` semantics: unchecked = skip a file that already exists at the destination (not an
  error); checked = replace it (`src/cmd/rwfs/restorefile.go:61-78`). Copy must say this, not
  paraphrase it.
- A version pin is always encoded as `notBefore === notAfter === <that version's
  store_created_at>` (a single-instant window). The default (no explicit pin) is the *current
  catalog filter's* `receivedAfter`/`receivedBefore` window, captured once at selection time — not
  an unbounded "true latest ever."
- Folder version-pinning is allowed even when the folder's version list spans multiple source
  hosts; the picker must show which host each row came from and note that pinning narrows the
  selection to whichever host(s) actually captured something at that exact instant.
- Run `npm run test` (from `web/`) after every task; run `npm run test:e2e` (also from `web/`,
  needs the full stack up per `web/e2e/helpers`) after Task 9.

---

## Task 1: Thread a version window through the cart's rule model

**Files:**
- Modify: `web/src/utils/restoreRules.js`
- Modify: `web/src/stores/restoreCart.js`
- Test: `web/src/stores/restoreCart.spec.js`

**Interfaces:**
- Consumes: nothing new — `toggleFileRule`/`toggleFolderRule` (`restoreRules.js`) already accept an
  `extra` object spread onto a newly-created rule; this task only adds new keys callers can pass
  through it.
- Produces: `restoreCartStore.toggleFile(host, path, storeHost, size, notBefore, notAfter)`,
  `restoreCartStore.toggleFolder(path, notBefore, notAfter)`, and a new
  `restoreCartStore.setVersionWindow(entry, notBefore, notAfter)` action (mirrors the existing
  `setDestPath(entry, destPath)`). A rule object now optionally carries `notBefore`/`notAfter`
  (Unix seconds). Also exports `entryKey(entry)` from `restoreRules.js` — `` `${entry.host ??
  ''}:${entry.path}` `` — replacing the equivalent private helper duplicated today in
  `restoreSubmission.js` and `RestoreView.vue` (both consumed by Task 5 and Task 7).

- [ ] **Step 1: Write the failing tests**

Add to `web/src/utils/restoreRules.js`'s existing test file — actually `entryKey` has no dedicated
spec file today (it's private in each caller), so add these to the top of
`web/src/stores/restoreCart.spec.js` instead, alongside the store tests below, since that's where
its behavior is actually exercised end to end:

```js
  it('toggleFile threads notBefore/notAfter onto the created rule when passed', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', 'bwfs-1', 4096, 1000, 2000)
    expect(cart.rules).toEqual([
      {
        path: '/etc/hosts',
        host: 'web01',
        include: true,
        destPath: '/etc/hosts',
        storeHost: 'bwfs-1',
        size: 4096,
        notBefore: 1000,
        notAfter: 2000,
      },
    ])
  })

  it('toggleFolder threads notBefore/notAfter onto the created rule when passed', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var', 1000, 2000)
    expect(cart.rules).toEqual([
      { path: '/var', host: null, include: true, destPath: '/var', notBefore: 1000, notAfter: 2000 },
    ])
  })

  it('setVersionWindow updates the matching rule and leaves others untouched', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    cart.toggleFolder('/var')

    cart.setVersionWindow({ host: 'web01', path: '/etc/hosts' }, 1000, 1000)

    expect(cart.rules).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts', notBefore: 1000, notAfter: 1000 },
      { path: '/var', host: null, include: true, destPath: '/var' },
    ])
  })

  it('setVersionWindow on a folder rule (host: null) updates its window', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')

    cart.setVersionWindow({ host: null, path: '/var' }, 500, 600)

    expect(cart.rules).toEqual([{ path: '/var', host: null, include: true, destPath: '/var', notBefore: 500, notAfter: 600 }])
  })

  it('setVersionWindow is a no-op when no rule matches', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')

    cart.setVersionWindow({ host: 'web02', path: '/nope' }, 1, 2)

    expect(cart.rules).toEqual([{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }])
  })
```

Add these five `it(...)` blocks inside the existing `describe('restoreCart store', ...)` block, after
the existing `setDestPath` tests.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/stores/restoreCart.spec.js`
Expected: FAIL — `toggleFile`/`toggleFolder` ignore the extra positional args, `setVersionWindow` is
not a function.

- [ ] **Step 3: Implement**

In `web/src/utils/restoreRules.js`, add (near the top, after `pathCrumbs` import — no existing
export needs to change, `toggleFile`/`toggleFolder` already spread `extra` onto a new rule):

```js
// entryKey identifies a cart entry/rule by its (host, path) identity -- the
// same key restoreCart's rule list, restoreSubmission's per-entry status
// map, and RestoreView's table rows all use, so a single source of truth
// replaces three independent copies of the same template string.
export function entryKey(entry) {
  return `${entry.host ?? ''}:${entry.path}`
}
```

In `web/src/stores/restoreCart.js`, replace the `toggleFile`/`toggleFolder` actions and add
`setVersionWindow`:

```js
    toggleFile(host, path, storeHost, size, notBefore, notAfter) {
      this.rules = toggleFileRule(this.rules, host, path, { storeHost, size, notBefore, notAfter })
    },
    toggleFolder(path, notBefore, notAfter) {
      this.rules = toggleFolderRule(this.rules, path, { notBefore, notAfter })
    },
    removeEntry(entry) {
      if (entry.host === null) this.toggleFolder(entry.path)
      else this.toggleFile(entry.host, entry.path)
    },
    setDestPath(entry, destPath) {
      const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
      if (rule) rule.destPath = destPath
    },
    // setVersionWindow pins (or, called with 0, 0, resets) the version an
    // already-selected entry resolves to -- see restoreRules.js's toggle
    // functions for how a *new* selection gets its initial window instead
    // (the current catalog filter's receivedAfter/receivedBefore).
    setVersionWindow(entry, notBefore, notAfter) {
      const rule = this.rules.find((r) => r.host === entry.host && r.path === entry.path)
      if (rule) {
        rule.notBefore = notBefore
        rule.notAfter = notAfter
      }
    },
```

(`removeEntry`/`setDestPath` are unchanged — shown for placement context only.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/stores/restoreCart.spec.js`
Expected: PASS, all tests including the five new ones.

- [ ] **Step 5: Commit**

```bash
cd web && git add src/utils/restoreRules.js src/stores/restoreCart.js src/stores/restoreCart.spec.js
git commit -m "feat(web): thread a version window through the restore cart's rule model"
```

---

## Task 2: `catalog.js` — fetch a single path's own version history

**Files:**
- Modify: `web/src/stores/catalog.js`
- Test: `web/src/stores/catalog.spec.js`

**Interfaces:**
- Consumes: `splitPath` from `web/src/utils/pathSplit.js` (`splitPath(path).parentPath`).
- Produces: `catalogStore.fetchPathVersions(path, sourceHost)` → `Promise<entryDTO[]>`, sorted
  newest-first by `store_created_at`. `sourceHost` is optional; omitted, it returns every source
  host's versions of that exact path (used for a folder, which is host-agnostic). Deliberately
  **not** scoped by the current `receivedAfter`/`receivedBefore` filter — this always returns full
  history, since picking an older version is the entire point (Task 3/4 rely on this). Deliberately
  a plain action, not routed through `withRequest` — it's called on-demand by a modal (Task 3), and
  `withRequest` would otherwise flip the whole catalog page's `loading`/`error` state for a request
  that has nothing to do with the page's own table.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/stores/catalog.spec.js`, as a new `describe` block after the existing `describe('search', ...)`:

```js
  describe('fetchPathVersions', () => {
    it("queries the path's parent directory and filters the response down to that exact path, sorted newest-first", async () => {
      apiFetch.mockResolvedValue({
        data: [
          { path: '/var/lib/dbdata', store_created_at: 100, source_host: 'db1' },
          { path: '/var/lib/other', store_created_at: 200, source_host: 'db1' },
          { path: '/var/lib/dbdata', store_created_at: 300, source_host: 'db1' },
        ],
      })
      const catalog = useCatalogStore()

      const versions = await catalog.fetchPathVersions('/var/lib/dbdata')

      expect(apiFetch).toHaveBeenCalledWith('/catalog?parent_directories=%2Fvar%2Flib&limit=500')
      expect(versions).toEqual([
        { path: '/var/lib/dbdata', store_created_at: 300, source_host: 'db1' },
        { path: '/var/lib/dbdata', store_created_at: 100, source_host: 'db1' },
      ])
    })

    it('scopes the query to a single source host when one is passed', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const catalog = useCatalogStore()

      await catalog.fetchPathVersions('/etc/hosts', 'web01')

      expect(apiFetch).toHaveBeenCalledWith('/catalog?parent_directories=%2Fetc&limit=500&source_hosts=web01')
    })

    it('ignores the current receivedAfter/receivedBefore filter -- always full history', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const catalog = useCatalogStore()
      catalog.filters.receivedAfter = 12345
      catalog.filters.receivedBefore = 67890

      await catalog.fetchPathVersions('/etc/hosts')

      const [url] = apiFetch.mock.calls[0]
      expect(url).not.toContain('received_after')
      expect(url).not.toContain('received_before')
    })
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/stores/catalog.spec.js`
Expected: FAIL — `catalog.fetchPathVersions is not a function`.

- [ ] **Step 3: Implement**

In `web/src/stores/catalog.js`, add the import and the new action:

```js
import { splitPath } from '../utils/pathSplit'
```

(add alongside the existing `apiFetch`/`withRequest` imports at the top)

```js
    // fetchPathVersions returns every captured version of exactly one path
    // (a file's or a folder's own row -- both are FileVersionRecord rows
    // server-side, see storage/filesystem/models.go), newest first.
    // Deliberately ignores the active date-range filter: the whole point is
    // to let a user reach further back than what's currently browsed.
    async fetchPathVersions(path, sourceHost) {
      const { parentPath } = splitPath(path)
      const params = new URLSearchParams()
      params.set('parent_directories', parentPath)
      params.set('limit', String(MAX_PAGE_LIMIT))
      if (sourceHost) params.set('source_hosts', sourceHost)
      const body = await apiFetch(`/catalog?${params.toString()}`)
      return body.data
        .filter((e) => e.path === path)
        .sort((a, b) => b.store_created_at - a.store_created_at)
    },
```

Add this action inside the `actions` block, after `fetchDirectoryChildren` and before `refresh`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/stores/catalog.spec.js`
Expected: PASS, all tests including the three new ones.

- [ ] **Step 5: Commit**

```bash
cd web && git add src/stores/catalog.js src/stores/catalog.spec.js
git commit -m "feat(web): fetch a single catalog path's full version history"
```

---

## Task 3: Generalize `VersionsModal` into an interactive, file-and-folder version picker

**Files:**
- Modify: `web/src/components/VersionsModal.vue`
- Modify: `web/src/components/VersionsModal.spec.js`

**Interfaces:**
- Consumes: `catalogStore.fetchPathVersions(path, sourceHost)` (Task 2).
- Produces: `VersionsModal` now takes props `{ path: String (required), sourceHost: String (default
  null) }` instead of `{ group: Object }`, and emits `close`, `select-version` (payload: the chosen
  version's `entryDTO`), and `use-latest` (no payload) instead of only `close`. This is a breaking
  prop change — its one caller, `CatalogView.vue`, is fixed in Task 4, and its new second caller,
  `RestoreView.vue`, is added in Task 7. Between this task and Task 4 landing,
  `CatalogView.spec.js`'s modal-related tests will fail; that's expected and resolved by Task 4 in
  the same plan run.

- [ ] **Step 1: Write the failing tests**

Replace `web/src/components/VersionsModal.spec.js` entirely:

```js
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import VersionsModal from './VersionsModal.vue'
import { useCatalogStore } from '../stores/catalog'

function mountModal(props, versions = []) {
  const pinia = createTestingPinia({ stubActions: false })
  const catalog = useCatalogStore(pinia)
  vi.spyOn(catalog, 'fetchPathVersions').mockResolvedValue(versions)
  const wrapper = mount(VersionsModal, { props, global: { plugins: [pinia] } })
  return { wrapper, catalog }
}

function version(overrides) {
  return {
    id: 1,
    source_host: 'database',
    store_created_at: 1752400000,
    size: 8192,
    mode: '-rw-r--r--',
    mod_time: 1752400000,
    job_id: 'backup:daily-db-backup:2',
    store_host: 'bwfs-east',
    ...overrides,
  }
}

describe('VersionsModal', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('fetches versions scoped to the given path and source host on mount', () => {
    const { catalog } = mountModal({ path: '/var/lib/dbdata/data.db', sourceHost: 'database' })
    expect(catalog.fetchPathVersions).toHaveBeenCalledWith('/var/lib/dbdata/data.db', 'database')
  })

  it('renders the heading with the source host for a file', async () => {
    const { wrapper } = mountModal({ path: '/var/lib/dbdata/data.db', sourceHost: 'database' })
    await flushPromises()
    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
  })

  it('renders the heading without a host for a folder', async () => {
    const { wrapper } = mountModal({ path: '/srv/shared', sourceHost: null })
    await flushPromises()
    expect(wrapper.text()).toContain('Versions of /srv/shared')
    expect(wrapper.text()).not.toContain('on ')
  })

  it('renders every version newest-first with a "Restore this version" button each', async () => {
    const { wrapper } = mountModal(
      { path: '/var/lib/dbdata/data.db', sourceHost: 'database' },
      [version({ id: 2, store_created_at: 300 }), version({ id: 1, store_created_at: 200 })]
    )
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].find('[data-test="restore-version-2"]').exists()).toBe(true)
    expect(rows[1].find('[data-test="restore-version-1"]').exists()).toBe(true)
  })

  it('emits select-version with the chosen version on click', async () => {
    const v = version({ id: 1 })
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' }, [v])
    await flushPromises()
    await wrapper.get('[data-test="restore-version-1"]').trigger('click')
    expect(wrapper.emitted('select-version')).toEqual([[v]])
  })

  it('emits use-latest when the "Use latest" button is clicked', async () => {
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' })
    await flushPromises()
    await wrapper.get('[data-test="use-latest"]').trigger('click')
    expect(wrapper.emitted('use-latest')).toHaveLength(1)
  })

  it('shows a multi-host note only when the fetched versions span more than one source host', async () => {
    const single = await mountModal({ path: '/srv/shared', sourceHost: null }, [version({ source_host: 'a' })])
    await flushPromises()
    expect(single.wrapper.find('[data-test="multi-host-note"]').exists()).toBe(false)

    const multi = mountModal({ path: '/srv/shared', sourceHost: null }, [
      version({ id: 1, source_host: 'a' }),
      version({ id: 2, source_host: 'b' }),
    ])
    await flushPromises()
    expect(multi.wrapper.find('[data-test="multi-host-note"]').exists()).toBe(true)
  })

  it('shows an error message when the fetch rejects', async () => {
    const pinia = createTestingPinia({ stubActions: false })
    const catalog = useCatalogStore(pinia)
    vi.spyOn(catalog, 'fetchPathVersions').mockRejectedValue(new Error('catalog unavailable'))
    const wrapper = mount(VersionsModal, { props: { path: '/etc/hosts', sourceHost: 'web01' }, global: { plugins: [pinia] } })
    await flushPromises()
    expect(wrapper.get('[data-test="versions-error"]').text()).toBe('catalog unavailable')
  })

  it('renders a dash for a zero timestamp instead of the literal value', async () => {
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' }, [
      version({ store_created_at: 0, mod_time: 0 }),
    ])
    await flushPromises()
    const cells = wrapper.findAll('tbody td')
    expect(cells[0].text()).toBe('—')
  })

  it('emits close when the Close button is clicked', async () => {
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' })
    await flushPromises()
    await wrapper.get('[data-test="close"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on a backdrop click and on Escape', async () => {
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' })
    await flushPromises()
    await wrapper.find('.fixed').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(2)
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/components/VersionsModal.spec.js`
Expected: FAIL — component still expects a `group` prop and never calls `fetchPathVersions`.

- [ ] **Step 3: Implement**

Replace `web/src/components/VersionsModal.vue` entirely:

```vue
<script setup>
import { onMounted, onBeforeUnmount, ref, computed } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import BaseButton from './ui/BaseButton.vue'

const props = defineProps({
  path: { type: String, required: true },
  sourceHost: { type: String, default: null },
})
const emit = defineEmits(['close', 'select-version', 'use-latest'])

const catalog = useCatalogStore()
const versions = ref([])
const loading = ref(true)
const error = ref(null)

onMounted(async () => {
  try {
    versions.value = await catalog.fetchPathVersions(props.path, props.sourceHost)
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})

// A folder's own path is host-agnostic (sourceHost prop is null), and its
// fetched rows can span every source host that ever had something at that
// path -- worth calling out, since pinning a version still narrows the
// selection to whichever host(s) actually captured something at that exact
// instant (see restoreRules.js/restoreCart.js's setVersionWindow).
const spansMultipleHosts = computed(() => new Set(versions.value.map((v) => v.source_host)).size > 1)

function close() {
  emit('close')
}

function useLatest() {
  emit('use-latest')
}

function selectVersion(version) {
  emit('select-version', version)
}

function onKeydown(event) {
  if (event.key === 'Escape') close()
}
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-3xl w-full max-h-[80vh] overflow-auto">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">
          Versions of {{ path }}<span v-if="sourceHost"> on {{ sourceHost }}</span>
        </h2>
        <div class="flex gap-2">
          <BaseButton data-test="use-latest" variant="secondary" @click="useLatest">Use latest</BaseButton>
          <BaseButton data-test="close" variant="secondary" @click="close">Close</BaseButton>
        </div>
      </div>
      <p v-if="spansMultipleHosts" data-test="multi-host-note" class="text-sm text-gray-600 mb-2">
        This folder was captured separately per host — picking a version scopes this selection to
        that host's capture only.
      </p>
      <p v-if="loading" data-test="versions-loading">Loading versions…</p>
      <p v-else-if="error" data-test="versions-error" class="text-red-600">{{ error }}</p>
      <table v-else class="w-full text-left border-collapse">
        <thead>
          <tr class="border-b">
            <th class="py-2 pr-4">Captured</th>
            <th class="py-2 pr-4">Source Host</th>
            <th class="py-2 pr-4">Size</th>
            <th class="py-2 pr-4">Mode</th>
            <th class="py-2 pr-4">Modified</th>
            <th class="py-2 pr-4">Job ID</th>
            <th class="py-2 pr-4">Store Host</th>
            <th class="py-2 pr-4"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="version in versions" :key="version.id" class="border-b">
            <td class="py-2 pr-4">{{ formatTimestamp(version.store_created_at) || '—' }}</td>
            <td class="py-2 pr-4">{{ version.source_host }}</td>
            <td class="py-2 pr-4">{{ formatBytes(version.size) }}</td>
            <td class="py-2 pr-4">{{ version.mode }}</td>
            <td class="py-2 pr-4">{{ formatTimestamp(version.mod_time) || '—' }}</td>
            <td class="py-2 pr-4">{{ version.job_id }}</td>
            <td class="py-2 pr-4">{{ version.store_host }}</td>
            <td class="py-2 pr-4">
              <BaseButton :data-test="`restore-version-${version.id}`" variant="secondary" @click="selectVersion(version)">
                Restore this version
              </BaseButton>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/components/VersionsModal.spec.js`
Expected: PASS, all tests.

- [ ] **Step 5: Commit**

```bash
cd web && git add src/components/VersionsModal.vue src/components/VersionsModal.spec.js
git commit -m "feat(web): turn VersionsModal into an interactive file/folder version picker"
```

---

## Task 4: Wire the version picker into `CatalogView`

**Files:**
- Modify: `web/src/views/CatalogView.vue`
- Modify: `web/src/views/CatalogView.spec.js`

**Interfaces:**
- Consumes: `VersionsModal` (Task 3, new props/events); `restoreCartStore.toggleFile`/`toggleFolder`
  (Task 1, now take `notBefore`/`notAfter`); `restoreCartStore.setVersionWindow` (Task 1).
- Produces: nothing new consumed elsewhere — this task only changes `CatalogView.vue`'s own
  behavior and template.

- [ ] **Step 1: Write the failing tests**

In `web/src/views/CatalogView.spec.js`, replace these five existing tests (they assert the old
"Versions" count column and old `group`-prop modal wiring, which no longer exist):
`'groups entries sharing source_host and path into a single row with a version count'`,
`'renders a single-version file without a version count'`,
`'opens the versions modal for the row actually clicked, even after sorting reorders the table'`,
`'does not open the versions modal when a single-version row is clicked'`,
`'closes the versions modal via its Close button'`

with:

This file's existing `mountView(state, restoreCartState = {})` helper (top of file) takes the
catalog store's state directly as its first argument and the restore-cart store's state as its
optional second — reuse it exactly as every surrounding test already does, not nested under a
`catalog:`/`restoreCart:` key:

```js
  it('shows each file row\'s representative captured timestamp as a clickable "Captured" cell', () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01', store_created_at: 1752400000 })] })
    const cell = wrapper.get('[data-test="captured-web01:/etc/hosts"]')
    expect(cell.text()).not.toBe('')
  })

  it('opens the version picker for the row whose Captured cell was clicked, scoped to that file\'s host', async () => {
    const wrapper = mountView({
      entries: [
        entry({ path: '/etc/hosts', source_host: 'web01' }),
        entry({ path: '/etc/hostname', source_host: 'web01' }),
      ],
    })
    await wrapper.get('[data-test="captured-web01:/etc/hostname"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'VersionsModal' })
    expect(modal.props('path')).toBe('/etc/hostname')
    expect(modal.props('sourceHost')).toBe('web01')
  })

  it('opens the version picker for a folder row scoped host-agnostically (sourceHost null)', async () => {
    const wrapper = mountView({ directoryChildren: [{ path: '/var', name: 'var', file_count: 3, last_seen: 100 }] })
    await wrapper.get('[data-test="captured-/var"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'VersionsModal' })
    expect(modal.props('path')).toBe('/var')
    expect(modal.props('sourceHost')).toBe(null)
  })

  it('closes the version picker via its close event', async () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] })
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'VersionsModal' }).exists()).toBe(false)
  })

  it('selecting a version selects the file (if not already) and pins its window to that exact instant', async () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] })
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 555, store_host: 'bwfs-1', size: 10 })
    expect(restoreCart.toggleFile).toHaveBeenCalledWith('web01', '/etc/hosts', 'bwfs-1', 10, expect.any(Number), expect.any(Number))
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith({ host: 'web01', path: '/etc/hosts' }, 555, 555)
  })

  it('selecting a version for an already-selected file does not re-toggle it', async () => {
    const wrapper = mountView(
      { entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] },
      { rules: [{ host: 'web01', path: '/etc/hosts', include: true, destPath: '/etc/hosts' }] }
    )
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 555 })
    expect(restoreCart.toggleFile).not.toHaveBeenCalled()
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith({ host: 'web01', path: '/etc/hosts' }, 555, 555)
  })

  it('using latest resets the window to the current catalog filter range', async () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] })
    const restoreCart = useRestoreCartStore()
    const catalog = useCatalogStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('use-latest')
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith(
      { host: 'web01', path: '/etc/hosts' },
      catalog.filters.receivedAfter,
      catalog.filters.receivedBefore
    )
  })

  it('checking a file box passes the current catalog filter range as its default version window', async () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] })
    const restoreCart = useRestoreCartStore()
    const catalog = useCatalogStore()
    await wrapper.get('[data-test="file-checkbox-web01:/etc/hosts"]').trigger('click')
    expect(restoreCart.toggleFile).toHaveBeenCalledWith(
      'web01', '/etc/hosts', undefined, undefined, catalog.filters.receivedAfter, catalog.filters.receivedBefore
    )
  })
```

(`useCatalogStore`/`useRestoreCartStore` are already imported at the top of this file, per its
existing `mountView` helper's own return value — no new imports needed.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: FAIL — old "Versions" column/`group` prop still in place, new data-test hooks don't exist.

- [ ] **Step 3: Implement**

In `web/src/views/CatalogView.vue`'s `<script setup>`:

Replace `const selectedGroup = ref(null)` with `const versionsFor = ref(null) // { path, sourceHost } | null`.

Replace `toggleSelection`:

```js
function toggleSelection(row) {
  if (row.isFolder) {
    restoreCart.toggleFolder(row.path, catalog.filters.receivedAfter, catalog.filters.receivedBefore)
  } else {
    restoreCart.toggleFile(
      row.sourceHost,
      row.path,
      row.representative?.store_host,
      row.representative?.size,
      catalog.filters.receivedAfter,
      catalog.filters.receivedBefore
    )
  }
}
```

Replace `onRowClick` (drop the file/`selectedGroup` branch entirely — version viewing moves to the
dedicated Captured-cell handler below):

```js
function onRowClick(row) {
  if (row.isFolder) catalog.navigateTo(row.path)
}
```

Add, after `onRowClick`:

```js
function openVersions(row) {
  versionsFor.value = row.isFolder ? { path: row.path, sourceHost: null } : { path: row.path, sourceHost: row.sourceHost }
}

function selectVersion(version) {
  const target = versionsFor.value
  if (target.sourceHost === null) {
    if (resolveFolderState(restoreCart.rules, target.path) !== 'checked') {
      restoreCart.toggleFolder(target.path, catalog.filters.receivedAfter, catalog.filters.receivedBefore)
    }
  } else if (!resolveFile(restoreCart.rules, target.sourceHost, target.path)) {
    restoreCart.toggleFile(
      target.sourceHost, target.path, version.store_host, version.size,
      catalog.filters.receivedAfter, catalog.filters.receivedBefore
    )
  }
  restoreCart.setVersionWindow({ host: target.sourceHost, path: target.path }, version.store_created_at, version.store_created_at)
  versionsFor.value = null
}

function useLatestVersion() {
  const target = versionsFor.value
  restoreCart.setVersionWindow(
    { host: target.sourceHost, path: target.path },
    catalog.filters.receivedAfter,
    catalog.filters.receivedBefore
  )
  versionsFor.value = null
}
```

Update the `baseColumns` array: replace `{ label: 'Versions', field: 'versions', sortable: false }`
with `{ label: 'Captured', field: 'captured', sortable: false }`.

In the template, replace the two `versions`-field branches (inside `v-else-if="row.isFolder"` and
the final `v-else` file branch) with:

```html
<span v-else-if="column.field === 'captured'">
  <button
    type="button"
    :data-test="`captured-${row.path}`"
    class="text-blue-600 hover:underline"
    @click.stop="openVersions(row)"
  >
    {{ formatTimestamp(row.last_seen) || '—' }}
  </button>
</span>
```

inside the folder branch, and:

```html
<span v-else-if="column.field === 'captured'">
  <button
    type="button"
    :data-test="`captured-${row.sourceHost}:${row.path}`"
    class="text-blue-600 hover:underline"
    @click.stop="openVersions(row)"
  >
    {{ formatTimestamp(row.representative.store_created_at) || '—' }}
  </button>
</span>
```

inside the file branch.

Replace the `<VersionsModal v-if="selectedGroup" ...>` line at the bottom of the template:

```html
<VersionsModal
  v-if="versionsFor"
  :path="versionsFor.path"
  :source-host="versionsFor.sourceHost"
  @close="versionsFor = null"
  @select-version="selectVersion"
  @use-latest="useLatestVersion"
/>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: PASS, all tests.

- [ ] **Step 5: Commit**

```bash
cd web && git add src/views/CatalogView.vue src/views/CatalogView.spec.js
git commit -m "feat(web): wire the version picker into the catalog table, defaulted to the active filter"
```

---

## Task 5: `restoreSubmission.js` — per-entry status, version window and `job_id` on the wire

**Files:**
- Modify: `web/src/stores/restoreSubmission.js`
- Modify: `web/src/stores/restoreSubmission.spec.js` (full rewrite — the state shape changes from a
  flat `results` array to a per-entry `entryStatus` map, so most existing assertions no longer
  apply as written)

**Interfaces:**
- Consumes: `entryKey` from `restoreRules.js` (Task 1).
- Produces: `restoreSubmissionStore.entryStatus` — an object keyed by `entryKey(entry)`, each value
  an array of `{ status: 'submitting' | 'success' | 'error', jobId?, message? }` (an array because
  one folder entry can fan out to more than one storage host's policy, each with its own outcome;
  almost always length 1 in practice). `results` is removed. `submit()`'s signature is unchanged.
  `toWireRule` now emits `not_before`/`not_after` when the rule is included and has them set.

- [ ] **Step 1: Write the failing tests**

Replace `web/src/stores/restoreSubmission.spec.js` entirely:

```js
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestoreSubmissionStore } from './restoreSubmission'
import { useRestoreCartStore } from './restoreCart'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

function mockStorageAndRestore({ store = 'store-a', policyId = 's1', restoreOk = true, restoreError } = {}) {
  apiFetch.mockImplementation((path, opts) => {
    if (path.startsWith('/catalog/stores')) return Promise.resolve({ data: [{ name: store, count: 1, last_seen: 100 }] })
    if (path === '/policies?type=storage') {
      return Promise.resolve({ data: [{ id: policyId, port: 8080, checkins: [{ hostname: store, last_seen_at: 1 }] }] })
    }
    if (path === '/restore') {
      if (!restoreOk) return Promise.reject(new Error(restoreError))
      return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name, job_id: 'restore:r1:1' })
    }
    throw new Error(`unexpected apiFetch call: ${path}`)
  })
}

describe('restoreSubmission store', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-10T00:00:00.000Z'))
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('reports an error and makes no network calls when the cart is empty', async () => {
    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(apiFetch).not.toHaveBeenCalled()
    expect(submission.error).toBe('Nothing selected for restore.')
    expect(submission.entryStatus).toEqual({})
  })

  it('records a success status carrying the job_id for a single-store entry', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    mockStorageAndRestore()

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'success', jobId: 'restore:r1:1' }])
  })

  it('records a per-store error without blocking other stores, keyed by their own entries', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores?source_hosts=database')) return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      if (path.startsWith('/catalog/stores?source_hosts=web01')) return Promise.resolve({ data: [{ name: 'store-b', count: 1, last_seen: 100 }] })
      if (path === '/policies?type=storage') {
        return Promise.resolve({ data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }] })
      }
      if (path === '/restore') return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name, job_id: 'restore:r1:1' })
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    expect(submission.entryStatus['database:/var/lib/dbdata/dump.sql']).toEqual([{ status: 'success', jobId: 'restore:r1:1' }])
    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'error', message: 'No storage policy found for store-b' }])
  })

  it('includes not_before/not_after on the wire only for an included rule that has them', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', undefined, undefined, 1000, 2000)
    mockStorageAndRestore()

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules).toEqual([{ host: 'web01', path: '/etc/hosts', include: true, not_before: 1000, not_after: 2000 }])
  })

  it('omits not_before/not_after from an exclusion rule even if present', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/srv/shared', 1000, 2000)
    cart.toggleFile('web01', '/srv/shared/secret.env', undefined, undefined, 1000, 2000) // deselect -> exclusion rule
    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      if (path === '/policies?type=storage') return Promise.resolve({ data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }] })
      if (path === '/restore') return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name, job_id: 'restore:r1:1' })
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    const exclusion = body.rules.find((r) => r.include === false)
    expect(exclusion).toEqual({ host: 'web01', path: '/srv/shared/secret.env', include: false })
  })

  it('excludes an already-succeeded entry from a later submit, without dropping still-eligible entries', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    mockStorageAndRestore()
    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(apiFetch).toHaveBeenCalledTimes(3) // stores facet, storage policies, /restore

    apiFetch.mockClear()
    cart.toggleFile('web01', '/etc/nginx/nginx.conf')
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules).toEqual([{ host: 'web01', path: '/etc/nginx/nginx.conf', include: true }])
  })

  it('retries a previously-failed entry on a later submit', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    mockStorageAndRestore({ restoreOk: false, restoreError: 'transient failure' })
    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'error', message: 'transient failure' }])

    mockStorageAndRestore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'success', jobId: 'restore:r1:1' }])
  })

  it('tracks submitting state across the whole flow', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')
    apiFetch.mockResolvedValue({ data: [] })

    const submission = useRestoreSubmissionStore()
    const pending = submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(submission.submitting).toBe(true)
    await pending
    expect(submission.submitting).toBe(false)
  })

  it('sends mode and overwrite through on every per-store /restore call', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')
    mockStorageAndRestore()

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'restore', overwrite: true })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.mode).toBe('restore')
    expect(body.overwrite).toBe(true)
  })

  it('never sends storeHost, size, or notBefore/notAfter-as-camelCase on the wire', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', 'bwfs-1', 4096, 1000, 1000)
    mockStorageAndRestore()

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules[0]).not.toHaveProperty('storeHost')
    expect(body.rules[0]).not.toHaveProperty('size')
    expect(body.rules[0]).not.toHaveProperty('notBefore')
    expect(body.rules[0]).not.toHaveProperty('notAfter')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: FAIL — `submission.entryStatus` doesn't exist yet, `results` is still what's populated.

- [ ] **Step 3: Implement**

Replace `web/src/stores/restoreSubmission.js` entirely:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { useRestoreCartStore } from './restoreCart'
import { useStoragePoliciesStore } from './storagePolicies'
import { useRestorePoliciesStore } from './restorePolicies'
import { entryKey } from '../utils/restoreRules'

// distinctPositiveEntries returns cart.entries (the positively-selected
// top-level rules), deduped by (host, path) -- submitting the same
// top-level selection twice would otherwise issue a redundant facet query.
function distinctPositiveEntries(entries) {
  const seen = new Set()
  return entries.filter((e) => {
    const key = entryKey(e)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function buildStoreFacetsQuery(entry) {
  const params = new URLSearchParams()
  if (entry.host) params.set('source_hosts', entry.host)
  params.set('pattern', entry.path)
  return params.toString()
}

// storesTouchedByEntry finds every store_host holding data matching one
// entry's pattern -- a cheap facet query (bounded by distinct-store-count,
// not by how many files match), replacing the old full-file-pagination
// approach.
async function storesTouchedByEntry(entry) {
  const qs = buildStoreFacetsQuery(entry)
  const body = await apiFetch(`/catalog/stores?${qs}`)
  return body.data.map((f) => f.name)
}

// buildRulesByStore groups the cart's rules per store, so each store's
// restore policy is told to verify only what that store could actually
// have, and (entriesByStore) which cart entries that policy's outcome
// should be reported against -- see restoreSubmission's own module doc at
// the top of submit() below. Three kinds of rule, three treatments:
//
//   - A host-specific (file) *include* rule goes only to the store(s) that
//     entry's own facet lookup found it on.
//   - A host-agnostic (folder) include rule goes to every store.
//   - An *exclude* rule of either kind goes to every store.
//
// Rule order is not significant to consumers: both restoreRules.js's
// resolveFile and rwfs's resolveRestoreFile resolve by specificity
// (exact-host rule, else longest matching ancestor folder rule), never by
// position in the list.
async function buildRulesByStore(positiveEntries, allRules) {
  const perEntryStores = await Promise.all(positiveEntries.map((e) => storesTouchedByEntry(e)))
  const allStores = new Set(perEntryStores.flat())

  const sharedRules = allRules.filter((r) => !r.include || !r.host)
  const sharedFolderEntries = positiveEntries.filter((e) => e.host === null)

  const fileEntriesByStore = new Map()
  positiveEntries.forEach((entry, i) => {
    if (!entry.host) return
    for (const store of perEntryStores[i]) {
      if (!fileEntriesByStore.has(store)) fileEntriesByStore.set(store, [])
      fileEntriesByStore.get(store).push(entry)
    }
  })

  const rulesByStore = new Map()
  const entriesByStore = new Map()
  for (const store of allStores) {
    const fileEntries = fileEntriesByStore.get(store) || []
    rulesByStore.set(store, [...sharedRules, ...fileEntries])
    entriesByStore.set(store, [...sharedFolderEntries, ...fileEntries])
  }
  return { rulesByStore, entriesByStore }
}

// toWireRule strips the cart's client-only display fields (storeHost, size
// -- see restoreCart.js's toggleFile) and omits dest_path entirely when
// it's unchanged from path (the "no rename" case). not_before/not_after
// are only ever meaningful on an included rule (see policyserver.proto's
// RestoreRule doc) -- an excluded rule can carry a stale window from
// whatever the box's *last checked* state set, so it's deliberately never
// sent for one.
function toWireRule(rule) {
  const wire = { host: rule.host, path: rule.path, include: rule.include }
  if (rule.destPath && rule.destPath !== rule.path) wire.dest_path = rule.destPath
  if (rule.include && rule.notBefore) wire.not_before = rule.notBefore
  if (rule.include && rule.notAfter) wire.not_after = rule.notAfter
  return wire
}

function storagePolicyIdForHost(storagePolicies, storeHost) {
  for (const policy of storagePolicies) {
    if ((policy.checkins || []).some((c) => c.hostname === storeHost)) return policy.id
  }
  return null
}

export const useRestoreSubmissionStore = defineStore('restoreSubmission', {
  state: () => ({
    submitting: false,
    // entryKey(entry) -> [{ status: 'submitting'|'success'|'error', jobId?, message? }].
    // An array because one folder entry can fan out to more than one
    // storage host's policy, each with its own independent outcome --
    // almost always length 1 for a file entry, which only ever touches
    // one store. Persists across submit() calls (not reset to {} each
    // time) so a completed row's status/link stays visible after the
    // request that produced it finishes -- see submit()'s
    // already-succeeded filter below for how re-submission is guarded
    // instead of relying on the cart being emptied.
    entryStatus: {},
    error: null,
  }),
  actions: {
    async submit(destinationHost, { mode, overwrite }) {
      const cart = useRestoreCartStore()
      const storagePolicies = useStoragePoliciesStore()
      const restorePolicies = useRestorePoliciesStore()

      this.submitting = true
      this.error = null

      try {
        const alreadySucceeded = (entry) =>
          (this.entryStatus[entryKey(entry)] || []).some((s) => s.status === 'success')
        const positiveEntries = distinctPositiveEntries(cart.entries).filter((e) => !alreadySucceeded(e))
        if (positiveEntries.length === 0) {
          this.error = 'Nothing selected for restore.'
          return
        }

        const { rulesByStore, entriesByStore } = await buildRulesByStore(positiveEntries, cart.rules)

        await storagePolicies.fetchAll()
        if (storagePolicies.error) {
          this.error = `Could not look up storage policies: ${storagePolicies.error}`
          return
        }

        for (const entry of positiveEntries) this.entryStatus[entryKey(entry)] = [{ status: 'submitting' }]

        for (const [storeHost, rules] of rulesByStore) {
          const coveredEntries = entriesByStore.get(storeHost)
          const storagePolicyId = storagePolicyIdForHost(storagePolicies.list, storeHost)
          if (!storagePolicyId) {
            this.recordOutcome(coveredEntries, { status: 'error', message: `No storage policy found for ${storeHost}` })
            continue
          }
          try {
            const name = `restore-${new Date().toISOString()}-${storeHost}`
            const policy = await restorePolicies.create({
              name,
              client_filters: { hostnames: [destinationHost], labels: {} },
              storage_policy_id: storagePolicyId,
              rules: rules.map(toWireRule),
              mode,
              overwrite,
            })
            this.recordOutcome(coveredEntries, { status: 'success', jobId: policy.job_id })
          } catch (err) {
            this.recordOutcome(coveredEntries, { status: 'error', message: err.message })
          }
        }
      } catch (err) {
        this.error = err.message
      } finally {
        this.submitting = false
      }
    },

    recordOutcome(entries, outcome) {
      for (const entry of entries) {
        const key = entryKey(entry)
        this.entryStatus[key] = (this.entryStatus[key] || []).filter((s) => s.status !== 'submitting')
        this.entryStatus[key].push(outcome)
      }
    },
  },
})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: PASS, all tests.

- [ ] **Step 5: Commit**

```bash
cd web && git add src/stores/restoreSubmission.js src/stores/restoreSubmission.spec.js
git commit -m "feat(web): track restore submission status per cart entry, thread version window and job_id"
```

---

## Task 6: `RestoreConfirmModal` — pre-restore confirmation

**Files:**
- Create: `web/src/components/RestoreConfirmModal.vue`
- Test: `web/src/components/RestoreConfirmModal.spec.js`

**Interfaces:**
- Consumes: `formatBytes` from `web/src/utils/format.js`.
- Produces: props `{ fileCount: Number, totalSize: Number, destinationHost: String, overwrite:
  Boolean, pinnedCount: Number }`, emits `confirm`, `cancel`. Consumed by Task 7.

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/RestoreConfirmModal.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import RestoreConfirmModal from './RestoreConfirmModal.vue'

function renderModal(overrides = {}) {
  return mount(RestoreConfirmModal, {
    props: { fileCount: 3, totalSize: 1024, destinationHost: 'web-03', overwrite: false, pinnedCount: 0, ...overrides },
  })
}

describe('RestoreConfirmModal', () => {
  it('renders the file count, size, and destination', () => {
    const wrapper = renderModal()
    const text = wrapper.get('[data-test="confirm-summary"]').text()
    expect(text).toContain('3 items')
    expect(text).toContain('1.0 KB')
    expect(text).toContain('web-03')
  })

  it('uses singular "item" for a single file', () => {
    const wrapper = renderModal({ fileCount: 1 })
    expect(wrapper.get('[data-test="confirm-summary"]').text()).toContain('1 item ')
  })

  it('shows the overwrite callout only when overwrite is true', () => {
    expect(renderModal({ overwrite: false }).find('[data-test="confirm-overwrite"]').exists()).toBe(false)
    expect(renderModal({ overwrite: true }).find('[data-test="confirm-overwrite"]').exists()).toBe(true)
  })

  it('shows the pinned-version callout only when pinnedCount is greater than zero', () => {
    expect(renderModal({ pinnedCount: 0 }).find('[data-test="confirm-pinned"]').exists()).toBe(false)
    const wrapper = renderModal({ pinnedCount: 2 })
    expect(wrapper.get('[data-test="confirm-pinned"]').text()).toContain('2 items pinned')
  })

  it('emits confirm and cancel from their respective buttons', async () => {
    const wrapper = renderModal()
    await wrapper.get('[data-test="confirm-restore"]').trigger('click')
    expect(wrapper.emitted('confirm')).toHaveLength(1)
    await wrapper.get('[data-test="confirm-cancel"]').trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(1)
  })

  it('emits cancel on Escape and on a backdrop click', async () => {
    const wrapper = renderModal()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('cancel')).toHaveLength(1)
    await wrapper.get('.fixed').trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(2)
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/components/RestoreConfirmModal.spec.js`
Expected: FAIL — file doesn't exist.

- [ ] **Step 3: Implement**

Create `web/src/components/RestoreConfirmModal.vue`:

```vue
<script setup>
import { onMounted, onBeforeUnmount } from 'vue'
import BaseButton from './ui/BaseButton.vue'
import { formatBytes } from '../utils/format'

const props = defineProps({
  fileCount: { type: Number, required: true },
  totalSize: { type: Number, required: true },
  destinationHost: { type: String, required: true },
  overwrite: { type: Boolean, required: true },
  pinnedCount: { type: Number, required: true },
})
const emit = defineEmits(['confirm', 'cancel'])

function onKeydown(event) {
  if (event.key === 'Escape') emit('cancel')
}
onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="$emit('cancel')">
    <div class="bg-white rounded p-4 max-w-md w-full">
      <h2 class="text-lg font-semibold mb-4">Confirm restore</h2>
      <p data-test="confirm-summary" class="mb-2">
        You're about to restore {{ fileCount }} item{{ fileCount === 1 ? '' : 's' }}
        ({{ formatBytes(totalSize) }}) to <strong>{{ destinationHost }}</strong>.
      </p>
      <p v-if="overwrite" data-test="confirm-overwrite" class="mb-2 text-amber-700">
        Existing files at the destination will be overwritten.
      </p>
      <p v-if="pinnedCount > 0" data-test="confirm-pinned" class="mb-4 text-amber-700">
        {{ pinnedCount }} item{{ pinnedCount === 1 ? '' : 's' }} pinned to an older version.
      </p>
      <div class="flex justify-end gap-2">
        <BaseButton data-test="confirm-cancel" variant="secondary" @click="$emit('cancel')">Cancel</BaseButton>
        <BaseButton data-test="confirm-restore" variant="primary" @click="$emit('confirm')">Restore</BaseButton>
      </div>
    </div>
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/components/RestoreConfirmModal.spec.js`
Expected: PASS, all tests.

- [ ] **Step 5: Commit**

```bash
cd web && git add src/components/RestoreConfirmModal.vue src/components/RestoreConfirmModal.spec.js
git commit -m "feat(web): add a pre-restore confirmation modal"
```

---

## Task 7: `RestoreView` — cart restructure

**Files:**
- Modify: `web/src/views/RestoreView.vue`
- Modify: `web/src/views/RestoreView.spec.js` (substantial rewrite — the Storage Host column is
  removed, the flat results list is replaced by a per-row status column, and several new elements
  are added)

**Interfaces:**
- Consumes: `entryKey` (Task 1), `VersionsModal` (Task 3/4's shape), `RestoreConfirmModal` (Task 6),
  `restoreSubmissionStore.entryStatus` (Task 5), `useJobsStore` (existing, `jobs.js`).
- Produces: nothing new consumed elsewhere.

- [ ] **Step 1: Write the failing tests**

Replace `web/src/views/RestoreView.spec.js` entirely:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RestoreView from './RestoreView.vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'

function mountView(initialState = {}) {
  const pinia = createTestingPinia({ stubActions: true, initialState })
  const wrapper = mount(RestoreView, {
    global: { plugins: [pinia], stubs: { 'router-link': { template: '<a><slot /></a>' } } },
  })
  return { wrapper, pinia }
}

const fileEntry = { host: 'web01', path: '/etc/hosts', include: true, destPath: '/etc/hosts', size: 100 }
const folderEntry = { host: null, path: '/var', include: true, destPath: '/var' }
const pinnedEntry = { host: 'web01', path: '/etc/nginx.conf', include: true, destPath: '/etc/nginx.conf', notBefore: 555, notAfter: 555 }

describe('RestoreView', () => {
  it('shows the empty state when the cart has no selections', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [] } })
    expect(wrapper.text()).toContain('No files selected for restore yet.')
  })

  it('shows a summary line with item count and total size', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry, folderEntry] } })
    expect(wrapper.get('[data-test="cart-summary"]').text()).toContain('2 items selected')
  })

  it('never renders a storage/store host anywhere on the page', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    expect(wrapper.text()).not.toMatch(/store.?host/i)
  })

  it('shows "Latest" for an entry with no version pin, and the pinned timestamp for one that has it', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry, pinnedEntry] } })
    expect(wrapper.get('[data-test="captured-web01:/etc/hosts"]').text()).toBe('Latest')
    expect(wrapper.get('[data-test="captured-web01:/etc/nginx.conf"]').text()).not.toBe('Latest')
  })

  it('clicking a Captured cell opens the version picker scoped to that entry', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'VersionsModal' })
    expect(modal.props('path')).toBe('/etc/hosts')
    expect(modal.props('sourceHost')).toBe('web01')
  })

  it('selecting a version from the picker calls setVersionWindow for that entry', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 999 })
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith({ host: 'web01', path: '/etc/hosts' }, 999, 999)
  })

  it('shows an edit control for the destination path instead of relying on clicking plain text', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    expect(wrapper.get('[data-test="edit-dest-path-web01:/etc/hosts"]').exists()).toBe(true)
  })

  it('clicking the edit control opens an editable input prefilled with the current value', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    await wrapper.get('[data-test="edit-dest-path-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.get('[data-test="dest-path-input-web01:/etc/hosts"]')
    expect(input.element.value).toBe('/etc/hosts')
  })

  it('committing an edited destination path calls restoreCart.setDestPath and exits edit mode', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="edit-dest-path-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.get('[data-test="dest-path-input-web01:/etc/hosts"]')
    await input.setValue('/etc/hosts.bak')
    await input.trigger('keyup.enter')
    expect(restoreCart.setDestPath).toHaveBeenCalledWith(fileEntry, '/etc/hosts.bak')
    expect(wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]').exists()).toBe(false)
  })

  it('shows overwrite and verify/restore helper copy', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    expect(wrapper.text()).toContain('Replaces files that already exist at the destination')
    expect(wrapper.text()).toContain('Verify checks integrity only')
    expect(wrapper.text()).toContain('Restore writes files to the destination')
  })

  it('shows a stale-checkin warning when the selected destination host has not checked in recently', async () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      clients: { list: [{ hostname: 'web-03', last_seen_at: 0 }] },
    })
    const select = wrapper.get('[data-test="destination-select"]')
    await select.setValue('web-03')
    expect(wrapper.get('[data-test="destination-stale-warning"]').exists()).toBe(true)
  })

  it('shows no stale-checkin warning for a recently-checked-in destination host', async () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      clients: { list: [{ hostname: 'web-03', last_seen_at: Math.floor(Date.now() / 1000) }] },
    })
    const select = wrapper.get('[data-test="destination-select"]')
    await select.setValue('web-03')
    expect(wrapper.find('[data-test="destination-stale-warning"]').exists()).toBe(false)
  })

  it('clicking Verify calls restoreSubmission.submit directly, without a confirmation step', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] }, clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] } })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="verify-button"]').trigger('click')
    expect(submission.submit).toHaveBeenCalledWith('web-03', { mode: 'verify', overwrite: false })
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).exists()).toBe(false)
  })

  it('clicking Restore opens the confirmation modal instead of submitting immediately', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] }, clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] } })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    expect(submission.submit).not.toHaveBeenCalled()
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).exists()).toBe(true)
  })

  it('confirming the modal submits with mode restore and the checked overwrite flag, and closes it', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] }, clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] } })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="overwrite-checkbox"]').setValue(true)
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    await wrapper.findComponent({ name: 'RestoreConfirmModal' }).vm.$emit('confirm')
    expect(submission.submit).toHaveBeenCalledWith('web-03', { mode: 'restore', overwrite: true })
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).exists()).toBe(false)
  })

  it('cancelling the modal submits nothing and closes it', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] }, clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] } })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    await wrapper.findComponent({ name: 'RestoreConfirmModal' }).vm.$emit('cancel')
    expect(submission.submit).not.toHaveBeenCalled()
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).exists()).toBe(false)
  })

  it('passes the pinned-entry count into the confirmation modal', async () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry, pinnedEntry] },
      clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] },
    })
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).props('pinnedCount')).toBe(1)
  })

  it("renders a submitting badge, then a success badge linking to the entry's job", () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      restoreSubmission: { entryStatus: { 'web01:/etc/hosts': [{ status: 'success', jobId: 'restore:r1:1' }] } },
      jobs: { list: [{ job_id: 'restore:r1:1', state: 'success' }] },
    })
    const link = wrapper.get('[data-test="status-web01:/etc/hosts"] a')
    expect(link.attributes('href') || link.exists()).toBeTruthy()
    expect(wrapper.get('[data-test="status-web01:/etc/hosts"]').text()).toContain('success')
  })

  it('renders an error message inline for an entry whose submission failed', () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      restoreSubmission: { entryStatus: { 'web01:/etc/hosts': [{ status: 'error', message: 'No storage policy found for store-b' }] } },
    })
    expect(wrapper.get('[data-test="status-web01:/etc/hosts"]').text()).toContain('No storage policy found for store-b')
  })

  it('removing an entry calls restoreCart.removeEntry with that entry', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="remove-web01:/etc/hosts"]').trigger('click')
    expect(restoreCart.removeEntry).toHaveBeenCalledWith(fileEntry)
  })

  it('disables verify and restore until the cart has a selection and a destination is chosen', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    expect(wrapper.get('[data-test="verify-button"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="destination-select"]').setValue('web01')
    expect(wrapper.get('[data-test="verify-button"]').attributes('disabled')).toBeUndefined()
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/views/RestoreView.spec.js`
Expected: FAIL — Storage Host column still present, no summary line/edit button/status column/confirm modal.

- [ ] **Step 3: Implement**

Replace `web/src/views/RestoreView.vue` entirely:

```vue
<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useClientsStore } from '../stores/clients'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'
import { useJobsStore } from '../stores/jobs'
import { formatBytes, formatTimestamp } from '../utils/format'
import { entryKey } from '../utils/restoreRules'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import BaseField from '../components/ui/BaseField.vue'
import BaseSelect from '../components/ui/BaseSelect.vue'
import Badge from '../components/ui/Badge.vue'
import VersionsModal from '../components/VersionsModal.vue'
import RestoreConfirmModal from '../components/RestoreConfirmModal.vue'

// 2x agent's default 15-minute operating-refresh cadence
// (OperatingCertFetchIntervalSec, docs/components/agent.md) -- a host more
// than twice its own expected check-in interval overdue is reasonably
// "stale" rather than just between ticks.
const STALE_CHECKIN_SEC = 1800

const restoreCart = useRestoreCartStore()
const clients = useClientsStore()
const submission = useRestoreSubmissionStore()
const jobs = useJobsStore()

const destinationHost = ref('')
const overwrite = ref(false)
// Key of the entry currently being edited (see entryKey), or null when no
// destination-path cell is in edit mode. Only one cell can be edited at a
// time.
const editingKey = ref(null)
const editingInput = ref(null)
const versionsFor = ref(null) // the cart entry a version picker is open for, or null
const confirming = ref(false)

onMounted(() => {
  if (clients.list.length === 0) clients.fetchAll()
  jobs.connectJobsStream()
})

function sourcePathLabel(entry) {
  return entry.host === null ? `${entry.path}/*` : entry.path
}

function remove(entry) {
  restoreCart.removeEntry(entry)
}

function startEditing(entry) {
  editingKey.value = entryKey(entry)
  nextTick(() => editingInput.value?.focus())
}

// Guarded against double-firing: in a real browser, Enter triggers
// commitEdit -> editingKey is cleared -> Vue removes the (still-focused)
// input from the DOM -> the removal itself fires a native blur, which is
// still wired to commitEdit at that instant. Without this guard that fires
// restoreCart.setDestPath a second time for the same edit.
function commitEdit(entry, value) {
  if (editingKey.value !== entryKey(entry)) return
  restoreCart.setDestPath(entry, value)
  editingKey.value = null
}

function openVersions(entry) {
  versionsFor.value = entry
}

function selectVersion(version) {
  restoreCart.setVersionWindow(versionsFor.value, version.store_created_at, version.store_created_at)
  versionsFor.value = null
}

// Unlike CatalogView's useLatestVersion (Task 4), which resets to the
// *currently browsed* catalog filter window, this page has no live catalog
// filter in scope to fall back to -- entries here may have been selected
// under different browsing filters at different times. 0/0 wire-encodes as
// fully unbounded (see toWireRule's truthy check, Task 5), which resolves
// to a genuine "whatever's newest at restore time" -- the simplest correct
// meaning "latest" can have on this page.
function useLatestVersion() {
  restoreCart.setVersionWindow(versionsFor.value, 0, 0)
  versionsFor.value = null
}

function isPinned(entry) {
  return Boolean(entry.notBefore) && Boolean(entry.notAfter) && entry.notBefore === entry.notAfter
}

const totalSize = computed(() => restoreCart.entries.reduce((sum, e) => sum + (e.size || 0), 0))
const pinnedCount = computed(() => restoreCart.entries.filter(isPinned).length)

const destinationClient = computed(() => clients.list.find((c) => c.hostname === destinationHost.value))
const destinationStale = computed(() => {
  const client = destinationClient.value
  if (!client) return false
  return Math.floor(Date.now() / 1000) - client.last_seen_at > STALE_CHECKIN_SEC
})

const canSubmit = computed(
  () => restoreCart.hasSelections && destinationHost.value !== '' && !submission.submitting
)

function verify() {
  submission.submit(destinationHost.value, { mode: 'verify', overwrite: overwrite.value })
}

function openConfirm() {
  confirming.value = true
}

function confirmRestore() {
  confirming.value = false
  submission.submit(destinationHost.value, { mode: 'restore', overwrite: overwrite.value })
}

function statusesFor(entry) {
  return submission.entryStatus[entryKey(entry)] || []
}

function jobState(jobId) {
  return jobs.list.find((j) => j.job_id === jobId)?.state
}

function badgeVariant(status) {
  if (status.status === 'error') return 'bad'
  if (status.status === 'submitting') return 'neutral'
  const state = jobState(status.jobId)
  if (state === 'success') return 'ok'
  if (state === 'failure') return 'bad'
  return 'neutral'
}
</script>

<template>
  <div>
    <PageHeader title="Restore" :crumbs="[{ label: 'Restore' }]" />
    <StatusMessage :empty="restoreCart.entries.length === 0" empty-text="No files selected for restore yet.">
      <p data-test="cart-summary" class="mb-2">
        {{ restoreCart.entries.length }} item{{ restoreCart.entries.length === 1 ? '' : 's' }} selected, {{ formatBytes(totalSize) }}
      </p>
      <table data-test="restore-table" class="w-full text-left">
        <thead>
          <tr>
            <th class="font-medium">Source Host</th>
            <th class="font-medium">Source Path</th>
            <th class="font-medium">Captured</th>
            <th class="font-medium">Destination Path</th>
            <th class="font-medium">Size</th>
            <th class="font-medium">Status</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="entry in restoreCart.entries" :key="entryKey(entry)" :data-test="`restore-row-${entryKey(entry)}`">
            <td>{{ entry.host ?? '—' }}</td>
            <td>{{ sourcePathLabel(entry) }}</td>
            <td>
              <button
                type="button"
                :data-test="`captured-${entryKey(entry)}`"
                class="cursor-pointer text-blue-600 hover:underline"
                @click="openVersions(entry)"
              >
                {{ isPinned(entry) ? formatTimestamp(entry.notBefore) : 'Latest' }}
              </button>
            </td>
            <td>
              <input
                v-if="editingKey === entryKey(entry)"
                :ref="(el) => (editingInput = el)"
                :data-test="`dest-path-input-${entryKey(entry)}`"
                :value="entry.destPath"
                @blur="commitEdit(entry, $event.target.value)"
                @keyup.enter="commitEdit(entry, $event.target.value)"
              />
              <template v-else>
                <span :data-test="`dest-path-text-${entryKey(entry)}`">{{ entry.destPath }}</span>
                <button
                  type="button"
                  :data-test="`edit-dest-path-${entryKey(entry)}`"
                  aria-label="Edit destination path"
                  @click="startEditing(entry)"
                >
                  ✏️
                </button>
              </template>
            </td>
            <td>{{ formatBytes(entry.size) }}</td>
            <td :data-test="`status-${entryKey(entry)}`">
              <Badge v-for="(status, i) in statusesFor(entry)" :key="i" :variant="badgeVariant(status)">
                <router-link v-if="status.jobId" :to="{ name: 'job-detail', params: { job_id: status.jobId } }">
                  {{ status.status === 'submitting' ? 'submitting…' : jobState(status.jobId) || 'submitted' }}
                </router-link>
                <span v-else>{{ status.status === 'submitting' ? 'submitting…' : status.message }}</span>
              </Badge>
            </td>
            <td>
              <button type="button" :data-test="`remove-${entryKey(entry)}`" @click="remove(entry)">Remove</button>
            </td>
          </tr>
        </tbody>
      </table>
      <BaseField label="Destination host">
        <BaseSelect data-test="destination-select" v-model="destinationHost">
          <option value="" disabled>Select a destination host</option>
          <option v-for="client in clients.list" :key="client.hostname" :value="client.hostname">
            {{ client.hostname }}
          </option>
        </BaseSelect>
      </BaseField>
      <p v-if="destinationStale" data-test="destination-stale-warning" class="text-amber-700">
        This host hasn't checked in recently — the restore will wait until it comes online.
      </p>
      <label class="flex items-center gap-2">
        <input type="checkbox" data-test="overwrite-checkbox" v-model="overwrite" />
        Overwrite existing files
      </label>
      <p class="text-sm text-gray-600">
        Replaces files that already exist at the destination. Unchecked: existing files are left
        alone and skipped.
      </p>
      <BaseButton data-test="verify-button" variant="secondary" :disabled="!canSubmit" @click="verify">
        Verify
      </BaseButton>
      <p class="text-sm text-gray-600">Verify checks integrity only — writes nothing.</p>
      <BaseButton data-test="restore-button" variant="primary" :disabled="!canSubmit" @click="openConfirm">
        Restore
      </BaseButton>
      <p class="text-sm text-gray-600">Restore writes files to the destination.</p>
    </StatusMessage>
    <p v-if="submission.error" data-test="submission-error">{{ submission.error }}</p>
    <VersionsModal
      v-if="versionsFor"
      :path="versionsFor.path"
      :source-host="versionsFor.host"
      @close="versionsFor = null"
      @select-version="selectVersion"
      @use-latest="useLatestVersion"
    />
    <RestoreConfirmModal
      v-if="confirming"
      :file-count="restoreCart.entries.length"
      :total-size="totalSize"
      :destination-host="destinationHost"
      :overwrite="overwrite"
      :pinned-count="pinnedCount"
      @confirm="confirmRestore"
      @cancel="confirming = false"
    />
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/views/RestoreView.spec.js`
Expected: PASS, all tests.

- [ ] **Step 5: Commit**

```bash
cd web && git add src/views/RestoreView.vue src/views/RestoreView.spec.js
git commit -m "feat(web): restructure the restore cart page for clarity"
```

---

## Task 8: `JobDetailView` — human-readable summary banner for restore/verify jobs

**Files:**
- Modify: `web/src/views/JobDetailView.vue`
- Modify: `web/src/views/JobDetailView.spec.js`

**Interfaces:**
- Consumes: `parseLogLine` from `web/src/utils/logLine.js` (already imports `logKey` from the same
  module); `formatBytes` from `web/src/utils/format.js`.
- Produces: nothing new consumed elsewhere.

- [ ] **Step 1: Write the failing tests**

In `web/src/views/JobDetailView.spec.js`, the route mock is currently a fixed
`{ params: { job_id: 'backup:nightly:1752400000' } }`. Replace the mock at the top of the file so
individual tests can vary it:

```js
const { getJobId, setJobId } = vi.hoisted(() => {
  let jobId = 'backup:nightly:1752400000'
  return { getJobId: () => jobId, setJobId: (v) => { jobId = v } }
})

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { job_id: getJobId() } }),
}))
```

(replaces the existing `vi.mock('vue-router', ...)` block)

Add, inside the `beforeEach`, right after `vi.stubGlobal('IntersectionObserver', ...)`:

```js
  setJobId('backup:nightly:1752400000')
```

Add a new `describe` block at the end, inside the outer `describe('JobDetailView', ...)`:

```js
  describe('restore/verify summary banner', () => {
    function logLine(binary, fields) {
      return { timestamp: 1, hostname: 'h', binary, line: JSON.stringify(fields) }
    }

    it('renders no banner for a non-restore/verify job', () => {
      setJobId('backup:nightly:1752400000')
      const { wrapper, jobs } = mountView({ logs: [] })
      expect(wrapper.find('[data-test="restore-summary-banner"]').exists()).toBe(false)
    })

    it('shows an in-progress banner when no finish line has arrived yet', () => {
      setJobId('restore:x:1')
      const { wrapper } = mountView({ logs: [] })
      expect(wrapper.get('[data-test="restore-summary-banner"]').text()).toContain('in progress')
    })

    it('shows a success banner with file counts for a finished restore job', () => {
      setJobId('restore:x:1')
      const { wrapper } = mountView({
        logs: [
          logLine('rwfs', { msg: 'restore complete', files_written: 12, bytes_written: 4400000000, skipped: 0 }),
          logLine('agent', { msg: 'policy execution completed', event: 'finish', status: 'success' }),
        ],
      })
      const text = wrapper.get('[data-test="restore-summary-banner"]').text()
      expect(text).toContain('12 files written')
      expect(text).toContain('0 skipped')
    })

    it('shows a failure banner without counts for a failed restore job', () => {
      setJobId('restore:x:1')
      const { wrapper } = mountView({
        logs: [logLine('agent', { msg: 'policy execution completed', event: 'finish', status: 'failure' })],
      })
      expect(wrapper.get('[data-test="restore-summary-banner"]').text()).toContain('Restore failed')
    })

    it('shows a success banner with verified/warnings counts for a finished verify job', () => {
      setJobId('verify:x:1')
      const { wrapper } = mountView({
        logs: [
          logLine('rwfs', { msg: 'summary', verified: 8, warnings: 1 }),
          logLine('agent', { msg: 'policy execution completed', event: 'finish', status: 'success' }),
        ],
      })
      const text = wrapper.get('[data-test="restore-summary-banner"]').text()
      expect(text).toContain('8 files verified')
      expect(text).toContain('1 warning')
    })
  })
```

`mountView` here reuses this file's existing top-of-file helper — check it accepts a `logs` array
in its `initialState.jobs` (it already does, per the existing tests in this file).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/views/JobDetailView.spec.js`
Expected: FAIL — `[data-test="restore-summary-banner"]` doesn't exist, and the route mock isn't
configurable yet (compile/runtime error until the mock replacement lands).

- [ ] **Step 3: Implement**

In `web/src/views/JobDetailView.vue`, update the import line:

```js
import { logKey, parseLogLine } from '../utils/logLine'
```

Add, after `import { logKey } from '../utils/logLine'` is replaced above, and after the existing
`import LogLine from '../components/LogLine.vue'` line, add:

```js
import { formatBytes } from '../utils/format'
```

Add these computeds after `const jobId = computed(() => route.params.job_id)`:

```js
const kind = computed(() => {
  if (jobId.value.startsWith('restore:')) return 'restore'
  if (jobId.value.startsWith('verify:')) return 'verify'
  return null
})

// Scans newest-to-oldest for the last line whose parsed `msg` matches, and
// picks the requested fields off it. Returns null if no such line has
// arrived yet (in-progress, or the job predates this feature).
function lastMatchingFields(msg, fields) {
  for (let i = jobs.logs.length - 1; i >= 0; i--) {
    const parsed = parseLogLine(jobs.logs[i].line)
    if (parsed.ok && parsed.message === msg) {
      return Object.fromEntries(fields.map((f) => [f, parsed.fields[f]]))
    }
  }
  return null
}

// agent's own event=finish line is the authoritative terminal status (see
// docs/components/agent.md's "Logging and correlation") -- rwfs's own
// "restore complete"/"summary" line only ever carries the human-readable
// counts, and is absent entirely on a hard failure (e.g. the directory
// structure phase aborting on its first error, per rwfs's restore docs).
const finishStatus = computed(() => lastMatchingFields('policy execution completed', ['status'])?.status ?? null)

const summary = computed(() => {
  if (kind.value === 'restore') return lastMatchingFields('restore complete', ['files_written', 'bytes_written', 'skipped'])
  if (kind.value === 'verify') return lastMatchingFields('summary', ['verified', 'warnings'])
  return null
})
```

Add this banner in the template, right after the closing `</PageHeader>` tag and before the
`<div v-if="jobs.hasOlderLogs" ...>` block:

```html
<div v-if="kind" data-test="restore-summary-banner" class="mb-4 rounded border px-3 py-2 text-sm">
  <span v-if="!finishStatus">⏳ {{ kind === 'restore' ? 'Restore' : 'Verify' }} in progress.</span>
  <span v-else-if="finishStatus === 'failure'">❌ {{ kind === 'restore' ? 'Restore' : 'Verify' }} failed — see log below.</span>
  <span v-else-if="kind === 'restore' && summary">
    ✅ Restore complete — {{ summary.files_written }} file{{ summary.files_written === 1 ? '' : 's' }} written
    ({{ formatBytes(summary.bytes_written) }}), {{ summary.skipped }} skipped.
  </span>
  <span v-else-if="kind === 'verify' && summary">
    ✅ Verify complete — {{ summary.verified }} file{{ summary.verified === 1 ? '' : 's' }} verified, {{ summary.warnings }} warning{{ summary.warnings === 1 ? '' : 's' }}.
  </span>
  <span v-else>✅ {{ kind === 'restore' ? 'Restore' : 'Verify' }} complete.</span>
</div>
```

(`'policy execution completed'` is `logExecCompletion`'s real message text, confirmed against
`src/cmd/agent/reconcile.go:196-216` — every `event: "finish"` line agent emits uses this exact
`msg`, regardless of success/failure/exit-code branch.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/views/JobDetailView.spec.js`
Expected: PASS, all tests.

- [ ] **Step 5: Commit**

```bash
cd web && git add src/views/JobDetailView.vue src/views/JobDetailView.spec.js
git commit -m "feat(web): show a human-readable summary banner for restore/verify jobs"
```

---

## Task 9: e2e coverage, docs, and changelog

**Files:**
- Modify: `web/e2e/restore-cart.spec.js`
- Modify: `web/e2e/restore-content.spec.js`
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: everything from Tasks 1–8 running against a real backend (per `web/e2e/helpers`).
- Produces: nothing further consumed by other tasks — this is the plan's last task.

Two things need fixing, discovered by actually reading both files (not assumed): first,
`restore-content.spec.js` currently reads `page.getByTestId('submission-results')` and regexes
`"Started restore policy (\S+) from"` out of its text (lines ~125-128) to find the policy it just
created — that element is removed entirely by Task 7 (no more flat results list), so this test is
now broken independently of anything version-related, and needs fixing regardless of whether
version-picking coverage is added at all. Second, `restore-cart.spec.js`'s fixture
(`seedRestoreCartCatalogData`, `web/e2e/helpers/policySeeding.js`) only ever backs up
`/var/lib/dbdata/{dump.sql,schema.sql}` once — but calling it can be repeated: each ad-hoc backup
run creates a fresh `FileVersionRecord` row per `(JobID, ObjectID)`
(`storage/filesystem/models.go:29-39`) regardless of whether the file's content changed, so running
the same seeding flow twice against the same file genuinely produces two distinct, pickable
versions — sufficient to exercise the picker's UI mechanics (does it list both, does picking the
older one pin the right timestamp) even without differing content. Verifying that a *different*
version's *different content* is what actually lands on disk stays out of this task — it would need
new fixture-mutation infrastructure in the demo stack that doesn't exist yet, and guessing at exact
`docker exec` commands for that without verifying them against the live stack risks leaving behind
plausible-looking but wrong test code; flagged below as a deliberate follow-up instead.

- [ ] **Step 1: Fix `restore-content.spec.js`'s now-broken results lookup**

Replace lines ~123-134 of `web/e2e/restore-content.spec.js` (from `await
page.getByTestId('restore-button').click()` through the `trackPolicy(restorePolicy.id)` line) with:

```js
    await page.getByTestId('restore-button').click()
    await page.getByTestId('confirm-restore').click() // Task 6's pre-restore confirmation modal

    const statusLink = page.getByTestId(`status-${entryKey}`).getByRole('link')
    await expect(statusLink).toBeVisible({ timeout: 30_000 })
    const href = await statusLink.getAttribute('href') // "/jobs/<job_id>", from RestoreView's router-link
    const jobId = href.replace(/^\/jobs\//, '')

    const restorePoliciesResp = await page.request.get('/api/v1/policies?type=restore', { headers: AUTH_HEADERS })
    const { data: restorePolicies } = await restorePoliciesResp.json()
    const restorePolicy = restorePolicies.find((p) => p.job_id === jobId)
    expect(restorePolicy).toBeTruthy()
    trackPolicy(restorePolicy.id)
```

(`entryKey` is already defined two lines above the block being replaced — `` const entryKey =
`:${FIXTURE_SRC_DIR}` `` — reuse it, don't redeclare it.)

- [ ] **Step 2: Run the fixed test in isolation**

Run: `cd web && npm run test:e2e -- restore-content`
Expected: PASS. Requires the full local stack up (see `demo/README.md`).

- [ ] **Step 3: Extend `restore-cart.spec.js` with a real multi-version picking scenario**

Add a new `test` in `web/e2e/restore-cart.spec.js`, in the same
`test.describe.configure({ mode: 'serial' })` block, after the existing `restore cart selection`
test:

```js
test('picking an older version pins the cart entry to it instead of "Latest"', async ({ page, context, trackPolicy }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  // Two separate ad-hoc backup runs of the same fixture file, each its own
  // FileVersionRecord row (storage/filesystem/models.go) regardless of
  // content -- genuinely two distinct, pickable versions.
  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page, trackPolicy)
  await new Promise((resolve) => setTimeout(resolve, 1000)) // ensure a distinct store_created_at from the second run
  await seedRestoreCartCatalogData(page, trackPolicy)
  const [firstFile] = files
  const filePath = `${dirPath}/${firstFile}`

  const segments = dirPath.split('/').filter(Boolean)
  await page.getByRole('link', { name: 'Catalog' }).click()
  await page.getByTestId('crumb-home').click()
  await page.getByText('//', { exact: true }).click()
  for (const segment of segments) {
    await page.getByText(`${segment}/`, { exact: true }).click()
  }

  await page.getByTestId(`captured-${sourceHost}:${filePath}`).click()
  const rows = page.locator('tbody tr')
  await expect(rows).toHaveCount(2)
  const olderRow = rows.nth(1) // newest-first, so index 1 is the older of the two
  const olderTimestampText = await olderRow.locator('td').first().innerText()
  await olderRow.getByRole('button', { name: 'Restore this version' }).click()

  await page.getByRole('link', { name: 'Restore' }).click()
  const capturedCell = page.getByTestId(`captured-${sourceHost}:${filePath}`)
  await expect(capturedCell).not.toHaveText('Latest')
  await expect(capturedCell).toHaveText(olderTimestampText)
})
```

Add `seedRestoreCartCatalogData` to this file's existing import from `./helpers/policySeeding.js` if
it isn't already imported (it is, per the existing test in this file).

- [ ] **Step 4: Run the new scenario and the full restore e2e suite**

Run: `cd web && npm run test:e2e -- restore-cart restore-content restore-verify`
Expected: PASS. `restore-verify.spec.js` is included because it also submits through `RestoreView`
and is worth a quick confirmation it still passes post-Task-7, even though this plan didn't
specifically target it.

- [ ] **Step 5: Update documentation**

In `docs/components/web.md`, find the section describing the restore cart/submission flow (grep for
"restore" in that file first) and update it to describe: the flat cart with no storage-host
grouping, the pre-restore confirmation modal, per-row live status instead of a results list, the
version picker's filter-window default and multi-host note, and the job-detail summary banner. Keep
the edit scoped to what actually changed — don't rewrite unrelated sections.

Add an entry to `CHANGELOG.md` (top of file, most-recent-first, matching the existing entries'
style — a dated heading and a short paragraph, not a file list):

```markdown
## 2026-08-27 — Restore workflow UI clarity

Reworked the restore cart and submission flow for clarity: the cart no longer groups or reveals
which storage host anything lives on, Restore now requires confirming a summary before it fires,
each selected item shows its own live submission status linked to its job instead of a flat unlined
results list, and the job detail view shows a human-readable outcome banner for restore/verify jobs
instead of only a raw log tail. Selecting a file or folder now defaults to the latest version within
the catalog's current date filter (previously an unbounded true-latest), and a specific historical
version -- of a file or a folder -- can be picked directly from the catalog or cart via a shared,
now-interactive version picker. See
`docs/superpowers/specs/2026-08-27-restore-ui-clarity-design.md`.
```

- [ ] **Step 6: Run the full test suite one last time**

Run: `cd web && npm run test && npm run test:e2e`
Expected: PASS, everything.

- [ ] **Step 7: Commit**

```bash
git add web/e2e/restore-cart.spec.js web/e2e/restore-content.spec.js docs/components/web.md CHANGELOG.md
git commit -m "test(e2e): cover version picking in the restore flow; docs: restore UI clarity"
```

## Deliberate Follow-up (not in this plan)

**End-to-end proof that picking an older version restores that version's actual (different)
content**, not just that the picker's UI mechanics work. Task 9 covers the latter with a real,
verified e2e scenario (two versions of the fixture file, same content, picking pins the right
timestamp). Covering the former needs the demo stack's fixture-mutation story worked out first —
writing new content into `restore-content.spec.js`'s `FIXTURE_SRC_DIR` between two backup runs and
forcing an out-of-schedule second backup — which doesn't exist today and wasn't verified against the
live stack while writing this plan. Since `rwfs`/`bwfs`'s `not_before`/`not_after` resolution itself
is unchanged and already covered by `src/cmd/rwfs`'s and `src/cmd/bwfs`'s own Go tests (see
`docs/protocols/list.md#resolverestorefiles` and `docs/protocols/restore.md`), this is a coverage
nice-to-have, not a correctness gap in what this plan ships.
