<script setup>
import { reactive, computed, watch } from 'vue'
import RepeatableFieldList from './ui/RepeatableFieldList.vue'
import BaseButton from './ui/BaseButton.vue'

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

function normalize(list) {
  return [...new Set(list.map((s) => s.trim()).filter(Boolean))].sort()
}

const dirty = computed(() => JSON.stringify(draft) !== JSON.stringify(snapshot))

function submit() {
  const draftSet = new Set(normalize(draft))
  const snapshotSet = new Set(normalize(snapshot))
  const add = [...draftSet].filter((s) => !snapshotSet.has(s))
  const remove = [...snapshotSet].filter((s) => !draftSet.has(s))
  // dirty is a raw draft-vs-snapshot comparison (so an empty added row
  // enables Update immediately), but that can leave add/remove both empty
  // after normalization -- e.g. an added-then-untouched blank row. Skip
  // the round-trip rather than emitting a no-op save.
  if (add.length === 0 && remove.length === 0) return
  emit('save', { add, remove })
}
</script>

<template>
  <div>
    <label class="block font-medium mb-1">SANs</label>
    <RepeatableFieldList :items="draft" add-label="Add SAN" test-prefix="san">
      <template #row="{ index }">
        <input data-test="san-input" v-model="draft[index]" class="flex-1 border rounded px-2 py-1" />
      </template>
    </RepeatableFieldList>
    <BaseButton variant="primary" data-test="san-update" :disabled="!dirty" class="mt-2" @click="submit">
      Update
    </BaseButton>
  </div>
</template>
