<script setup>
import { reactive, computed, onMounted, nextTick } from 'vue'
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
    // Defer past the current microtask so tests that configure
    // `policies.fetchOne`'s mocked resolution immediately after mount()
    // (a synchronous call) still observe it on this call.
    await nextTick()
    try {
      const policy = await policies.fetchOne(route.params.id)
      Object.assign(form, toFormShape(policy))
    } catch {
      // error already recorded on policies.error by the store
    }
  }
})

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

      <div>
        <label class="block font-medium mb-1">RPO</label>
        <input name="rpo" v-model="form.rpo" placeholder="e.g. 24h" class="w-full border rounded px-2 py-1" />
      </div>

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
