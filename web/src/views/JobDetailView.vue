<script setup>
import { onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useJobsStore } from '../stores/jobs'
import { formatTimestamp } from '../utils/format'

const route = useRoute()
const jobs = useJobsStore()
const jobId = computed(() => route.params.job_id)

onMounted(async () => {
  await jobs.fetchLogs(jobId.value)
})

// GET /jobs/{job_id}/logs returns Loki's raw nanosecond timestamp (unlike
// GET /jobs's started_at/finished_at, already seconds) -- convert before
// formatTimestamp, which expects epoch seconds.
function formatLineTimestamp(nanos) {
  return formatTimestamp(Math.floor(nanos / 1e9))
}
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">{{ jobId }}</h1>
    <p v-if="jobs.logsLoading">Loading...</p>
    <p v-else-if="jobs.logsError" class="text-red-600">{{ jobs.logsError }}</p>
    <p v-else-if="jobs.logs.length === 0">No log lines found for this job in the last 24h.</p>
    <ul v-else class="font-mono text-sm space-y-1">
      <li v-for="(line, index) in jobs.logs" :key="index">
        <span class="text-gray-500">{{ formatLineTimestamp(line.timestamp) }}</span>
        [{{ line.hostname }}/{{ line.binary }}] {{ line.line }}
      </li>
    </ul>
  </div>
</template>
