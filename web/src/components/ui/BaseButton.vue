<script setup>
defineProps({
  variant: { type: String, default: 'secondary' },
  type: { type: String, default: 'button' },
  to: { type: [String, Object], default: null },
})

const VARIANT_CLASSES = {
  primary: 'bg-blue-600 text-white hover:bg-blue-700',
  secondary: 'border border-gray-300 hover:bg-gray-50',
  danger: 'border border-red-300 text-red-600 hover:bg-red-50',
}
</script>

<template>
  <router-link
    v-if="to"
    :to="to"
    class="inline-block rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed"
    :class="VARIANT_CLASSES[variant]"
  >
    <slot />
  </router-link>
  <button
    v-else
    :type="type"
    class="rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed"
    :class="VARIANT_CLASSES[variant]"
  >
    <slot />
  </button>
</template>
