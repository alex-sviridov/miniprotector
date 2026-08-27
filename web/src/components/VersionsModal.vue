<script setup>
import { onMounted, onBeforeUnmount, ref, computed } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import BaseButton from './ui/BaseButton.vue'

const props = defineProps({
  path: { type: String, required: true },
  sourceHost: { type: String, default: null },
})
const emit = defineEmits(['close', 'select-version', 'use-latest'])

const catalog = useCatalogStore()
const versions = ref([])
const loading = ref(true)
const error = ref(null)

onMounted(async () => {
  try {
    versions.value = await catalog.fetchPathVersions(props.path, props.sourceHost)
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})

// A folder's own path is host-agnostic (sourceHost prop is null), and its
// fetched rows can span every source host that ever had something at that
// path -- worth calling out, since pinning a version still narrows the
// selection to whichever host(s) actually captured something at that exact
// instant (see restoreRules.js/restoreCart.js's setVersionWindow).
const spansMultipleHosts = computed(() => new Set(versions.value.map((v) => v.source_host)).size > 1)

function close() {
  emit('close')
}

function useLatest() {
  emit('use-latest')
}

function selectVersion(version) {
  emit('select-version', version)
}

function onKeydown(event) {
  if (event.key === 'Escape') close()
}
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-3xl w-full max-h-[80vh] overflow-auto">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">
          Versions of {{ path }}<span v-if="sourceHost"> on {{ sourceHost }}</span>
        </h2>
        <div class="flex gap-2">
          <BaseButton data-test="use-latest" variant="secondary" @click="useLatest">Use latest</BaseButton>
          <BaseButton data-test="close" variant="secondary" @click="close">Close</BaseButton>
        </div>
      </div>
      <p v-if="spansMultipleHosts" data-test="multi-host-note" class="text-sm text-gray-600 mb-2">
        This folder was captured separately per host — picking a version scopes this selection to
        that host's capture only.
      </p>
      <p v-if="loading" data-test="versions-loading">Loading versions…</p>
      <p v-else-if="error" data-test="versions-error" class="text-red-600">{{ error }}</p>
      <table v-else class="w-full text-left border-collapse">
        <thead>
          <tr class="border-b">
            <th class="py-2 pr-4">Captured</th>
            <th class="py-2 pr-4">Source Host</th>
            <th class="py-2 pr-4">Size</th>
            <th class="py-2 pr-4">Mode</th>
            <th class="py-2 pr-4">Modified</th>
            <th class="py-2 pr-4">Job ID</th>
            <th class="py-2 pr-4">Store Host</th>
            <th class="py-2 pr-4"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="version in versions" :key="version.id" class="border-b">
            <td class="py-2 pr-4">{{ formatTimestamp(version.store_created_at) || '—' }}</td>
            <td class="py-2 pr-4">{{ version.source_host }}</td>
            <td class="py-2 pr-4">{{ formatBytes(version.size) }}</td>
            <td class="py-2 pr-4">{{ version.mode }}</td>
            <td class="py-2 pr-4">{{ formatTimestamp(version.mod_time) || '—' }}</td>
            <td class="py-2 pr-4">{{ version.job_id }}</td>
            <td class="py-2 pr-4">{{ version.store_host }}</td>
            <td class="py-2 pr-4">
              <BaseButton :data-test="`restore-version-${version.id}`" variant="secondary" @click="selectVersion(version)">
                Restore this version
              </BaseButton>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
