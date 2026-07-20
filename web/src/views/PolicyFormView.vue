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
