<script setup>
import { onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'

const route = useRoute()
const clients = useClientsStore()
const hostname = computed(() => route.params.hostname)

onMounted(async () => {
  try {
    await clients.fetchOne(hostname.value)
  } catch {
    // error already recorded on clients.error by the store
  }
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">{{ hostname }}</h1>
    <p v-if="clients.loading">Loading...</p>
    <p v-else-if="clients.error" class="text-red-600">{{ clients.error }}</p>
    <dl v-else-if="clients.byHostname[hostname]" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
      <dt class="font-medium">Revoked</dt>
      <dd>{{ clients.byHostname[hostname].revoked ? 'Yes' : 'No' }}</dd>
      <dt class="font-medium">Revoked At</dt>
      <dd>{{ formatTimestamp(clients.byHostname[hostname].revoked_at) || '—' }}</dd>
      <dt class="font-medium">Last Seen</dt>
      <dd>{{ formatTimestamp(clients.byHostname[hostname].last_seen_at) || 'Never' }}</dd>
      <dt class="font-medium">SANs</dt>
      <dd>{{ (clients.byHostname[hostname].sans || []).join(', ') || '—' }}</dd>
      <dt class="font-medium">Attributes</dt>
      <dd>{{ JSON.stringify(clients.byHostname[hostname].attributes || {}) }}</dd>
      <dt class="font-medium">Descriptions</dt>
      <dd>{{ JSON.stringify(clients.byHostname[hostname].descriptions || {}) }}</dd>
    </dl>
  </div>
</template>
