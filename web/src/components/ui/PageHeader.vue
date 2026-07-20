<!-- web/src/components/ui/PageHeader.vue -->
<script setup>
defineProps({
  title: { type: String, required: true },
})
</script>

<template>
  <div class="flex items-center justify-between mb-4">
    <h1 class="text-xl font-semibold">{{ title }}</h1>
    <div v-if="$slots.actions" data-test="page-header-actions" class="flex gap-2">
      <slot name="actions" />
    </div>
  </div>
  <slot />
</template>
