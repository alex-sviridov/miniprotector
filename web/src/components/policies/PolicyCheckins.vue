<script setup>
import { computed } from 'vue'
import { formatTimestamp } from '../../utils/format'
import BaseButton from '../ui/BaseButton.vue'

const props = defineProps({
  checkins: { type: Array, required: true },
  loading: { type: Boolean, default: false },
  error: { type: String, default: null },
})
defineEmits(['refresh'])

const sortedCheckins = computed(() => [...props.checkins].sort((a, b) => b.last_seen_at - a.last_seen_at))
</script>

<template>
  <div>
    <div class="flex justify-end mb-2">
      <BaseButton data-test="checkins-refresh" variant="secondary" :disabled="loading" @click="$emit('refresh')">
        {{ loading ? 'Refreshing…' : 'Refresh' }}
      </BaseButton>
    </div>
    <p v-if="error" data-test="checkins-error" class="text-red-600 mb-4">{{ error }}</p>
    <p v-if="sortedCheckins.length === 0" class="text-gray-500">No hosts have checked in yet.</p>
    <table v-else class="w-full text-left">
      <thead>
        <tr>
          <th class="font-medium">Hostname</th>
          <th class="font-medium">Last Seen</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="checkin in sortedCheckins" :key="checkin.hostname">
          <td>{{ checkin.hostname }}</td>
          <td>{{ formatTimestamp(checkin.last_seen_at) }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
