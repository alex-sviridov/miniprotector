<!-- web/src/components/ui/RepeatableFieldList.vue -->
<script setup>
const props = defineProps({
  items: { type: Array, required: true },
  newItem: { type: Function, default: () => '' },
  addLabel: { type: String, required: true },
  removeLabel: { type: String, default: 'Remove' },
  rowClass: { type: String, default: 'flex gap-2 mb-1' },
  testPrefix: { type: String, required: true },
})

function add() {
  props.items.push(props.newItem())
}
function remove(index) {
  props.items.splice(index, 1)
}
</script>

<template>
  <div>
    <div v-for="(item, index) in items" :key="index" :class="rowClass">
      <slot name="row" :item="item" :index="index" />
      <button type="button" :data-test="`${testPrefix}-remove`" class="border rounded px-2 self-start" @click="remove(index)">
        {{ removeLabel }}
      </button>
    </div>
    <button type="button" :data-test="`${testPrefix}-add`" class="border rounded px-3 py-1" @click="add">
      {{ addLabel }}
    </button>
  </div>
</template>
