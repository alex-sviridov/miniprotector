<script setup>
import { computed } from 'vue'
import { formatTimestamp } from '../../utils/format'
import DataTable from '../ui/DataTable.vue'

const props = defineProps({
  facets: { type: Array, required: true },
  error: { type: String, default: null },
  nameLabel: { type: String, required: true },
  countLabel: { type: String, required: true },
})

const selected = defineModel('selected', { type: Array, required: true })

const columns = computed(() => [
  { label: props.nameLabel, field: 'name', sortable: true },
  { label: props.countLabel, field: 'count', sortable: true, type: 'number' },
  {
    label: 'Last seen',
    field: 'last_seen',
    sortable: true,
    type: 'number',
    formatFn: (v) => formatTimestamp(v) || '—',
  },
])

const rows = computed(() =>
  props.facets.map((f) => ({ ...f, vgtSelected: selected.value.includes(f.name) }))
)

function onSelectionChange(selectedRows) {
  selected.value = selectedRows.map((r) => r.name)
}
</script>

<template>
  <div>
    <DataTable :columns="columns" :rows="rows" selectable @selection-change="onSelectionChange" />
    <p v-if="error" class="text-red-600 text-sm mt-2">{{ error }}</p>
  </div>
</template>
