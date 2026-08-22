<script setup>
import { onMounted, onUnmounted, computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useJobsStore } from '../stores/jobs'
import { useAutoFollow } from '../composables/useAutoFollow'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import ConnectionStatus from '../components/ui/ConnectionStatus.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import LogLine from '../components/LogLine.vue'
import { logKey } from '../utils/logLine'

const route = useRoute()
const jobs = useJobsStore()
const jobId = computed(() => route.params.job_id)

// Tail activity, not logs.length: loadOlder also grows the array, and
// counting its prepended history as "new lines" would offer to scroll the
// reader away from the history they just paged in.
const tailSeq = computed(() => jobs.tailSeq)
const { sentinel, isFollowing, newLineCount, scrollToBottom } = useAutoFollow(tailSeq)

watch(isFollowing, (value) => jobs.setFollowing(value), { immediate: true })

// Keep the sentinel in view while following, so appended lines don't push
// it below the fold and knock the view out of follow mode. flush: 'post'
// so the scroll happens after the new line has actually been rendered,
// not against the pre-update layout.
watch(tailSeq, () => {
  if (isFollowing.value) scrollToBottom()
}, { flush: 'post' })

onMounted(async () => {
  await jobs.connectLogsStream(jobId.value)
})

onUnmounted(() => {
  jobs.disconnectLogsStream()
})

function loadOlder() {
  jobs.loadOlder(jobId.value)
}
</script>

<template>
  <div>
    <PageHeader :title="jobId" :crumbs="[{ label: 'Jobs', to: { name: 'jobs' } }, { label: jobId }]">
      <template #actions>
        <ConnectionStatus :status="jobs.logsStatus" />
      </template>
    </PageHeader>
    <div v-if="jobs.hasOlderLogs" class="mb-2">
      <BaseButton data-test="load-older" :disabled="jobs.logsOlderLoading" @click="loadOlder">
        {{ jobs.logsOlderLoading ? 'Loading…' : 'Load older lines' }}
      </BaseButton>
      <p v-if="jobs.logsOlderError" data-test="load-older-error" class="text-red-600 text-sm mt-1">
        {{ jobs.logsOlderError }}
      </p>
    </div>
    <StatusMessage
      :loading="jobs.logsLoading"
      :error="jobs.logsError"
      :empty="jobs.logs.length === 0"
      empty-text="No log lines found for this job in the last 24h."
    >
      <ul>
        <LogLine v-for="line in jobs.logs" :key="logKey(line)" :line="line" />
      </ul>
      <div ref="sentinel" data-test="scroll-sentinel"></div>
    </StatusMessage>
    <button
      v-if="!isFollowing && newLineCount > 0"
      type="button"
      data-test="jump-to-latest"
      class="fixed bottom-6 right-6 rounded-full bg-blue-600 text-white px-4 py-2 text-sm shadow-lg hover:bg-blue-700"
      @click="scrollToBottom"
    >
      {{ newLineCount }} new line{{ newLineCount === 1 ? '' : 's' }} — jump to latest
    </button>
  </div>
</template>
