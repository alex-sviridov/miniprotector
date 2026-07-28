<!-- web/src/components/storage/StorageEditModal.vue -->
<script setup>
import { reactive, onMounted, onBeforeUnmount } from 'vue'
import BaseButton from '../ui/BaseButton.vue'

const props = defineProps({
  policy: { type: Object, default: null },
})
const emit = defineEmits(['close', 'save'])

function parseConfig(configText) {
  try {
    return JSON.parse(configText || '{}')
  } catch {
    return {}
  }
}

const form = reactive({
  name: props.policy?.name || '',
  hostname: props.policy?.hostname || '',
  port: props.policy ? String(props.policy.port) : '',
  storageType: parseConfig(props.policy?.config).backend || 'filesystem',
  path: parseConfig(props.policy?.config).root || '',
})

const errors = reactive({ message: '' })

function close() {
  emit('close')
}

function onKeydown(event) {
  if (event.key === 'Escape') close()
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})

function submit() {
  errors.message = ''
  const port = Number(form.port)

  if (!form.name.trim()) {
    errors.message = 'Name is required.'
    return
  }
  if (!form.hostname.trim()) {
    errors.message = 'Hostname is required.'
    return
  }
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    errors.message = 'A valid port between 1 and 65535 is required.'
    return
  }
  if (!form.path.trim()) {
    errors.message = 'Filesystem path is required.'
    return
  }

  emit('save', {
    name: form.name.trim(),
    hostname: form.hostname.trim(),
    port,
    config: JSON.stringify({ backend: form.storageType, root: form.path.trim() }),
    client_filters: { hostnames: [], labels: {} },
  })
}
</script>

<template>
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center" @click.self="close">
    <div class="bg-white rounded p-4 max-w-lg w-full">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-lg font-semibold">{{ policy ? 'Edit Storage Policy' : 'New Storage Policy' }}</h2>
        <BaseButton variant="secondary" data-test="storage-cancel" @click="close">Cancel</BaseButton>
      </div>
      <p v-if="errors.message" class="text-red-600 mb-4">{{ errors.message }}</p>
      <form @submit.prevent="submit" class="space-y-4">
        <div>
          <label class="block font-medium mb-1">Name</label>
          <input data-test="storage-name-input" v-model="form.name" class="w-full border rounded px-2 py-1" />
        </div>
        <div>
          <label class="block font-medium mb-1">Hostname</label>
          <input data-test="storage-hostname-input" v-model="form.hostname" class="w-full border rounded px-2 py-1" />
        </div>
        <div>
          <label class="block font-medium mb-1">Port</label>
          <input data-test="storage-port-input" v-model="form.port" type="number" class="w-full border rounded px-2 py-1" />
        </div>
        <div>
          <label class="block font-medium mb-1">Storage Type</label>
          <select data-test="storage-type-select" v-model="form.storageType" class="w-full border rounded px-2 py-1">
            <option value="filesystem">filesystem</option>
          </select>
        </div>
        <div v-if="form.storageType === 'filesystem'">
          <label class="block font-medium mb-1">Filesystem Path</label>
          <input data-test="storage-path-input" v-model="form.path" class="w-full border rounded px-2 py-1" />
        </div>
        <BaseButton type="submit" variant="primary">
          {{ policy ? 'Save Changes' : 'Create Storage Policy' }}
        </BaseButton>
      </form>
    </div>
  </div>
</template>
