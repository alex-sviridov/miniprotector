<script setup>
import { computed, ref } from 'vue'
import { formatTimestamp } from '../../utils/format'
import DataTable from '../ui/DataTable.vue'

const props = defineProps({
  facets: { type: Array, required: true },
  error: { type: String, default: null },
  nameLabel: { type: String, required: true },
  countLabel: { type: String, required: true },
})

const selected = defineModel('selected', { type: Array, required: true })
const search = ref('')

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

const visibleFacets = computed(() => {
  if (!search.value) return props.facets
  const needle = search.value.toLowerCase()
  return props.facets.filter((f) => f.name.toLowerCase().includes(needle))
})

const rows = computed(() =>
  visibleFacets.value.map((f) => ({ ...f, vgtSelected: selected.value.includes(f.name) }))
)

// onSelectionChange only ever reflects the currently-visible rows (vue-good-table
// has no notion of anything filtered out of view). Names that are selected but
// not currently visible -- because the local search filter hides them, or
// because a facet refetch's new `facets` prop no longer contains them -- must
// be preserved rather than silently dropped when the visible selection changes.
function onSelectionChange(selectedRows) {
  const visibleNames = new Set(rows.value.map((r) => r.name))
  const hiddenButStillSelected = selected.value.filter((name) => !visibleNames.has(name))
  const nowSelectedVisible = selectedRows.map((r) => r.name)
  selected.value = [...new Set([...hiddenButStillSelected, ...nowSelectedVisible])]
}
</script>

<template>
  <div>
    <input
      v-model="search"
      placeholder="Filter by name…"
      class="border rounded px-2 py-1 w-full mb-2"
    />
    <DataTable
      :columns="columns"
      :rows="rows"
      :search-enabled="false"
      selectable
      @selection-change="onSelectionChange"
    />
    <p v-if="error" class="text-red-600 text-sm mt-2">{{ error }}</p>
  </div>
</template>
