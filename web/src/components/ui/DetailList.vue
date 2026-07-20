<script setup>
defineProps({
  rows: { type: Array, required: true },
})
</script>

<template>
  <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2">
    <template v-for="row in rows" :key="row.key">
      <dt class="font-medium">{{ row.label }}</dt>
      <dd>
        <slot :name="row.key">{{ row.value }}</slot>
      </dd>
    </template>
  </dl>
</template>
