<script setup>
defineOptions({ inheritAttrs: false })
const model = defineModel({ type: [String, Number], default: '' })
</script>

<template>
  <input v-model="model" v-bind="$attrs" class="w-full border rounded px-2 py-1" />
</template>
