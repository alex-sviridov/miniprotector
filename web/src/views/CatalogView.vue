<script setup>
import { computed, onBeforeUnmount, nextTick, reactive, ref } from 'vue'
import { DataTable } from 'simple-datatables'
import 'simple-datatables/dist/style.css'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'
import VersionsModal from '../components/VersionsModal.vue'

const catalog = useCatalogStore()
const form = reactive({ sourceHost: '', storeHost: '', pattern: '' })
const tableRef = ref(null)
const groups = ref([])
const hasSearched = ref(false)
const selectedGroup = ref(null)
let dataTable = null

const canSearch = computed(() => Boolean(form.sourceHost || form.storeHost || form.pattern))

function destroyTable() {
  if (dataTable) {
    dataTable.destroy()
    dataTable = null
  }
}

async function renderTable() {
  groups.value = groupEntriesByFile(catalog.entries)
  destroyTable()
  await nextTick()
  if (tableRef.value) {
    dataTable = new DataTable(tableRef.value, { searchable: false, perPage: 25 })
    dataTable.on('datatable.selectrow', (rowIndex) => {
      const group = groups.value[rowIndex]
      if (group && group.versions.length > 1) {
        selectedGroup.value = group
      }
    })
  }
}

async function submit() {
  if (!canSearch.value) return
  hasSearched.value = true
  await catalog.search({ ...form })
  await renderTable()
}

onBeforeUnmount(() => {
  destroyTable()
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Catalog</h1>
    <form @submit.prevent="submit" class="flex gap-2 mb-4">
      <input v-model="form.sourceHost" placeholder="source host" class="border rounded px-2 py-1" />
      <input v-model="form.storeHost" placeholder="store host" class="border rounded px-2 py-1" />
      <input v-model="form.pattern" placeholder="path pattern" class="border rounded px-2 py-1" />
      <button
        type="submit"
        :disabled="!canSearch"
        class="bg-blue-600 text-white rounded px-3 py-1 disabled:opacity-50"
      >
        Search
      </button>
    </form>
    <p v-if="catalog.loading">Loading...</p>
    <p v-else-if="catalog.error" class="text-red-600">{{ catalog.error }}</p>
    <p v-else-if="!hasSearched" class="text-gray-500">Enter a filter and search.</p>
    <p v-else-if="groups.length === 0" class="text-gray-500">No entries match this filter.</p>
    <!-- simple-datatables replaces this subtree's DOM internally; wrapping it in its own div ensures Vue's v-if/v-else unmount removes the whole thing cleanly on every re-search, instead of leaving the library's injected wrapper orphaned as a sibling of the form above. -->
    <div v-else>
      <table ref="tableRef" class="w-full text-left border-collapse">
        <thead>
          <tr class="border-b">
            <th class="py-2 pr-4">Path</th>
            <th class="py-2 pr-4">Source Host</th>
            <th class="py-2 pr-4">Store Host</th>
            <th class="py-2 pr-4">Size</th>
            <th class="py-2 pr-4">Mode</th>
            <th class="py-2 pr-4">Modified</th>
            <th class="py-2 pr-4">Versions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="group in groups" :key="`${group.sourceHost}|${group.path}`" class="border-b">
            <td class="py-2 pr-4">{{ group.path }}</td>
            <td class="py-2 pr-4">{{ group.sourceHost }}</td>
            <td class="py-2 pr-4">{{ group.representative.store_host }}</td>
            <td class="py-2 pr-4">{{ formatBytes(group.representative.size) }}</td>
            <td class="py-2 pr-4">{{ group.representative.mode }}</td>
            <td class="py-2 pr-4">{{ formatTimestamp(group.representative.mod_time) || '—' }}</td>
            <td class="py-2 pr-4">{{ group.versions.length > 1 ? group.versions.length : '' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <VersionsModal v-if="selectedGroup" :group="selectedGroup" @close="selectedGroup = null" />
  </div>
</template>
