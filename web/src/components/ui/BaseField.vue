<script setup>
defineProps({
  label: { type: String, required: true },
  required: { type: Boolean, default: false },
})
</script>

<template>
  <div>
    <label class="block font-medium mb-1">
      {{ label }}<span v-if="required" class="text-red-600"> *</span>
    </label>
    <slot />
  </div>
</template>
