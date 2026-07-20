<!-- web/src/components/ui/DataTable.vue -->
<script setup>
import { VueGoodTable } from 'vue-good-table-next'
import 'vue-good-table-next/dist/vue-good-table-next.css'

defineProps({
  columns: { type: Array, required: true },
  rows: { type: Array, required: true },
  searchEnabled: { type: Boolean, default: true },
  perPage: { type: Number, default: 25 },
})
const emit = defineEmits(['row-click'])

function handleRowClick({ row }) {
  emit('row-click', row)
}
</script>

<template>
  <vue-good-table
    :columns="columns"
    :rows="rows"
    :search-options="{ enabled: searchEnabled, placeholder: 'Search...' }"
    :pagination-options="{ enabled: true, perPage }"
    @row-click="handleRowClick"
  >
    <template #table-row="props">
      <slot name="table-row" v-bind="props">
        <span>{{ props.formattedRow[props.column.field] }}</span>
      </slot>
    </template>
  </vue-good-table>
</template>
