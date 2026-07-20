# Web Frontend Consistency & Best-Practices Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring `web` (Vue 3.5 + Pinia + vue-router 4 + Tailwind 4) onto a shared UI layer, replace `simple-datatables` with `vue-good-table-next` (fixing a real sort/click row-correlation bug in `CatalogView`), DRY out repeated store boilerplate via a `withRequest` helper, and switch routing to lazy-loaded, named routes.

**Architecture:** A new `components/ui/` directory holds five small, single-purpose components (`BaseButton`, `PageHeader`, `StatusMessage`, `DetailList`, `RepeatableFieldList`) plus a `DataTable` wrapper around `vue-good-table-next`. Every existing view/component is refactored to consume these instead of hand-rolling the same markup. Stores gain one shared `withRequest` helper but stay Options-style Pinia stores. `router.js` switches every route to a lazy `component: () => import(...)` and a `name`; every internal link/`router.push` switches from a string path to `{ name, params }`.

**Tech Stack:** Vue 3 `<script setup>`, Pinia (Options-style stores), vue-router 4 (named, lazy routes), Tailwind 4, `vue-good-table-next` (replacing `simple-datatables`), Vitest + `@vue/test-utils` (+ `RouterLinkStub`) + `@pinia/testing`.

## Global Constraints

- No backend/API changes — every REST endpoint, request, and response shape is unchanged.
- No new pages or new data-fetching capability — this is a consistency/best-practices pass, not a feature.
- Pinia stores stay Options-style (`defineStore({ state, actions })`) — no conversion to setup-stores.
- No `BaseInput`/form-control abstraction — labels and `<input>` stay plain Tailwind-styled native elements. `BaseButton` is only for `<button>` elements; navigation links (`router-link`) keep their own inline classes, matching today's "New X" links.
- `vue-good-table-next` is used client-side only — no server-side pagination/grouping/virtual scroll.
- Minimal visual refresh: keep the existing neutral/blue Tailwind palette, add a `danger` (red) `BaseButton` variant, standardize spacing/hover/pagination-size via the shared components. No new color palette, no dark mode, no layout restructuring.
- `docs/components/web.md` and `CHANGELOG.md` are updated before this branch merges to `main`, per this repo's `.claude/CLAUDE.md`.
- `vue-good-table-next` version: `^0.2.2` (latest on npm as of this plan). Install/registration is a **local component import** (`import { VueGoodTable } from 'vue-good-table-next'`), not the global Vue plugin — matches this app's existing pattern of importing what each `.vue` file needs rather than globally registering libraries.
- `@vue/test-utils` (already a dependency) exports `RouterLinkStub` — a stub `<router-link>` that renders `<a><slot/></a>` and exposes the `to` prop via `.props('to')`. Every spec that currently stubs `RouterLink` with a hand-written `<a :href="to">` template switches to `RouterLinkStub` and asserts on `.props('to')` instead of `href`, since named-route `:to` objects don't resolve to a string href in a stub.
- vue-good-table-next DOM facts (confirmed by rendering it in this repo's Docker/Vitest setup before writing this plan — see each task for the selectors that depend on them): rows render as plain `<table id="vgt-table" class="vgt-table bordered"><thead><tr><th>...</th></tr></thead><tbody><tr><td>...</td></tr></tbody></table>`; the search box (when `searchOptions.enabled`) renders as `<input class="vgt-input" placeholder="Search Table">`; **sorting a column requires clicking the `<button>` nested inside the `<th>`, not the `<th>` itself** — clicking the bare `<th>` does nothing; `@row-click` fires with `{ row, pageIndex, selected, event }` where `row` is the exact original row object (mutated in place with extra `vgt_id`/`originalIndex`/`vgtSelected` tracking fields, harmless to ignore); the `table-row` scoped slot exposes `{ column, row, formattedRow, index }`, where `formattedRow[column.field]` is the value after that column's `formatFn` has already run.

---

## Task 1: `withRequest` store helper

**Files:**
- Create: `web/src/stores/helpers.js`
- Test: `web/src/stores/helpers.spec.js`

**Interfaces:**
- Produces: `withRequest(store, fn, { rethrow = true, loadingKey = 'loading', errorKey = 'error' } = {})` — sets `store[loadingKey] = true` and `store[errorKey] = null`, awaits `fn()`, on failure sets `store[errorKey] = err.message` and rethrows unless `rethrow: false`, always sets `store[loadingKey] = false` in a `finally`. Returns whatever `fn()` resolved to. Every store task below (Task 2) consumes this exact signature.

- [ ] **Step 1: Write the failing tests**

```js
// web/src/stores/helpers.spec.js
import { describe, it, expect } from 'vitest'
import { withRequest } from './helpers'

function fakeStore() {
  return { loading: false, error: null, logsLoading: false, logsError: null }
}

describe('withRequest', () => {
  it('sets loading true during the call and false after success, clearing error, and returns the result', async () => {
    const store = fakeStore()
    let loadingDuringCall
    const result = await withRequest(store, async () => {
      loadingDuringCall = store.loading
      return 'ok'
    })
    expect(loadingDuringCall).toBe(true)
    expect(store.loading).toBe(false)
    expect(store.error).toBeNull()
    expect(result).toBe('ok')
  })

  it('records the error message and rethrows by default on failure', async () => {
    const store = fakeStore()
    await expect(withRequest(store, () => Promise.reject(new Error('boom')))).rejects.toThrow('boom')
    expect(store.error).toBe('boom')
    expect(store.loading).toBe(false)
  })

  it('records the error but swallows it when rethrow is false', async () => {
    const store = fakeStore()
    await expect(
      withRequest(store, () => Promise.reject(new Error('boom')), { rethrow: false })
    ).resolves.toBeUndefined()
    expect(store.error).toBe('boom')
  })

  it('uses a custom loadingKey/errorKey pair instead of the defaults', async () => {
    const store = fakeStore()
    await withRequest(store, () => Promise.reject(new Error('boom')), {
      rethrow: false,
      loadingKey: 'logsLoading',
      errorKey: 'logsError',
    })
    expect(store.logsError).toBe('boom')
    expect(store.loading).toBe(false)
    expect(store.error).toBeNull()
  })

  it('clears a stale error at the start of a new call even before it resolves', () => {
    const store = fakeStore()
    store.error = 'stale error from an earlier action'
    withRequest(store, () => new Promise(() => {}))
    expect(store.error).toBeNull()
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/stores/helpers.spec.js`
Expected: FAIL — `Failed to resolve import "./helpers"`

- [ ] **Step 3: Implement `withRequest`**

```js
// web/src/stores/helpers.js
export async function withRequest(store, fn, { rethrow = true, loadingKey = 'loading', errorKey = 'error' } = {}) {
  store[loadingKey] = true
  store[errorKey] = null
  try {
    return await fn()
  } catch (err) {
    store[errorKey] = err.message
    if (rethrow) throw err
  } finally {
    store[loadingKey] = false
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/stores/helpers.spec.js`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/helpers.js web/src/stores/helpers.spec.js
git commit -m "feat(web): add withRequest store helper"
```

---

## Task 2: Refactor all four data-fetching stores onto `withRequest`

**Files:**
- Modify: `web/src/stores/clients.js`, `web/src/stores/jobs.js`, `web/src/stores/policies.js`, `web/src/stores/catalog.js`
- Test: no changes to `clients.spec.js`, `jobs.spec.js`, `policies.spec.js`, `catalog.spec.js` — they are the safety net for this refactor. `auth.js` is untouched (it has no async/loading/error pattern to DRY).

**Interfaces:**
- Consumes: `withRequest` from Task 1.
- Produces: same public store API as today (same action names, params, return values, state shape) — this is a pure internal refactor.

This is a refactor-under-green-tests task, not new-behavior TDD: the existing spec files already pin every store action's request shape, success state, and error/rethrow behavior. Run them first to confirm the baseline, refactor, then rerun to confirm nothing changed.

- [ ] **Step 1: Run the existing store tests to confirm the baseline passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/stores/clients.spec.js src/stores/jobs.spec.js src/stores/policies.spec.js src/stores/catalog.spec.js`
Expected: PASS (all existing tests, unmodified)

- [ ] **Step 2: Refactor `clients.js`**

```js
// web/src/stores/clients.js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

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
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/clients')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
    async fetchOne(hostname) {
      if (this.byHostname[hostname]) {
        this.error = null
        return this.byHostname[hostname]
      }
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}`)
        this.byHostname[hostname] = client
        return client
      })
    },
    async enroll(hostname, sans) {
      return withRequest(this, async () => {
        const result = await apiFetch('/clients', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hostname, sans }),
        })
        this.list.push({ hostname: result.hostname, revoked: false, revoked_at: 0, last_seen_at: 0 })
        this.pendingToken = { hostname: result.hostname, token: result.token }
        return result
      })
    },
    async reenroll(hostname, sans) {
      return withRequest(this, async () => {
        const result = await apiFetch(`/clients/${encodeURIComponent(hostname)}/reenroll`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sans }),
        })
        this.pendingToken = { hostname: result.hostname, token: result.token }
        return result
      })
    },
    async revoke(hostname) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/revoke`, { method: 'POST' })
        this.updateCache(client)
        return client
      })
    },
    async unrevoke(hostname) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/unrevoke`, { method: 'POST' })
        this.updateCache(client)
        return client
      })
    },
    async updateDescription(hostname, set, unset) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/description`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ set, unset }),
        })
        this.updateCache(client)
        return client
      })
    },
    async updateAttributes(hostname, set, unset) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/attributes`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ set, unset }),
        })
        this.updateCache(client)
        return client
      })
    },
    async updateSans(hostname, add, remove) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/sans`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ add, remove }),
        })
        this.updateCache(client)
        return client
      })
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

- [ ] **Step 3: Refactor `jobs.js`**

```js
// web/src/stores/jobs.js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

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
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/jobs')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
    async fetchLogs(jobId) {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs`)
          this.logs = body.data ?? []
        },
        { rethrow: false, loadingKey: 'logsLoading', errorKey: 'logsError' }
      )
    },
  },
})
```

- [ ] **Step 4: Refactor `policies.js`**

```js
// web/src/stores/policies.js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

export const usePoliciesStore = defineStore('policies', {
  state: () => ({
    list: [],
    byId: {},
    loading: false,
    error: null,
  }),
  actions: {
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/policies')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
    async fetchOne(id) {
      if (this.byId[id]) {
        this.error = null
        return this.byId[id]
      }
      return withRequest(this, async () => {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
        this.byId[id] = policy
        return policy
      })
    },
    async create(input) {
      return withRequest(this, async () => {
        const policy = await apiFetch('/policies', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        this.list.push(policy)
        this.byId[policy.id] = policy
        return policy
      })
    },
    async update(id, input) {
      return withRequest(this, async () => {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        const idx = this.list.findIndex((p) => p.id === id)
        if (idx !== -1) this.list[idx] = policy
        this.byId[id] = policy
        return policy
      })
    },
    async remove(id) {
      return withRequest(this, async () => {
        await apiFetch(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' })
        this.list = this.list.filter((p) => p.id !== id)
        delete this.byId[id]
      })
    },
  },
})
```

- [ ] **Step 5: Refactor `catalog.js`**

`catalog.search`'s original `catch` block does one thing `withRequest`'s generic catch doesn't know about: it unconditionally resets `this.entries = []` on failure, discarding whatever a previous successful search had loaded, rather than leaving stale results on screen. `withRequest` alone can't express that side effect, so `search` keeps its own thin `try/catch` around a `withRequest(..., { rethrow: true })` call (the default) purely to add that reset, then swallows the rethrow itself (matching the original, since no caller of `search()` awaits it in a try/catch — `CatalogView.submit` calls `await catalog.search(...)` directly).

```js
// web/src/stores/catalog.js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

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
      try {
        await withRequest(this, async () => {
          const collected = []
          let startingAfter
          for (;;) {
            const qs = buildQuery(this.filters, startingAfter, MAX_PAGE_LIMIT)
            const body = await apiFetch(`/catalog?${qs}`)
            collected.push(...body.data)
            if (!body.has_more || body.data.length === 0) break
            startingAfter = body.data[body.data.length - 1].id
          }
          this.entries = collected
        })
      } catch {
        // withRequest already recorded this.error; discard any partial or
        // stale results rather than leaving a previous search's rows on screen.
        this.entries = []
      }
    },
  },
})
```

- [ ] **Step 6: Rerun the existing store tests to confirm they still pass unchanged**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/stores/clients.spec.js src/stores/jobs.spec.js src/stores/policies.spec.js src/stores/catalog.spec.js`
Expected: PASS (all existing tests, identical assertions to Step 1)

- [ ] **Step 7: Commit**

```bash
git add web/src/stores/clients.js web/src/stores/jobs.js web/src/stores/policies.js web/src/stores/catalog.js
git commit -m "refactor(web): collapse store loading/error boilerplate onto withRequest"
```

---

## Task 3: `BaseButton`

**Files:**
- Create: `web/src/components/ui/BaseButton.vue`
- Test: `web/src/components/ui/BaseButton.spec.js`

**Interfaces:**
- Produces: `<BaseButton variant="primary|secondary|danger" type="button|submit">` (variant defaults `'secondary'`, type defaults `'button'`). All non-declared attributes (`disabled`, `data-test`, `class`, `@click`) fall through onto the root `<button>` via Vue's default attribute inheritance. Consumed by every other task from Task 9 onward.

- [ ] **Step 1: Write the failing tests**

```js
// web/src/components/ui/BaseButton.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BaseButton from './BaseButton.vue'

describe('BaseButton', () => {
  it('renders slot content and defaults to type=button with secondary styling', () => {
    const wrapper = mount(BaseButton, { slots: { default: 'Click me' } })
    expect(wrapper.text()).toBe('Click me')
    expect(wrapper.attributes('type')).toBe('button')
    expect(wrapper.classes()).toContain('border')
  })

  it('applies primary variant classes', () => {
    const wrapper = mount(BaseButton, { props: { variant: 'primary' } })
    expect(wrapper.classes()).toContain('bg-blue-600')
  })

  it('applies danger variant classes', () => {
    const wrapper = mount(BaseButton, { props: { variant: 'danger' } })
    expect(wrapper.classes()).toContain('text-red-600')
  })

  it('passes through type and disabled as native attributes', () => {
    const wrapper = mount(BaseButton, { props: { type: 'submit' }, attrs: { disabled: true } })
    expect(wrapper.attributes('type')).toBe('submit')
    expect(wrapper.attributes('disabled')).toBeDefined()
  })

  it('passes through data-test and additional classes', () => {
    const wrapper = mount(BaseButton, { attrs: { 'data-test': 'revoke-button', class: 'mt-2' } })
    expect(wrapper.attributes('data-test')).toBe('revoke-button')
    expect(wrapper.classes()).toContain('mt-2')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/BaseButton.spec.js`
Expected: FAIL — `Failed to resolve import "./BaseButton.vue"`

- [ ] **Step 3: Implement `BaseButton.vue`**

```vue
<!-- web/src/components/ui/BaseButton.vue -->
<script setup>
defineProps({
  variant: { type: String, default: 'secondary' },
  type: { type: String, default: 'button' },
})

const VARIANT_CLASSES = {
  primary: 'bg-blue-600 text-white hover:bg-blue-700',
  secondary: 'border border-gray-300 hover:bg-gray-50',
  danger: 'border border-red-300 text-red-600 hover:bg-red-50',
}
</script>

<template>
  <button :type="type" class="rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed" :class="VARIANT_CLASSES[variant]">
    <slot />
  </button>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/BaseButton.spec.js`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/BaseButton.vue web/src/components/ui/BaseButton.spec.js
git commit -m "feat(web): add BaseButton"
```

---

## Task 4: `PageHeader`

**Files:**
- Create: `web/src/components/ui/PageHeader.vue`
- Test: `web/src/components/ui/PageHeader.spec.js`

**Interfaces:**
- Produces: `<PageHeader title="...">` with a default slot (body content below the header row) and a named `actions` slot (right-aligned, next to the title). Renders nothing extra when `actions` isn't provided.

- [ ] **Step 1: Write the failing tests**

```js
// web/src/components/ui/PageHeader.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import PageHeader from './PageHeader.vue'

describe('PageHeader', () => {
  it('renders the title as an h1', () => {
    const wrapper = mount(PageHeader, { props: { title: 'Clients' } })
    expect(wrapper.find('h1').text()).toBe('Clients')
  })

  it('renders default slot content below the header row', () => {
    const wrapper = mount(PageHeader, {
      props: { title: 'Clients' },
      slots: { default: '<p>body</p>' },
    })
    expect(wrapper.find('p').text()).toBe('body')
  })

  it('renders the actions slot when provided', () => {
    const wrapper = mount(PageHeader, {
      props: { title: 'Clients' },
      slots: { actions: '<button>New Client</button>' },
    })
    expect(wrapper.find('button').text()).toBe('New Client')
  })

  it('does not render an actions wrapper when no actions slot is given', () => {
    const wrapper = mount(PageHeader, { props: { title: 'Clients' } })
    expect(wrapper.find('[data-test="page-header-actions"]').exists()).toBe(false)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/PageHeader.spec.js`
Expected: FAIL — `Failed to resolve import "./PageHeader.vue"`

- [ ] **Step 3: Implement `PageHeader.vue`**

```vue
<!-- web/src/components/ui/PageHeader.vue -->
<script setup>
defineProps({
  title: { type: String, required: true },
})
</script>

<template>
  <div class="flex items-center justify-between mb-4">
    <h1 class="text-xl font-semibold">{{ title }}</h1>
    <div v-if="$slots.actions" data-test="page-header-actions" class="flex gap-2">
      <slot name="actions" />
    </div>
  </div>
  <slot />
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/PageHeader.spec.js`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/PageHeader.vue web/src/components/ui/PageHeader.spec.js
git commit -m "feat(web): add PageHeader"
```

---

## Task 5: `StatusMessage`

**Files:**
- Create: `web/src/components/ui/StatusMessage.vue`
- Test: `web/src/components/ui/StatusMessage.spec.js`

**Interfaces:**
- Produces: `<StatusMessage :loading :error :empty empty-text="...">` with a default slot for the real content. Priority: `loading` > `error` > `empty` > default slot.

- [ ] **Step 1: Write the failing tests**

```js
// web/src/components/ui/StatusMessage.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StatusMessage from './StatusMessage.vue'

describe('StatusMessage', () => {
  it('shows Loading when loading is true', () => {
    const wrapper = mount(StatusMessage, { props: { loading: true, error: null, empty: false } })
    expect(wrapper.text()).toBe('Loading...')
  })

  it('shows the error message when error is set', () => {
    const wrapper = mount(StatusMessage, { props: { loading: false, error: 'boom', empty: false } })
    expect(wrapper.text()).toBe('boom')
  })

  it('shows the empty text when empty is true', () => {
    const wrapper = mount(StatusMessage, {
      props: { loading: false, error: null, empty: true, emptyText: 'No clients enrolled yet.' },
    })
    expect(wrapper.text()).toBe('No clients enrolled yet.')
  })

  it('renders the default slot when none of loading/error/empty apply', () => {
    const wrapper = mount(StatusMessage, {
      props: { loading: false, error: null, empty: false },
      slots: { default: '<table><tbody><tr><td>row</td></tr></tbody></table>' },
    })
    expect(wrapper.find('td').text()).toBe('row')
  })

  it('prioritizes loading over error and empty', () => {
    const wrapper = mount(StatusMessage, { props: { loading: true, error: 'boom', empty: true } })
    expect(wrapper.text()).toBe('Loading...')
  })

  it('prioritizes error over empty', () => {
    const wrapper = mount(StatusMessage, { props: { loading: false, error: 'boom', empty: true } })
    expect(wrapper.text()).toBe('boom')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/StatusMessage.spec.js`
Expected: FAIL — `Failed to resolve import "./StatusMessage.vue"`

- [ ] **Step 3: Implement `StatusMessage.vue`**

```vue
<!-- web/src/components/ui/StatusMessage.vue -->
<script setup>
defineProps({
  loading: { type: Boolean, default: false },
  error: { type: String, default: null },
  empty: { type: Boolean, default: false },
  emptyText: { type: String, default: 'No results.' },
})
</script>

<template>
  <p v-if="loading" class="text-gray-500">Loading...</p>
  <p v-else-if="error" class="text-red-600">{{ error }}</p>
  <p v-else-if="empty" class="text-gray-500">{{ emptyText }}</p>
  <slot v-else />
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/StatusMessage.spec.js`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/StatusMessage.vue web/src/components/ui/StatusMessage.spec.js
git commit -m "feat(web): add StatusMessage"
```

---

## Task 6: `DetailList`

**Files:**
- Create: `web/src/components/ui/DetailList.vue`
- Test: `web/src/components/ui/DetailList.spec.js`

**Interfaces:**
- Produces: `<DetailList :rows="[{ key, label, value }]">`. Each row renders as a `<dt>`/`<dd>` pair; `<dd>` content is a named slot keyed by `row.key` (falls back to `row.value` as plain text when no matching slot is given), for rows needing custom markup (e.g. a list, not a string).

- [ ] **Step 1: Write the failing tests**

```js
// web/src/components/ui/DetailList.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DetailList from './DetailList.vue'

describe('DetailList', () => {
  it('renders each row as a term/definition pair', () => {
    const wrapper = mount(DetailList, {
      props: {
        rows: [
          { key: 'rpo', label: 'RPO', value: '1h' },
          { key: 'destination', label: 'Destination', value: 'store:8080' },
        ],
      },
    })
    expect(wrapper.findAll('dt').map((t) => t.text())).toEqual(['RPO', 'Destination'])
    expect(wrapper.findAll('dd').map((d) => d.text())).toEqual(['1h', 'store:8080'])
  })

  it("renders a named slot in place of a row's plain value", () => {
    const wrapper = mount(DetailList, {
      props: { rows: [{ key: 'objectFilters', label: 'Object Filters', value: '' }] },
      slots: { objectFilters: '<ul><li>/var/lib/dbdata</li></ul>' },
    })
    expect(wrapper.find('li').text()).toBe('/var/lib/dbdata')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/DetailList.spec.js`
Expected: FAIL — `Failed to resolve import "./DetailList.vue"`

- [ ] **Step 3: Implement `DetailList.vue`**

```vue
<!-- web/src/components/ui/DetailList.vue -->
<script setup>
defineProps({
  rows: { type: Array, required: true },
})
</script>

<template>
  <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
    <template v-for="row in rows" :key="row.key">
      <dt class="font-medium">{{ row.label }}</dt>
      <dd>
        <slot :name="row.key">{{ row.value }}</slot>
      </dd>
    </template>
  </dl>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/DetailList.spec.js`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/DetailList.vue web/src/components/ui/DetailList.spec.js
git commit -m "feat(web): add DetailList"
```

---

## Task 7: `RepeatableFieldList`

**Files:**
- Create: `web/src/components/ui/RepeatableFieldList.vue`
- Test: `web/src/components/ui/RepeatableFieldList.spec.js`

**Interfaces:**
- Produces: `<RepeatableFieldList :items :new-item :add-label :remove-label :row-class :test-prefix>` with a scoped `row` slot (`{ item, index }`). `items` is a **direct reference to a reactive array the caller owns** (e.g. `form.client_filters.hostnames`, or a component's own local `draft` array) — the component mutates it in place via `.push()`/`.splice()`, it does not emit `update:modelValue`. This is a deliberate choice over full `v-model`: every current call site already holds its array inside a larger `reactive()` form (or its own local `reactive` draft), where in-place mutation is exactly what Vue 3's reactivity expects and is simpler than round-tripping a whole-array replacement through an event.

- [ ] **Step 1: Write the failing tests**

```js
// web/src/components/ui/RepeatableFieldList.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import RepeatableFieldList from './RepeatableFieldList.vue'

describe('RepeatableFieldList', () => {
  it('renders one row per item via the row slot', () => {
    const wrapper = mount(RepeatableFieldList, {
      props: { items: ['a', 'b'], addLabel: 'Add Hostname', testPrefix: 'hostname' },
      slots: {
        row: `<template #row="{ item, index }"><span :data-test="'item-' + index">{{ item }}</span></template>`,
      },
    })
    expect(wrapper.findAll('[data-test^="item-"]').map((n) => n.text())).toEqual(['a', 'b'])
  })

  it('pushes a new item from the newItem factory when Add is clicked', async () => {
    const items = []
    const wrapper = mount(RepeatableFieldList, {
      props: { items, newItem: () => 'x', addLabel: 'Add Hostname', testPrefix: 'hostname' },
    })
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    expect(items).toEqual(['x'])
  })

  it('defaults newItem to an empty string when none is provided', async () => {
    const items = []
    const wrapper = mount(RepeatableFieldList, {
      props: { items, addLabel: 'Add Hostname', testPrefix: 'hostname' },
    })
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    expect(items).toEqual([''])
  })

  it("splices an item out when its Remove button is clicked", async () => {
    const items = ['a', 'b']
    const wrapper = mount(RepeatableFieldList, {
      props: { items, addLabel: 'Add Hostname', testPrefix: 'hostname' },
      slots: { row: `<template #row="{ item }">{{ item }}</template>` },
    })
    await wrapper.findAll('[data-test="hostname-remove"]')[0].trigger('click')
    expect(items).toEqual(['b'])
  })

  it('uses a custom removeLabel when provided', () => {
    const wrapper = mount(RepeatableFieldList, {
      props: { items: ['a'], addLabel: 'Add Filter', removeLabel: 'Remove Filter', testPrefix: 'filter' },
    })
    expect(wrapper.find('[data-test="filter-remove"]').text()).toBe('Remove Filter')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/RepeatableFieldList.spec.js`
Expected: FAIL — `Failed to resolve import "./RepeatableFieldList.vue"`

- [ ] **Step 3: Implement `RepeatableFieldList.vue`**

```vue
<!-- web/src/components/ui/RepeatableFieldList.vue -->
<script setup>
const props = defineProps({
  items: { type: Array, required: true },
  newItem: { type: Function, default: () => '' },
  addLabel: { type: String, required: true },
  removeLabel: { type: String, default: 'Remove' },
  rowClass: { type: String, default: 'flex gap-2 mb-1' },
  testPrefix: { type: String, required: true },
})

function add() {
  props.items.push(props.newItem())
}
function remove(index) {
  props.items.splice(index, 1)
}
</script>

<template>
  <div>
    <div v-for="(item, index) in items" :key="index" :class="rowClass">
      <slot name="row" :item="item" :index="index" />
      <button type="button" :data-test="`${testPrefix}-remove`" class="border rounded px-2 self-start" @click="remove(index)">
        {{ removeLabel }}
      </button>
    </div>
    <button type="button" :data-test="`${testPrefix}-add`" class="border rounded px-3 py-1" @click="add">
      {{ addLabel }}
    </button>
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/RepeatableFieldList.spec.js`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/RepeatableFieldList.vue web/src/components/ui/RepeatableFieldList.spec.js
git commit -m "feat(web): add RepeatableFieldList"
```

---

## Task 8: `DataTable` (replaces `simple-datatables`)

**Files:**
- Modify: `web/package.json` (remove `simple-datatables`, add `vue-good-table-next`)
- Create: `web/src/components/ui/DataTable.vue`
- Test: `web/src/components/ui/DataTable.spec.js`

**Interfaces:**
- Produces: `<DataTable :columns :rows :search-enabled :per-page @row-click>`. `columns` is `vue-good-table-next`'s column-option array (`{ label, field, sortable, type, formatFn }`, `field` supports dot notation for nested properties). `searchEnabled` defaults `true`, `perPage` defaults `25`. Emits `row-click` with the clicked row object directly (not an index). Exposes a scoped `table-row` slot (`{ column, row, formattedRow, index }`) for callers that need custom cell markup (links, buttons); columns with no matching custom markup fall back to `formattedRow[column.field]` automatically, both when a caller doesn't use the slot at all and inside a caller's own slot content via the `v-else` branch shown in Task 10 onward.

- [ ] **Step 1: Update `package.json`**

Edit `web/package.json`'s `dependencies`: remove `"simple-datatables": "^10.2.0"`, add `"vue-good-table-next": "^0.2.2"`.

- [ ] **Step 2: Install and write the failing tests**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm install`

```js
// web/src/components/ui/DataTable.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DataTable from './DataTable.vue'

const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'Size', field: 'size', sortable: true, type: 'number', formatFn: (v) => `${v} B` },
]
const rows = [
  { id: 1, name: 'a', size: 10 },
  { id: 2, name: 'b', size: 20 },
]

describe('DataTable', () => {
  it('renders one row per item with formatted cell values', () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    const trs = wrapper.findAll('tbody tr')
    expect(trs).toHaveLength(2)
    expect(trs[0].text()).toContain('a')
    expect(trs[0].text()).toContain('10 B')
  })

  it('shows a search box by default', () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    expect(wrapper.find('input.vgt-input').exists()).toBe(true)
  })

  it('hides the search box when searchEnabled is false', () => {
    const wrapper = mount(DataTable, { props: { columns, rows, searchEnabled: false } })
    expect(wrapper.find('input.vgt-input').exists()).toBe(false)
  })

  it('emits row-click with the clicked row object, not an index', async () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    await wrapper.findAll('tbody tr')[1].trigger('click')
    expect(wrapper.emitted('row-click')).toHaveLength(1)
    expect(wrapper.emitted('row-click')[0][0]).toMatchObject({ id: 2, name: 'b' })
  })

  it('lets the caller override cell rendering via the table-row slot', () => {
    const wrapper = mount(DataTable, {
      props: { columns, rows },
      slots: {
        'table-row': `<template #table-row="{ column, row, formattedRow }">
          <a v-if="column.field === 'name'" :href="'/x/' + row.name">{{ row.name }}</a>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>`,
      },
    })
    expect(wrapper.find('a[href="/x/a"]').exists()).toBe(true)
  })
})
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/DataTable.spec.js`
Expected: FAIL — `Failed to resolve import "./DataTable.vue"`

- [ ] **Step 4: Implement `DataTable.vue`**

```vue
<!-- web/src/components/ui/DataTable.vue -->
<script setup>
import { VueGoodTable } from 'vue-good-table-next'
import 'vue-good-table-next/dist/vue-good-table-next.css'

defineProps({
  columns: { type: Array, required: true },
  rows: { type: Array, required: true },
  searchEnabled: { type: Boolean, default: true },
  perPage: { type: Number, default: 25 },
})
const emit = defineEmits(['row-click'])

function handleRowClick({ row }) {
  emit('row-click', row)
}
</script>

<template>
  <vue-good-table
    :columns="columns"
    :rows="rows"
    :search-options="{ enabled: searchEnabled, placeholder: 'Search...' }"
    :pagination-options="{ enabled: true, perPage }"
    @row-click="handleRowClick"
  >
    <template #table-row="props">
      <slot name="table-row" v-bind="props">
        <span>{{ props.formattedRow[props.column.field] }}</span>
      </slot>
    </template>
  </vue-good-table>
</template>
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/ui/DataTable.spec.js`
Expected: PASS (5 tests)

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/package-lock.json web/src/components/ui/DataTable.vue web/src/components/ui/DataTable.spec.js
git commit -m "feat(web): add DataTable wrapper around vue-good-table-next"
```

---

## Task 9: Router — named, lazy-loaded routes

**Files:**
- Modify: `web/src/router.js`
- Create: `web/src/router.spec.js`

**Interfaces:**
- Produces: every route now has a `name` (`home`, `clients`, `client-new`, `client-detail`, `catalog`, `policies`, `policy-new`, `policy-detail`, `policy-edit`, `jobs`, `job-detail`) and a lazy `component: () => import('./views/X.vue')`. Every task from Task 10 onward consumes these exact names for `:to=` bindings and `router.push` calls.

- [ ] **Step 1: Write the failing test**

```js
// web/src/router.spec.js
import { describe, it, expect } from 'vitest'
import { router } from './router'

const EXPECTED_NAMES = [
  'home',
  'clients',
  'client-new',
  'client-detail',
  'catalog',
  'policies',
  'policy-new',
  'policy-detail',
  'policy-edit',
  'jobs',
  'job-detail',
]

describe('router', () => {
  it('gives every route a unique, expected name', () => {
    const names = router.getRoutes().map((r) => r.name)
    expect(new Set(names).size).toBe(names.length)
    expect(names.sort()).toEqual([...EXPECTED_NAMES].sort())
  })

  it('lazily resolves each route to its view component', async () => {
    for (const route of router.getRoutes()) {
      const resolved = await route.components.default()
      expect(resolved.default).toBeDefined()
    }
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/router.spec.js`
Expected: FAIL — route names are `undefined` (current `router.js` has no `name` keys)

- [ ] **Step 3: Update `router.js`**

```js
// web/src/router.js
import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: () => import('./views/HomeView.vue') },
    { path: '/clients', name: 'clients', component: () => import('./views/ClientsListView.vue') },
    { path: '/clients/new', name: 'client-new', component: () => import('./views/ClientFormView.vue') },
    { path: '/clients/:hostname', name: 'client-detail', component: () => import('./views/ClientDetailView.vue') },
    { path: '/catalog', name: 'catalog', component: () => import('./views/CatalogView.vue') },
    { path: '/policies', name: 'policies', component: () => import('./views/PoliciesListView.vue') },
    { path: '/policies/new', name: 'policy-new', component: () => import('./views/PolicyFormView.vue') },
    { path: '/policies/:id', name: 'policy-detail', component: () => import('./views/PolicyDetailView.vue') },
    { path: '/policies/:id/edit', name: 'policy-edit', component: () => import('./views/PolicyFormView.vue') },
    { path: '/jobs', name: 'jobs', component: () => import('./views/JobsListView.vue') },
    { path: '/jobs/:job_id', name: 'job-detail', component: () => import('./views/JobDetailView.vue') },
  ],
})
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/router.spec.js`
Expected: PASS (2 tests)

- [ ] **Step 5: Run the full suite to confirm nothing else broke**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run`
Expected: PASS — `App.spec.js` stubs `RouterView`/`RouterLink` as `true` and doesn't touch route names, so it's unaffected; every view/component spec that mocks `vue-router` directly (`useRoute`/`useRouter`) is also unaffected since those mocks don't reference `router.js` at all. Views still using plain string `:to=`/`router.push(` (not yet migrated — Tasks 10–19) still pass, since `router.js` doesn't change what strings resolve to, only adds `name`.

- [ ] **Step 6: Commit**

```bash
git add web/src/router.js web/src/router.spec.js
git commit -m "refactor(web): lazy-load route components and name every route"
```

---

## Task 10: Migrate `ClientsListView`

**Files:**
- Modify: `web/src/views/ClientsListView.vue`
- Modify: `web/src/views/ClientsListView.spec.js`

**Interfaces:**
- Consumes: `PageHeader` (Task 4), `StatusMessage` (Task 5), `DataTable` (Task 8), named route `client-detail`/`client-new` (Task 9).

- [ ] **Step 1: Rewrite the spec against the target markup**

```js
// web/src/views/ClientsListView.spec.js
import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientsListView from './ClientsListView.vue'
import { useClientsStore } from '../stores/clients'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientsListView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
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
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'webserver')
    expect(link.props('to')).toEqual({ name: 'client-detail', params: { hostname: 'webserver' } })
  })

  it('renders Revoked as Yes/No and a Never fallback for an unset Last Seen', () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'webserver', revoked: true, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    const cells = wrapper.findAll('tbody td')
    expect(cells[1].text()).toBe('Yes')
    expect(cells[2].text()).toBe('Never')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('shows an empty-state message when there are no clients', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.text()).toContain('No clients enrolled yet.')
  })

  it('links to the enroll form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'New Client')
    expect(link.props('to')).toEqual({ name: 'client-new' })
  })
})
```

- [ ] **Step 2: Run the spec to verify it fails against the current view**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/ClientsListView.spec.js`
Expected: FAIL — old view still uses `simple-datatables`/string paths

- [ ] **Step 3: Rewrite `ClientsListView.vue`**

```vue
<!-- web/src/views/ClientsListView.vue -->
<script setup>
import { onMounted } from 'vue'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'

const clients = useClientsStore()

onMounted(() => {
  clients.fetchAll()
})

const columns = [
  { label: 'Hostname', field: 'hostname', sortable: true },
  { label: 'Revoked', field: 'revoked', sortable: true, type: 'boolean', formatFn: (v) => (v ? 'Yes' : 'No') },
  {
    label: 'Last Seen',
    field: 'last_seen_at',
    sortable: true,
    type: 'number',
    formatFn: (v) => formatTimestamp(v) || 'Never',
  },
]
</script>

<template>
  <div>
    <PageHeader title="Clients">
      <template #actions>
        <router-link :to="{ name: 'client-new' }" class="bg-blue-600 text-white rounded px-3 py-1">
          New Client
        </router-link>
      </template>
    </PageHeader>
    <StatusMessage
      :loading="clients.loading"
      :error="clients.error"
      :empty="clients.list.length === 0"
      empty-text="No clients enrolled yet."
    >
      <DataTable :columns="columns" :rows="clients.list">
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'hostname'"
            :to="{ name: 'client-detail', params: { hostname: row.hostname } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.hostname }}
          </router-link>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run the spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/ClientsListView.spec.js`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/ClientsListView.vue web/src/views/ClientsListView.spec.js
git commit -m "refactor(web): migrate ClientsListView onto DataTable and named routes"
```

---

## Task 11: Migrate `PoliciesListView`

**Files:**
- Modify: `web/src/views/PoliciesListView.vue`
- Modify: `web/src/views/PoliciesListView.spec.js`

**Interfaces:**
- Consumes: `PageHeader`, `StatusMessage`, `DataTable`, `BaseButton` (danger variant for Delete), named routes `policy-detail`/`policy-new`.

- [ ] **Step 1: Rewrite the spec against the target markup**

```js
// web/src/views/PoliciesListView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import PoliciesListView from './PoliciesListView.vue'
import { usePoliciesStore } from '../stores/policies'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(PoliciesListView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
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
      list: [{ id: 'p1', name: 'nightly-db-backup', rpo: '1h', destination: 'store:8080' }],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('nightly-db-backup')
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'nightly-db-backup')
    expect(link.props('to')).toEqual({ name: 'policy-detail', params: { id: 'p1' } })
  })

  it('links to the create form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'New Policy')
    expect(link.props('to')).toEqual({ name: 'policy-new' })
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('shows an empty-state message when there are no policies', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.text()).toContain('No policies defined yet.')
  })

  it('deletes a policy after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, policies } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup', rpo: '1h', destination: 'store:8080' }],
      loading: false,
      error: null,
    })

    await wrapper.find('[data-test="policy-delete"]').trigger('click')

    expect(policies.remove).toHaveBeenCalledWith('p1')
  })

  it('does not delete when the confirm dialog is dismissed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { wrapper, policies } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup', rpo: '1h', destination: 'store:8080' }],
      loading: false,
      error: null,
    })

    await wrapper.find('[data-test="policy-delete"]').trigger('click')

    expect(policies.remove).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/PoliciesListView.spec.js`
Expected: FAIL

- [ ] **Step 3: Rewrite `PoliciesListView.vue`**

```vue
<!-- web/src/views/PoliciesListView.vue -->
<script setup>
import { onMounted } from 'vue'
import { usePoliciesStore } from '../stores/policies'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'

const policies = usePoliciesStore()

onMounted(() => {
  policies.fetchAll()
})

function confirmDelete(id) {
  if (window.confirm('Delete this policy?')) {
    policies.remove(id)
  }
}

const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'RPO', field: 'rpo', sortable: true },
  { label: 'Destination', field: 'destination', sortable: true },
  { label: '', field: 'actions', sortable: false },
]
</script>

<template>
  <div>
    <PageHeader title="Policies">
      <template #actions>
        <router-link :to="{ name: 'policy-new' }" class="bg-blue-600 text-white rounded px-3 py-1">
          New Policy
        </router-link>
      </template>
    </PageHeader>
    <StatusMessage
      :loading="policies.loading"
      :error="policies.error"
      :empty="policies.list.length === 0"
      empty-text="No policies defined yet."
    >
      <DataTable :columns="columns" :rows="policies.list">
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'name'"
            :to="{ name: 'policy-detail', params: { id: row.id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.name }}
          </router-link>
          <BaseButton
            v-else-if="column.field === 'actions'"
            data-test="policy-delete"
            variant="danger"
            @click="confirmDelete(row.id)"
          >
            Delete
          </BaseButton>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run the spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/PoliciesListView.spec.js`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/PoliciesListView.vue web/src/views/PoliciesListView.spec.js
git commit -m "refactor(web): migrate PoliciesListView onto DataTable and named routes"
```

---

## Task 12: Migrate `JobsListView`

**Files:**
- Modify: `web/src/views/JobsListView.vue`
- Modify: `web/src/views/JobsListView.spec.js`

**Interfaces:**
- Consumes: `PageHeader`, `StatusMessage`, `DataTable`, named route `job-detail`.

- [ ] **Step 1: Rewrite the spec against the target markup**

```js
// web/src/views/JobsListView.spec.js
import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import JobsListView from './JobsListView.vue'
import { useJobsStore } from '../stores/jobs'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { jobs: state } })
  const wrapper = mount(JobsListView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
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
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'backup:nightly:1752400000')
    expect(link.props('to')).toEqual({ name: 'job-detail', params: { job_id: 'backup:nightly:1752400000' } })
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
    const cells = wrapper.findAll('tbody td')
    expect(cells[3].text()).toBe('—')
    expect(cells[5].text()).toBe('—')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('shows an empty-state message when there are no jobs', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.text()).toContain('No jobs in the last 24h.')
  })
})
```

- [ ] **Step 2: Run the spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/JobsListView.spec.js`
Expected: FAIL

- [ ] **Step 3: Rewrite `JobsListView.vue`**

```vue
<!-- web/src/views/JobsListView.vue -->
<script setup>
import { onMounted } from 'vue'
import { useJobsStore } from '../stores/jobs'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'

const jobs = useJobsStore()

onMounted(() => {
  jobs.fetchAll()
})

const columns = [
  { label: 'Job ID', field: 'job_id', sortable: true },
  { label: 'Kind', field: 'kind', sortable: true },
  { label: 'Source Host', field: 'source_host', sortable: true },
  { label: 'Store Host', field: 'store_host', sortable: true, formatFn: (v) => v || '—' },
  { label: 'Started At', field: 'started_at', sortable: true, type: 'number', formatFn: (v) => formatTimestamp(v) || '—' },
  { label: 'Finished At', field: 'finished_at', sortable: true, type: 'number', formatFn: (v) => formatTimestamp(v) || '—' },
  { label: 'State', field: 'state', sortable: true },
]
</script>

<template>
  <div>
    <PageHeader title="Jobs" />
    <StatusMessage
      :loading="jobs.loading"
      :error="jobs.error"
      :empty="jobs.list.length === 0"
      empty-text="No jobs in the last 24h."
    >
      <DataTable :columns="columns" :rows="jobs.list">
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'job_id'"
            :to="{ name: 'job-detail', params: { job_id: row.job_id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.job_id }}
          </router-link>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run the spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/JobsListView.spec.js`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/JobsListView.vue web/src/views/JobsListView.spec.js
git commit -m "refactor(web): migrate JobsListView onto DataTable and named routes"
```

---

## Task 13: Migrate `CatalogView` (fixes the sort/click row-correlation bug)

**Files:**
- Modify: `web/src/views/CatalogView.vue`
- Modify: `web/src/views/CatalogView.spec.js`

**Interfaces:**
- Consumes: `StatusMessage`, `DataTable` (`@row-click` with the real group object — no index), `groupEntriesByFile` (unchanged), `VersionsModal` (Task 14 restyles it but doesn't change its props/events).

This is the task that removes the bug documented in the design spec: the old code correlated `simple-datatables`' post-sort `rowIndex` back into the pre-sort `groups.value` array. `DataTable`'s `row-click` hands back the actual clicked row object, so there is no index to correlate — the bug is structurally impossible after this change, not just patched. Step 1's third test proves it by sorting the table before clicking.

- [ ] **Step 1: Rewrite the spec against the target markup**

```js
// web/src/views/CatalogView.spec.js
import { describe, it, expect } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'

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

  it('opens the versions modal for the row actually clicked, even after sorting reorders the table', async () => {
    const { wrapper, catalog } = mountView({})
    // Insertion order: "webserver" (1 version) first, "database" (2
    // versions) second — so row 0 is webserver *before* sorting. Sorting
    // ascending by Path puts "database" (/var/lib/...) before "webserver"
    // (/var/www/...), so row 0 becomes database *after* sorting. The old
    // simple-datatables integration mapped a clicked row's index back into
    // the pre-sort array, so clicking post-sort row 0 there would have
    // resolved to webserver (wrong, and single-version besides). Row-click
    // now hands back the actual clicked row object, so it must resolve to
    // database regardless of sort order.
    catalog.entries = [
      entry({ id: 3, source_host: 'webserver', path: '/var/www/index.html', store_created_at: 1752350000 }),
      entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db', store_created_at: 1752300000 }),
      entry({ id: 2, source_host: 'database', path: '/var/lib/dbdata/data.db', store_created_at: 1752400000 }),
    ]
    await search(wrapper)

    await wrapper.find('thead th button').trigger('click') // sorts by Path ascending
    await flushPromises()
    const sortedRows = wrapper.findAll('tbody tr')
    expect(sortedRows[0].text()).toContain('/var/lib/dbdata/data.db')

    await sortedRows[0].trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
  })

  it('does not open the versions modal when a single-version row is clicked', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [entry({ id: 1 })]
    await search(wrapper)

    await wrapper.find('tbody tr').trigger('click')
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

    await wrapper.find('tbody tr').trigger('click')
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

- [ ] **Step 2: Run the spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/CatalogView.spec.js`
Expected: FAIL

- [ ] **Step 3: Rewrite `CatalogView.vue`**

```vue
<!-- web/src/views/CatalogView.vue -->
<script setup>
import { computed, reactive, ref } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import VersionsModal from '../components/VersionsModal.vue'

const catalog = useCatalogStore()
const form = reactive({ sourceHost: '', storeHost: '', pattern: '' })
const hasSearched = ref(false)
const selectedGroup = ref(null)

const canSearch = computed(() => Boolean(form.sourceHost || form.storeHost || form.pattern))
const groups = computed(() => groupEntriesByFile(catalog.entries))

async function submit() {
  selectedGroup.value = null
  if (!canSearch.value) return
  hasSearched.value = true
  await catalog.search({ ...form })
}

function onRowClick(group) {
  if (group.versions.length > 1) selectedGroup.value = group
}

const columns = [
  { label: 'Path', field: 'path', sortable: true },
  { label: 'Source Host', field: 'sourceHost', sortable: true },
  { label: 'Store Host', field: 'representative.store_host', sortable: true },
  { label: 'Size', field: 'representative.size', sortable: true, type: 'number', formatFn: (v) => formatBytes(v) },
  { label: 'Mode', field: 'representative.mode', sortable: true },
  {
    label: 'Modified',
    field: 'representative.mod_time',
    sortable: true,
    type: 'number',
    formatFn: (v) => formatTimestamp(v) || '—',
  },
  { label: 'Versions', field: 'versions', sortable: false, formatFn: (v) => (v.length > 1 ? v.length : '') },
]
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
    <p v-if="!hasSearched" class="text-gray-500">Enter a filter and search.</p>
    <StatusMessage
      v-else
      :loading="catalog.loading"
      :error="catalog.error"
      :empty="groups.length === 0"
      empty-text="No entries match this filter."
    >
      <DataTable :columns="columns" :rows="groups" :search-enabled="false" @row-click="onRowClick" />
    </StatusMessage>
    <VersionsModal v-if="selectedGroup" :group="selectedGroup" @close="selectedGroup = null" />
  </div>
</template>
```

Note this drops the manual `tableRef`/`nextTick`/`renderTable`/`destroyTable`/`onBeforeUnmount` lifecycle entirely — `groups` is now a plain `computed`, so `DataTable` re-renders automatically whenever `catalog.entries` changes. There's nothing left to manually construct, destroy, or recreate.

- [ ] **Step 4: Run the spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/CatalogView.spec.js`
Expected: PASS (13 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js
git commit -m "fix(web): eliminate CatalogView's sort/click row-correlation bug via DataTable"
```

---

## Task 14: Restyle `VersionsModal` onto `BaseButton`

**Files:**
- Modify: `web/src/components/VersionsModal.vue`
- No changes expected to `web/src/components/VersionsModal.spec.js` — it's the safety net.

**Interfaces:**
- Consumes: `BaseButton` (Task 3). Props/events (`group`, `close`) are unchanged.

- [ ] **Step 1: Run the existing spec to confirm the baseline passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/VersionsModal.spec.js`
Expected: PASS

- [ ] **Step 2: Swap the Close button onto `BaseButton`**

```vue
<!-- web/src/components/VersionsModal.vue -->
<script setup>
import { onMounted, onBeforeUnmount } from 'vue'
import { formatBytes, formatTimestamp } from '../utils/format'
import BaseButton from './ui/BaseButton.vue'

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
        <BaseButton variant="secondary" @click="close">Close</BaseButton>
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

- [ ] **Step 3: Rerun the existing spec to confirm it still passes unchanged**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/VersionsModal.spec.js`
Expected: PASS (6 tests, unmodified)

- [ ] **Step 4: Commit**

```bash
git add web/src/components/VersionsModal.vue
git commit -m "refactor(web): restyle VersionsModal's Close button onto BaseButton"
```

---

## Task 15: Refactor `KeyValueEditor` onto `RepeatableFieldList`/`BaseButton`

**Files:**
- Modify: `web/src/components/KeyValueEditor.vue`
- No changes expected to `web/src/components/KeyValueEditor.spec.js` — it's the safety net (its `data-test` names already match `RepeatableFieldList`'s `${testPrefix}-add`/`${testPrefix}-remove` convention).

**Interfaces:**
- Consumes: `RepeatableFieldList` (with `items="draft"`, its own local reactive array), `BaseButton`. Props/events (`modelValue`, `label`, `testPrefix`, `save`) are unchanged.

- [ ] **Step 1: Run the existing spec to confirm the baseline passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/KeyValueEditor.spec.js`
Expected: PASS

- [ ] **Step 2: Rewrite `KeyValueEditor.vue`**

```vue
<!-- web/src/components/KeyValueEditor.vue -->
<script setup>
import { reactive, computed, watch } from 'vue'
import RepeatableFieldList from './ui/RepeatableFieldList.vue'
import BaseButton from './ui/BaseButton.vue'

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
    <RepeatableFieldList :items="draft" :new-item="() => ({ key: '', value: '' })" add-label="Add" :test-prefix="testPrefix">
      <template #row="{ index }">
        <input
          :data-test="`${testPrefix}-key-input`"
          v-model="draft[index].key"
          placeholder="key"
          class="flex-1 border rounded px-2 py-1"
        />
        <input
          :data-test="`${testPrefix}-value-input`"
          v-model="draft[index].value"
          placeholder="value"
          class="flex-1 border rounded px-2 py-1"
        />
      </template>
    </RepeatableFieldList>
    <BaseButton variant="primary" :data-test="`${testPrefix}-update`" :disabled="!dirty" class="mt-2" @click="submit">
      Update
    </BaseButton>
  </div>
</template>
```

- [ ] **Step 3: Rerun the existing spec to confirm it still passes unchanged**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/KeyValueEditor.spec.js`
Expected: PASS (5 tests, unmodified)

- [ ] **Step 4: Commit**

```bash
git add web/src/components/KeyValueEditor.vue
git commit -m "refactor(web): rebuild KeyValueEditor on RepeatableFieldList and BaseButton"
```

---

## Task 16: Refactor `SanListEditor` onto `RepeatableFieldList`/`BaseButton`

**Files:**
- Modify: `web/src/components/SanListEditor.vue`
- No changes expected to `web/src/components/SanListEditor.spec.js` — it's the safety net.

**Interfaces:**
- Consumes: `RepeatableFieldList`, `BaseButton`. Props/events (`modelValue`, `save`) are unchanged.

- [ ] **Step 1: Run the existing spec to confirm the baseline passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/SanListEditor.spec.js`
Expected: PASS

- [ ] **Step 2: Rewrite `SanListEditor.vue`**

```vue
<!-- web/src/components/SanListEditor.vue -->
<script setup>
import { reactive, computed, watch } from 'vue'
import RepeatableFieldList from './ui/RepeatableFieldList.vue'
import BaseButton from './ui/BaseButton.vue'

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

function normalize(list) {
  return [...new Set(list.map((s) => s.trim()).filter(Boolean))].sort()
}

const dirty = computed(() => JSON.stringify(draft) !== JSON.stringify(snapshot))

function submit() {
  const draftSet = new Set(normalize(draft))
  const snapshotSet = new Set(normalize(snapshot))
  const add = [...draftSet].filter((s) => !snapshotSet.has(s))
  const remove = [...snapshotSet].filter((s) => !draftSet.has(s))
  // dirty is a raw draft-vs-snapshot comparison (so an empty added row
  // enables Update immediately), but that can leave add/remove both empty
  // after normalization -- e.g. an added-then-untouched blank row. Skip
  // the round-trip rather than emitting a no-op save.
  if (add.length === 0 && remove.length === 0) return
  emit('save', { add, remove })
}
</script>

<template>
  <div>
    <label class="block font-medium mb-1">SANs</label>
    <RepeatableFieldList :items="draft" add-label="Add SAN" test-prefix="san">
      <template #row="{ index }">
        <input data-test="san-input" v-model="draft[index]" class="flex-1 border rounded px-2 py-1" />
      </template>
    </RepeatableFieldList>
    <BaseButton variant="primary" data-test="san-update" :disabled="!dirty" class="mt-2" @click="submit">
      Update
    </BaseButton>
  </div>
</template>
```

- [ ] **Step 3: Rerun the existing spec to confirm it still passes unchanged**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/SanListEditor.spec.js`
Expected: PASS (5 tests, unmodified)

- [ ] **Step 4: Commit**

```bash
git add web/src/components/SanListEditor.vue
git commit -m "refactor(web): rebuild SanListEditor on RepeatableFieldList and BaseButton"
```

---

## Task 17: Migrate `ClientDetailView`

**Files:**
- Modify: `web/src/views/ClientDetailView.vue`
- Modify: `web/src/views/ClientDetailView.spec.js`

**Interfaces:**
- Consumes: `PageHeader`, `StatusMessage`, `DetailList`, `BaseButton` (danger for Revoke).

The Revoke/Unrevoke/Re-enroll buttons and the token banner stay gated behind `client` being loaded (`v-if="client"` inside `StatusMessage`'s default slot) — **not** moved into `PageHeader`'s `actions` slot, which (like every other view using it) renders unconditionally. The original template only showed these buttons once `client` existed; moving them into the header would show a "Revoke" button before the client has loaded, or during an error state. `PoliciesListView`/`PolicyDetailView`'s header actions are fine to leave unconditional because their original markup was already unconditional too — this view is the one exception.

- [ ] **Step 1: Rewrite the spec against the target markup**

```js
// web/src/views/ClientDetailView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
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

  it('does not show any action buttons while the client has not loaded', () => {
    const { wrapper } = mountView({ byHostname: {}, loading: false, error: null, pendingToken: null })
    expect(wrapper.find('[data-test="revoke-button"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="reenroll-button"]').exists()).toBe(false)
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

  it('shows the token banner when pendingToken matches the route hostname on mount, and clears it', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: { hostname: 'webserver', token: 'tok-abc' },
    })
    await flushPromises()

    expect(wrapper.find('[data-test="token-banner"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="token-value"]').text()).toBe('tok-abc')
    expect(clients.pendingToken).toBeNull()
  })

  it('does not show the token banner when pendingToken is for a different hostname', async () => {
    const { wrapper } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: { hostname: 'other-host', token: 'tok-abc' },
    })
    await flushPromises()

    expect(wrapper.find('[data-test="token-banner"]').exists()).toBe(false)
  })

  it('does not show the token banner when pendingToken is null', async () => {
    const { wrapper } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: null,
    })
    await flushPromises()

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

- [ ] **Step 2: Run the spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/ClientDetailView.spec.js`
Expected: FAIL

- [ ] **Step 3: Rewrite `ClientDetailView.vue`**

```vue
<!-- web/src/views/ClientDetailView.vue -->
<script setup>
import { onMounted, computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'
import KeyValueEditor from '../components/KeyValueEditor.vue'
import SanListEditor from '../components/SanListEditor.vue'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DetailList from '../components/ui/DetailList.vue'
import BaseButton from '../components/ui/BaseButton.vue'

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

async function confirmRevoke() {
  if (window.confirm(`Revoke ${hostname.value}?`)) {
    try {
      await clients.revoke(hostname.value)
    } catch {
      // error already recorded on clients.error by the store
    }
  }
}
async function confirmUnrevoke() {
  if (window.confirm(`Unrevoke ${hostname.value}?`)) {
    try {
      await clients.unrevoke(hostname.value)
    } catch {
      // error already recorded on clients.error by the store
    }
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
  try {
    await navigator.clipboard.writeText(tokenValue.value)
  } catch {
    // clipboard access can fail (insecure context, denied permission);
    // the token is still visible in the banner for manual copying
  }
}

async function saveDescription({ set, unset }) {
  try {
    await clients.updateDescription(hostname.value, set, unset)
  } catch {
    // error already recorded on clients.error by the store
  }
}
async function saveAttributes({ set, unset }) {
  try {
    await clients.updateAttributes(hostname.value, set, unset)
  } catch {
    // error already recorded on clients.error by the store
  }
}
async function saveSans({ add, remove }) {
  try {
    await clients.updateSans(hostname.value, add, remove)
  } catch {
    // error already recorded on clients.error by the store
  }
}

const detailRows = computed(() => {
  if (!client.value) return []
  return [
    { key: 'revoked', label: 'Revoked', value: client.value.revoked ? 'Yes' : 'No' },
    { key: 'revokedAt', label: 'Revoked At', value: formatTimestamp(client.value.revoked_at) || '—' },
    { key: 'lastSeen', label: 'Last Seen', value: formatTimestamp(client.value.last_seen_at) || 'Never' },
  ]
})
</script>

<template>
  <div>
    <PageHeader :title="hostname" />
    <StatusMessage :loading="clients.loading" :error="clients.error">
      <template v-if="client">
        <div v-if="showToken" data-test="token-banner" class="bg-yellow-50 border border-yellow-400 rounded p-3 mb-4">
          <p class="font-medium">Enrollment token (shown once):</p>
          <code data-test="token-value" class="block bg-white border rounded px-2 py-1 my-1 break-all">{{ tokenValue }}</code>
          <BaseButton variant="secondary" class="mr-2" @click="copyToken">Copy</BaseButton>
          <BaseButton variant="secondary" @click="showToken = false">Dismiss</BaseButton>
          <p class="text-sm text-gray-600 mt-1">This token won't be shown again — relay it to the node now.</p>
        </div>

        <div class="mb-4 flex gap-2">
          <BaseButton v-if="!client.revoked" data-test="revoke-button" variant="danger" @click="confirmRevoke">
            Revoke
          </BaseButton>
          <BaseButton v-else data-test="unrevoke-button" variant="secondary" @click="confirmUnrevoke">
            Unrevoke
          </BaseButton>
          <BaseButton data-test="reenroll-button" variant="secondary" @click="reenroll">Re-enroll</BaseButton>
        </div>

        <DetailList :rows="detailRows" class="mb-6" />

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
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run the spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/ClientDetailView.spec.js`
Expected: PASS (14 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/ClientDetailView.vue web/src/views/ClientDetailView.spec.js
git commit -m "refactor(web): migrate ClientDetailView onto the shared ui/ layer"
```

---

## Task 18: Migrate `PolicyDetailView`

**Files:**
- Modify: `web/src/views/PolicyDetailView.vue`
- Modify: `web/src/views/PolicyDetailView.spec.js`

**Interfaces:**
- Consumes: `PageHeader` (actions slot, unconditional — matches the original's always-visible Edit/Delete), `StatusMessage`, `DetailList`, `BaseButton`, named routes `policy-edit`/`policies`.

- [ ] **Step 1: Rewrite the spec against the target markup**

```js
// web/src/views/PolicyDetailView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
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
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
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
    const editLink = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'Edit')
    expect(editLink.props('to')).toEqual({ name: 'policy-edit', params: { id: 'p1' } })
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

    await wrapper.find('[data-test="policy-delete"]').trigger('click')
    await Promise.resolve()

    expect(policies.remove).toHaveBeenCalledWith('p1')
    expect(push).toHaveBeenCalledWith({ name: 'policies' })
  })
})
```

- [ ] **Step 2: Run the spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/PolicyDetailView.spec.js`
Expected: FAIL

- [ ] **Step 3: Rewrite `PolicyDetailView.vue`**

```vue
<!-- web/src/views/PolicyDetailView.vue -->
<script setup>
import { onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DetailList from '../components/ui/DetailList.vue'
import BaseButton from '../components/ui/BaseButton.vue'

const route = useRoute()
const router = useRouter()
const policies = usePoliciesStore()
const id = computed(() => route.params.id)
const policy = computed(() => policies.byId[id.value])

onMounted(async () => {
  try {
    await policies.fetchOne(id.value)
  } catch {
    // error already recorded on policies.error by the store
  }
})

const detailRows = computed(() => {
  if (!policy.value) return []
  return [
    { key: 'rpo', label: 'RPO', value: policy.value.rpo },
    { key: 'destination', label: 'Destination', value: policy.value.destination },
    { key: 'backupWindow', label: 'Backup Window', value: (policy.value.backup_window || []).join(', ') || '—' },
    { key: 'hostnames', label: 'Hostnames', value: (policy.value.client_filters?.hostnames || []).join(', ') || '—' },
    { key: 'labels', label: 'Labels', value: JSON.stringify(policy.value.client_filters?.labels || {}) },
    { key: 'objectFilters', label: 'Object Filters', value: '' },
    { key: 'created', label: 'Created', value: formatTimestamp(policy.value.created_at) || '—' },
    { key: 'updated', label: 'Updated', value: formatTimestamp(policy.value.updated_at) || '—' },
  ]
})

async function confirmDelete() {
  if (window.confirm('Delete this policy?')) {
    await policies.remove(id.value)
    router.push({ name: 'policies' })
  }
}
</script>

<template>
  <div>
    <PageHeader :title="policy?.name || id">
      <template #actions>
        <router-link :to="{ name: 'policy-edit', params: { id } }" class="border rounded px-3 py-1">Edit</router-link>
        <BaseButton data-test="policy-delete" variant="danger" @click="confirmDelete">Delete</BaseButton>
      </template>
    </PageHeader>
    <StatusMessage :loading="policies.loading" :error="policies.error">
      <DetailList v-if="policy" :rows="detailRows">
        <template #objectFilters>
          <ul>
            <li v-for="f in policy.object_filters || []" :key="f.id">
              {{ f.path }}
              <span v-if="f.include?.length"> include: {{ f.include.join(', ') }}</span>
              <span v-if="f.exclude?.length"> exclude: {{ f.exclude.join(', ') }}</span>
            </li>
          </ul>
        </template>
      </DetailList>
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run the spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/PolicyDetailView.spec.js`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/PolicyDetailView.vue web/src/views/PolicyDetailView.spec.js
git commit -m "refactor(web): migrate PolicyDetailView onto the shared ui/ layer"
```

---

## Task 19: Migrate `PolicyFormView`

**Files:**
- Modify: `web/src/views/PolicyFormView.vue`
- Modify: `web/src/views/PolicyFormView.spec.js`

**Interfaces:**
- Consumes: `RepeatableFieldList` (four instances: hostnames, labels, object filters, backup window), `BaseButton`, named route `policy-detail`.

`data-test` names change from the old inconsistent mix (`add-hostname`, `hostname-input`) to `RepeatableFieldList`'s uniform `${testPrefix}-add`/`${testPrefix}-remove` suffix convention: `add-hostname`→`hostname-add`, `remove-hostname`→`hostname-remove`, `add-label`→`label-add`, `remove-label`→`label-remove`, `add-window`→`window-add`, `remove-window`→`window-remove`, `add-filter`→`filter-add`, `remove-filter`→`filter-remove`. The `*-input` names are unchanged.

- [ ] **Step 1: Rewrite the spec against the target markup**

```js
// web/src/views/PolicyFormView.spec.js
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
      expect(push).toHaveBeenCalledWith({ name: 'policy-detail', params: { id: 'p9' } })
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
      expect(push).toHaveBeenCalledWith({ name: 'policy-detail', params: { id: 'p1' } })
    })
  })

  describe('dynamic list fields', () => {
    it('adds and removes hostname rows, sending only non-empty trimmed values', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('[data-test="hostname-add"]').trigger('click')
      await wrapper.find('[data-test="hostname-add"]').trigger('click')
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

      await wrapper.find('[data-test="label-add"]').trigger('click')
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

      await wrapper.find('[data-test="window-add"]').trigger('click')
      await wrapper.find('[data-test="window-input"]').setValue('0 2 * * *')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.create).toHaveBeenCalledWith(expect.objectContaining({ backup_window: ['0 2 * * *'] }))
    })

    it('adds an object filter and splits comma-separated include/exclude into arrays', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('[data-test="filter-add"]').trigger('click')
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

      await wrapper.find('[data-test="hostname-add"]').trigger('click')
      await wrapper.find('[data-test="hostname-input"]').setValue('database')
      await wrapper.find('[data-test="hostname-remove"]').trigger('click')
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

- [ ] **Step 2: Run the spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/PolicyFormView.spec.js`
Expected: FAIL

- [ ] **Step 3: Rewrite `PolicyFormView.vue`**

```vue
<!-- web/src/views/PolicyFormView.vue -->
<script setup>
import { reactive, computed, onMounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import RepeatableFieldList from '../components/ui/RepeatableFieldList.vue'
import BaseButton from '../components/ui/BaseButton.vue'

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
    await nextTick()
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
    router.push({ name: 'policy-detail', params: { id: policy.id } })
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
        <label class="block font-medium mb-1">Hostnames (glob patterns)</label>
        <RepeatableFieldList :items="form.client_filters.hostnames" add-label="Add Hostname" test-prefix="hostname">
          <template #row="{ index }">
            <input
              data-test="hostname-input"
              v-model="form.client_filters.hostnames[index]"
              class="flex-1 border rounded px-2 py-1"
            />
          </template>
        </RepeatableFieldList>
      </div>

      <div>
        <label class="block font-medium mb-1">Labels</label>
        <RepeatableFieldList
          :items="form.client_filters.labels"
          :new-item="() => ({ key: '', value: '' })"
          add-label="Add Label"
          test-prefix="label"
        >
          <template #row="{ index }">
            <input
              data-test="label-key-input"
              v-model="form.client_filters.labels[index].key"
              placeholder="key"
              class="flex-1 border rounded px-2 py-1"
            />
            <input
              data-test="label-value-input"
              v-model="form.client_filters.labels[index].value"
              placeholder="value"
              class="flex-1 border rounded px-2 py-1"
            />
          </template>
        </RepeatableFieldList>
      </div>

      <div>
        <label class="block font-medium mb-1">Object Filters</label>
        <RepeatableFieldList
          :items="form.object_filters"
          :new-item="() => ({ path: '', includeText: '', excludeText: '' })"
          add-label="Add Object Filter"
          remove-label="Remove Filter"
          row-class="border rounded p-2 mb-2 space-y-1"
          test-prefix="filter"
        >
          <template #row="{ index }">
            <input
              data-test="filter-path-input"
              v-model="form.object_filters[index].path"
              placeholder="path"
              class="w-full border rounded px-2 py-1"
            />
            <input
              data-test="filter-include-input"
              v-model="form.object_filters[index].includeText"
              placeholder="include patterns, comma-separated"
              class="w-full border rounded px-2 py-1"
            />
            <input
              data-test="filter-exclude-input"
              v-model="form.object_filters[index].excludeText"
              placeholder="exclude patterns, comma-separated"
              class="w-full border rounded px-2 py-1"
            />
          </template>
        </RepeatableFieldList>
      </div>

      <div>
        <label class="block font-medium mb-1">RPO</label>
        <input name="rpo" v-model="form.rpo" placeholder="e.g. 24h" class="w-full border rounded px-2 py-1" />
      </div>

      <div>
        <label class="block font-medium mb-1">Backup Window (cron expressions)</label>
        <RepeatableFieldList :items="form.backup_window" add-label="Add Window" test-prefix="window">
          <template #row="{ index }">
            <input
              data-test="window-input"
              v-model="form.backup_window[index]"
              placeholder="0 2 * * *"
              class="flex-1 border rounded px-2 py-1"
            />
          </template>
        </RepeatableFieldList>
      </div>

      <div>
        <label class="block font-medium mb-1">Destination</label>
        <input name="destination" v-model="form.destination" placeholder="host:port" class="w-full border rounded px-2 py-1" />
      </div>

      <BaseButton type="submit" variant="primary">
        {{ isEdit ? 'Save Changes' : 'Create Policy' }}
      </BaseButton>
    </form>
  </div>
</template>
```

- [ ] **Step 4: Run the spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/PolicyFormView.spec.js`
Expected: PASS (11 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/PolicyFormView.vue web/src/views/PolicyFormView.spec.js
git commit -m "refactor(web): migrate PolicyFormView onto RepeatableFieldList and named routes"
```

---

## Task 20: Migrate `ClientFormView`

**Files:**
- Modify: `web/src/views/ClientFormView.vue`
- Modify: `web/src/views/ClientFormView.spec.js`

**Interfaces:**
- Consumes: `RepeatableFieldList`, `BaseButton`, named route `client-detail`.

`data-test` names change: `add-san`→`san-add`, `remove-san`→`san-remove` (now matching `SanListEditor`'s existing convention).

- [ ] **Step 1: Rewrite the spec against the target markup**

```js
// web/src/views/ClientFormView.spec.js
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
    expect(push).toHaveBeenCalledWith({ name: 'client-detail', params: { hostname: 'node-1' } })
  })

  it('adds and removes SAN rows, sending only non-empty trimmed values', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })

    await wrapper.find('[data-test="san-add"]').trigger('click')
    await wrapper.find('[data-test="san-add"]').trigger('click')
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

    await wrapper.find('[data-test="san-add"]').trigger('click')
    await wrapper.find('[data-test="san-input"]').setValue('alias.internal')
    await wrapper.find('[data-test="san-remove"]').trigger('click')
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

- [ ] **Step 2: Run the spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/ClientFormView.spec.js`
Expected: FAIL

- [ ] **Step 3: Rewrite `ClientFormView.vue`**

```vue
<!-- web/src/views/ClientFormView.vue -->
<script setup>
import { reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useClientsStore } from '../stores/clients'
import RepeatableFieldList from '../components/ui/RepeatableFieldList.vue'
import BaseButton from '../components/ui/BaseButton.vue'

const router = useRouter()
const clients = useClientsStore()

const form = reactive({ hostname: '', sans: [] })

async function submit() {
  const sans = form.sans.map((s) => s.trim()).filter(Boolean)
  try {
    const result = await clients.enroll(form.hostname, sans)
    router.push({ name: 'client-detail', params: { hostname: result.hostname } })
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
        <RepeatableFieldList :items="form.sans" add-label="Add SAN" test-prefix="san">
          <template #row="{ index }">
            <input data-test="san-input" v-model="form.sans[index]" class="flex-1 border rounded px-2 py-1" />
          </template>
        </RepeatableFieldList>
      </div>

      <BaseButton type="submit" variant="primary">Enroll</BaseButton>
    </form>
  </div>
</template>
```

- [ ] **Step 4: Run the spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/ClientFormView.spec.js`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/ClientFormView.vue web/src/views/ClientFormView.spec.js
git commit -m "refactor(web): migrate ClientFormView onto RepeatableFieldList and named routes"
```

---

## Task 21: Migrate `JobDetailView`, `Sidebar`, `TokenGate`, `HomeView`

**Files:**
- Modify: `web/src/views/JobDetailView.vue`
- Modify: `web/src/views/JobDetailView.spec.js`
- Modify: `web/src/components/Sidebar.vue`
- Create: `web/src/components/Sidebar.spec.js`
- Modify: `web/src/components/TokenGate.vue` (no spec changes — safety net)
- Modify: `web/src/views/HomeView.vue` (no spec — none existed before, and its content stays static text)

**Interfaces:**
- Consumes: `PageHeader`, `StatusMessage`, `BaseButton`, named routes (`Sidebar`'s four top-level links).

These four are grouped into one task: each is a small, mechanical swap onto components already proven in Tasks 3–9, none has cross-dependencies on the others, and none is complex enough to need its own reviewer gate.

- [ ] **Step 1: Rewrite `JobDetailView.spec.js`**

```js
// web/src/views/JobDetailView.spec.js
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

- [ ] **Step 2: Run the spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/JobDetailView.spec.js`
Expected: FAIL

- [ ] **Step 3: Rewrite `JobDetailView.vue`**

```vue
<!-- web/src/views/JobDetailView.vue -->
<script setup>
import { onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useJobsStore } from '../stores/jobs'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'

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
    <PageHeader :title="jobId" />
    <StatusMessage
      :loading="jobs.logsLoading"
      :error="jobs.logsError"
      :empty="jobs.logs.length === 0"
      empty-text="No log lines found for this job in the last 24h."
    >
      <ul class="font-mono text-sm space-y-1">
        <li v-for="(line, index) in jobs.logs" :key="index">
          <span class="text-gray-500">{{ formatLineTimestamp(line.timestamp) }}</span>
          [{{ line.hostname }}/{{ line.binary }}] {{ line.line }}
        </li>
      </ul>
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run the `JobDetailView` spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/views/JobDetailView.spec.js`
Expected: PASS (5 tests)

- [ ] **Step 5: Write the failing `Sidebar` spec**

```js
// web/src/components/Sidebar.spec.js
import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import Sidebar from './Sidebar.vue'

describe('Sidebar', () => {
  it('links to each top-level named route', () => {
    const wrapper = mount(Sidebar, { global: { stubs: { RouterLink: RouterLinkStub } } })
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links.map((l) => l.props('to'))).toEqual([
      { name: 'clients' },
      { name: 'catalog' },
      { name: 'policies' },
      { name: 'jobs' },
    ])
  })
})
```

- [ ] **Step 6: Run the `Sidebar` spec to verify it fails**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/Sidebar.spec.js`
Expected: FAIL — current `Sidebar.vue` uses string `to="/clients"` etc., so `.props('to')` is a string, not `{ name: 'clients' }`

- [ ] **Step 7: Update `Sidebar.vue`**

```vue
<!-- web/src/components/Sidebar.vue -->
<template>
  <nav class="w-48 bg-gray-100 h-screen p-4 space-y-2">
    <router-link :to="{ name: 'clients' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Clients
    </router-link>
    <router-link :to="{ name: 'catalog' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Catalog
    </router-link>
    <router-link :to="{ name: 'policies' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Policies
    </router-link>
    <router-link :to="{ name: 'jobs' }" class="block px-2 py-1 rounded hover:bg-gray-200" active-class="bg-gray-200 font-semibold">
      Jobs
    </router-link>
  </nav>
</template>
```

- [ ] **Step 8: Run the `Sidebar` spec to verify it passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/Sidebar.spec.js`
Expected: PASS (1 test)

- [ ] **Step 9: Run the existing `TokenGate` spec to confirm the baseline passes**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/TokenGate.spec.js`
Expected: PASS

- [ ] **Step 10: Swap `TokenGate`'s submit button onto `BaseButton`**

```vue
<!-- web/src/components/TokenGate.vue -->
<script setup>
import { ref } from 'vue'
import { useAuthStore } from '../stores/auth'
import BaseButton from './ui/BaseButton.vue'

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
      <p v-if="auth.error" class="text-red-600 text-sm">{{ auth.error }}</p>
      <input
        v-model="input"
        type="password"
        placeholder="Bearer token"
        class="w-full border rounded px-2 py-1"
      />
      <BaseButton type="submit" variant="primary" class="w-full">Continue</BaseButton>
    </form>
  </div>
</template>
```

- [ ] **Step 11: Rerun the `TokenGate` spec to confirm it still passes unchanged**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/components/TokenGate.spec.js`
Expected: PASS (4 tests, unmodified)

- [ ] **Step 12: Update `HomeView.vue`**

```vue
<!-- web/src/views/HomeView.vue -->
<script setup>
import PageHeader from '../components/ui/PageHeader.vue'
</script>

<template>
  <PageHeader title="Miniprotector">
    <p class="text-gray-600">Select a page from the sidebar.</p>
  </PageHeader>
</template>
```

- [ ] **Step 13: Run the full suite once to confirm nothing regressed**

Run: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run`
Expected: PASS (all specs)

- [ ] **Step 14: Commit**

```bash
git add web/src/views/JobDetailView.vue web/src/views/JobDetailView.spec.js \
  web/src/components/Sidebar.vue web/src/components/Sidebar.spec.js \
  web/src/components/TokenGate.vue web/src/views/HomeView.vue
git commit -m "refactor(web): migrate JobDetailView, Sidebar, TokenGate, HomeView onto the shared ui/ layer"
```

---

## Task 22: Documentation

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none — documentation only, per this repo's `.claude/CLAUDE.md` rule that feature changes update component docs and the changelog before merging to `main`.

- [ ] **Step 1: Update `docs/components/web.md`**

In the `## Pages` section, update the three bullets that mention `simple-datatables`:

Change:
```
- `/clients` — every enrolled client (hostname, revoked, last seen), with client-side search/sort
  via `simple-datatables`, linking to:
```
to:
```
- `/clients` — every enrolled client (hostname, revoked, last seen), with client-side search/sort
  via `vue-good-table-next`, linking to:
```

Change (within the `/catalog` bullet):
```
  grouped into one row per distinct file (source host + path) and handed to `simple-datatables` for
  client-side sort/pagination — grouping over the complete result set means a file's versions are
```
to:
```
  grouped into one row per distinct file (source host + path) and handed to a client-side
  sortable/paginated table (`vue-good-table-next`) — grouping over the complete result set means a
  file's versions are
```

Change:
```
- `/jobs` — every job across the fleet from the last 24h (job ID, kind, source host, store host,
  started/finished time, state), with client-side search, sort, and pagination via
  `simple-datatables` (also used on `/catalog`), linking to:
```
to:
```
- `/jobs` — every job across the fleet from the last 24h (job ID, kind, source host, store host,
  started/finished time, state), with client-side search, sort, and pagination via
  `vue-good-table-next` (also used on `/catalog`, `/clients`, and `/policies`), linking to:
```

In `## See Also`, add:
```
- [Design: web frontend consistency & best-practices refresh](../superpowers/specs/2026-07-20-web-frontend-refresh-design.md)
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Insert as the new first entry (above the `2026-07-20 — web: rewrite the catalog view's...` entry), matching this file's existing style:

```markdown
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
```

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs(web): document the frontend consistency and best-practices refresh"
```

---

## Final Verification

After Task 22, run the full suite and build once more to confirm the whole branch is green end to end:

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run build
```

Expected: all specs pass, and `vite build` completes without errors (confirms the lazy-loaded route chunks and `vue-good-table-next`'s CSS import resolve correctly in a production build, not just under Vitest/jsdom).

Then do one manual pass per `docs/superpowers/specs/2026-07-20-web-frontend-refresh-design.md`'s Testing section: `make demo-up`, click through every page, and specifically confirm the Catalog sort-then-click case (sort by Path, click the new first row, confirm the *displayed* file's versions open — not whatever was first before sorting).
