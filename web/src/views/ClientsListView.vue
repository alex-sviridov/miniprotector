<script setup>
import { onMounted, onBeforeUnmount, nextTick, ref } from 'vue'
import { DataTable } from 'simple-datatables'
import 'simple-datatables/dist/style.css'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'

const clients = useClientsStore()
const tableRef = ref(null)
let dataTable = null

onMounted(async () => {
  await clients.fetchAll()
  await nextTick()
  if (tableRef.value) {
    dataTable = new DataTable(tableRef.value)
  }
})

onBeforeUnmount(() => {
  if (dataTable) {
    dataTable.destroy()
    dataTable = null
  }
})
</script>

<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-semibold">Clients</h1>
      <router-link to="/clients/new" class="bg-blue-600 text-white rounded px-3 py-1">
        New Client
      </router-link>
    </div>
    <p v-if="clients.loading">Loading...</p>
    <p v-else-if="clients.error" class="text-red-600">{{ clients.error }}</p>
    <table v-else ref="tableRef" class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Hostname</th>
          <th class="py-2 pr-4">Revoked</th>
          <th class="py-2 pr-4">Last Seen</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="client in clients.list" :key="client.hostname" class="border-b hover:bg-gray-50">
          <td class="py-2 pr-4">
            <router-link :to="`/clients/${client.hostname}`" class="text-blue-600 hover:underline">
              {{ client.hostname }}
            </router-link>
          </td>
          <td class="py-2 pr-4">{{ client.revoked ? 'Yes' : 'No' }}</td>
          <td class="py-2 pr-4">
            {{ formatTimestamp(client.last_seen_at) || 'Never' }}
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
