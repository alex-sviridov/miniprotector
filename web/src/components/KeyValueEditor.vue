<script setup>
import { reactive, computed, watch } from 'vue'
import RepeatableFieldList from './ui/RepeatableFieldList.vue'
import BaseButton from './ui/BaseButton.vue'

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
    <RepeatableFieldList :items="draft" :new-item="() => ({ key: '', value: '' })" add-label="Add" :test-prefix="testPrefix">
      <template #row="{ index }">
        <input
          :data-test="`${testPrefix}-key-input`"
          v-model="draft[index].key"
          placeholder="key"
          class="flex-1 border rounded px-2 py-1"
        />
        <input
          :data-test="`${testPrefix}-value-input`"
          v-model="draft[index].value"
          placeholder="value"
          class="flex-1 border rounded px-2 py-1"
        />
      </template>
    </RepeatableFieldList>
    <BaseButton variant="primary" :data-test="`${testPrefix}-update`" :disabled="!dirty" class="mt-2" @click="submit">
      Update
    </BaseButton>
  </div>
</template>
