<script setup>
import { reactive, computed, watch } from 'vue'

const props = defineProps({
  modelValue: { type: Array, default: () => [] },
})
const emit = defineEmits(['save'])

const snapshot = reactive([...(props.modelValue || [])])
const draft = reactive([...(props.modelValue || [])])

watch(
  () => props.modelValue,
  (newValue) => {
    snapshot.splice(0, snapshot.length, ...(newValue || []))
    draft.splice(0, draft.length, ...(newValue || []))
  }
)

function addRow() {
  draft.push('')
}
function removeRow(i) {
  draft.splice(i, 1)
}

function normalize(list) {
  return [...new Set(list.map((s) => s.trim()).filter(Boolean))].sort()
}

const dirty = computed(() => JSON.stringify(draft) !== JSON.stringify(snapshot))

function submit() {
  const draftSet = new Set(normalize(draft))
  const snapshotSet = new Set(normalize(snapshot))
  const add = [...draftSet].filter((s) => !snapshotSet.has(s))
  const remove = [...snapshotSet].filter((s) => !draftSet.has(s))
  emit('save', { add, remove })
}
</script>

<template>
  <div>
    <label class="block font-medium mb-1">SANs</label>
    <div v-for="(_, i) in draft" :key="i" class="flex gap-2 mb-1">
      <input data-test="san-input" v-model="draft[i]" class="flex-1 border rounded px-2 py-1" />
      <button type="button" data-test="san-remove" @click="removeRow(i)" class="border rounded px-2">
        Remove
      </button>
    </div>
    <button type="button" data-test="san-add" @click="addRow" class="border rounded px-3 py-1 mr-2">
      Add SAN
    </button>
    <button
      type="button"
      data-test="san-update"
      :disabled="!dirty"
      @click="submit"
      class="bg-blue-600 text-white rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed"
    >
      Update
    </button>
  </div>
</template>
