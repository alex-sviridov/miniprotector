<script setup>
defineProps({
  variant: { type: String, default: 'neutral' },
})

const VARIANT_CLASSES = {
  ok: 'bg-emerald-50 text-emerald-600',
  bad: 'bg-red-50 text-red-600',
  neutral: 'bg-gray-100 text-gray-600',
}
</script>

<template>
  <span class="inline-block rounded-full px-2 py-0.5 text-xs font-semibold" :class="VARIANT_CLASSES[variant]">
    <slot />
  </span>
</template>
