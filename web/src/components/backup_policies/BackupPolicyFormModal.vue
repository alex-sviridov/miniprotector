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
