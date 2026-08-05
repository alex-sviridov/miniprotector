<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import DateRangePanel from '../components/catalog/DateRangePanel.vue'
import FacetPanel from '../components/catalog/FacetPanel.vue'
import VersionsModal from '../components/VersionsModal.vue'

const catalog = useCatalogStore()
const activePanel = ref('date')
const selectedGroup = ref(null)

const groups = computed(() => groupEntriesByFile(catalog.entries))

function summaryLabel(names, allLabel) {
  if (names.length === 0) return allLabel
  if (names.length <= 2) return names.join(', ')
  return `${names.length} selected`
}
const clientsSummary = computed(() => summaryLabel(catalog.filters.sourceHosts, 'All hosts'))
const jobsSummary = computed(() => summaryLabel(catalog.filters.jobNames, 'All policies'))
const dateSummary = computed(() => {
  const days = Math.round((catalog.filters.receivedBefore - catalog.filters.receivedAfter) / 86400)
  return `Last ${days} day${days === 1 ? '' : 's'}`
})

function togglePanel(name) {
  activePanel.value = activePanel.value === name ? null : name
}

function onRowClick(group) {
  if (group.versions.length > 1) selectedGroup.value = group
}

onMounted(() => {
  catalog.search()
  catalog.fetchClientFacets()
  catalog.fetchJobFacets()
})

let pathDebounce
watch(
  () => catalog.filters.pattern,
  () => {
    clearTimeout(pathDebounce)
    pathDebounce = setTimeout(() => {
      catalog.search()
      catalog.fetchClientFacets()
      catalog.fetchJobFacets()
    }, 300)
  }
)
onUnmounted(() => clearTimeout(pathDebounce))
watch(
  () => [catalog.filters.receivedAfter, catalog.filters.receivedBefore],
  () => {
    catalog.search()
    catalog.fetchClientFacets()
    catalog.fetchJobFacets()
  }
)
watch(
  () => catalog.filters.jobNames,
  () => {
    catalog.search()
    catalog.fetchClientFacets()
  },
  { deep: true }
)
watch(
  () => catalog.filters.sourceHosts,
  () => {
    catalog.search()
    catalog.fetchJobFacets()
  },
  { deep: true }
)

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

    <div class="mb-4">
      <div class="flex gap-2 mb-2">
        <button
          type="button"
          data-test="chip-date"
          class="flex-1 border rounded px-3 py-2 text-left"
          :class="{ 'border-blue-500': activePanel === 'date' }"
          @click="togglePanel('date')"
        >
          <div class="text-xs uppercase text-gray-500">Date range</div>
          <div>{{ dateSummary }}</div>
        </button>
      </div>
      <div class="flex gap-2 mb-2">
        <button
          type="button"
          data-test="chip-clients"
          class="flex-1 border rounded px-3 py-2 text-left"
          :class="{ 'border-blue-500': activePanel === 'clients' }"
          @click="togglePanel('clients')"
        >
          <div class="text-xs uppercase text-gray-500">Clients</div>
          <div>{{ clientsSummary }}</div>
        </button>
        <button
          type="button"
          data-test="chip-jobs"
          class="flex-1 border rounded px-3 py-2 text-left"
          :class="{ 'border-blue-500': activePanel === 'jobs' }"
          @click="togglePanel('jobs')"
        >
          <div class="text-xs uppercase text-gray-500">Job / Policy</div>
          <div>{{ jobsSummary }}</div>
        </button>
      </div>
      <div class="mb-2">
        <input
          data-test="path-input"
          :value="catalog.filters.pattern"
          @input="catalog.filters.pattern = $event.target.value"
          placeholder="Path contains…"
          class="border rounded px-2 py-1 w-full"
        />
      </div>

      <DateRangePanel
        v-if="activePanel === 'date'"
        v-model:received-after="catalog.filters.receivedAfter"
        v-model:received-before="catalog.filters.receivedBefore"
      />
      <FacetPanel
        v-if="activePanel === 'clients'"
        :facets="catalog.clientFacets"
        :error="catalog.clientFacetsError"
        name-label="Client"
        count-label="Entries in range"
        v-model:selected="catalog.filters.sourceHosts"
      />
      <FacetPanel
        v-if="activePanel === 'jobs'"
        :facets="catalog.jobFacets"
        :error="catalog.jobFacetsError"
        name-label="Policy"
        count-label="Runs in range"
        v-model:selected="catalog.filters.jobNames"
      />
    </div>

    <StatusMessage
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
