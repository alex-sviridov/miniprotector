<!-- web/src/components/ui/DataTable.vue -->
<script setup>
import { VueGoodTable } from 'vue-good-table-next'
import { computed } from 'vue'
import 'vue-good-table-next/dist/vue-good-table-next.css'

const props = defineProps({
  columns: { type: Array, required: true },
  rows: { type: Array, required: true },
  searchEnabled: { type: Boolean, default: true },
  perPage: { type: Number, default: 25 },
  selectable: { type: Boolean, default: false },
  // { field, type: 'asc' | 'desc' } -- without this, vue-good-table-next
  // renders rows in whatever order the `rows` prop happens to be in (here,
  // aggregator map-iteration order for a snapshot, insertion order for a
  // live upsert), so a newly live-upserted row can land on any page of a
  // long, paginated table instead of being visible where a user is
  // actually looking. Optional: only pages backed by unordered/live data
  // need it.
  defaultSort: { type: Object, default: null },
})
const emit = defineEmits(['row-click', 'selection-change'])

const selectOptions = computed(() => ({
  enabled: props.selectable,
  selectOnCheckboxOnly: true,
}))

const sortOptions = computed(() => ({
  enabled: true,
  ...(props.defaultSort ? { initialSortBy: props.defaultSort } : {}),
}))

function handleRowClick({ row }) {
  emit('row-click', row)
}

function handleSelectionChange({ selectedRows }) {
  emit('selection-change', selectedRows)
}
</script>

<template>
  <vue-good-table
    :columns="columns"
    :rows="rows"
    :search-options="{ enabled: searchEnabled, placeholder: 'Search...' }"
    :pagination-options="{ enabled: true, perPage }"
    :sort-options="sortOptions"
    :select-options="selectOptions"
    @row-click="handleRowClick"
    @selected-rows-change="handleSelectionChange"
  >
    <template #table-row="props">
      <slot name="table-row" v-bind="props">
        <span>{{ props.formattedRow[props.column.field] }}</span>
      </slot>
    </template>
  </vue-good-table>
</template>

<style scoped>
.vgt-wrap {
  border: 1px solid #e2e8f0;
  border-radius: 0.5rem;
  overflow: hidden;
}
:deep(.vgt-table) {
  border: none;
}
:deep(.vgt-table thead th) {
  background-color: #f1f5f9;
  color: #475569;
  font-size: 0.6875rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.02em;
  border-color: #e2e8f0;
}
:deep(.vgt-table tbody td) {
  border-color: #f1f5f9;
  color: #334155;
}
:deep(.vgt-table tbody tr:hover td) {
  background-color: #f8fafc;
}
:deep(.vgt-global-search) {
  border-color: #e2e8f0;
  background-color: #ffffff;
}
:deep(.vgt-input) {
  border-color: #e2e8f0;
}
:deep(.vgt-input:focus) {
  outline: none;
  border-color: #2563eb;
}
:deep(.vgt-wrap__footer) {
  background-color: #ffffff;
  border-color: #e2e8f0;
  color: #475569;
}
:deep(.footer__navigation__page-btn) {
  color: #2563eb;
}
</style>
