<script setup>
import { computed } from 'vue'
import { VueDatePicker } from '@vuepic/vue-datepicker'
import '@vuepic/vue-datepicker/dist/main.css'

const receivedAfter = defineModel('receivedAfter', { type: Number, required: true })
const receivedBefore = defineModel('receivedBefore', { type: Number, required: true })

const DAY = 24 * 60 * 60

function preset(label, spanSeconds) {
  const now = Math.floor(Date.now() / 1000)
  return { label, value: [new Date((now - spanSeconds) * 1000), new Date(now * 1000)] }
}

const presetDates = computed(() => [
  preset('Today', DAY),
  preset('Last 7 days', 7 * DAY),
  preset('Last 30 days', 30 * DAY),
  preset('This month', 30 * DAY),
])

const range = computed({
  get: () => [new Date(receivedAfter.value * 1000), new Date(receivedBefore.value * 1000)],
  set: ([start, end]) => {
    receivedAfter.value = Math.floor(start.getTime() / 1000)
    receivedBefore.value = Math.floor(end.getTime() / 1000)
  },
})
</script>

<template>
  <div class="border rounded p-4">
    <VueDatePicker
      v-model="range"
      range
      :preset-dates="presetDates"
      :time-config="{ enableTimePicker: false }"
      :input-attrs="{ clearable: false }"
    />
  </div>
</template>
