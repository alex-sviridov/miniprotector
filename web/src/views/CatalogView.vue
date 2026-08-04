<script setup>
import { computed, reactive, ref } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'
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
    <PageHeader title="Catalog" :crumbs="[{ label: 'Catalog' }]" />
    <form @submit.prevent="submit" class="flex gap-2 mb-4">
      <input v-model="form.sourceHost" placeholder="source host" class="border rounded px-2 py-1" />
      <input v-model="form.storeHost" placeholder="store host" class="border rounded px-2 py-1" />
      <input v-model="form.pattern" placeholder="path pattern" class="border rounded px-2 py-1" />
      <BaseButton type="submit" variant="primary" :disabled="!canSearch">
        Search
      </BaseButton>
    </form>
    <p v-if="!hasSearched && !catalog.error" class="text-gray-500">Enter a filter and search.</p>
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
