<script setup>
import { onMounted } from 'vue'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'

const clients = useClientsStore()

onMounted(() => {
  clients.fetchAll()
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Clients</h1>
    <p v-if="clients.loading">Loading...</p>
    <p v-else-if="clients.error" class="text-red-600">{{ clients.error }}</p>
    <table v-else class="w-full text-left border-collapse">
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
