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
