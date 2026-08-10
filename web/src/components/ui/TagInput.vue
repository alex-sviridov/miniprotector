<script setup>
import { computed, reactive, ref, watch } from 'vue'
import { validateGlobPattern, findParentChildConflict } from '../../utils/globPattern'

const props = defineProps({
  items: { type: Array, required: true },
  testPrefix: { type: String, required: true },
  placeholder: { type: String, default: '' },
})

let nextId = 0

function errorFor(value, otherValues) {
  const syntax = validateGlobPattern(value)
  if (!syntax.valid) return syntax.error
  const conflict = findParentChildConflict(otherValues, value)
  if (conflict) return `overlaps with "${conflict}"`
  return undefined
}

function buildChips(values) {
  return values.map((value) => ({ id: nextId++, value }))
}

const chips = reactive(buildChips(props.items))
const text = ref('')

// RepeatableFieldList keys its v-for by array index, not a stable id, so
// removing a row can leave this component instance mounted while its
// `items` prop now points at a different row's data. Resync `chips`
// whenever `items` diverges from what we currently show. `syncItems()`
// below always leaves `items` and `chips` in agreement by construction, so
// this guard naturally no-ops in response to our own writes.
watch(
  () => props.items,
  (items) => {
    const current = chips.map((c) => c.value)
    if (items.length === current.length && items.every((v, i) => v === current[i])) return
    chips.splice(0, chips.length, ...buildChips(items))
  },
  { deep: true }
)

// Errors are derived reactively from the current chip set so that removing
// a chip that was causing a conflict immediately clears the error on the
// chip(s) it conflicted with, instead of leaving stale styling behind.
const chipErrors = computed(() =>
  chips.map((c, i) => errorFor(c.value, chips.filter((_, j) => j !== i).map((x) => x.value)))
)

function syncItems() {
  props.items.splice(0, props.items.length, ...chips.map((c) => c.value))
}

function commit(rawText) {
  const values = rawText
    .split(',')
    .map((v) => v.trim())
    .filter(Boolean)
  if (values.length === 0) return
  for (const value of values) {
    chips.push({ id: nextId++, value })
  }
  syncItems()
}

function removeChip(id) {
  const index = chips.findIndex((c) => c.id === id)
  if (index !== -1) chips.splice(index, 1)
  syncItems()
}

function removeLast() {
  if (chips.length === 0) return
  chips.pop()
  syncItems()
}

function onKeydown(event) {
  if (event.key === 'Enter' || event.key === ',') {
    event.preventDefault()
    commit(text.value)
    text.value = ''
  } else if (event.key === 'Backspace' && text.value === '') {
    removeLast()
  }
}

function onBlur() {
  if (text.value.trim()) {
    commit(text.value)
    text.value = ''
  }
}

function isValid() {
  return chipErrors.value.every((e) => !e)
}

defineExpose({ isValid })
</script>

<template>
  <div>
    <div class="flex flex-wrap gap-1 mb-1">
      <span
        v-for="(chip, index) in chips"
        :key="chip.id"
        :data-test="`${testPrefix}-chip`"
        :title="chipErrors[index] || ''"
        class="inline-flex items-center gap-1 border rounded px-2 py-0.5 text-sm"
        :class="chipErrors[index] ? 'border-red-500 text-red-600' : 'border-gray-300'"
      >
        {{ chip.value }}
        <button
          type="button"
          :data-test="`${testPrefix}-chip-remove`"
          class="leading-none"
          @click="removeChip(chip.id)"
        >
          ×
        </button>
      </span>
    </div>
    <input
      :data-test="`${testPrefix}-input`"
      :placeholder="placeholder"
      v-model="text"
      class="border rounded px-2 py-1 text-sm"
      @keydown="onKeydown"
      @blur="onBlur"
    />
  </div>
</template>
