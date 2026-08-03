# Unified Policy Detail Tabs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give both backup and storage policies a details page with `Details`/`Check-ins` tabs, backed by a reusable `Tabs` component, and give storage policies a details page for the first time so both policy types share one scheme.

**Architecture:** A new generic `Tabs.vue` UI component (URL-query-synced active tab) wraps each detail page's existing read-only fields (`Details` tab) plus a new shared `PolicyCheckins.vue` component (`Check-ins` tab) that lists hosts + last-seen time with a manual Refresh button. Refresh calls a new `refresh(id)` store action that force-refetches the policy (bypassing the existing `fetchOne` cache) since check-ins ride along on the existing policy payload — no new backend endpoint. Storage policies get a new `StoragePolicyView.vue` + `/storage/:id` route mirroring `BackupPolicyView.vue`'s existing shape; `StorageView.vue`'s list drops its click-to-edit-inline behavior in favor of navigating to the new detail page, matching how `BackupPoliciesView.vue` already behaves.

**Tech Stack:** Vue 3 (`<script setup>`), Vue Router 4, Pinia (`@pinia/testing` for tests), Vitest + `@vue/test-utils`, Tailwind CSS.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-03-unified-policy-detail-tabs-design.md`.
- Test runner: from the `web/` directory, `npx vitest run <path>` (e.g. `npx vitest run src/components/ui/Tabs.spec.js`). Full suite: `npm test` (also run from `web/`).
- Follow existing test conventions exactly: `createTestingPinia({ stubActions: true, initialState: {...} })` for store-backed components; `vi.mock('vue-router', () => ({ useRoute: () => (...), useRouter: () => (...) }))` for components that call `useRoute`/`useRouter`; `RouterLinkStub` (imported from `@vue/test-utils`) via `global: { stubs: { RouterLink: RouterLinkStub } }` for components that only render `<router-link>` without calling the composables.
- `data-test="..."` attributes on every interactive element, following each file's existing naming (e.g. `policy-edit`, `storage-delete-${id}`).
- No comments in source files unless documenting a genuinely non-obvious constraint (this codebase's existing files have almost none — match that).
- Per `.claude/CLAUDE.md`: any feature change needs `docs/components/<component>.md` updated and a `CHANGELOG.md` entry before merge — handled in the final task.

---

### Task 1: `Tabs.vue` reusable UI component

**Files:**
- Create: `web/src/components/ui/Tabs.vue`
- Test: `web/src/components/ui/Tabs.spec.js`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: `Tabs` component — prop `tabs: Array<{ key: string, label: string }>` (required, first entry is the default active tab); renders a tab-strip of buttons (`data-test="tab-${key}"`) and the active tab's named slot (`<slot :name="activeKey" />`). Active tab derives from `route.query.tab` (falls back to `tabs[0].key` if absent/unrecognized); clicking a tab calls `router.replace({ query: { ...route.query, tab: key } })`. Later tasks import this as `Tabs` from `../components/ui/Tabs.vue` (from `web/src/views/`) and pass two tabs keyed `details`/`checkins` with matching named slots.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/ui/Tabs.spec.js`:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import Tabs from './Tabs.vue'

const replace = vi.fn()
let routeQuery = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: routeQuery }),
  useRouter: () => ({ replace }),
}))

const TABS = [
  { key: 'details', label: 'Details' },
  { key: 'checkins', label: 'Check-ins' },
]

function mountTabs() {
  return mount(Tabs, {
    props: { tabs: TABS },
    slots: {
      details: '<p>details content</p>',
      checkins: '<p>checkins content</p>',
    },
  })
}

describe('Tabs', () => {
  afterEach(() => {
    replace.mockReset()
    routeQuery = {}
  })

  it('renders a button per tab', () => {
    const wrapper = mountTabs()
    expect(wrapper.find('[data-test="tab-details"]').text()).toBe('Details')
    expect(wrapper.find('[data-test="tab-checkins"]').text()).toBe('Check-ins')
  })

  it('defaults to the first tab when the query param is absent', () => {
    const wrapper = mountTabs()
    expect(wrapper.text()).toContain('details content')
    expect(wrapper.text()).not.toContain('checkins content')
  })

  it('defaults to the first tab when the query param matches no tab', () => {
    routeQuery = { tab: 'nonsense' }
    const wrapper = mountTabs()
    expect(wrapper.text()).toContain('details content')
  })

  it('shows the tab matching the query param', () => {
    routeQuery = { tab: 'checkins' }
    const wrapper = mountTabs()
    expect(wrapper.text()).toContain('checkins content')
    expect(wrapper.text()).not.toContain('details content')
  })

  it('replaces the route query with the clicked tab key, preserving other params', async () => {
    routeQuery = { foo: 'bar' }
    const wrapper = mountTabs()
    await wrapper.find('[data-test="tab-checkins"]').trigger('click')
    expect(replace).toHaveBeenCalledWith({ query: { foo: 'bar', tab: 'checkins' } })
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run (from `web/`): `npx vitest run src/components/ui/Tabs.spec.js`
Expected: FAIL — `Tabs.vue` does not exist / "Failed to resolve import".

- [ ] **Step 3: Write the implementation**

Create `web/src/components/ui/Tabs.vue`:

```vue
<!-- web/src/components/ui/Tabs.vue -->
<script setup>
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

const props = defineProps({
  tabs: { type: Array, required: true },
})

const route = useRoute()
const router = useRouter()

const activeKey = computed(() => {
  const key = route.query.tab
  return props.tabs.some((tab) => tab.key === key) ? key : props.tabs[0].key
})

function selectTab(key) {
  router.replace({ query: { ...route.query, tab: key } })
}
</script>

<template>
  <div>
    <div class="flex gap-4 border-b mb-4">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        :data-test="`tab-${tab.key}`"
        class="pb-2 px-1 -mb-px border-b-2"
        :class="
          activeKey === tab.key
            ? 'border-blue-600 text-blue-600 font-medium'
            : 'border-transparent text-gray-500 hover:text-gray-700'
        "
        @click="selectTab(tab.key)"
      >
        {{ tab.label }}
      </button>
    </div>
    <slot :name="activeKey" />
  </div>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/components/ui/Tabs.spec.js`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/Tabs.vue web/src/components/ui/Tabs.spec.js
git commit -m "feat(web): add reusable Tabs UI component"
```

---

### Task 2: `PolicyCheckins.vue` shared check-ins list

**Files:**
- Create: `web/src/components/policies/PolicyCheckins.vue`
- Test: `web/src/components/policies/PolicyCheckins.spec.js`

**Interfaces:**
- Consumes: `formatTimestamp` from `web/src/utils/format.js` (existing), `BaseButton` from `../ui/BaseButton.vue` (existing).
- Produces: `PolicyCheckins` component — props `checkins: Array<{hostname: string, last_seen_at: number}>` (required), `loading: Boolean` (default false), `error: String|null` (default null); emits `refresh` (no payload). Later tasks import this as `PolicyCheckins` from `../components/policies/PolicyCheckins.vue` and wire `@refresh="<store>.refresh(id)"`.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/policies/PolicyCheckins.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import PolicyCheckins from './PolicyCheckins.vue'

describe('PolicyCheckins', () => {
  it('renders checkins sorted by last_seen_at descending', () => {
    const wrapper = mount(PolicyCheckins, {
      props: {
        checkins: [
          { hostname: 'web-01', last_seen_at: 1752400000 },
          { hostname: 'web-02', last_seen_at: 1752400500 },
        ],
        loading: false,
        error: null,
      },
    })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('web-02')
    expect(rows[1].text()).toContain('web-01')
  })

  it('shows an empty-state message when there are no checkins', () => {
    const wrapper = mount(PolicyCheckins, { props: { checkins: [], loading: false, error: null } })
    expect(wrapper.text()).toContain('No hosts have checked in yet.')
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('emits refresh when the Refresh button is clicked', async () => {
    const wrapper = mount(PolicyCheckins, { props: { checkins: [], loading: false, error: null } })
    await wrapper.find('[data-test="checkins-refresh"]').trigger('click')
    expect(wrapper.emitted('refresh')).toHaveLength(1)
  })

  it('disables the Refresh button while loading', () => {
    const wrapper = mount(PolicyCheckins, { props: { checkins: [], loading: true, error: null } })
    expect(wrapper.find('[data-test="checkins-refresh"]').attributes('disabled')).toBeDefined()
  })

  it('shows the error message without clearing existing rows', () => {
    const wrapper = mount(PolicyCheckins, {
      props: {
        checkins: [{ hostname: 'web-01', last_seen_at: 1752400000 }],
        loading: false,
        error: 'network error',
      },
    })
    expect(wrapper.find('[data-test="checkins-error"]').text()).toBe('network error')
    expect(wrapper.text()).toContain('web-01')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/components/policies/PolicyCheckins.spec.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the implementation**

Create `web/src/components/policies/PolicyCheckins.vue`:

```vue
<!-- web/src/components/policies/PolicyCheckins.vue -->
<script setup>
import { computed } from 'vue'
import { formatTimestamp } from '../../utils/format'
import BaseButton from '../ui/BaseButton.vue'

const props = defineProps({
  checkins: { type: Array, required: true },
  loading: { type: Boolean, default: false },
  error: { type: String, default: null },
})
defineEmits(['refresh'])

const sortedCheckins = computed(() => [...props.checkins].sort((a, b) => b.last_seen_at - a.last_seen_at))
</script>

<template>
  <div>
    <div class="flex justify-end mb-2">
      <BaseButton data-test="checkins-refresh" variant="secondary" :disabled="loading" @click="$emit('refresh')">
        {{ loading ? 'Refreshing…' : 'Refresh' }}
      </BaseButton>
    </div>
    <p v-if="error" data-test="checkins-error" class="text-red-600 mb-4">{{ error }}</p>
    <p v-if="sortedCheckins.length === 0" class="text-gray-500">No hosts have checked in yet.</p>
    <table v-else class="w-full text-left">
      <thead>
        <tr>
          <th class="font-medium">Hostname</th>
          <th class="font-medium">Last Seen</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="checkin in sortedCheckins" :key="checkin.hostname">
          <td>{{ checkin.hostname }}</td>
          <td>{{ formatTimestamp(checkin.last_seen_at) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/components/policies/PolicyCheckins.spec.js`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/components/policies/PolicyCheckins.vue web/src/components/policies/PolicyCheckins.spec.js
git commit -m "feat(web): add shared PolicyCheckins component"
```

---

### Task 3: `refresh(id)` action on `stores/policies.js`

**Files:**
- Modify: `web/src/stores/policies.js`
- Test: `web/src/stores/policies.spec.js`

**Interfaces:**
- Consumes: `withRequest` from `./helpers.js` (existing, already supports `loadingKey`/`errorKey` overrides — unused elsewhere today), `apiFetch` from `../api/client.js` (existing).
- Produces: `usePoliciesStore().refresh(id)` — always issues `GET /policies/{id}` (ignores the `byId` cache, unlike `fetchOne`), on success overwrites `byId[id]` and the matching `list` entry and returns the policy; on failure sets `checkinsError` and rethrows. Uses new state fields `checkinsLoading: boolean` and `checkinsError: string|null`. Later task (5) calls this as `policies.refresh(id)`.

- [ ] **Step 1: Write the failing test**

Add to `web/src/stores/policies.spec.js`, inside the existing `describe('policies store', ...)` block (after the `fetchOne` tests):

```js
  it('refresh always refetches, bypassing the byId cache', async () => {
    apiFetch.mockResolvedValueOnce({ id: 'p1', name: 'nightly', checkins: [] })
    const policies = usePoliciesStore()
    await policies.fetchOne('p1')

    apiFetch.mockResolvedValueOnce({
      id: 'p1',
      name: 'nightly',
      checkins: [{ hostname: 'web-01', last_seen_at: 123 }],
    })
    const result = await policies.refresh('p1')

    expect(apiFetch).toHaveBeenCalledTimes(2)
    expect(apiFetch).toHaveBeenNthCalledWith(2, '/policies/p1')
    expect(result.checkins).toEqual([{ hostname: 'web-01', last_seen_at: 123 }])
    expect(policies.byId.p1).toEqual(result)
  })

  it('refresh updates the matching list entry when present', async () => {
    apiFetch.mockResolvedValueOnce({ data: [{ id: 'p1', name: 'nightly' }] })
    const policies = usePoliciesStore()
    await policies.fetchAll()

    apiFetch.mockResolvedValueOnce({ id: 'p1', name: 'nightly-renamed' })
    await policies.refresh('p1')

    expect(policies.list).toEqual([{ id: 'p1', name: 'nightly-renamed' }])
  })

  it('refresh tracks checkinsLoading separately from loading', async () => {
    let resolveFetch
    apiFetch.mockReturnValue(
      new Promise((resolve) => {
        resolveFetch = resolve
      })
    )
    const policies = usePoliciesStore()

    const pending = policies.refresh('p1')
    expect(policies.checkinsLoading).toBe(true)
    expect(policies.loading).toBe(false)
    resolveFetch({ id: 'p1' })
    await pending
    expect(policies.checkinsLoading).toBe(false)
  })

  it('refresh records the error on checkinsError (not error) and rethrows', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const policies = usePoliciesStore()

    await expect(policies.refresh('p1')).rejects.toThrow('boom')
    expect(policies.checkinsError).toBe('boom')
    expect(policies.error).toBeNull()
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/stores/policies.spec.js`
Expected: FAIL — `policies.refresh is not a function`.

- [ ] **Step 3: Write the implementation**

In `web/src/stores/policies.js`, add `checkinsLoading: false, checkinsError: null,` to the `state()` return object, and add a new action alongside `fetchOne`:

```js
    async refresh(id) {
      return withRequest(
        this,
        async () => {
          const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
          this.byId[id] = policy
          const idx = this.list.findIndex((p) => p.id === id)
          if (idx !== -1) this.list[idx] = policy
          return policy
        },
        { loadingKey: 'checkinsLoading', errorKey: 'checkinsError' }
      )
    },
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/stores/policies.spec.js`
Expected: PASS (all tests, including the 4 new ones).

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/policies.js web/src/stores/policies.spec.js
git commit -m "feat(web): add policies store refresh action for check-in refetch"
```

---

### Task 4: `refresh(id)` action on `stores/storagePolicies.js`

**Files:**
- Modify: `web/src/stores/storagePolicies.js`
- Test: `web/src/stores/storagePolicies.spec.js`

**Interfaces:**
- Consumes: same as Task 3, applied to the storage policies store.
- Produces: `useStoragePoliciesStore().refresh(id)` — identical shape/behavior to Task 3's `policies.refresh(id)` (same `GET /policies/{id}` endpoint — storage and backup policies are both read through `/policies/{id}`, per the existing `fetchOne`). Later task (6) calls this as `storagePolicies.refresh(id)`.

- [ ] **Step 1: Write the failing test**

Add to `web/src/stores/storagePolicies.spec.js`, inside the existing `describe('storagePolicies store', ...)` block (after the `fetchOne` test):

```js
  it('refresh always refetches, bypassing the byId cache', async () => {
    apiFetch.mockResolvedValueOnce({ id: 's1', name: 'east-1-storage', checkins: [] })
    const storagePolicies = useStoragePoliciesStore()
    await storagePolicies.fetchOne('s1')

    apiFetch.mockResolvedValueOnce({
      id: 's1',
      name: 'east-1-storage',
      checkins: [{ hostname: 'storage-east-1', last_seen_at: 456 }],
    })
    const result = await storagePolicies.refresh('s1')

    expect(apiFetch).toHaveBeenCalledTimes(2)
    expect(apiFetch).toHaveBeenNthCalledWith(2, '/policies/s1')
    expect(result.checkins).toEqual([{ hostname: 'storage-east-1', last_seen_at: 456 }])
    expect(storagePolicies.byId.s1).toEqual(result)
  })

  it('refresh updates the matching list entry when present', async () => {
    apiFetch.mockResolvedValueOnce({ data: [{ id: 's1', name: 'east-1-storage' }] })
    const storagePolicies = useStoragePoliciesStore()
    await storagePolicies.fetchAll()

    apiFetch.mockResolvedValueOnce({ id: 's1', name: 'east-1-storage-renamed' })
    await storagePolicies.refresh('s1')

    expect(storagePolicies.list).toEqual([{ id: 's1', name: 'east-1-storage-renamed' }])
  })

  it('refresh tracks checkinsLoading separately from loading', async () => {
    let resolveFetch
    apiFetch.mockReturnValue(
      new Promise((resolve) => {
        resolveFetch = resolve
      })
    )
    const storagePolicies = useStoragePoliciesStore()

    const pending = storagePolicies.refresh('s1')
    expect(storagePolicies.checkinsLoading).toBe(true)
    expect(storagePolicies.loading).toBe(false)
    resolveFetch({ id: 's1' })
    await pending
    expect(storagePolicies.checkinsLoading).toBe(false)
  })

  it('refresh records the error on checkinsError (not error) and rethrows', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const storagePolicies = useStoragePoliciesStore()

    await expect(storagePolicies.refresh('s1')).rejects.toThrow('boom')
    expect(storagePolicies.checkinsError).toBe('boom')
    expect(storagePolicies.error).toBeNull()
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `npx vitest run src/stores/storagePolicies.spec.js`
Expected: FAIL — `storagePolicies.refresh is not a function`.

- [ ] **Step 3: Write the implementation**

In `web/src/stores/storagePolicies.js`, add `checkinsLoading: false, checkinsError: null,` to the `state()` return object, and add a new action alongside `fetchOne`:

```js
    async refresh(id) {
      return withRequest(
        this,
        async () => {
          const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
          this.byId[id] = policy
          const idx = this.list.findIndex((p) => p.id === id)
          if (idx !== -1) this.list[idx] = policy
          return policy
        },
        { loadingKey: 'checkinsLoading', errorKey: 'checkinsError' }
      )
    },
```

- [ ] **Step 4: Run test to verify it passes**

Run: `npx vitest run src/stores/storagePolicies.spec.js`
Expected: PASS (all tests, including the 4 new ones).

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/storagePolicies.js web/src/stores/storagePolicies.spec.js
git commit -m "feat(web): add storagePolicies store refresh action for check-in refetch"
```

---

### Task 5: Wire `Tabs` + `PolicyCheckins` into `BackupPolicyView.vue`

**Files:**
- Modify: `web/src/views/BackupPolicyView.vue`
- Modify: `web/src/views/BackupPolicyView.spec.js`

**Interfaces:**
- Consumes: `Tabs` (Task 1), `PolicyCheckins` (Task 2), `policies.refresh` / `policies.checkinsLoading` / `policies.checkinsError` (Task 3).
- Produces: no new exports; this is a leaf view.

- [ ] **Step 1: Update the failing/changed tests**

In `web/src/views/BackupPolicyView.spec.js`, replace the top of the file (imports through the `vi.mock` block) so the router mock includes `query` and `replace` (`Tabs`, mounted inside `BackupPolicyView`, calls `useRoute`/`useRouter` too):

```js
// web/src/views/BackupPolicyView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import BackupPolicyView from './BackupPolicyView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()
const replace = vi.fn()
let routeQuery = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 'p1' }, query: routeQuery }),
  useRouter: () => ({ push, replace }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(BackupPolicyView, { global: { plugins: [pinia] } })
  return { wrapper, policies: usePoliciesStore() }
}
```

Update the `afterEach` block to also reset `replace` and `routeQuery`:

```js
  afterEach(() => {
    push.mockReset()
    replace.mockReset()
    routeQuery = {}
    vi.restoreAllMocks()
  })
```

Every existing test in the file keeps working unchanged (`routeQuery` defaults to `{}`, so `Tabs` shows its first/default `Details` tab, matching today's behavior). Add two new tests at the end of the `describe` block, before the closing `})`:

```js
  it('shows both tab buttons once the policy has loaded', () => {
    const { wrapper } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {}, checkins: [] } },
      loading: false,
      error: null,
    })
    expect(wrapper.find('[data-test="tab-details"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="tab-checkins"]').exists()).toBe(true)
  })

  it('renders check-ins and wires Refresh to the store on the Checkins tab', async () => {
    routeQuery = { tab: 'checkins' }
    const { wrapper, policies } = mountView({
      byId: {
        p1: {
          id: 'p1',
          name: 'nightly-db-backup',
          object_filters: [],
          client_filters: {},
          checkins: [{ hostname: 'web-01', last_seen_at: 1752400000 }],
        },
      },
      loading: false,
      error: null,
      checkinsLoading: false,
      checkinsError: null,
    })
    expect(wrapper.text()).toContain('web-01')

    await wrapper.find('[data-test="checkins-refresh"]').trigger('click')
    expect(policies.refresh).toHaveBeenCalledWith('p1')
  })
```

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `npx vitest run src/views/BackupPolicyView.spec.js`
Expected: FAIL on the two new tests — no `tab-details`/`tab-checkins`/`checkins-refresh` elements exist yet (the rest still pass).

- [ ] **Step 3: Write the implementation**

Replace `web/src/views/BackupPolicyView.vue` in full:

```vue
<script setup>
import { onMounted, computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DetailList from '../components/ui/DetailList.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import Tabs from '../components/ui/Tabs.vue'
import BackupPolicyFormModal from '../components/backup_policies/BackupPolicyFormModal.vue'
import PolicyCheckins from '../components/policies/PolicyCheckins.vue'

const route = useRoute()
const router = useRouter()
const policies = usePoliciesStore()
const id = computed(() => route.params.id)
const policy = computed(() => policies.byId[id.value])

const showModal = ref(false)
const serverError = ref('')

const TABS = [
  { key: 'details', label: 'Details' },
  { key: 'checkins', label: 'Check-ins' },
]

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

function openEdit() {
  serverError.value = ''
  showModal.value = true
}

function closeModal() {
  showModal.value = false
  serverError.value = ''
}

async function save(payload) {
  try {
    await policies.update(id.value, payload)
    closeModal()
  } catch {
    serverError.value = policies.error
  }
}

async function runNow(payload) {
  try {
    await policies.runAdhoc(payload)
    closeModal()
    router.push({ name: 'jobs' })
  } catch {
    serverError.value = policies.error
  }
}
</script>

<template>
  <div>
    <PageHeader :title="policy?.name || id">
      <template #actions>
        <BaseButton v-if="policy" data-test="policy-edit" variant="secondary" @click="openEdit">Edit</BaseButton>
        <BaseButton data-test="policy-delete" variant="danger" @click="confirmDelete">Delete</BaseButton>
      </template>
    </PageHeader>
    <StatusMessage :loading="policies.loading" :error="policies.error">
      <Tabs v-if="policy" :tabs="TABS">
        <template #details>
          <DetailList :rows="detailRows">
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
        </template>
        <template #checkins>
          <PolicyCheckins
            :checkins="policy.checkins || []"
            :loading="policies.checkinsLoading"
            :error="policies.checkinsError"
            @refresh="policies.refresh(id)"
          />
        </template>
      </Tabs>
    </StatusMessage>
    <BackupPolicyFormModal
      v-if="showModal"
      :policy="policy"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
      @run-now="runNow"
    />
  </div>
</template>
```

The only functional changes from the current file: two new imports (`Tabs`, `PolicyCheckins`), the `TABS` constant, and the `DetailList`/object-filters block now sitting inside `<Tabs>`'s `#details` slot alongside a new `#checkins` slot. Every other function/handler is unchanged.

- [ ] **Step 4: Run tests to verify they pass**

Run: `npx vitest run src/views/BackupPolicyView.spec.js`
Expected: PASS (all tests, including the 2 new ones).

- [ ] **Step 5: Commit**

```bash
git add web/src/views/BackupPolicyView.vue web/src/views/BackupPolicyView.spec.js
git commit -m "feat(web): add Details/Check-ins tabs to the backup policy detail page"
```

---

### Task 6: `StoragePolicyView.vue` + `/storage/:id` route

**Files:**
- Create: `web/src/views/StoragePolicyView.vue`
- Create: `web/src/views/StoragePolicyView.spec.js`
- Modify: `web/src/router.js`
- Modify: `web/src/router.spec.js`

**Interfaces:**
- Consumes: `Tabs` (Task 1), `PolicyCheckins` (Task 2), `storagePolicies.refresh` / `.checkinsLoading` / `.checkinsError` (Task 4), `StorageEditModal` (existing, unchanged).
- Produces: route `{ name: 'storage-detail', path: '/storage/:id' }`. Task 7 links to it via `router-link :to="{ name: 'storage-detail', params: { id: row.id } }"`.

- [ ] **Step 1: Write the failing tests**

Create `web/src/views/StoragePolicyView.spec.js`:

```js
// web/src/views/StoragePolicyView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import StoragePolicyView from './StoragePolicyView.vue'
import { useStoragePoliciesStore } from '../stores/storagePolicies'

const push = vi.fn()
const replace = vi.fn()
let routeQuery = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 's1' }, query: routeQuery }),
  useRouter: () => ({ push, replace }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { storagePolicies: state } })
  const wrapper = mount(StoragePolicyView, { global: { plugins: [pinia] } })
  return { wrapper, storagePolicies: useStoragePoliciesStore() }
}

describe('StoragePolicyView', () => {
  afterEach(() => {
    push.mockReset()
    replace.mockReset()
    routeQuery = {}
    vi.restoreAllMocks()
  })

  it('calls fetchOne with the route id on mount', () => {
    const { storagePolicies } = mountView({ byId: {}, loading: false, error: null })
    expect(storagePolicies.fetchOne).toHaveBeenCalledWith('s1')
  })

  it('renders the cached storage policy record', () => {
    const { wrapper } = mountView({
      byId: {
        s1: {
          id: 's1',
          name: 'east-1-storage',
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage"}',
          client_filters: { hostnames: ['storage-east-1.internal'], labels: {} },
          checkins: [],
        },
      },
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('east-1-storage')
    expect(wrapper.text()).toContain('storage-east-1.internal')
    expect(wrapper.text()).toContain('9400')
    expect(wrapper.text()).toContain('filesystem')
    expect(wrapper.text()).toContain('/data/storage')
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byId: {}, loading: false, error: 'policy not found' })
    expect(wrapper.text()).toContain('policy not found')
  })

  it('shows both tab buttons once the policy has loaded', () => {
    const { wrapper } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {}, checkins: [] } },
      loading: false,
      error: null,
    })
    expect(wrapper.find('[data-test="tab-details"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="tab-checkins"]').exists()).toBe(true)
  })

  it('renders check-ins and wires Refresh to the store on the Checkins tab', async () => {
    routeQuery = { tab: 'checkins' }
    const { wrapper, storagePolicies } = mountView({
      byId: {
        s1: {
          id: 's1',
          name: 'east-1-storage',
          config: '{}',
          client_filters: {},
          checkins: [{ hostname: 'storage-east-1', last_seen_at: 1752400000 }],
        },
      },
      loading: false,
      error: null,
      checkinsLoading: false,
      checkinsError: null,
    })
    expect(wrapper.text()).toContain('storage-east-1')

    await wrapper.find('[data-test="checkins-refresh"]').trigger('click')
    expect(storagePolicies.refresh).toHaveBeenCalledWith('s1')
  })

  it('deletes the policy after confirming and navigates to the storage list', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, storagePolicies } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    storagePolicies.remove.mockResolvedValue(undefined)

    await wrapper.find('[data-test="storage-policy-delete"]').trigger('click')
    await Promise.resolve()

    expect(storagePolicies.remove).toHaveBeenCalledWith('s1')
    expect(push).toHaveBeenCalledWith({ name: 'storage' })
  })

  it('opens the edit modal pre-filled with the policy when Edit is clicked', async () => {
    const policy = { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} }
    const { wrapper } = mountView({ byId: { s1: policy }, loading: false, error: null })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'StorageEditModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('policy')).toEqual(policy)
  })

  it('calls update and closes the modal on save', async () => {
    const { wrapper, storagePolicies } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    storagePolicies.update.mockResolvedValue({ id: 's1', name: 'renamed' })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')

    const payload = { name: 'renamed', port: 9400, config: '{}', client_filters: { hostnames: [], labels: {} } }
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(storagePolicies.update).toHaveBeenCalledWith('s1', payload)
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })

  it('keeps the modal open and shows the server error when update fails', async () => {
    const { wrapper, storagePolicies } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    storagePolicies.update.mockImplementation(async () => {
      storagePolicies.error = 'port must be between 1 and 65535'
      throw new Error('port must be between 1 and 65535')
    })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')

    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', { name: 'bad' })
    await nextTick()

    const modal = wrapper.findComponent({ name: 'StorageEditModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('port must be between 1 and 65535')
  })

  it('closes the modal without saving on close', async () => {
    const { wrapper } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })
})
```

Update `web/src/router.spec.js`'s `EXPECTED_NAMES` array to add `'storage-detail'` after `'storage'`:

```js
const EXPECTED_NAMES = [
  'home',
  'clients',
  'client-new',
  'client-detail',
  'catalog',
  'policies',
  'policy-detail',
  'storage',
  'storage-detail',
  'jobs',
  'job-detail',
]
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx vitest run src/views/StoragePolicyView.spec.js src/router.spec.js`
Expected: FAIL — `StoragePolicyView.vue` doesn't exist; `router.spec.js` fails the name-set comparison (missing `storage-detail`).

- [ ] **Step 3: Write the implementation**

Create `web/src/views/StoragePolicyView.vue`:

```vue
<script setup>
import { onMounted, computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useStoragePoliciesStore } from '../stores/storagePolicies'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DetailList from '../components/ui/DetailList.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import Tabs from '../components/ui/Tabs.vue'
import StorageEditModal from '../components/storage/StorageEditModal.vue'
import PolicyCheckins from '../components/policies/PolicyCheckins.vue'

const route = useRoute()
const router = useRouter()
const storagePolicies = useStoragePoliciesStore()
const id = computed(() => route.params.id)
const policy = computed(() => storagePolicies.byId[id.value])

const showModal = ref(false)
const serverError = ref('')

const TABS = [
  { key: 'details', label: 'Details' },
  { key: 'checkins', label: 'Check-ins' },
]

onMounted(async () => {
  try {
    await storagePolicies.fetchOne(id.value)
  } catch {
    // error already recorded on storagePolicies.error by the store
  }
})

function parseConfig(configText) {
  try {
    const c = JSON.parse(configText || '{}')
    return c && typeof c === 'object' ? c : {}
  } catch {
    return {}
  }
}

const detailRows = computed(() => {
  if (!policy.value) return []
  const config = parseConfig(policy.value.config)
  return [
    { key: 'targetHostname', label: 'Target Hostname', value: policy.value.client_filters?.hostnames?.[0] || '—' },
    { key: 'port', label: 'Port', value: policy.value.port },
    { key: 'storageType', label: 'Storage Type', value: config.backend || '—' },
    { key: 'path', label: 'Path', value: config.root || '—' },
    { key: 'created', label: 'Created', value: formatTimestamp(policy.value.created_at) || '—' },
    { key: 'updated', label: 'Updated', value: formatTimestamp(policy.value.updated_at) || '—' },
  ]
})

async function confirmDelete() {
  if (window.confirm('Delete this storage policy?')) {
    await storagePolicies.remove(id.value)
    router.push({ name: 'storage' })
  }
}

function openEdit() {
  serverError.value = ''
  showModal.value = true
}

function closeModal() {
  showModal.value = false
  serverError.value = ''
}

async function save(payload) {
  try {
    await storagePolicies.update(id.value, payload)
    closeModal()
  } catch {
    serverError.value = storagePolicies.error
  }
}
</script>

<template>
  <div>
    <PageHeader :title="policy?.name || id">
      <template #actions>
        <BaseButton v-if="policy" data-test="storage-policy-edit" variant="secondary" @click="openEdit">
          Edit
        </BaseButton>
        <BaseButton data-test="storage-policy-delete" variant="danger" @click="confirmDelete">Delete</BaseButton>
      </template>
    </PageHeader>
    <StatusMessage :loading="storagePolicies.loading" :error="storagePolicies.error">
      <Tabs v-if="policy" :tabs="TABS">
        <template #details>
          <DetailList :rows="detailRows" />
        </template>
        <template #checkins>
          <PolicyCheckins
            :checkins="policy.checkins || []"
            :loading="storagePolicies.checkinsLoading"
            :error="storagePolicies.checkinsError"
            @refresh="storagePolicies.refresh(id)"
          />
        </template>
      </Tabs>
    </StatusMessage>
    <StorageEditModal
      v-if="showModal"
      :policy="policy"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
    />
  </div>
</template>
```

In `web/src/router.js`, add a route right after the existing `storage` route:

```js
    { path: '/storage', name: 'storage', component: () => import('./views/StorageView.vue') },
    { path: '/storage/:id', name: 'storage-detail', component: () => import('./views/StoragePolicyView.vue') },
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npx vitest run src/views/StoragePolicyView.spec.js src/router.spec.js`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/views/StoragePolicyView.vue web/src/views/StoragePolicyView.spec.js web/src/router.js web/src/router.spec.js
git commit -m "feat(web): add storage policy detail page with Details/Check-ins tabs"
```

---

### Task 7: `StorageView.vue` list navigates to the detail page

**Files:**
- Modify: `web/src/views/StorageView.vue`
- Modify: `web/src/views/StorageView.spec.js`

**Interfaces:**
- Consumes: route `storage-detail` (Task 6).
- Produces: no new exports.

- [ ] **Step 1: Update the failing/changed tests**

In `web/src/views/StorageView.spec.js`, add the `RouterLinkStub` import and stub (matching `BackupPoliciesView.spec.js`'s existing pattern):

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import StorageView from './StorageView.vue'
import { useStoragePoliciesStore } from '../stores/storagePolicies'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { storagePolicies: state } })
  const wrapper = mount(StorageView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
  })
  return { wrapper, storagePolicies: useStoragePoliciesStore() }
}
```

Replace the `'opens the modal in edit mode when a row is clicked'` test with:

```js
  it("links each policy's name to its detail page", () => {
    const { wrapper } = mountView({
      list: [{ id: 's1', name: 'east-1-storage', port: 9400, config: '{}' }],
      loading: false,
      error: null,
    })
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'east-1-storage')
    expect(link.props('to')).toEqual({ name: 'storage-detail', params: { id: 's1' } })
  })
```

Delete the `'calls update and closes the modal on save in edit mode'` test entirely — editing has moved to `StoragePolicyView.spec.js` (Task 6), which already covers it.

- [ ] **Step 2: Run tests to verify the expected failures**

Run: `npx vitest run src/views/StorageView.spec.js`
Expected: FAIL — the new "links each policy's name" test can't find a `RouterLinkStub` with that text yet (name column still triggers `openEdit`).

- [ ] **Step 3: Write the implementation**

Replace `web/src/views/StorageView.vue` in full:

```vue
<script setup>
import { onMounted, ref } from 'vue'
import { useStoragePoliciesStore } from '../stores/storagePolicies'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import StorageEditModal from '../components/storage/StorageEditModal.vue'

const storagePolicies = useStoragePoliciesStore()
const showModal = ref(false)
const serverError = ref('')

onMounted(() => {
  storagePolicies.fetchAll()
})

function storageBackend(configText) {
  try {
    return JSON.parse(configText).backend || '—'
  } catch {
    return '—'
  }
}

function targetHostname(clientFilters) {
  return clientFilters?.hostnames?.[0] || '—'
}

function openCreate() {
  serverError.value = ''
  showModal.value = true
}

function closeModal() {
  showModal.value = false
  serverError.value = ''
}

async function save(payload) {
  try {
    await storagePolicies.create(payload)
    closeModal()
  } catch {
    serverError.value = storagePolicies.error
  }
}

function confirmDelete(id) {
  if (window.confirm('Delete this storage policy?')) {
    storagePolicies.remove(id)
  }
}

const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'Target Hostname', field: 'targetHostname', sortable: false },
  { label: 'Port', field: 'port', sortable: true },
  { label: 'Storage Type', field: 'storageType', sortable: false },
  { label: '', field: 'actions', sortable: false },
]
</script>

<template>
  <div>
    <PageHeader title="Storage">
      <template #actions>
        <BaseButton data-test="storage-new" variant="primary" @click="openCreate">
          New Storage Policy
        </BaseButton>
      </template>
    </PageHeader>
    <StatusMessage
      :loading="storagePolicies.loading"
      :error="storagePolicies.error"
      :empty="storagePolicies.list.length === 0"
      empty-text="No storage policies defined yet."
    >
      <DataTable :columns="columns" :rows="storagePolicies.list">
        <template #table-row="{ column, row }">
          <router-link
            v-if="column.field === 'name'"
            :to="{ name: 'storage-detail', params: { id: row.id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.name }}
          </router-link>
          <span v-else-if="column.field === 'storageType'">{{ storageBackend(row.config) }}</span>
          <span v-else-if="column.field === 'targetHostname'">{{ targetHostname(row.client_filters) }}</span>
          <BaseButton
            v-else-if="column.field === 'actions'"
            :data-test="`storage-delete-${row.id}`"
            variant="danger"
            @click="confirmDelete(row.id)"
          >
            Delete
          </BaseButton>
          <span v-else>{{ row[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
    <StorageEditModal
      v-if="showModal"
      :policy="null"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
    />
  </div>
</template>
```

Changes from the current file: the `editingPolicy` ref and `openEdit` function are removed (editing now happens only on `StoragePolicyView`); the name column is a `router-link` instead of a `<button>`; `StorageEditModal`'s `:policy` prop is always `null` (create-only, matching `BackupPoliciesView.vue`'s existing list-page pattern); `save()` always calls `storagePolicies.create`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `npx vitest run src/views/StorageView.spec.js`
Expected: PASS (all remaining/updated tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/views/StorageView.vue web/src/views/StorageView.spec.js
git commit -m "feat(web): navigate to the storage policy detail page from the list"
```

---

### Task 8: Documentation and changelog

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

**Interfaces:** N/A (documentation only).

- [ ] **Step 1: Update `docs/components/web.md`**

Replace the `/policies/:id` bullet (currently line 38):

Old:
```
- `/policies/:id` — one policy's full record (client filters, object filters, backup window), with Edit and Delete buttons; Edit opens `BackupPolicyFormModal` pre-filled with the policy's current values (both "Save" and "Run now" are available here). No separate `/policies/new` or `/policies/:id/edit` routes.
```

New:
```
- `/policies/:id` — one policy's full record, in two tabs built on a reusable `Tabs` component
  (`components/ui/Tabs.vue`, active tab synced to `?tab=details`/`?tab=checkins` so either can be
  linked directly): `Details` (the default — client filters, object filters, backup window) and
  `Check-ins` (`components/policies/PolicyCheckins.vue` — every host that has received this policy
  from `policy-server`, each with its most recent check-in time, and a manual Refresh button that
  re-fetches the policy). Edit and Delete buttons sit at the page level, outside the tabs; Edit opens
  `BackupPolicyFormModal` pre-filled with the policy's current values (both "Save" and "Run now" are
  available here). No separate `/policies/new` or `/policies/:id/edit` routes.
```

Replace the `/storage` block (currently lines 39-48):

Old:
```
- `/storage` — every storage policy (name, target hostname, port, storage type), with a "New Storage
  Policy" action and a click-to-edit name column, both opening the same `StorageEditModal` (fields:
  name, target hostname, port, storage type — `filesystem` only today — and, when `filesystem` is
  selected, a filesystem path). "Target hostname" submits as `client_filters.hostnames` — the same
  targeting mechanism `/policies` uses, not a separate field. Its own component folder
  (`components/storage/`) and no detail or form routes of its own — list and modal only — but its
  store (`stores/storagePolicies.js`) is no longer read exclusively by `/storage`: `/policies`' form
  modal also reads it to populate its destination select (see the `/policies` bullet above).
  `/policies` itself still requests only `type=backup` policies, so a storage policy never appears
  in its list.
```

New:
```
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
```

`README.md` does not need changes: it only lists `policy-server`/`api-server` at the
component/architecture level (`grep -n -i "polic\|storage" README.md`), with no mention of specific
web routes, list/modal/tab behavior, or anything else this task touches.

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Add this new section at the top of `CHANGELOG.md`, directly under the `# Changelog` header/intro (above the `## 2026-08-03 — policy-server: check-in tracking and cleanup` entry):

```
## 2026-08-03 — web: unified policy detail tabs with check-in visibility

Both `/policies/:id` and the new `/storage/:id` now share a `Details`/`Check-ins` tabbed layout,
built on a reusable `Tabs` component (`components/ui/Tabs.vue`) that syncs the active tab to the
URL. The `Check-ins` tab (`components/policies/PolicyCheckins.vue`) surfaces the check-in data
`policy-server`/`api-server` started tracking earlier today -- every host that has received a
policy, with its most recent check-in time and a manual Refresh button. Storage policies get a
details page for the first time, closing the gap with backup policies: the `/storage` list's name
column now navigates there instead of opening the edit modal inline.
```

- [ ] **Step 3: Verify the full frontend test suite still passes**

Run (from `web/`): `npm test`
Expected: PASS, all suites.

- [ ] **Step 4: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document unified policy detail tabs"
```
