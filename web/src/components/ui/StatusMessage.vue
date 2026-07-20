<script setup>
defineProps({
  loading: { type: Boolean, default: false },
  error: { type: String, default: null },
  empty: { type: Boolean, default: false },
  emptyText: { type: String, default: 'No results.' },
})
</script>

<template>
  <p v-if="loading" class="text-gray-500">Loading...</p>
  <p v-else-if="error" class="text-red-600">{{ error }}</p>
  <p v-else-if="empty" class="text-gray-500">{{ emptyText }}</p>
  <slot v-else />
</template>
