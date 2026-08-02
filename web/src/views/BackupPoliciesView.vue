<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import BackupPolicyFormModal from '../components/backup_policies/BackupPolicyFormModal.vue'

const router = useRouter()
const policies = usePoliciesStore()
const showModal = ref(false)
const serverError = ref('')

onMounted(() => {
  policies.fetchAll()
})

function confirmDelete(id) {
  if (window.confirm('Delete this policy?')) {
    policies.remove(id)
  }
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
    const policy = await policies.create(payload)
    closeModal()
    router.push({ name: 'policy-detail', params: { id: policy.id } })
  } catch {
    serverError.value = policies.error
  }
}

async function runNow(payload) {
  try {
    await policies.runAdhoc(payload)
    closeModal()
    router.push({ name: 'jobs' })
  } catch {
    serverError.value = policies.error
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
        <BaseButton data-test="policy-new" variant="primary" @click="openCreate">
          New backup
        </BaseButton>
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
    <BackupPolicyFormModal
      v-if="showModal"
      :policy="null"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
      @run-now="runNow"
    />
  </div>
</template>
