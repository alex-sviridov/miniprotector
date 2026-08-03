# Backup Policy Form Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `BackupPolicyFormModal.vue` (and `StorageEditModal.vue`) shared form primitives, replace the free-text destination field with a required storage-policy select bound to `storage_policy_id`, and add basic guardrails — following Vue 3.5 best practices (`defineModel`, `inheritAttrs: false`).

**Architecture:** Three new thin wrapper components (`BaseField`, `BaseInput`, `BaseSelect`) in `web/src/components/ui/` replace the hand-rolled label/input markup in both modals. `BackupPolicyFormModal.vue`'s form state gains `storage_policy_id` (a Pinia-store-backed `<select>` over `storagePolicies.list`) in place of `destination`; edit-mode pre-fill is direct (`policy.storage_policy_id`), since the backend (`backup-policy-storage-link`, already merged into this branch's history) already resolves `destination` server-side from that field.

**Tech Stack:** Vue 3.5 (`<script setup>`, `defineModel()`), Pinia 2.2, Vitest + `@vue/test-utils` + `@pinia/testing`.

## Global Constraints

- This branch forked from `backup-policy-storage-link`, which already ships `storage_policy_id` on backup policies end to end (`api-server`'s `policyDTO`/`policyInput` both have it; `destination` is still present on read responses, server-derived, never accepted as create/update input).
- Every test in `web/` must pass: `cd web && npm test` (or the project's configured `vitest` invocation — check `web/package.json`'s `scripts.test`).
- Follow this repo's `.claude/CLAUDE.md` doc rule: a feature change requires the affected `docs/components/<component>.md` updated (here: `docs/components/web.md`).
- `CHANGELOG.md` is deliberately **not** touched by this plan — per the design doc, its entry is added once this branch and `backup-policy-storage-link` are both ready to merge together.
- Design reference: `docs/superpowers/specs/2026-08-03-backup-policy-form-improvements-design.md`.

---

## Task 1: Reusable form primitives — `BaseField`, `BaseInput`, `BaseSelect`

**Files:**
- Create: `web/src/components/ui/BaseField.vue`
- Create: `web/src/components/ui/BaseField.spec.js`
- Create: `web/src/components/ui/BaseInput.vue`
- Create: `web/src/components/ui/BaseInput.spec.js`
- Create: `web/src/components/ui/BaseSelect.vue`
- Create: `web/src/components/ui/BaseSelect.spec.js`

**Interfaces:**
- Produces: `BaseField` — props `{ label: String (required), required: Boolean (default false) }`, default slot for the control.
- Produces: `BaseInput` — `v-model` (via `defineModel`), `inheritAttrs: false` + `v-bind="$attrs"` on the `<input>` so `type`/`placeholder`/`required`/`pattern`/`name`/`data-test` pass through.
- Produces: `BaseSelect` — same shape as `BaseInput` but wraps `<select>`; default slot holds native `<option>` children.

- [ ] **Step 1: Write `BaseField.vue`**

```vue
<!-- web/src/components/ui/BaseField.vue -->
<script setup>
defineProps({
  label: { type: String, required: true },
  required: { type: Boolean, default: false },
})
</script>

<template>
  <div>
    <label class="block font-medium mb-1">
      {{ label }}<span v-if="required" class="text-red-600"> *</span>
    </label>
    <slot />
  </div>
</template>
```

- [ ] **Step 2: Write `BaseField.spec.js`**

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BaseField from './BaseField.vue'

describe('BaseField', () => {
  it('renders the label and slot content', () => {
    const wrapper = mount(BaseField, {
      props: { label: 'Name' },
      slots: { default: '<input data-test="name-input" />' },
    })
    expect(wrapper.text()).toContain('Name')
    expect(wrapper.find('[data-test="name-input"]').exists()).toBe(true)
  })

  it('shows a required asterisk when required is true', () => {
    const wrapper = mount(BaseField, { props: { label: 'Name', required: true } })
    expect(wrapper.find('.text-red-600').exists()).toBe(true)
  })

  it('omits the asterisk when required is false (the default)', () => {
    const wrapper = mount(BaseField, { props: { label: 'Name' } })
    expect(wrapper.find('.text-red-600').exists()).toBe(false)
  })
})
```

- [ ] **Step 3: Run the new test**

Run: `cd web && npx vitest run src/components/ui/BaseField.spec.js`
Expected: 3/3 passing.

- [ ] **Step 4: Write `BaseInput.vue`**

```vue
<!-- web/src/components/ui/BaseInput.vue -->
<script setup>
defineOptions({ inheritAttrs: false })
const model = defineModel({ type: [String, Number], default: '' })
</script>

<template>
  <input v-model="model" v-bind="$attrs" class="w-full border rounded px-2 py-1" />
</template>
```

- [ ] **Step 5: Write `BaseInput.spec.js`**

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BaseInput from './BaseInput.vue'

describe('BaseInput', () => {
  it('renders the modelValue and emits update:modelValue on input', async () => {
    const wrapper = mount(BaseInput, { props: { modelValue: 'x' } })
    expect(wrapper.find('input').element.value).toBe('x')
    await wrapper.find('input').setValue('y')
    expect(wrapper.emitted('update:modelValue')[0]).toEqual(['y'])
  })

  it('passes through arbitrary attributes to the underlying input', () => {
    const wrapper = mount(BaseInput, {
      props: { modelValue: '' },
      attrs: { type: 'number', required: true, 'data-test': 'port-input', pattern: '[0-9]+' },
    })
    const input = wrapper.find('input')
    expect(input.attributes('type')).toBe('number')
    expect(input.attributes('required')).toBeDefined()
    expect(input.attributes('data-test')).toBe('port-input')
    expect(input.attributes('pattern')).toBe('[0-9]+')
  })

  it('applies the shared input styling', () => {
    const wrapper = mount(BaseInput, { props: { modelValue: '' } })
    expect(wrapper.find('input').classes()).toContain('border')
  })
})
```

- [ ] **Step 6: Run the new test**

Run: `cd web && npx vitest run src/components/ui/BaseInput.spec.js`
Expected: 3/3 passing.

- [ ] **Step 7: Write `BaseSelect.vue`**

```vue
<!-- web/src/components/ui/BaseSelect.vue -->
<script setup>
defineOptions({ inheritAttrs: false })
const model = defineModel({ type: String, default: '' })
</script>

<template>
  <select v-model="model" v-bind="$attrs" class="w-full border rounded px-2 py-1">
    <slot />
  </select>
</template>
```

- [ ] **Step 8: Write `BaseSelect.spec.js`**

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BaseSelect from './BaseSelect.vue'

describe('BaseSelect', () => {
  it('reflects modelValue in the rendered selection', () => {
    const wrapper = mount(BaseSelect, {
      props: { modelValue: 'b' },
      slots: { default: '<option value="a">A</option><option value="b">B</option>' },
    })
    expect(wrapper.find('select').element.value).toBe('b')
  })

  it('emits update:modelValue when the selection changes', async () => {
    const wrapper = mount(BaseSelect, {
      props: { modelValue: 'a' },
      slots: { default: '<option value="a">A</option><option value="b">B</option>' },
    })
    await wrapper.find('select').setValue('b')
    expect(wrapper.emitted('update:modelValue')[0]).toEqual(['b'])
  })

  it('passes through arbitrary attributes to the underlying select', () => {
    const wrapper = mount(BaseSelect, {
      props: { modelValue: '' },
      attrs: { required: true, 'data-test': 'storage-select' },
    })
    const select = wrapper.find('select')
    expect(select.attributes('required')).toBeDefined()
    expect(select.attributes('data-test')).toBe('storage-select')
  })
})
```

- [ ] **Step 9: Run the new test**

Run: `cd web && npx vitest run src/components/ui/BaseSelect.spec.js`
Expected: 3/3 passing.

- [ ] **Step 10: Commit**

```bash
cd /home/alex/miniprotector/.worktrees/backup-policy-form-improvements
git add web/src/components/ui/BaseField.vue web/src/components/ui/BaseField.spec.js \
        web/src/components/ui/BaseInput.vue web/src/components/ui/BaseInput.spec.js \
        web/src/components/ui/BaseSelect.vue web/src/components/ui/BaseSelect.spec.js
git commit -m "feat(web): add BaseField/BaseInput/BaseSelect reusable form primitives"
```

---

## Task 2: Refactor `StorageEditModal.vue` onto the new primitives

**Files:**
- Modify: `web/src/components/storage/StorageEditModal.vue`
- Test: `web/src/components/storage/StorageEditModal.spec.js` (existing — verify unmodified, no new assertions needed)

**Interfaces:**
- Consumes: `BaseField`, `BaseInput`, `BaseSelect` from Task 1.
- No change to this component's props (`policy`, `serverError`), emits (`close`, `save`), or the shape of the payload it emits — this task is a pure markup refactor.

- [ ] **Step 1: Confirm the current test suite is green before refactoring**

Run: `cd web && npx vitest run src/components/storage/StorageEditModal.spec.js`
Expected: all passing (this is the baseline you must not break).

- [ ] **Step 2: Replace the file's `<script setup>` imports and `<template>`**

Replace the whole file's contents with:

```vue
<!-- web/src/components/storage/StorageEditModal.vue -->
<script setup>
import { reactive, onMounted, onBeforeUnmount } from 'vue'
import BaseButton from '../ui/BaseButton.vue'
import BaseField from '../ui/BaseField.vue'
import BaseInput from '../ui/BaseInput.vue'
import BaseSelect from '../ui/BaseSelect.vue'

const props = defineProps({
  policy: { type: Object, default: null },
  serverError: { type: String, default: '' },
})
const emit = defineEmits(['close', 'save'])

function parseConfig(configText) {
  try {
    const c = JSON.parse(configText || '{}')
    return c && typeof c === 'object' ? c : {}
  } catch {
    return {}
  }
}

const form = reactive({
  name: props.policy?.name || '',
  targetHostname: props.policy?.client_filters?.hostnames?.[0] || '',
  port: props.policy ? String(props.policy.port) : '',
  storageType: parseConfig(props.policy?.config).backend || 'filesystem',
  path: parseConfig(props.policy?.config).root || '',
})

const errors = reactive({ message: '' })

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

function submit() {
  errors.message = ''
  const port = Number(form.port)

  if (!form.name.trim()) {
    errors.message = 'Name is required.'
    return
  }
  if (!form.targetHostname.trim()) {
    errors.message = 'Target hostname is required.'
    return
  }
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    errors.message = 'A valid port between 1 and 65535 is required.'
    return
  }
  if (!form.path.trim()) {
    errors.message = 'Filesystem path is required.'
    return
  }

  emit('save', {
    name: form.name.trim(),
    port,
    config: JSON.stringify({
      ...parseConfig(props.policy?.config),
      backend: form.storageType,
      root: form.path.trim(),
    }),
    client_filters: { hostnames: [form.targetHostname.trim()], labels: {} },
  })
}
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-lg w-full">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">{{ policy ? 'Edit Storage Policy' : 'New Storage Policy' }}</h2>
        <BaseButton variant="secondary" data-test="storage-cancel" @click="close">Cancel</BaseButton>
      </div>
      <p v-if="errors.message || serverError" class="text-red-600 mb-4">{{ errors.message || serverError }}</p>
      <form @submit.prevent="submit" class="space-y-4">
        <BaseField label="Name">
          <BaseInput data-test="storage-name-input" v-model="form.name" />
        </BaseField>
        <BaseField label="Target Hostname">
          <BaseInput data-test="storage-target-hostname-input" v-model="form.targetHostname" />
        </BaseField>
        <BaseField label="Port">
          <BaseInput data-test="storage-port-input" v-model="form.port" type="number" />
        </BaseField>
        <BaseField label="Storage Type">
          <BaseSelect data-test="storage-type-select" v-model="form.storageType">
            <option value="filesystem">filesystem</option>
          </BaseSelect>
        </BaseField>
        <BaseField v-if="form.storageType === 'filesystem'" label="Filesystem Path">
          <BaseInput data-test="storage-path-input" v-model="form.path" />
        </BaseField>
        <BaseButton type="submit" variant="primary">
          {{ policy ? 'Save Changes' : 'Create Storage Policy' }}
        </BaseButton>
      </form>
    </div>
  </div>
</template>
```

Note: every `data-test` attribute is preserved exactly, on the same logical field, so the existing spec file needs no changes — its selectors (`[data-test="storage-name-input"]`, etc.) resolve to the same underlying `<input>`/`<select>` elements as before, now rendered via `BaseInput`/`BaseSelect`'s `v-bind="$attrs"` passthrough. Do **not** touch the `RepeatableFieldList`-based fields in `BackupPolicyFormModal.vue` in this task — this task only touches `StorageEditModal.vue`.

- [ ] **Step 3: Run the existing test suite unmodified**

Run: `cd web && npx vitest run src/components/storage/StorageEditModal.spec.js`
Expected: same pass count as Step 1 — every test still passes with no changes to the spec file.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector/.worktrees/backup-policy-form-improvements
git add web/src/components/storage/StorageEditModal.vue
git commit -m "refactor(web): rebuild StorageEditModal on BaseField/BaseInput/BaseSelect"
```

---

## Task 3: Storage-driven destination + guardrails in `BackupPolicyFormModal.vue`

**Files:**
- Modify: `web/src/components/backup_policies/BackupPolicyFormModal.vue`
- Modify: `web/src/components/backup_policies/BackupPolicyFormModal.spec.js` (near-total rewrite — the destination field's shape changes from a text input to a store-backed select)
- Modify: `web/src/views/BackupPoliciesView.spec.js` (payload fixtures only)
- Modify: `web/src/views/BackupPolicyView.spec.js` (payload fixtures only)
- Modify: `docs/components/web.md`

**Interfaces:**
- Consumes: `BaseField`/`BaseInput`/`BaseSelect` (Task 1); `useStoragePoliciesStore` from `web/src/stores/storagePolicies.js` — `{ list, loading, error, fetchAll() }` (existing, unmodified).
- Produces: `BackupPolicyFormModal`'s `save`/`run-now` emit payload now has `storage_policy_id: string` in place of `destination: string` — every consumer (`BackupPoliciesView.vue`, `BackupPolicyView.vue`) already forwards the payload opaquely to `usePoliciesStore()`'s `create`/`update`/`runAdhoc`, so neither `.vue` file needs a code change, only their tests' payload fixtures.

- [ ] **Step 1: Confirm the current test suite is green before changing anything**

Run: `cd web && npx vitest run src/components/backup_policies/BackupPolicyFormModal.spec.js src/views/BackupPoliciesView.spec.js src/views/BackupPolicyView.spec.js`
Expected: all passing (baseline).

- [ ] **Step 2: Replace `BackupPolicyFormModal.vue`**

Replace the whole file's contents with:

```vue
<!-- web/src/components/backup_policies/BackupPolicyFormModal.vue -->
<script setup>
import { reactive, ref, onMounted, onBeforeUnmount } from 'vue'
import { useStoragePoliciesStore } from '../../stores/storagePolicies'
import RepeatableFieldList from '../ui/RepeatableFieldList.vue'
import BaseButton from '../ui/BaseButton.vue'
import BaseField from '../ui/BaseField.vue'
import BaseInput from '../ui/BaseInput.vue'
import BaseSelect from '../ui/BaseSelect.vue'

const props = defineProps({
  policy: { type: Object, default: null },
  serverError: { type: String, default: '' },
})
const emit = defineEmits(['close', 'save', 'run-now'])

const storagePolicies = useStoragePoliciesStore()

function toFormShape(policy) {
  if (!policy) {
    return {
      name: '',
      client_filters: { hostnames: [], labels: [] },
      object_filters: [],
      rpo: '',
      backup_window: [],
      storage_policy_id: '',
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
    storage_policy_id: policy.storage_policy_id || '',
  }
}

const form = reactive(toFormShape(props.policy))
const errors = reactive({ message: '' })
const formEl = ref(null)

// storageOptionLabel: "(incomplete)" covers a storage policy with no
// client_filters.hostnames or no port set -- still selectable (an operator
// may be about to fix it), just not hidden as if it didn't exist.
function storageOptionLabel(storagePolicy) {
  const hostname = storagePolicy.client_filters?.hostnames?.[0]
  const port = storagePolicy.port
  if (!hostname || !port) return `${storagePolicy.name} (incomplete)`
  return `${storagePolicy.name} (${hostname}:${port})`
}

function close() {
  emit('close')
}

function onKeydown(event) {
  if (event.key === 'Escape') close()
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
  if (storagePolicies.list.length === 0) {
    storagePolicies.fetchAll()
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})

function splitCsv(text) {
  return text.split(',').map((s) => s.trim()).filter(Boolean)
}

function buildPayload() {
  return {
    name: form.name.trim(),
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
    storage_policy_id: form.storage_policy_id,
  }
}

// validate: name is checked in JS (native `required` alone doesn't reject
// whitespace-only); everything else -- the destination select's `required`
// and the RPO pattern -- goes through the form's own native validity via
// reportValidity(), same mechanism this component already relied on.
function validate() {
  errors.message = ''
  if (!form.name.trim()) {
    errors.message = 'Name is required.'
    return false
  }
  return formEl.value.reportValidity()
}

function submit() {
  if (!validate()) return
  emit('save', buildPayload())
}

function runNow() {
  if (!validate()) return
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
        <BaseField label="Name" required>
          <BaseInput name="name" v-model="form.name" required />
        </BaseField>

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

        <BaseField label="RPO">
          <BaseInput
            name="rpo"
            v-model="form.rpo"
            placeholder="e.g. 24h"
            pattern="\d+(\.\d+)?(ns|us|µs|ms|s|m|h)(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))*"
            title="A duration like 24h, 30m, or 1h30m"
          />
        </BaseField>

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

        <BaseField label="Destination" required>
          <div class="flex gap-2 items-start">
            <BaseSelect
              data-test="backup-policy-storage-select"
              v-model="form.storage_policy_id"
              required
              class="flex-1"
            >
              <option value="" disabled>Select a storage policy</option>
              <option v-for="sp in storagePolicies.list" :key="sp.id" :value="sp.id">
                {{ storageOptionLabel(sp) }}
              </option>
            </BaseSelect>
            <BaseButton
              type="button"
              variant="secondary"
              data-test="backup-policy-storage-reload"
              :disabled="storagePolicies.loading"
              @click="storagePolicies.fetchAll()"
            >
              {{ storagePolicies.loading ? 'Reloading…' : 'Reload' }}
            </BaseButton>
          </div>
        </BaseField>

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

Do **not** convert the `RepeatableFieldList` row `<input>` elements (hostname/label/filter/window rows) to `BaseInput` — they have no per-row label, so `BaseField` doesn't fit them, and `RepeatableFieldList`'s own `#row` slot contract is unaffected either way. Leave those four groups' own `<label>` tags (`Hostnames (glob patterns)`, `Labels`, `Object Filters`, `Backup Window (cron expressions)`) as plain `<label>` too, for the same reason — they label a list, not a single control.

- [ ] **Step 3: Replace `BackupPolicyFormModal.spec.js`**

Replace the whole file's contents with:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import BackupPolicyFormModal from './BackupPolicyFormModal.vue'
import { useStoragePoliciesStore } from '../../stores/storagePolicies'

const defaultStorageState = {
  list: [{ id: 'sp1', name: 'store', client_filters: { hostnames: ['store'], labels: {} }, port: 8080 }],
  loading: false,
  error: null,
}

function mountModal(props, storageState = defaultStorageState) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { storagePolicies: storageState } })
  const wrapper = mount(BackupPolicyFormModal, { props, global: { plugins: [pinia] } })
  return { wrapper, storagePolicies: useStoragePoliciesStore() }
}

describe('BackupPolicyFormModal', () => {
  it('renders empty fields in create mode', () => {
    const { wrapper } = mountModal({ policy: null })
    expect(wrapper.find('input[name="name"]').element.value).toBe('')
    expect(wrapper.find('input[name="rpo"]').element.value).toBe('')
    expect(wrapper.find('[data-test="backup-policy-storage-select"]').element.value).toBe('')
    expect(wrapper.findAll('[data-test="hostname-input"]')).toHaveLength(0)
  })

  it('pre-fills fields from the policy prop in edit mode', () => {
    const { wrapper } = mountModal({
      policy: {
        id: 'p1',
        name: 'nightly-db-backup',
        rpo: '1h',
        storage_policy_id: 'sp1',
        client_filters: { hostnames: ['database'], labels: { env: 'prod' } },
        object_filters: [{ id: 'f1', path: '/var/lib/dbdata', include: ['*.sql'], exclude: [] }],
        backup_window: ['0 2 * * *'],
      },
    })
    expect(wrapper.find('input[name="name"]').element.value).toBe('nightly-db-backup')
    expect(wrapper.find('input[name="rpo"]').element.value).toBe('1h')
    expect(wrapper.find('[data-test="backup-policy-storage-select"]').element.value).toBe('sp1')
    expect(wrapper.find('[data-test="hostname-input"]').element.value).toBe('database')
    expect(wrapper.find('[data-test="label-key-input"]').element.value).toBe('env')
    expect(wrapper.find('[data-test="label-value-input"]').element.value).toBe('prod')
    expect(wrapper.find('[data-test="filter-path-input"]').element.value).toBe('/var/lib/dbdata')
    expect(wrapper.find('[data-test="filter-include-input"]').element.value).toBe('*.sql')
    expect(wrapper.find('[data-test="window-input"]').element.value).toBe('0 2 * * *')
  })

  it('emits close when Cancel is clicked', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('[data-test="backup-policy-cancel"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on Escape', () => {
    const { wrapper } = mountModal({ policy: null })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('does not emit save when the name is blank', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('does not emit save when no storage policy is selected', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('nightly-db-backup')
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('emits save with the built payload on valid submit', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('nightly-db-backup')
    await wrapper.find('input[name="rpo"]').setValue('1h')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      storage_policy_id: 'sp1',
    })
  })

  it('rejects a malformed RPO value', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('input[name="rpo"]').setValue('not-a-duration')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('accepts an empty RPO', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toHaveLength(1)
  })

  it('accepts a compound RPO duration like 1h30m', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('input[name="rpo"]').setValue('1h30m')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toHaveLength(1)
  })

  it('adds and removes hostname rows, sending only non-empty trimmed values', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
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
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('[data-test="label-add"]').trigger('click')
    await wrapper.find('[data-test="label-key-input"]').setValue('env')
    await wrapper.find('[data-test="label-value-input"]').setValue('prod')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({ client_filters: { hostnames: [], labels: { env: 'prod' } } })
    )
  })

  it('adds a backup window row', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('[data-test="window-add"]').trigger('click')
    await wrapper.find('[data-test="window-input"]').setValue('0 2 * * *')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(expect.objectContaining({ backup_window: ['0 2 * * *'] }))
  })

  it('adds an object filter and splits comma-separated include/exclude into arrays', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
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
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    await wrapper.find('[data-test="hostname-input"]').setValue('database')
    await wrapper.find('[data-test="hostname-remove"]').trigger('click')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({ client_filters: { hostnames: [], labels: {} } })
    )
  })

  it('shows the server error message', () => {
    const { wrapper } = mountModal({ policy: null, serverError: 'name is required' })
    expect(wrapper.text()).toContain('name is required')
  })

  it('emits run-now with the built payload when clicked with a valid form', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('oneoff')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('[data-test="backup-policy-run-now"]').trigger('click')

    expect(wrapper.emitted('run-now')).toHaveLength(1)
    expect(wrapper.emitted('run-now')[0][0]).toEqual({
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '',
      backup_window: [],
      storage_policy_id: 'sp1',
    })
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('does not emit run-now when the name is blank', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('[data-test="backup-policy-run-now"]').trigger('click')
    expect(wrapper.emitted('run-now')).toBeUndefined()
  })

  it('calls fetchAll on mount when the storage policies store is empty', () => {
    const { storagePolicies } = mountModal({ policy: null }, { list: [], loading: false, error: null })
    expect(storagePolicies.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('does not call fetchAll on mount when the storage policies store already has data', () => {
    const { storagePolicies } = mountModal({ policy: null })
    expect(storagePolicies.fetchAll).not.toHaveBeenCalled()
  })

  it('the Reload button calls fetchAll', async () => {
    const { wrapper, storagePolicies } = mountModal({ policy: null })
    await wrapper.find('[data-test="backup-policy-storage-reload"]').trigger('click')
    expect(storagePolicies.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('labels a complete storage policy option with its host:port', () => {
    const { wrapper } = mountModal({ policy: null })
    expect(wrapper.text()).toContain('store (store:8080)')
  })

  it('labels a storage policy missing a hostname or port as incomplete', () => {
    const { wrapper } = mountModal(
      { policy: null },
      {
        list: [{ id: 'sp2', name: 'broken', client_filters: { hostnames: [], labels: {} }, port: 0 }],
        loading: false,
        error: null,
      }
    )
    expect(wrapper.text()).toContain('broken (incomplete)')
  })
})
```

- [ ] **Step 4: Run the modal's own test suite**

Run: `cd web && npx vitest run src/components/backup_policies/BackupPolicyFormModal.spec.js`
Expected: all tests passing.

- [ ] **Step 5: Update the two consumer views' payload fixtures**

These two view components (`BackupPoliciesView.vue`, `BackupPolicyView.vue`) don't inspect the modal's emitted payload — they just forward it to the store — so they need no code changes. Their specs, though, construct a `payload` object standing in for "what the modal emits" to simulate a `save`/`run-now` event; that shape must track Step 2's change (`destination` → `storage_policy_id`).

In `web/src/views/BackupPoliciesView.spec.js`, in `'calls create, closes the modal, and navigates to the detail page on save'`, change:

```js
    const payload = {
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
```

to:

```js
    const payload = {
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      storage_policy_id: 'sp1',
    }
```

In the same file, in `'calls runAdhoc, closes the modal, and navigates to jobs on run-now'`, change:

```js
    const payload = {
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '',
      backup_window: [],
      destination: 'store:8080',
    }
```

to:

```js
    const payload = {
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '',
      backup_window: [],
      storage_policy_id: 'sp1',
    }
```

Leave every other `destination: 'store:8080'` in this file untouched — those appear in `list: [...]` fixtures representing data already loaded *from* the store (still a real, server-derived field on read), not a payload being emitted *to* it.

In `web/src/views/BackupPolicyView.spec.js`, in `'calls update and closes the modal on save'`, change:

```js
    const payload = {
      name: 'renamed',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
```

to:

```js
    const payload = {
      name: 'renamed',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      storage_policy_id: 'sp1',
    }
```

In the same file, in `'calls runAdhoc, closes the modal, and navigates to jobs on run-now'`, change:

```js
    const payload = {
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
```

to:

```js
    const payload = {
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      storage_policy_id: 'sp1',
    }
```

Leave the `destination: 'store:8080'` inside this file's `byId: { p1: { ... } }` fixture (the loaded-policy detail data) untouched, same reasoning as above.

- [ ] **Step 6: Run both view test suites**

Run: `cd web && npx vitest run src/views/BackupPoliciesView.spec.js src/views/BackupPolicyView.spec.js`
Expected: all passing.

- [ ] **Step 7: Update `docs/components/web.md`**

Find, in the `/policies` bullet:

```
opening a form modal for creating new policies (fields: name, RPO, backup window, client filters, object filters, destination)
```

Replace `destination` with `storage policy`, so it reads:

```
opening a form modal for creating new policies (fields: name, RPO, backup window, client filters, object filters, storage policy)
```

- [ ] **Step 8: Run the full web test suite**

Run: `cd web && npm test`
Expected: every test in `web/` passes, including everything from Tasks 1 and 2.

- [ ] **Step 9: Commit**

```bash
cd /home/alex/miniprotector/.worktrees/backup-policy-form-improvements
git add web/src/components/backup_policies/BackupPolicyFormModal.vue \
        web/src/components/backup_policies/BackupPolicyFormModal.spec.js \
        web/src/views/BackupPoliciesView.spec.js \
        web/src/views/BackupPolicyView.spec.js \
        docs/components/web.md
git commit -m "feat(web): storage-policy-driven destination select and form guardrails"
```

---

## Final check

- [ ] Run the full web suite one more time end to end: `cd web && npm test` — expect all green.
- [ ] Run `git log --oneline -4` and confirm three commits landed in order: primitives, StorageEditModal refactor, BackupPolicyFormModal + consumer tests + doc.
- [ ] Confirm `git status` is clean.
