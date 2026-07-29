<script setup>
import { computed, ref } from 'vue'
import { formatTimestamp } from '../utils/format'
import { parseLogLine } from '../utils/logLine'

const props = defineProps({
  line: { type: Object, required: true },
})

const expanded = ref(false)

const parsed = computed(() => parseLogLine(props.line.line))
const fieldEntries = computed(() => Object.entries(parsed.value.fields))

const LEVEL_CLASSES = {
  DEBUG: 'bg-gray-100 text-gray-600',
  INFO: 'bg-blue-100 text-blue-700',
  WARN: 'bg-amber-100 text-amber-700',
  ERROR: 'bg-red-100 text-red-700',
}

const levelClass = computed(() => LEVEL_CLASSES[parsed.value.level] || 'bg-gray-100 text-gray-400')
const levelLabel = computed(() => parsed.value.level || '—')

// GET /jobs/{job_id}/logs returns Loki's raw nanosecond timestamp (unlike
// GET /jobs's started_at/finished_at, already seconds) -- convert before
// formatTimestamp, which expects epoch seconds.
function formatLineTimestamp(nanos) {
  return formatTimestamp(Math.floor(nanos / 1e9))
}

function formatFieldValue(value) {
  return typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean'
    ? String(value)
    : JSON.stringify(value)
}

function toggle() {
  if (fieldEntries.value.length === 0) return
  expanded.value = !expanded.value
}
</script>

<template>
  <li class="border-b py-1" data-test="log-line">
    <div
      class="font-mono text-sm flex items-baseline gap-2"
      :class="{ 'cursor-pointer': fieldEntries.length > 0 }"
      data-test="log-line-summary"
      @click="toggle"
    >
      <span v-if="fieldEntries.length > 0" class="text-gray-400 select-none" data-test="log-line-caret">{{
        expanded ? '▾' : '▸'
      }}</span>
      <span :class="['inline-block rounded px-1.5 text-xs font-semibold', levelClass]" data-test="log-line-level">{{
        levelLabel
      }}</span>
      <span class="text-gray-500">{{ formatLineTimestamp(line.timestamp) }}</span>
      <span>{{ line.binary }}@{{ line.hostname }}:</span>
      <span data-test="log-line-message">{{ parsed.message }}</span>
    </div>
    <dl
      v-if="expanded"
      class="mt-1 ml-6 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs font-mono text-gray-600"
      data-test="log-line-fields"
    >
      <template v-for="[key, value] in fieldEntries" :key="key">
        <dt class="font-semibold">{{ key }}</dt>
        <dd>{{ formatFieldValue(value) }}</dd>
      </template>
    </dl>
  </li>
</template>
