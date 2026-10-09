<script setup>
import { onMounted, onBeforeUnmount } from 'vue'
import BaseButton from './ui/BaseButton.vue'
import { formatBytes } from '../utils/format'

const props = defineProps({
  fileCount: { type: Number, required: true },
  totalSize: { type: Number, required: true },
  destinationHost: { type: String, required: true },
  overwrite: { type: Boolean, required: true },
  pinnedCount: { type: Number, required: true },
  damagedCount: { type: Number, default: 0 },
})
const emit = defineEmits(['confirm', 'cancel'])

function onKeydown(event) {
  if (event.key === 'Escape') emit('cancel')
}
onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="$emit('cancel')">
    <div class="bg-white rounded p-4 max-w-md w-full">
      <h2 class="text-lg font-semibold mb-4">Confirm restore</h2>
      <p data-test="confirm-summary" class="mb-2">
        You're about to restore {{ fileCount }} item{{ fileCount === 1 ? '' : 's' }}
        ({{ formatBytes(totalSize) }}) to <strong>{{ destinationHost }}</strong>.
      </p>
      <p v-if="overwrite" data-test="confirm-overwrite" class="mb-2 text-amber-700">
        Existing files at the destination will be overwritten.
      </p>
      <p v-if="pinnedCount > 0" data-test="confirm-pinned" class="mb-2 text-amber-700">
        {{ pinnedCount }} item{{ pinnedCount === 1 ? '' : 's' }} pinned to an older version.
      </p>
      <p v-if="damagedCount > 0" data-test="confirm-damaged" class="mb-2 text-amber-700">
        {{ damagedCount }} selected file{{ damagedCount === 1 ? '' : 's' }} may have damaged backup data and may fail to restore.
      </p>
      <div class="flex justify-end gap-2">
        <BaseButton data-test="confirm-cancel" variant="secondary" @click="$emit('cancel')">Cancel</BaseButton>
        <BaseButton data-test="confirm-restore" variant="primary" @click="$emit('confirm')">Restore</BaseButton>
      </div>
    </div>
  </div>
</template>
