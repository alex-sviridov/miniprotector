<script setup>
import { onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoliciesStore } from '../stores/policies'
import { formatTimestamp } from '../utils/format'

const route = useRoute()
const router = useRouter()
const policies = usePoliciesStore()
const id = computed(() => route.params.id)

onMounted(async () => {
  try {
    await policies.fetchOne(id.value)
  } catch {
    // error already recorded on policies.error by the store
  }
})

async function confirmDelete() {
  if (window.confirm('Delete this policy?')) {
    await policies.remove(id.value)
    router.push('/policies')
  }
}
</script>

<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-semibold">{{ policies.byId[id]?.name || id }}</h1>
      <div class="flex gap-2">
        <router-link :to="`/policies/${id}/edit`" class="border rounded px-3 py-1">Edit</router-link>
        <button @click="confirmDelete" class="border rounded px-3 py-1">Delete</button>
      </div>
    </div>
    <p v-if="policies.loading">Loading...</p>
    <p v-else-if="policies.error" class="text-red-600">{{ policies.error }}</p>
    <dl v-else-if="policies.byId[id]" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
      <dt class="font-medium">RPO</dt>
      <dd>{{ policies.byId[id].rpo }}</dd>
      <dt class="font-medium">Destination</dt>
      <dd>{{ policies.byId[id].destination }}</dd>
      <dt class="font-medium">Backup Window</dt>
      <dd>{{ (policies.byId[id].backup_window || []).join(', ') || '—' }}</dd>
      <dt class="font-medium">Hostnames</dt>
      <dd>{{ (policies.byId[id].client_filters?.hostnames || []).join(', ') || '—' }}</dd>
      <dt class="font-medium">Labels</dt>
      <dd>{{ JSON.stringify(policies.byId[id].client_filters?.labels || {}) }}</dd>
      <dt class="font-medium">Object Filters</dt>
      <dd>
        <ul>
          <li v-for="f in policies.byId[id].object_filters || []" :key="f.id">
            {{ f.path }}
            <span v-if="f.include?.length"> include: {{ f.include.join(', ') }}</span>
            <span v-if="f.exclude?.length"> exclude: {{ f.exclude.join(', ') }}</span>
          </li>
        </ul>
      </dd>
      <dt class="font-medium">Created</dt>
      <dd>{{ formatTimestamp(policies.byId[id].created_at) || '—' }}</dd>
      <dt class="font-medium">Updated</dt>
      <dd>{{ formatTimestamp(policies.byId[id].updated_at) || '—' }}</dd>
    </dl>
  </div>
</template>
