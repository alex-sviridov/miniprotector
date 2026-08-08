<script setup>
import { computed } from 'vue'
import { pathCrumbs } from '../../utils/pathSplit'

const props = defineProps({
  currentPath: { type: String, default: null },
})
const emit = defineEmits(['navigate'])

const crumbs = computed(() => (props.currentPath ? pathCrumbs(props.currentPath) : []))
</script>

<template>
  <nav
    data-test="directory-path-bar"
    aria-label="Directory path"
    class="flex items-center gap-1 text-sm text-gray-600 mb-2"
  >
    <button type="button" data-test="crumb-home" class="hover:underline" @click="emit('navigate', null)">
      Home
    </button>
    <template v-for="(crumb, index) in crumbs" :key="crumb.path">
      <span class="text-gray-400">&rsaquo;</span>
      <button
        v-if="index < crumbs.length - 1"
        type="button"
        data-test="crumb"
        class="hover:underline"
        @click="emit('navigate', crumb.path)"
      >
        {{ crumb.name }}
      </button>
      <span v-else data-test="crumb-current" class="font-semibold">{{ crumb.name }}</span>
    </template>
  </nav>
</template>
