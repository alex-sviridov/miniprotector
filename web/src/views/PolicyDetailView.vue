<script setup>
import { onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DetailList from '../components/ui/DetailList.vue'
import BaseButton from '../components/ui/BaseButton.vue'

const route = useRoute()
const router = useRouter()
const policies = usePoliciesStore()
const id = computed(() => route.params.id)
const policy = computed(() => policies.byId[id.value])

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
</script>

<template>
  <div>
    <PageHeader :title="policy?.name || id">
      <template #actions>
        <router-link :to="{ name: 'policy-edit', params: { id } }" class="border rounded px-3 py-1">Edit</router-link>
        <BaseButton data-test="policy-delete" variant="danger" @click="confirmDelete">Delete</BaseButton>
      </template>
    </PageHeader>
    <StatusMessage :loading="policies.loading" :error="policies.error">
      <DetailList v-if="policy" :rows="detailRows">
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
    </StatusMessage>
  </div>
</template>
