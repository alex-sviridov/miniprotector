<script setup>
defineProps({
  status: { type: String, required: true }, // 'live' | 'connecting' | 'reconnecting' | 'polling' | 'finished'
})

const LABELS = {
  live: 'Live',
  connecting: 'Connecting…',
  reconnecting: 'Reconnecting…',
  polling: 'Live updates unavailable — refreshing every 10s',
  finished: 'Finished',
}

const CLASSES = {
  live: 'bg-green-100 text-green-700',
  connecting: 'bg-gray-100 text-gray-500',
  reconnecting: 'bg-amber-100 text-amber-700',
  polling: 'bg-amber-100 text-amber-700',
  finished: 'bg-gray-100 text-gray-600',
}
</script>

<template>
  <span
    :class="['inline-block rounded px-2 py-0.5 text-xs font-semibold', CLASSES[status] || CLASSES.connecting]"
    data-test="connection-status"
  >
    {{ LABELS[status] || status }}
  </span>
</template>
