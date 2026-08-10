<script setup>
import { reactive, ref } from 'vue'
import { validateGlobPattern, findParentChildConflict } from '../../utils/globPattern'

const props = defineProps({
  items: { type: Array, required: true },
  testPrefix: { type: String, required: true },
  placeholder: { type: String, default: '' },
})

let nextId = 0

function errorFor(value, existingValues) {
  const syntax = validateGlobPattern(value)
  if (!syntax.valid) return syntax.error
  const conflict = findParentChildConflict(existingValues, value)
  if (conflict) return `overlaps with "${conflict}"`
  return undefined
}

const chips = reactive(
  props.items.reduce((acc, value) => {
    const priorValues = acc.map((c) => c.value)
    acc.push({ id: nextId++, value, error: errorFor(value, priorValues) })
    return acc
  }, [])
)
const text = ref('')

function syncItems() {
  props.items.splice(0, props.items.length, ...chips.map((c) => c.value))
}

function commit(rawText) {
  const value = rawText.trim()
  if (!value) return
  const error = errorFor(
    value,
    chips.map((c) => c.value)
  )
  chips.push({ id: nextId++, value, error })
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
  return chips.every((c) => !c.error)
}

defineExpose({ isValid })
</script>

<template>
  <div>
    <div class="flex flex-wrap gap-1 mb-1">
      <span
        v-for="chip in chips"
        :key="chip.id"
        :data-test="`${testPrefix}-chip`"
        :title="chip.error || ''"
        class="inline-flex items-center gap-1 border rounded px-2 py-0.5 text-sm"
        :class="chip.error ? 'border-red-500 text-red-600' : 'border-gray-300'"
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
