<script setup>
import { onMounted } from 'vue'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'

const clients = useClientsStore()

onMounted(() => {
  clients.fetchAll()
})

const columns = [
  { label: 'Hostname', field: 'hostname', sortable: true },
  { label: 'Revoked', field: 'revoked', sortable: true, type: 'boolean', formatFn: (v) => (v ? 'Yes' : 'No') },
  {
    label: 'Last Seen',
    field: 'last_seen_at',
    sortable: true,
    type: 'number',
    formatFn: (v) => formatTimestamp(v) || 'Never',
  },
]
</script>

<template>
  <div>
    <PageHeader title="Clients" :crumbs="[{ label: 'Clients' }]">
      <template #actions>
        <BaseButton :to="{ name: 'client-new' }" variant="primary">
          New Client
        </BaseButton>
      </template>
    </PageHeader>
    <StatusMessage
      :loading="clients.loading"
      :error="clients.error"
      :empty="clients.list.length === 0"
      empty-text="No clients enrolled yet."
    >
      <DataTable :columns="columns" :rows="clients.list">
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'hostname'"
            :to="{ name: 'client-detail', params: { hostname: row.hostname } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.hostname }}
          </router-link>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
  </div>
</template>
