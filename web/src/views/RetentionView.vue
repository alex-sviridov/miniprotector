<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRetentionPoliciesStore } from '../stores/retentionPolicies'
import { formatKeep, summarizeTarget } from '../utils/retentionRule'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import RetentionFormModal from '../components/retention/RetentionFormModal.vue'

const store = useRetentionPoliciesStore()

const showModal = ref(false)
const editing = ref(null)
const serverError = ref('')
const dragFrom = ref(null)

onMounted(() => {
  store.fetchAll()
})

const ids = computed(() => store.list.map((p) => p.id))

// moveTo reorders by sending the COMPLETE id list in the new order -- the
// server rejects anything incomplete, so a stale view can never silently
// drop rules it didn't see.
function moveTo(from, to) {
  if (from === to || from < 0 || to < 0 || to >= ids.value.length) return
  const next = [...ids.value]
  const [moved] = next.splice(from, 1)
  next.splice(to, 0, moved)
  store.reorder(next)
}

function onDragStart(index, event) {
  if (store.reordering) return
  dragFrom.value = index
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    // Firefox only starts a drag if some data is set.
    event.dataTransfer.setData('text/plain', String(index))
  }
}

function onDragOver(event) {
  // Without preventDefault the row is not a valid drop target.
  if (dragFrom.value !== null) event.preventDefault()
}

function onDrop(index) {
  const from = dragFrom.value
  dragFrom.value = null
  if (from === null) return
  moveTo(from, index)
}

function onDragEnd() {
  dragFrom.value = null
}

function openCreate() {
  editing.value = null
  serverError.value = ''
  showModal.value = true
}

function openEdit(policy) {
  editing.value = policy
  serverError.value = ''
  showModal.value = true
}

function closeModal() {
  showModal.value = false
  editing.value = null
  serverError.value = ''
}

async function save(payload) {
  try {
    if (editing.value) {
      await store.update(editing.value.id, payload)
    } else {
      await store.create(payload)
    }
    closeModal()
  } catch {
    serverError.value = store.error
  }
}

function confirmDelete(policy) {
  if (window.confirm(`Delete retention rule "${policy.name}"?`)) {
    store.remove(policy.id)
  }
}

function isDisabled(policy) {
  return !!policy.disabled_at && policy.disabled_at * 1000 <= Date.now()
}
</script>

<template>
  <div>
    <PageHeader title="Retention" :crumbs="[{ label: 'Retention' }]">
      <template #actions>
        <BaseButton data-test="retention-new" variant="primary" @click="openCreate">
          New Retention Rule
        </BaseButton>
      </template>
    </PageHeader>

    <p class="text-gray-600 mb-4 max-w-3xl">
      How long backed-up file versions are kept. Rules are checked from top to bottom and the first rule
      that matches a file (client, type and path) decides its retention. Drag rows, or use the arrows,
      to change the order.
    </p>

    <p v-if="store.error" class="text-red-600 mb-3" data-test="retention-error">{{ store.error }}</p>

    <StatusMessage :loading="store.loading">
      <p v-if="store.list.length === 0 && !store.error" class="text-gray-500 mb-3">
        No retention rules yet — every file uses the built-in default.
      </p>
      <ol class="border rounded divide-y bg-white">
        <li
          v-for="(policy, index) in store.list"
          :key="policy.id"
          :data-test="`retention-row-${policy.id}`"
          :draggable="!store.reordering"
          class="flex items-center gap-3 px-3 py-2"
          :class="{ 'opacity-60': dragFrom === index }"
          @dragstart="onDragStart(index, $event)"
          @dragover="onDragOver"
          @drop.prevent="onDrop(index)"
          @dragend="onDragEnd"
        >
          <span class="cursor-grab text-gray-400 select-none" title="Drag to reorder" aria-hidden="true">⠿</span>
          <span
            :data-test="`retention-position-${policy.id}`"
            class="w-6 text-right text-gray-500 tabular-nums"
          >{{ index + 1 }}</span>
          <div class="flex-1 min-w-0 grid grid-cols-1 md:grid-cols-4 gap-x-4 gap-y-1">
            <div class="font-medium truncate">
              {{ policy.name }}
              <span v-if="isDisabled(policy)" class="ml-1 text-xs text-gray-500 border rounded px-1">disabled</span>
            </div>
            <div class="text-gray-600 truncate">{{ summarizeTarget(policy.client_filters) }}</div>
            <div class="truncate">
              <span class="text-gray-500">Filesystem</span>
              <span class="font-mono ml-1">{{ policy.retention?.path }}</span>
              <span v-if="policy.retention?.include?.length" class="text-gray-500 ml-1">
                · {{ policy.retention.include.join(', ') }}
              </span>
            </div>
            <div class="font-medium">{{ formatKeep(policy.retention?.keep_seconds) }}</div>
          </div>
          <div class="flex gap-1">
            <BaseButton
              :data-test="`retention-up-${policy.id}`"
              :disabled="index === 0 || store.reordering"
              aria-label="Move up"
              @click="moveTo(index, index - 1)"
            >↑</BaseButton>
            <BaseButton
              :data-test="`retention-down-${policy.id}`"
              :disabled="index === store.list.length - 1 || store.reordering"
              aria-label="Move down"
              @click="moveTo(index, index + 1)"
            >↓</BaseButton>
            <BaseButton :data-test="`retention-edit-${policy.id}`" @click="openEdit(policy)">Edit</BaseButton>
            <BaseButton
              :data-test="`retention-delete-${policy.id}`"
              variant="danger"
              @click="confirmDelete(policy)"
            >Delete</BaseButton>
          </div>
        </li>
        <li
          data-test="retention-default-row"
          class="flex items-center gap-3 px-3 py-2 bg-gray-50 text-gray-600"
        >
          <span class="w-4" aria-hidden="true"></span>
          <span class="w-6 text-right tabular-nums">{{ store.list.length + 1 }}</span>
          <div class="flex-1">
            <span class="font-medium">Everything else</span> — built-in default. Applies to any file no rule
            above matches; 7 days unless the node's <code>RetentionDefaultDays</code> setting says otherwise.
          </div>
        </li>
      </ol>
    </StatusMessage>

    <RetentionFormModal
      v-if="showModal"
      :policy="editing"
      :server-error="serverError"
      @close="closeModal"
      @save="save"
    />
  </div>
</template>
