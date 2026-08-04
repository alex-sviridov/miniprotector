<script setup>
import { onMounted, computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DetailList from '../components/ui/DetailList.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import Tabs from '../components/ui/Tabs.vue'
import BackupPolicyFormModal from '../components/backup_policies/BackupPolicyFormModal.vue'
import PolicyCheckins from '../components/policies/PolicyCheckins.vue'

const route = useRoute()
const router = useRouter()
const policies = usePoliciesStore()
const id = computed(() => route.params.id)
const policy = computed(() => policies.byId[id.value])

const showModal = ref(false)
const serverError = ref('')

const TABS = [
  { key: 'details', label: 'Details' },
  { key: 'checkins', label: 'Check-ins' },
]

onMounted(async () => {
  try {
    await policies.fetchOne(id.value)
  } catch {
    // error already recorded on policies.error by the store
  }
})

const detailRows = computed(() => {
  if (!policy.value) return []
  return [
    { key: 'rpo', label: 'RPO', value: policy.value.rpo },
    { key: 'destination', label: 'Destination', value: policy.value.destination },
    { key: 'backupWindow', label: 'Backup Window', value: (policy.value.backup_window || []).join(', ') || '—' },
    { key: 'hostnames', label: 'Hostnames', value: (policy.value.client_filters?.hostnames || []).join(', ') || '—' },
    { key: 'labels', label: 'Labels', value: JSON.stringify(policy.value.client_filters?.labels || {}) },
    { key: 'objectFilters', label: 'Object Filters', value: '' },
    { key: 'created', label: 'Created', value: formatTimestamp(policy.value.created_at) || '—' },
    { key: 'updated', label: 'Updated', value: formatTimestamp(policy.value.updated_at) || '—' },
  ]
})

async function confirmDelete() {
  if (window.confirm('Delete this policy?')) {
    await policies.remove(id.value)
    router.push({ name: 'policies' })
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
    await policies.update(id.value, payload)
    closeModal()
    policies.refresh(id.value).catch(() => {})
  } catch {
    serverError.value = policies.error
  }
}

async function refreshCheckins() {
  try {
    await policies.refresh(id.value)
  } catch {
    // error already recorded on policies.checkinsError by the store
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
</script>

<template>
  <div>
    <PageHeader
      :title="policy?.name || id"
      :crumbs="[{ label: 'Policies', to: { name: 'policies' } }, { label: policy?.name || id }]"
    >
      <template #actions>
        <BaseButton v-if="policy" data-test="policy-edit" variant="secondary" @click="openEdit">Edit</BaseButton>
        <BaseButton data-test="policy-delete" variant="danger" @click="confirmDelete">Delete</BaseButton>
      </template>
    </PageHeader>
    <StatusMessage :loading="policies.loading" :error="policies.error">
      <Tabs v-if="policy" :tabs="TABS">
        <template #details>
          <DetailList :rows="detailRows">
            <template #objectFilters>
              <ul>
                <li v-for="f in policy.object_filters || []" :key="f.id">
                  {{ f.path }}
                  <span v-if="f.include?.length"> include: {{ f.include.join(', ') }}</span>
                  <span v-if="f.exclude?.length"> exclude: {{ f.exclude.join(', ') }}</span>
                </li>
              </ul>
            </template>
          </DetailList>
        </template>
        <template #checkins>
          <PolicyCheckins
            :checkins="policy.checkins || []"
            :loading="policies.checkinsLoading"
            :error="policies.checkinsError"
            @refresh="refreshCheckins"
          />
        </template>
      </Tabs>
    </StatusMessage>
    <BackupPolicyFormModal
      v-if="showModal"
      :policy="policy"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
      @run-now="runNow"
    />
  </div>
</template>
