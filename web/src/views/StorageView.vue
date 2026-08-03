<script setup>
import { onMounted, ref } from 'vue'
import { useStoragePoliciesStore } from '../stores/storagePolicies'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import StorageEditModal from '../components/storage/StorageEditModal.vue'

const storagePolicies = useStoragePoliciesStore()
const showModal = ref(false)
const serverError = ref('')

onMounted(() => {
  storagePolicies.fetchAll()
})

function storageBackend(configText) {
  try {
    return JSON.parse(configText).backend || '—'
  } catch {
    return '—'
  }
}

function targetHostname(clientFilters) {
  return clientFilters?.hostnames?.[0] || '—'
}

function openCreate() {
  serverError.value = ''
  showModal.value = true
}

function closeModal() {
  showModal.value = false
  serverError.value = ''
}

async function save(payload) {
  try {
    await storagePolicies.create(payload)
    closeModal()
  } catch {
    serverError.value = storagePolicies.error
  }
}

function confirmDelete(id) {
  if (window.confirm('Delete this storage policy?')) {
    storagePolicies.remove(id)
  }
}

const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'Target Hostname', field: 'targetHostname', sortable: false },
  { label: 'Port', field: 'port', sortable: true },
  { label: 'Storage Type', field: 'storageType', sortable: false },
  { label: '', field: 'actions', sortable: false },
]
</script>

<template>
  <div>
    <PageHeader title="Storage">
      <template #actions>
        <BaseButton data-test="storage-new" variant="primary" @click="openCreate">
          New Storage Policy
        </BaseButton>
      </template>
    </PageHeader>
    <StatusMessage
      :loading="storagePolicies.loading"
      :error="storagePolicies.error"
      :empty="storagePolicies.list.length === 0"
      empty-text="No storage policies defined yet."
    >
      <DataTable :columns="columns" :rows="storagePolicies.list">
        <template #table-row="{ column, row }">
          <router-link
            v-if="column.field === 'name'"
            :to="{ name: 'storage-detail', params: { id: row.id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.name }}
          </router-link>
          <span v-else-if="column.field === 'storageType'">{{ storageBackend(row.config) }}</span>
          <span v-else-if="column.field === 'targetHostname'">{{ targetHostname(row.client_filters) }}</span>
          <BaseButton
            v-else-if="column.field === 'actions'"
            :data-test="`storage-delete-${row.id}`"
            variant="danger"
            @click="confirmDelete(row.id)"
          >
            Delete
          </BaseButton>
          <span v-else>{{ row[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
    <StorageEditModal
      v-if="showModal"
      :policy="null"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
    />
  </div>
</template>
