<script setup>
import { onMounted, onBeforeUnmount, nextTick, ref } from 'vue'
import { DataTable } from 'simple-datatables'
import 'simple-datatables/dist/style.css'
import { useJobsStore } from '../stores/jobs'
import { formatTimestamp } from '../utils/format'

const jobs = useJobsStore()
const tableRef = ref(null)
let dataTable = null

onMounted(async () => {
  await jobs.fetchAll()
  await nextTick()
  if (tableRef.value) {
    dataTable = new DataTable(tableRef.value)
  }
})

onBeforeUnmount(() => {
  if (dataTable) {
    dataTable.destroy()
    dataTable = null
  }
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Jobs</h1>
    <p v-if="jobs.loading">Loading...</p>
    <p v-else-if="jobs.error" class="text-red-600">{{ jobs.error }}</p>
    <table v-else ref="tableRef" class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Job ID</th>
          <th class="py-2 pr-4">Kind</th>
          <th class="py-2 pr-4">Source Host</th>
          <th class="py-2 pr-4">Store Host</th>
          <th class="py-2 pr-4">Started At</th>
          <th class="py-2 pr-4">Finished At</th>
          <th class="py-2 pr-4">State</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="job in jobs.list" :key="job.job_id" class="border-b hover:bg-gray-50">
          <td class="py-2 pr-4">
            <router-link :to="`/jobs/${job.job_id}`" class="text-blue-600 hover:underline">
              {{ job.job_id }}
            </router-link>
          </td>
          <td class="py-2 pr-4">{{ job.kind }}</td>
          <td class="py-2 pr-4">{{ job.source_host }}</td>
          <td class="py-2 pr-4">{{ job.store_host || '—' }}</td>
          <td class="py-2 pr-4">{{ formatTimestamp(job.started_at) || '—' }}</td>
          <td class="py-2 pr-4">{{ formatTimestamp(job.finished_at) || '—' }}</td>
          <td class="py-2 pr-4">{{ job.state }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
