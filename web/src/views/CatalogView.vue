<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { useRestoreCartStore } from '../stores/restoreCart'
import { resolveFile, resolveFolderState } from '../utils/restoreRules'
import { formatBytes, formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import DateRangePanel from '../components/catalog/DateRangePanel.vue'
import FacetPanel from '../components/catalog/FacetPanel.vue'
import DirectoryPathBar from '../components/catalog/DirectoryPathBar.vue'
import VersionsModal from '../components/VersionsModal.vue'
import TriStateCheckbox from '../components/ui/TriStateCheckbox.vue'

const catalog = useCatalogStore()
const restoreCart = useRestoreCartStore()
const activePanel = ref('date')
const selectedGroup = ref(null)

// browsing is true whenever we're not in the flat, cross-directory
// pattern-search mode -- the two are mutually exclusive (see the
// catalog store's refresh()).
const browsing = computed(() => !catalog.filters.pattern)

// Gated on `browsing` rather than relying solely on the store clearing
// directoryChildren -- filters.pattern updates (and therefore `browsing`)
// synchronously on every keystroke, but the store only clears
// directoryChildren inside the debounced refresh(). Without this gate,
// folder rows from the previously-browsed directory would linger for up
// to the debounce window after the user starts typing a pattern search.
const folderRows = computed(() =>
  browsing.value
    ? catalog.directoryChildren.map((d) => ({
        isFolder: true,
        path: d.path,
        name: d.name,
        file_count: d.file_count,
        last_seen: d.last_seen,
      }))
    : []
)
const fileRows = computed(() => groupEntriesByFile(catalog.entries).map((g) => ({ isFolder: false, ...g })))
// Folders always precede files.
const rows = computed(() => [...folderRows.value, ...fileRows.value])

function checkboxProps(row) {
  if (row.isFolder) {
    const state = resolveFolderState(restoreCart.rules, row.path)
    return { checked: state === 'checked', indeterminate: state === 'indeterminate' }
  }
  return { checked: resolveFile(restoreCart.rules, row.sourceHost, row.path), indeterminate: false }
}

function toggleSelection(row) {
  if (row.isFolder) restoreCart.toggleFolder(row.path)
  else restoreCart.toggleFile(row.sourceHost, row.path)
}

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

function onRowClick(row) {
  if (row.isFolder) {
    catalog.navigateTo(row.path)
    return
  }
  if (row.versions.length > 1) selectedGroup.value = row
}

function onPathBarNavigate(path) {
  if (path === null) catalog.navigateHome()
  else catalog.navigateTo(path)
}

onMounted(() => {
  catalog.refresh()
  catalog.fetchClientFacets()
  catalog.fetchJobFacets()
})

let pathDebounce
watch(
  () => catalog.filters.pattern,
  () => {
    clearTimeout(pathDebounce)
    pathDebounce = setTimeout(() => {
      catalog.refresh()
      catalog.fetchClientFacets()
      catalog.fetchJobFacets()
    }, 300)
  }
)
onUnmounted(() => clearTimeout(pathDebounce))
watch(
  () => [catalog.filters.receivedAfter, catalog.filters.receivedBefore],
  () => {
    catalog.refresh()
    catalog.fetchClientFacets()
    catalog.fetchJobFacets()
  }
)
watch(
  () => catalog.filters.jobNames,
  () => {
    catalog.refresh()
    catalog.fetchClientFacets()
  },
  { deep: true }
)
watch(
  () => catalog.filters.sourceHosts,
  () => {
    catalog.refresh()
    catalog.fetchJobFacets()
  },
  { deep: true }
)

const baseColumns = [
  { label: '', field: 'select', sortable: false },
  { label: 'Path', field: 'path', sortable: true },
  { label: 'Source Host', field: 'sourceHost', sortable: true },
  { label: 'Store Host', field: 'representative.store_host', sortable: true },
  { label: 'Size', field: 'representative.size', sortable: true, type: 'number' },
  { label: 'Mode', field: 'representative.mode', sortable: true },
  { label: 'Modified', field: 'representative.mod_time', sortable: true, type: 'number' },
  { label: 'Versions', field: 'versions', sortable: false },
]
// Sorting is disabled while browsing so folder rows stay pinned above
// file rows -- vue-good-table's per-column sort has no notion of
// "folders first," only a single ordering. Pattern-search mode is a
// flat file-only list, so sorting there is unaffected.
const columns = computed(() => (browsing.value ? baseColumns.map((c) => ({ ...c, sortable: false })) : baseColumns))
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

    <DirectoryPathBar v-if="browsing" :current-path="catalog.currentPath" @navigate="onPathBarNavigate" />

    <StatusMessage
      :loading="catalog.loading || catalog.directoryChildrenLoading"
      :error="catalog.error || catalog.directoryChildrenError"
      :empty="rows.length === 0"
      empty-text="No entries match this filter."
    >
      <DataTable :columns="columns" :rows="rows" :search-enabled="false" @row-click="onRowClick">
        <template #table-row="{ column, row }">
          <span v-if="column.field === 'select'">
            <TriStateCheckbox
              :data-test="row.isFolder ? `folder-checkbox-${row.path}` : `file-checkbox-${row.sourceHost}:${row.path}`"
              v-bind="checkboxProps(row)"
              @toggle="toggleSelection(row)"
            />
          </span>
          <template v-else-if="row.isFolder">
            <span v-if="column.field === 'path'" class="font-semibold">{{ row.name }}/</span>
            <span v-else-if="column.field === 'representative.mod_time'">{{ formatTimestamp(row.last_seen) || '—' }}</span>
            <span v-else-if="column.field === 'versions'">{{ row.file_count || '' }}</span>
            <span v-else></span>
          </template>
          <template v-else>
            <span v-if="column.field === 'path'">{{ browsing ? row.representative.short_filename : row.path }}</span>
            <span v-else-if="column.field === 'sourceHost'">{{ row.sourceHost }}</span>
            <span v-else-if="column.field === 'representative.store_host'">{{ row.representative.store_host }}</span>
            <span v-else-if="column.field === 'representative.size'">{{ formatBytes(row.representative.size) }}</span>
            <span v-else-if="column.field === 'representative.mode'">{{ row.representative.mode }}</span>
            <span v-else-if="column.field === 'representative.mod_time'">{{ formatTimestamp(row.representative.mod_time) || '—' }}</span>
            <span v-else-if="column.field === 'versions'">{{ row.versions.length > 1 ? row.versions.length : '' }}</span>
          </template>
        </template>
      </DataTable>
    </StatusMessage>
    <VersionsModal v-if="selectedGroup" :group="selectedGroup" @close="selectedGroup = null" />
  </div>
</template>
