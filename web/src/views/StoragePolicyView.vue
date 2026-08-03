<script setup>
import { onMounted, computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useStoragePoliciesStore } from '../stores/storagePolicies'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DetailList from '../components/ui/DetailList.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import Tabs from '../components/ui/Tabs.vue'
import StorageEditModal from '../components/storage/StorageEditModal.vue'
import PolicyCheckins from '../components/policies/PolicyCheckins.vue'

const route = useRoute()
const router = useRouter()
const storagePolicies = useStoragePoliciesStore()
const id = computed(() => route.params.id)
const policy = computed(() => storagePolicies.byId[id.value])

const showModal = ref(false)
const serverError = ref('')

const TABS = [
  { key: 'details', label: 'Details' },
  { key: 'checkins', label: 'Check-ins' },
]

onMounted(async () => {
  try {
    await storagePolicies.fetchOne(id.value)
  } catch {
    // error already recorded on storagePolicies.error by the store
  }
})

function parseConfig(configText) {
  try {
    const c = JSON.parse(configText || '{}')
    return c && typeof c === 'object' ? c : {}
  } catch {
    return {}
  }
}

const detailRows = computed(() => {
  if (!policy.value) return []
  const config = parseConfig(policy.value.config)
  return [
    { key: 'targetHostname', label: 'Target Hostname', value: policy.value.client_filters?.hostnames?.[0] || '—' },
    { key: 'port', label: 'Port', value: policy.value.port },
    { key: 'storageType', label: 'Storage Type', value: config.backend || '—' },
    { key: 'path', label: 'Path', value: config.root || '—' },
    { key: 'created', label: 'Created', value: formatTimestamp(policy.value.created_at) || '—' },
    { key: 'updated', label: 'Updated', value: formatTimestamp(policy.value.updated_at) || '—' },
  ]
})

async function confirmDelete() {
  if (window.confirm('Delete this storage policy?')) {
    await storagePolicies.remove(id.value)
    router.push({ name: 'storage' })
  }
}

function openEdit() {
  serverError.value = ''
  showModal.value = true
}

function closeModal() {
  showModal.value = false
  serverError.value = ''
}

async function save(payload) {
  try {
    await storagePolicies.update(id.value, payload)
    closeModal()
  } catch {
    serverError.value = storagePolicies.error
  }
}
</script>

<template>
  <div>
    <PageHeader :title="policy?.name || id">
      <template #actions>
        <BaseButton v-if="policy" data-test="storage-policy-edit" variant="secondary" @click="openEdit">
          Edit
        </BaseButton>
        <BaseButton data-test="storage-policy-delete" variant="danger" @click="confirmDelete">Delete</BaseButton>
      </template>
    </PageHeader>
    <StatusMessage :loading="storagePolicies.loading" :error="storagePolicies.error">
      <Tabs v-if="policy" :tabs="TABS">
        <template #details>
          <DetailList :rows="detailRows" />
        </template>
        <template #checkins>
          <PolicyCheckins
            :checkins="policy.checkins || []"
            :loading="storagePolicies.checkinsLoading"
            :error="storagePolicies.checkinsError"
            @refresh="storagePolicies.refresh(id)"
          />
        </template>
      </Tabs>
    </StatusMessage>
    <StorageEditModal
      v-if="showModal"
      :policy="policy"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
    />
  </div>
</template>
