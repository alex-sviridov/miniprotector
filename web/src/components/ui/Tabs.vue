<!-- web/src/components/ui/Tabs.vue -->
<script setup>
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'

const props = defineProps({
  tabs: { type: Array, required: true },
})

const route = useRoute()
const router = useRouter()

const activeKey = computed(() => {
  const key = route.query.tab
  return props.tabs.some((tab) => tab.key === key) ? key : props.tabs[0].key
})

function selectTab(key) {
  router.replace({ query: { ...route.query, tab: key } })
}
</script>

<template>
  <div>
    <div class="flex gap-4 border-b mb-4">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        :data-test="`tab-${tab.key}`"
        class="pb-2 px-1 -mb-px border-b-2"
        :class="
          activeKey === tab.key
            ? 'border-blue-600 text-blue-600 font-medium'
            : 'border-transparent text-gray-500 hover:text-gray-700'
        "
        @click="selectTab(tab.key)"
      >
        {{ tab.label }}
      </button>
    </div>
    <slot :name="activeKey" />
  </div>
</template>
