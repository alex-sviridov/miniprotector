# Backup Policies View Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consolidate the Backup Policies pages into the same list+modal pattern already used by Storage Policies, add a "Run now" adhoc-backup action to the shared form, and rename the two remaining views (`BackupPoliciesView`, `BackupPolicyView`) for list/detail symmetry.

**Architecture:** A new `BackupPolicyFormModal.vue` (in a new `components/backup_policies/` directory) replaces `PolicyFormView.vue`, reused by both the renamed list view (create mode) and the renamed detail view (edit mode). The modal emits `save`/`run-now` payloads; each hosting view calls the corresponding Pinia store action and handles navigation. `policy-new` and `policy-edit` routes are removed.

**Tech Stack:** Vue 3 `<script setup>`, Vue Router 4, Pinia, Vitest + `@vue/test-utils`, `@pinia/testing`.

## Global Constraints

- Shared/generic components live in `web/src/components/ui/`; view-specific components for this feature live in `web/src/components/backup_policies/` (per user instruction).
- View renames are final: `PoliciesListView.vue` → `BackupPoliciesView.vue`, `PolicyDetailView.vue` → `BackupPolicyView.vue` (per user instruction, for list/detail naming symmetry).
- "New backup" is the exact required label for the list page's create button (per user instruction).
- Follow Vue 3 best practices already established in this codebase: `<script setup>`, template refs (not DOM traversal from event handlers), props-down/emits-up for the modal, no direct DOM manipulation.
- Per `.claude/CLAUDE.md`: any feature change requires updating `docs/components/<component>.md` and `README.md` if affected, and a `CHANGELOG.md` entry before merging to `main`.
- Run tests from the `web/` directory: `npx vitest run <path>` for a single file, `npm test` for the full suite.

---

### Task 1: Add `runAdhoc` to the policies store

**Files:**
- Modify: `web/src/stores/policies.js`
- Test: `web/src/stores/policies.spec.js`

**Interfaces:**
- Produces: `usePoliciesStore().runAdhoc(payload: object) => Promise<policyDTO>` — POSTs `payload` (same shape as `create`/`update` payloads) to `/policies/adhoc`, does not mutate `list` or `byId`, uses `withRequest` with default options (rethrows on failure, matching `create`/`update`).

- [ ] **Step 1: Write the failing tests**

Add to `web/src/stores/policies.spec.js`, after the existing `remove` tests (before the closing `})`):

```js
  it('runAdhoc posts to the adhoc endpoint without touching list or byId', async () => {
    const created = { id: 'adhoc1', name: 'adhoc_oneoff' }
    apiFetch.mockResolvedValue(created)
    const policies = usePoliciesStore()

    const input = {
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
    const result = await policies.runAdhoc(input)

    expect(apiFetch).toHaveBeenCalledWith('/policies/adhoc', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(created)
    expect(policies.list).toEqual([])
    expect(policies.byId).toEqual({})
  })

  it('runAdhoc records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('destination is required'))
    const policies = usePoliciesStore()

    await expect(policies.runAdhoc({ name: 'oneoff' })).rejects.toThrow('destination is required')
    expect(policies.error).toBe('destination is required')
  })
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/stores/policies.spec.js`
Expected: FAIL — `policies.runAdhoc is not a function`

- [ ] **Step 3: Implement `runAdhoc`**

In `web/src/stores/policies.js`, add this action after `remove`, inside the `actions` object:

```js
    async runAdhoc(payload) {
      return withRequest(this, async () => {
        return apiFetch('/policies/adhoc', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        })
      })
    },
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/stores/policies.spec.js`
Expected: PASS (all tests in the file)

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/policies.js web/src/stores/policies.spec.js
git commit -m "feat(web): add runAdhoc action to the policies store"
```

---

### Task 2: Create `BackupPolicyFormModal.vue`

**Files:**
- Create: `web/src/components/backup_policies/BackupPolicyFormModal.vue`
- Test: `web/src/components/backup_policies/BackupPolicyFormModal.spec.js`

**Interfaces:**
- Consumes: `RepeatableFieldList` (`web/src/components/ui/RepeatableFieldList.vue`, props `items`/`newItem`/`addLabel`/`removeLabel`/`rowClass`/`testPrefix`), `BaseButton` (`web/src/components/ui/BaseButton.vue`, props `variant`/`type`).
- Produces: component `BackupPolicyFormModal` with:
  - Props: `policy: Object | null` (default `null` — `null` means create mode), `serverError: String` (default `''`).
  - Emits: `close`, `save(payload)`, `run-now(payload)` — `payload` shape: `{ name, client_filters: { hostnames: string[], labels: Record<string,string> }, object_filters: { path, include: string[], exclude: string[] }[], rpo, backup_window: string[], destination }`.
  - Both `save` and `run-now` are gated by `formEl.reportValidity()` (native HTML5 validation via the `required` Name field) before emitting.

- [ ] **Step 1: Write the failing test file**

Create `web/src/components/backup_policies/BackupPolicyFormModal.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BackupPolicyFormModal from './BackupPolicyFormModal.vue'

describe('BackupPolicyFormModal', () => {
  it('renders empty fields in create mode', () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    expect(wrapper.find('input[name="name"]').element.value).toBe('')
    expect(wrapper.find('input[name="rpo"]').element.value).toBe('')
    expect(wrapper.find('input[name="destination"]').element.value).toBe('')
    expect(wrapper.findAll('[data-test="hostname-input"]')).toHaveLength(0)
  })

  it('pre-fills fields from the policy prop in edit mode', () => {
    const wrapper = mount(BackupPolicyFormModal, {
      props: {
        policy: {
          id: 'p1',
          name: 'nightly-db-backup',
          rpo: '1h',
          destination: 'store:8080',
          client_filters: { hostnames: ['database'], labels: { env: 'prod' } },
          object_filters: [{ id: 'f1', path: '/var/lib/dbdata', include: ['*.sql'], exclude: [] }],
          backup_window: ['0 2 * * *'],
        },
      },
    })
    expect(wrapper.find('input[name="name"]').element.value).toBe('nightly-db-backup')
    expect(wrapper.find('input[name="rpo"]').element.value).toBe('1h')
    expect(wrapper.find('input[name="destination"]').element.value).toBe('store:8080')
    expect(wrapper.find('[data-test="hostname-input"]').element.value).toBe('database')
    expect(wrapper.find('[data-test="label-key-input"]').element.value).toBe('env')
    expect(wrapper.find('[data-test="label-value-input"]').element.value).toBe('prod')
    expect(wrapper.find('[data-test="filter-path-input"]').element.value).toBe('/var/lib/dbdata')
    expect(wrapper.find('[data-test="filter-include-input"]').element.value).toBe('*.sql')
    expect(wrapper.find('[data-test="window-input"]').element.value).toBe('0 2 * * *')
  })

  it('emits close when Cancel is clicked', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('[data-test="backup-policy-cancel"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on Escape', () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('does not emit save when the name is blank', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('emits save with the built payload on valid submit', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('nightly-db-backup')
    await wrapper.find('input[name="rpo"]').setValue('1h')
    await wrapper.find('input[name="destination"]').setValue('store:8080')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    })
  })

  it('adds and removes hostname rows, sending only non-empty trimmed values', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    const hostnameInputs = wrapper.findAll('[data-test="hostname-input"]')
    await hostnameInputs[0].setValue('database')
    await hostnameInputs[1].setValue('  ')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({ client_filters: { hostnames: ['database'], labels: {} } })
    )
  })

  it('adds a label row and sends it as a key/value map', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="label-add"]').trigger('click')
    await wrapper.find('[data-test="label-key-input"]').setValue('env')
    await wrapper.find('[data-test="label-value-input"]').setValue('prod')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({ client_filters: { hostnames: [], labels: { env: 'prod' } } })
    )
  })

  it('adds a backup window row', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="window-add"]').trigger('click')
    await wrapper.find('[data-test="window-input"]').setValue('0 2 * * *')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(expect.objectContaining({ backup_window: ['0 2 * * *'] }))
  })

  it('adds an object filter and splits comma-separated include/exclude into arrays', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="filter-add"]').trigger('click')
    await wrapper.find('[data-test="filter-path-input"]').setValue('/var/lib/dbdata')
    await wrapper.find('[data-test="filter-include-input"]').setValue('*.sql, *.dump')
    await wrapper.find('[data-test="filter-exclude-input"]').setValue('*.tmp')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({
        object_filters: [{ path: '/var/lib/dbdata', include: ['*.sql', '*.dump'], exclude: ['*.tmp'] }],
      })
    )
  })

  it('removes a row via its remove button', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    await wrapper.find('[data-test="hostname-input"]').setValue('database')
    await wrapper.find('[data-test="hostname-remove"]').trigger('click')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({ client_filters: { hostnames: [], labels: {} } })
    )
  })

  it('shows the server error message', () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null, serverError: 'name is required' } })
    expect(wrapper.text()).toContain('name is required')
  })

  it('emits run-now with the built payload when clicked with a valid form', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('oneoff')
    await wrapper.find('input[name="destination"]').setValue('store:8080')
    await wrapper.find('[data-test="backup-policy-run-now"]').trigger('click')

    expect(wrapper.emitted('run-now')).toHaveLength(1)
    expect(wrapper.emitted('run-now')[0][0]).toEqual({
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '',
      backup_window: [],
      destination: 'store:8080',
    })
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('does not emit run-now when the name is blank', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('[data-test="backup-policy-run-now"]').trigger('click')
    expect(wrapper.emitted('run-now')).toBeUndefined()
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/components/backup_policies/BackupPolicyFormModal.spec.js`
Expected: FAIL — cannot find module `./BackupPolicyFormModal.vue`

- [ ] **Step 3: Write the component**

Create `web/src/components/backup_policies/BackupPolicyFormModal.vue`:

```vue
<!-- web/src/components/backup_policies/BackupPolicyFormModal.vue -->
<script setup>
import { reactive, ref, onMounted, onBeforeUnmount } from 'vue'
import RepeatableFieldList from '../ui/RepeatableFieldList.vue'
import BaseButton from '../ui/BaseButton.vue'

const props = defineProps({
  policy: { type: Object, default: null },
  serverError: { type: String, default: '' },
})
const emit = defineEmits(['close', 'save', 'run-now'])

function toFormShape(policy) {
  if (!policy) {
    return {
      name: '',
      client_filters: { hostnames: [], labels: [] },
      object_filters: [],
      rpo: '',
      backup_window: [],
      destination: '',
    }
  }
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

const form = reactive(toFormShape(props.policy))
const errors = reactive({ message: '' })
const formEl = ref(null)

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

function submit() {
  errors.message = ''
  if (!formEl.value.reportValidity()) return
  emit('save', buildPayload())
}

function runNow() {
  errors.message = ''
  if (!formEl.value.reportValidity()) return
  emit('run-now', buildPayload())
}
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-2xl w-full max-h-[90vh] overflow-y-auto">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">{{ policy ? 'Edit Backup Policy' : 'New Backup Policy' }}</h2>
        <BaseButton variant="secondary" data-test="backup-policy-cancel" @click="close">Cancel</BaseButton>
      </div>
      <p v-if="errors.message || serverError" class="text-red-600 mb-4">{{ errors.message || serverError }}</p>
      <form ref="formEl" @submit.prevent="submit" class="space-y-6">
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

        <div class="flex gap-2">
          <BaseButton type="submit" variant="primary">
            {{ policy ? 'Save Changes' : 'Create Backup Policy' }}
          </BaseButton>
          <BaseButton data-test="backup-policy-run-now" variant="secondary" @click="runNow">
            Run now
          </BaseButton>
        </div>
      </form>
    </div>
  </div>
</template>
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/components/backup_policies/BackupPolicyFormModal.spec.js`
Expected: PASS (all tests in the file)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/backup_policies/BackupPolicyFormModal.vue web/src/components/backup_policies/BackupPolicyFormModal.spec.js
git commit -m "feat(web): add BackupPolicyFormModal with a Run now action"
```

---

### Task 3: Rename `PoliciesListView.vue` → `BackupPoliciesView.vue`, wire up the modal

**Files:**
- Rename: `web/src/views/PoliciesListView.vue` → `web/src/views/BackupPoliciesView.vue`
- Rename: `web/src/views/PoliciesListView.spec.js` → `web/src/views/BackupPoliciesView.spec.js`
- Modify: `web/src/router.js:11` (repoint the `policies` route's component import)

**Interfaces:**
- Consumes: `BackupPolicyFormModal` (Task 2) with props `policy`/`serverError`, emits `close`/`save`/`run-now`; `usePoliciesStore().create(payload)`, `.runAdhoc(payload)` (Task 1); `useRouter()` from `vue-router`.

- [ ] **Step 1: Rename the files**

```bash
git mv web/src/views/PoliciesListView.vue web/src/views/BackupPoliciesView.vue
git mv web/src/views/PoliciesListView.spec.js web/src/views/BackupPoliciesView.spec.js
```

- [ ] **Step 2: Rewrite the failing test file**

Replace the full contents of `web/src/views/BackupPoliciesView.spec.js`:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import BackupPoliciesView from './BackupPoliciesView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(BackupPoliciesView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
  })
  return { wrapper, policies: usePoliciesStore() }
}

describe('BackupPoliciesView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    push.mockReset()
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

  it('opens the modal in create mode when "New backup" is clicked', async () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    await wrapper.find('[data-test="policy-new"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('policy')).toBeNull()
  })

  it('calls create, closes the modal, and navigates to the detail page on save', async () => {
    const { wrapper, policies } = mountView({ list: [], loading: false, error: null })
    policies.create.mockResolvedValue({ id: 'p9', name: 'nightly-db-backup' })
    await wrapper.find('[data-test="policy-new"]').trigger('click')

    const payload = {
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(policies.create).toHaveBeenCalledWith(payload)
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
    expect(push).toHaveBeenCalledWith({ name: 'policy-detail', params: { id: 'p9' } })
  })

  it('keeps the modal open and shows the server error when create fails', async () => {
    const { wrapper, policies } = mountView({ list: [], loading: false, error: null })
    policies.create.mockImplementation(async () => {
      policies.error = 'name is required'
      throw new Error('name is required')
    })
    await wrapper.find('[data-test="policy-new"]').trigger('click')

    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', { name: '' })
    await nextTick()

    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('name is required')
  })

  it('closes the modal without saving on close', async () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    await wrapper.find('[data-test="policy-new"]').trigger('click')
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
  })

  it('calls runAdhoc, closes the modal, and navigates to jobs on run-now', async () => {
    const { wrapper, policies } = mountView({ list: [], loading: false, error: null })
    policies.runAdhoc.mockResolvedValue({ id: 'adhoc1' })
    await wrapper.find('[data-test="policy-new"]').trigger('click')

    const payload = {
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '',
      backup_window: [],
      destination: 'store:8080',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('run-now', payload)
    await nextTick()

    expect(policies.runAdhoc).toHaveBeenCalledWith(payload)
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
    expect(push).toHaveBeenCalledWith({ name: 'jobs' })
  })

  it('keeps the modal open and shows the server error when run-now fails', async () => {
    const { wrapper, policies } = mountView({ list: [], loading: false, error: null })
    policies.runAdhoc.mockImplementation(async () => {
      policies.error = 'destination is required'
      throw new Error('destination is required')
    })
    await wrapper.find('[data-test="policy-new"]').trigger('click')

    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('run-now', { name: 'oneoff' })
    await nextTick()

    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('destination is required')
    expect(push).not.toHaveBeenCalledWith({ name: 'jobs' })
  })
})
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd web && npx vitest run src/views/BackupPoliciesView.spec.js`
Expected: FAIL — no `[data-test="policy-new"]` element (the view still has the old router-link content)

- [ ] **Step 4: Rewrite the view**

Replace the full contents of `web/src/views/BackupPoliciesView.vue`:

```vue
<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import BackupPolicyFormModal from '../components/backup_policies/BackupPolicyFormModal.vue'

const router = useRouter()
const policies = usePoliciesStore()
const showModal = ref(false)
const serverError = ref('')

onMounted(() => {
  policies.fetchAll()
})

function confirmDelete(id) {
  if (window.confirm('Delete this policy?')) {
    policies.remove(id)
  }
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
    const policy = await policies.create(payload)
    closeModal()
    router.push({ name: 'policy-detail', params: { id: policy.id } })
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
        <BaseButton data-test="policy-new" variant="primary" @click="openCreate">
          New backup
        </BaseButton>
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
    <BackupPolicyFormModal
      v-if="showModal"
      :policy="null"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
      @run-now="runNow"
    />
  </div>
</template>
```

- [ ] **Step 5: Repoint the router**

In `web/src/router.js`, update the `policies` route (leave every other route untouched):

```js
    { path: '/policies', name: 'policies', component: () => import('./views/BackupPoliciesView.vue') },
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/views/BackupPoliciesView.spec.js src/router.spec.js`
Expected: PASS (both files)

- [ ] **Step 7: Commit**

```bash
git add web/src/views/BackupPoliciesView.vue web/src/views/BackupPoliciesView.spec.js web/src/router.js
git commit -m "feat(web): rename PoliciesListView to BackupPoliciesView, add New backup + Run now"
```

---

### Task 4: Rename `PolicyDetailView.vue` → `BackupPolicyView.vue`, wire up the modal

**Files:**
- Rename: `web/src/views/PolicyDetailView.vue` → `web/src/views/BackupPolicyView.vue`
- Rename: `web/src/views/PolicyDetailView.spec.js` → `web/src/views/BackupPolicyView.spec.js`
- Modify: `web/src/router.js:12` (repoint the `policy-detail` route's component import)

**Interfaces:**
- Consumes: `BackupPolicyFormModal` (Task 2); `usePoliciesStore().update(id, payload)`, `.runAdhoc(payload)` (Task 1); `useRoute()`/`useRouter()` from `vue-router`.

- [ ] **Step 1: Rename the files**

```bash
git mv web/src/views/PolicyDetailView.vue web/src/views/BackupPolicyView.vue
git mv web/src/views/PolicyDetailView.spec.js web/src/views/BackupPolicyView.spec.js
```

- [ ] **Step 2: Rewrite the failing test file**

Replace the full contents of `web/src/views/BackupPolicyView.spec.js`:

```js
// web/src/views/BackupPolicyView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import BackupPolicyView from './BackupPolicyView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 'p1' } }),
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(BackupPolicyView, { global: { plugins: [pinia] } })
  return { wrapper, policies: usePoliciesStore() }
}

describe('BackupPolicyView', () => {
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

  it('opens the modal in edit mode when Edit is clicked', async () => {
    const policy = { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} }
    const { wrapper } = mountView({ byId: { p1: policy }, loading: false, error: null })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('policy')).toEqual(policy)
  })

  it('calls update and closes the modal on save', async () => {
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.update.mockResolvedValue({ id: 'p1', name: 'renamed' })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')

    const payload = {
      name: 'renamed',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(policies.update).toHaveBeenCalledWith('p1', payload)
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
  })

  it('keeps the modal open and shows the server error when update fails', async () => {
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.update.mockImplementation(async () => {
      policies.error = 'invalid glob pattern'
      throw new Error('invalid glob pattern')
    })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')

    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', { name: 'bad' })
    await nextTick()

    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('invalid glob pattern')
  })

  it('calls runAdhoc, closes the modal, and navigates to jobs on run-now', async () => {
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.runAdhoc.mockResolvedValue({ id: 'adhoc1' })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')

    const payload = {
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('run-now', payload)
    await nextTick()

    expect(policies.runAdhoc).toHaveBeenCalledWith(payload)
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
    expect(push).toHaveBeenCalledWith({ name: 'jobs' })
  })

  it('closes the modal without saving on close', async () => {
    const { wrapper } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
  })
})
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd web && npx vitest run src/views/BackupPolicyView.spec.js`
Expected: FAIL — no `[data-test="policy-edit"]` element opens a modal (the view still router-links to the removed edit route)

- [ ] **Step 4: Rewrite the view**

Replace the full contents of `web/src/views/BackupPolicyView.vue`:

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
import BackupPolicyFormModal from '../components/backup_policies/BackupPolicyFormModal.vue'

const route = useRoute()
const router = useRouter()
const policies = usePoliciesStore()
const id = computed(() => route.params.id)
const policy = computed(() => policies.byId[id.value])

const showModal = ref(false)
const serverError = ref('')

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
        <BaseButton data-test="policy-edit" variant="secondary" @click="openEdit">Edit</BaseButton>
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

- [ ] **Step 5: Repoint the router**

In `web/src/router.js`, update the `policy-detail` route (leave every other route untouched):

```js
    { path: '/policies/:id', name: 'policy-detail', component: () => import('./views/BackupPolicyView.vue') },
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/views/BackupPolicyView.spec.js src/router.spec.js`
Expected: PASS (both files)

- [ ] **Step 7: Commit**

```bash
git add web/src/views/BackupPolicyView.vue web/src/views/BackupPolicyView.spec.js web/src/router.js
git commit -m "feat(web): rename PolicyDetailView to BackupPolicyView, open modal for Edit"
```

---

### Task 5: Remove the `policy-new`/`policy-edit` routes and `PolicyFormView`

**Files:**
- Delete: `web/src/views/PolicyFormView.vue`
- Delete: `web/src/views/PolicyFormView.spec.js`
- Modify: `web/src/router.js` (remove the `policy-new` and `policy-edit` route entries)
- Modify: `web/src/router.spec.js` (drop the two removed names from `EXPECTED_NAMES`)

**Interfaces:** None — this task only removes now-dead code and route entries. Both `PolicyFormView.vue`'s content (moved to `BackupPolicyFormModal.vue` in Task 2) and its routes (superseded by the modal wiring in Tasks 3–4) are fully replaced already.

- [ ] **Step 1: Update `router.spec.js` to drop the removed route names**

In `web/src/router.spec.js`, remove `'policy-new'` and `'policy-edit'` from `EXPECTED_NAMES`:

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
  'jobs',
  'job-detail',
]
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/router.spec.js`
Expected: FAIL — `router.getRoutes()` still returns `policy-new` and `policy-edit`, which are no longer in `EXPECTED_NAMES`

- [ ] **Step 3: Remove the routes and the dead view**

In `web/src/router.js`, delete these two lines:

```js
    { path: '/policies/new', name: 'policy-new', component: () => import('./views/PolicyFormView.vue') },
    { path: '/policies/:id/edit', name: 'policy-edit', component: () => import('./views/PolicyFormView.vue') },
```

```bash
git rm web/src/views/PolicyFormView.vue web/src/views/PolicyFormView.spec.js
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/router.spec.js`
Expected: PASS

- [ ] **Step 5: Run the full frontend test suite**

Run: `cd web && npm test`
Expected: PASS — every spec file, including the ones touched in Tasks 1–4

- [ ] **Step 6: Commit**

```bash
git add web/src/router.js web/src/router.spec.js
git commit -m "feat(web): remove policy-new/policy-edit routes and PolicyFormView"
```

---

### Task 6: Update documentation and changelog

**Files:**
- Modify: `docs/components/web.md` (Pages section, lines 37–40)
- Modify: `CHANGELOG.md`

**Interfaces:** None — documentation only.

- [ ] **Step 1: Update `docs/components/web.md`**

Replace the `/policies` bullet block (currently lines 37–40):

```
- `/policies` — every policy (name, RPO, destination), linking to:
- `/policies/:id` — one policy's full record (client filters, object filters, backup window)
- `/policies/new` — create a new policy
- `/policies/:id/edit` — edit an existing policy
```

with:

```
- `/policies` — every backup policy (name, RPO, destination), with a "New backup" action opening
  `BackupPolicyFormModal`, linking to:
- `/policies/:id` — one policy's full record (client filters, object filters, backup window), with
  an "Edit" action opening the same `BackupPolicyFormModal` pre-filled. No detail-page `/edit` route
  of its own — same list+modal pattern as `/storage` below, but keeping its read-only detail page.
  The form also has a "Run now" button, independent of Save: it fires the current form's fields at
  `POST /api/v1/policies/adhoc` (a one-time backup, ignoring rpo/backup_window since the server
  computes those) and redirects to `/jobs` on success. Fields and modal live in
  `components/backup_policies/`.
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Add to the top of `CHANGELOG.md`, right after the `# Changelog` header and its intro line:

```
## 2026-08-02 — web: backup policies view redesign with Run now

Backup Policies moves to the same list+modal pattern already used by Storage Policies:
`/policies/new` and `/policies/:id/edit` are gone, replaced by a shared `BackupPolicyFormModal`
opened from either the list ("New backup") or the detail page ("Edit"). The modal adds a "Run now"
button next to Save that fires the current form's fields at `POST /api/v1/policies/adhoc`
independent of saving, then redirects to `/jobs`. `PoliciesListView`/`PolicyDetailView` are renamed
to `BackupPoliciesView`/`BackupPolicyView` for list/detail symmetry, and `PolicyFormView` is removed
in favor of the modal.
```

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document backup policies view redesign and Run now action"
```
