<script setup>
import { onMounted, onUnmounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useJobsStore } from '../stores/jobs'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import ConnectionStatus from '../components/ui/ConnectionStatus.vue'
import LogLine from '../components/LogLine.vue'

const route = useRoute()
const jobs = useJobsStore()
const jobId = computed(() => route.params.job_id)

onMounted(async () => {
  await jobs.connectLogsStream(jobId.value)
})

onUnmounted(() => {
  jobs.disconnectLogsStream()
})
</script>

<template>
  <div>
    <PageHeader :title="jobId" :crumbs="[{ label: 'Jobs', to: { name: 'jobs' } }, { label: jobId }]">
      <template #actions>
        <ConnectionStatus :status="jobs.logsStatus" />
      </template>
    </PageHeader>
    <StatusMessage
      :loading="jobs.logsLoading"
      :error="jobs.logsError"
      :empty="jobs.logs.length === 0"
      empty-text="No log lines found for this job in the last 24h."
    >
      <ul>
        <LogLine v-for="(line, index) in jobs.logs" :key="index" :line="line" />
      </ul>
    </StatusMessage>
  </div>
</template>
