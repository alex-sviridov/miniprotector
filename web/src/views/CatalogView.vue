<script setup>
import { onMounted, onBeforeUnmount, nextTick, reactive, ref } from 'vue'
import { DataTable } from 'simple-datatables'
import 'simple-datatables/dist/style.css'
import { useCatalogStore } from '../stores/catalog'
import { formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'

const catalog = useCatalogStore()
const form = reactive({ sourceHost: '', storeHost: '', pattern: '' })
const tableRef = ref(null)
const groups = ref([])
let dataTable = null

const selectedGroup = ref(null)

function openVersions(group) {
  selectedGroup.value = group
}

function closeVersions() {
  selectedGroup.value = null
}

function onKeydown(event) {
  if (event.key === 'Escape') closeVersions()
}

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
    dataTable = new DataTable(tableRef.value)
  }
}

async function submit() {
  await catalog.search({ ...form })
  await renderTable()
}

async function goNext() {
  await catalog.nextPage()
  await renderTable()
}

async function goPrev() {
  await catalog.prevPage()
  await renderTable()
}

onMounted(async () => {
  document.addEventListener('keydown', onKeydown)
  await catalog.search({ ...form })
  await renderTable()
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
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
      <button type="submit" class="bg-blue-600 text-white rounded px-3 py-1">Search</button>
    </form>
    <p v-if="catalog.loading">Loading...</p>
    <p v-else-if="catalog.error" class="text-red-600">{{ catalog.error }}</p>
    <!-- simple-datatables replaces this subtree's DOM internally; wrapping it in its own div ensures Vue's v-if/v-else unmount removes the whole thing cleanly on every re-fetch, instead of leaving the library's injected wrapper orphaned as a sibling of the form/buttons above. -->
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
            <td class="py-2 pr-4">{{ group.representative.size }}</td>
            <td class="py-2 pr-4">{{ group.representative.mode }}</td>
            <td class="py-2 pr-4">{{ formatTimestamp(group.representative.mod_time) }}</td>
            <td class="py-2 pr-4">
              <button
                v-if="group.versions.length > 1"
                type="button"
                class="text-blue-600 hover:underline"
                @click="openVersions(group)"
              >
                {{ group.versions.length }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="flex gap-2 mt-4">
      <button :disabled="!catalog.canGoPrev" @click="goPrev" class="border rounded px-3 py-1 disabled:opacity-50">
        Prev
      </button>
      <button :disabled="!catalog.hasMore" @click="goNext" class="border rounded px-3 py-1 disabled:opacity-50">
        Next
      </button>
    </div>
    <div
      v-if="selectedGroup"
      class="fixed inset-0 bg-black/50 flex items-center justify-center"
      @click.self="closeVersions"
    >
      <div class="bg-white rounded p-4 max-w-2xl w-full max-h-[80vh] overflow-auto">
        <div class="flex justify-between items-center mb-4">
          <h2 class="text-lg font-semibold">
            Versions of {{ selectedGroup.path }} on {{ selectedGroup.sourceHost }}
          </h2>
          <button type="button" class="text-gray-500 hover:text-gray-800" @click="closeVersions">Close</button>
        </div>
        <table class="w-full text-left border-collapse">
          <thead>
            <tr class="border-b">
              <th class="py-2 pr-4">Captured</th>
              <th class="py-2 pr-4">Size</th>
              <th class="py-2 pr-4">Mode</th>
              <th class="py-2 pr-4">Modified</th>
              <th class="py-2 pr-4">Job ID</th>
              <th class="py-2 pr-4">Store Host</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="version in selectedGroup.versions" :key="version.id" class="border-b">
              <td class="py-2 pr-4">{{ formatTimestamp(version.store_created_at) }}</td>
              <td class="py-2 pr-4">{{ version.size }}</td>
              <td class="py-2 pr-4">{{ version.mode }}</td>
              <td class="py-2 pr-4">{{ formatTimestamp(version.mod_time) }}</td>
              <td class="py-2 pr-4">{{ version.job_id }}</td>
              <td class="py-2 pr-4">{{ version.store_host }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
