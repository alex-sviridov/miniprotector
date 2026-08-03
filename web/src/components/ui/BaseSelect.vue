<script setup>
defineOptions({ inheritAttrs: false })
const model = defineModel({ type: String, default: '' })
</script>

<template>
  <select v-model="model" v-bind="$attrs" class="w-full border rounded px-2 py-1">
    <slot />
  </select>
</template>
