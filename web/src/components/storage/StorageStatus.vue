<script setup>
import { formatBytes, formatTimestamp } from '../../utils/format'
import BaseButton from '../ui/BaseButton.vue'

defineProps({
  // undefined until the first fetch completes; [] when no node has reported.
  reports: { type: Array, default: undefined },
  loading: { type: Boolean, default: false },
  error: { type: String, default: null },
})
defineEmits(['refresh'])

const STATE_CLASS = {
  online: 'bg-green-100 text-green-800',
  stale: 'bg-amber-100 text-amber-800',
  offline: 'bg-red-100 text-red-800',
}

function usedPercent(r) {
  return r.disk_total_bytes ? Math.round((r.disk_used_bytes / r.disk_total_bytes) * 100) : 0
}

function barClass(r) {
  const pct = usedPercent(r)
  if (pct > 95) return 'bg-red-500'
  if (pct > 85) return 'bg-amber-500'
  return 'bg-blue-500'
}

function formatUptime(seconds) {
  if (!seconds) return '—'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d) return `${d} d ${h} h`
  if (h) return `${h} h ${m} min`
  return `${m} min`
}

function reportedAt(r) {
  return formatTimestamp(Date.parse(r.reported_at) / 1000) || '—'
}
</script>

<template>
  <div>
    <div class="flex justify-end mb-2">
      <BaseButton data-test="status-refresh" variant="secondary" :disabled="loading" @click="$emit('refresh')">
        {{ loading ? 'Refreshing…' : 'Refresh' }}
      </BaseButton>
    </div>
    <p v-if="error" data-test="status-error" class="text-red-600 mb-4">{{ error }}</p>
    <p v-if="reports && reports.length === 0" data-test="status-empty" class="text-gray-500">
      No status reported yet.
    </p>
    <div v-for="r in reports || []" :key="r.hostname" data-test="status-card" class="border rounded p-4 mb-4">
      <div class="flex items-center gap-3 mb-3">
        <span class="font-medium">{{ r.hostname }}:{{ r.port }}</span>
        <span data-test="status-state" class="px-2 py-0.5 rounded text-sm" :class="STATE_CLASS[r.state]">
          {{ r.state }}
        </span>
      </div>
      <div class="mb-3">
        <div class="flex justify-between text-sm mb-1">
          <span>Disk</span>
          <span data-test="status-disk">
            {{ formatBytes(r.disk_used_bytes) }} of {{ formatBytes(r.disk_total_bytes) }} ({{ usedPercent(r) }}%)
          </span>
        </div>
        <div class="h-2 bg-gray-200 rounded">
          <div
            data-test="status-disk-bar"
            class="h-2 rounded"
            :class="barClass(r)"
            :style="{ width: usedPercent(r) + '%' }"
          />
        </div>
      </div>
      <dl class="grid grid-cols-2 gap-2 text-sm">
        <dt class="text-gray-500">Active connections</dt>
        <dd data-test="status-connections">{{ r.active_connections }}</dd>
        <dt class="text-gray-500">Backup jobs in progress</dt>
        <dd data-test="status-jobs">{{ r.in_progress_jobs }}</dd>
        <dt class="text-gray-500">Uptime</dt>
        <dd>{{ formatUptime(r.uptime_seconds) }}</dd>
        <dt class="text-gray-500">Last reported</dt>
        <dd>{{ reportedAt(r) }}</dd>
      </dl>
    </div>
  </div>
</template>
