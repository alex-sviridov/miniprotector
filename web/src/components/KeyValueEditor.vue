<script setup>
import { reactive, computed, watch } from 'vue'

const props = defineProps({
  modelValue: { type: Object, default: () => ({}) },
  label: { type: String, required: true },
  testPrefix: { type: String, required: true },
})
const emit = defineEmits(['save'])

function toRows(obj) {
  return Object.entries(obj || {}).map(([key, value]) => ({ key, value }))
}

const snapshot = reactive(toRows(props.modelValue))
const draft = reactive(toRows(props.modelValue))

watch(
  () => props.modelValue,
  (newValue) => {
    snapshot.splice(0, snapshot.length, ...toRows(newValue))
    draft.splice(0, draft.length, ...toRows(newValue))
  }
)

function addRow() {
  draft.push({ key: '', value: '' })
}
function removeRow(i) {
  draft.splice(i, 1)
}

function toMap(rows) {
  return Object.fromEntries(rows.map((r) => [r.key.trim(), r.value]).filter(([key]) => key))
}

const dirty = computed(() => JSON.stringify(toMap(draft)) !== JSON.stringify(toMap(snapshot)))

function submit() {
  const draftMap = toMap(draft)
  const snapshotMap = toMap(snapshot)
  const set = {}
  for (const [key, value] of Object.entries(draftMap)) {
    if (snapshotMap[key] !== value) set[key] = value
  }
  const unset = Object.keys(snapshotMap).filter((key) => !(key in draftMap))
  emit('save', { set, unset })
}
</script>

<template>
  <div>
    <label class="block font-medium mb-1">{{ label }}</label>
    <div v-for="(_, i) in draft" :key="i" class="flex gap-2 mb-1">
      <input
        :data-test="`${testPrefix}-key-input`"
        v-model="draft[i].key"
        placeholder="key"
        class="flex-1 border rounded px-2 py-1"
      />
      <input
        :data-test="`${testPrefix}-value-input`"
        v-model="draft[i].value"
        placeholder="value"
        class="flex-1 border rounded px-2 py-1"
      />
      <button type="button" :data-test="`${testPrefix}-remove`" @click="removeRow(i)" class="border rounded px-2">
        Remove
      </button>
    </div>
    <button type="button" :data-test="`${testPrefix}-add`" @click="addRow" class="border rounded px-3 py-1 mr-2">
      Add
    </button>
    <button
      type="button"
      :data-test="`${testPrefix}-update`"
      :disabled="!dirty"
      @click="submit"
      class="bg-blue-600 text-white rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed"
    >
      Update
    </button>
  </div>
</template>
