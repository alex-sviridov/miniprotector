<script setup>
import { onMounted, onBeforeUnmount } from 'vue'
import { formatBytes, formatTimestamp } from '../utils/format'
import BaseButton from './ui/BaseButton.vue'

const props = defineProps({
  group: { type: Object, required: true },
})
const emit = defineEmits(['close'])

function close() {
  emit('close')
}

function onKeydown(event) {
  if (event.key === 'Escape') close()
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-2xl w-full max-h-[80vh] overflow-auto">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">
          Versions of {{ group.path }} on {{ group.sourceHost }}
        </h2>
        <BaseButton variant="secondary" @click="close">Close</BaseButton>
      </div>
      <table class="w-full text-left border-collapse">
        <thead>
          <tr class="border-b">
            <th class="py-2 pr-4">Captured</th>
            <th class="py-2 pr-4">Size</th>
            <th class="py-2 pr-4">Mode</th>
            <th class="py-2 pr-4">Modified</th>
            <th class="py-2 pr-4">Job ID</th>
            <th class="py-2 pr-4">Store Host</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="version in group.versions" :key="version.id" class="border-b">
            <td class="py-2 pr-4">{{ formatTimestamp(version.store_created_at) || '—' }}</td>
            <td class="py-2 pr-4">{{ formatBytes(version.size) }}</td>
            <td class="py-2 pr-4">{{ version.mode }}</td>
            <td class="py-2 pr-4">{{ formatTimestamp(version.mod_time) || '—' }}</td>
            <td class="py-2 pr-4">{{ version.job_id }}</td>
            <td class="py-2 pr-4">{{ version.store_host }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
