<script setup>
import { onMounted } from 'vue'
import { usePoliciesStore } from '../stores/policies'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'

const policies = usePoliciesStore()

onMounted(() => {
  policies.fetchAll()
})

function confirmDelete(id) {
  if (window.confirm('Delete this policy?')) {
    policies.remove(id)
  }
}

const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'RPO', field: 'rpo', sortable: true },
  { label: 'Destination', field: 'destination', sortable: true },
  { label: '', field: 'actions', sortable: false },
]
</script>

<template>
  <div>
    <PageHeader title="Policies">
      <template #actions>
        <router-link :to="{ name: 'policy-new' }" class="bg-blue-600 text-white rounded px-3 py-1">
          New Policy
        </router-link>
      </template>
    </PageHeader>
    <StatusMessage
      :loading="policies.loading"
      :error="policies.error"
      :empty="policies.list.length === 0"
      empty-text="No policies defined yet."
    >
      <DataTable :columns="columns" :rows="policies.list">
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'name'"
            :to="{ name: 'policy-detail', params: { id: row.id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.name }}
          </router-link>
          <BaseButton
            v-else-if="column.field === 'actions'"
            data-test="policy-delete"
            variant="danger"
            @click="confirmDelete(row.id)"
          >
            Delete
          </BaseButton>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
  </div>
</template>
