<script setup>
import { computed } from 'vue'
import { VueDatePicker } from '@vuepic/vue-datepicker'
import '@vuepic/vue-datepicker/dist/main.css'

const receivedAfter = defineModel('receivedAfter', { type: Number, required: true })
const receivedBefore = defineModel('receivedBefore', { type: Number, required: true })

const DAY = 24 * 60 * 60

function startOfDay(date) {
  const d = new Date(date)
  d.setHours(0, 0, 0, 0)
  return d
}

function endOfDay(date) {
  const d = new Date(date)
  d.setHours(23, 59, 59, 999)
  return d
}

function preset(label, spanSeconds) {
  const now = Math.floor(Date.now() / 1000)
  return { label, value: [new Date((now - spanSeconds) * 1000), new Date(now * 1000)] }
}

function thisMonthPreset() {
  const now = new Date()
  return { label: 'This month', value: [new Date(now.getFullYear(), now.getMonth(), 1), now] }
}

const presetDates = computed(() => [
  preset('Today', 0),
  preset('Last 7 days', 7 * DAY),
  preset('Last 30 days', 30 * DAY),
  thisMonthPreset(),
])

const range = computed({
  get: () => [new Date(receivedAfter.value * 1000), new Date(receivedBefore.value * 1000)],
  set: (value) => {
    const [start, end] = value || []
    if (!start || !end) return // a partial (single-date) selection -- ignore until both ends are picked
    receivedAfter.value = Math.floor(startOfDay(start).getTime() / 1000)
    receivedBefore.value = Math.floor(endOfDay(end).getTime() / 1000)
  },
})
</script>

<template>
  <div class="border rounded p-4">
    <VueDatePicker
      v-model="range"
      :range="{ partialRange: false }"
      :preset-dates="presetDates"
      :time-config="{ enableTimePicker: false }"
      :input-attrs="{ clearable: false }"
    />
  </div>
</template>
