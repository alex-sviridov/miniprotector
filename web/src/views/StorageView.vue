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
const editingPolicy = ref(null)
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

function openCreate() {
  editingPolicy.value = null
  serverError.value = ''
  showModal.value = true
}

function openEdit(row) {
  // DataTable (vue-good-table-next) clones rows internally and tags the
  // clone with tracking fields (vgt_id, originalIndex). Look the pristine
  // object up from the store by id instead of using the augmented clone.
  editingPolicy.value = storagePolicies.list.find((p) => p.id === row.id) || row
  serverError.value = ''
  showModal.value = true
}

function closeModal() {
  showModal.value = false
  editingPolicy.value = null
  serverError.value = ''
}

async function save(payload) {
  try {
    if (editingPolicy.value) {
      await storagePolicies.update(editingPolicy.value.id, payload)
    } else {
      await storagePolicies.create(payload)
    }
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
  { label: 'Hostname', field: 'hostname', sortable: true },
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
          <button
            v-if="column.field === 'name'"
            :data-test="`storage-edit-${row.id}`"
            class="text-blue-600 hover:underline"
            @click="openEdit(row)"
          >
            {{ row.name }}
          </button>
          <span v-else-if="column.field === 'storageType'">{{ storageBackend(row.config) }}</span>
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
      :policy="editingPolicy"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
    />
  </div>
</template>
