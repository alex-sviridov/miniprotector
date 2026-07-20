<script setup>
import { onMounted } from 'vue'
import { useJobsStore } from '../stores/jobs'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'

const jobs = useJobsStore()

onMounted(() => {
  jobs.fetchAll()
})

const columns = [
  { label: 'Job ID', field: 'job_id', sortable: true },
  { label: 'Kind', field: 'kind', sortable: true },
  { label: 'Source Host', field: 'source_host', sortable: true },
  { label: 'Store Host', field: 'store_host', sortable: true, formatFn: (v) => v || '—' },
  { label: 'Started At', field: 'started_at', sortable: true, type: 'number', formatFn: (v) => formatTimestamp(v) || '—' },
  { label: 'Finished At', field: 'finished_at', sortable: true, type: 'number', formatFn: (v) => formatTimestamp(v) || '—' },
  { label: 'State', field: 'state', sortable: true },
]
</script>

<template>
  <div>
    <PageHeader title="Jobs" />
    <StatusMessage
      :loading="jobs.loading"
      :error="jobs.error"
      :empty="jobs.list.length === 0"
      empty-text="No jobs in the last 24h."
    >
      <DataTable :columns="columns" :rows="jobs.list">
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'job_id'"
            :to="{ name: 'job-detail', params: { job_id: row.job_id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.job_id }}
          </router-link>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
  </div>
</template>
