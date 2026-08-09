<script setup>
import { ref, watchEffect, onMounted } from 'vue'

const props = defineProps({
  checked: { type: Boolean, default: false },
  indeterminate: { type: Boolean, default: false },
})
const emit = defineEmits(['toggle'])

const input = ref(null)

watchEffect(() => {
  if (input.value) input.value.indeterminate = props.indeterminate
})

onMounted(() => {
  if (input.value) input.value.indeterminate = props.indeterminate
})
</script>

<template>
  <input ref="input" type="checkbox" :checked="checked" @click.stop @change="emit('toggle')" />
</template>
