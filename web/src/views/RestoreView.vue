<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useClientsStore } from '../stores/clients'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'
import { formatBytes } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import BaseField from '../components/ui/BaseField.vue'
import BaseSelect from '../components/ui/BaseSelect.vue'

const restoreCart = useRestoreCartStore()
const clients = useClientsStore()
const submission = useRestoreSubmissionStore()

const destinationHost = ref('')
const overwrite = ref(false)
// Key of the entry currently being edited (see entryKey), or null when no
// destination-path cell is in edit mode. Only one cell can be edited at a
// time.
const editingKey = ref(null)
// Template ref for the single in-flight editing <input> (only one row's
// input is ever rendered at a time, since editingKey is shared), used to
// focus it as soon as it's revealed.
const editingInput = ref(null)

onMounted(() => {
  if (clients.list.length === 0) clients.fetchAll()
})

// entryKey matches the (host, path) pairing the restore cart itself keys
// rules by -- also reused as the data-test suffix for a row and its
// destination-path cell.
function entryKey(entry) {
  return `${entry.host ?? ''}:${entry.path}`
}

function sourcePathLabel(entry) {
  return entry.host === null ? `${entry.path}/*` : entry.path
}

function remove(entry) {
  restoreCart.removeEntry(entry)
}

function startEditing(entry) {
  editingKey.value = entryKey(entry)
  nextTick(() => editingInput.value?.focus())
}

// Guarded against double-firing: in a real browser, Enter triggers
// commitEdit -> editingKey is cleared -> Vue removes the (still-focused)
// input from the DOM -> the removal itself fires a native blur, which is
// still wired to commitEdit at that instant. Without this guard that fires
// restoreCart.setDestPath a second time for the same edit.
function commitEdit(entry, value) {
  if (editingKey.value !== entryKey(entry)) return
  restoreCart.setDestPath(entry, value)
  editingKey.value = null
}

const canSubmit = computed(
  () => restoreCart.hasSelections && destinationHost.value !== '' && !submission.submitting
)

function verify() {
  submission.submit(destinationHost.value, { mode: 'verify', overwrite: overwrite.value })
}

function restore() {
  submission.submit(destinationHost.value, { mode: 'restore', overwrite: overwrite.value })
}
</script>

<template>
  <div>
    <PageHeader title="Restore" :crumbs="[{ label: 'Restore' }]" />
    <StatusMessage :empty="restoreCart.entries.length === 0" empty-text="No files selected for restore yet.">
      <table data-test="restore-table" class="w-full text-left">
        <thead>
          <tr>
            <th class="font-medium">Storage Host</th>
            <th class="font-medium">Source Host</th>
            <th class="font-medium">Source Path</th>
            <th class="font-medium">Destination Path</th>
            <th class="font-medium">Size</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="entry in restoreCart.entries" :key="entryKey(entry)" :data-test="`restore-row-${entryKey(entry)}`">
            <td>{{ entry.storeHost ?? '—' }}</td>
            <td>{{ entry.host ?? '—' }}</td>
            <td>{{ sourcePathLabel(entry) }}</td>
            <td>
              <input
                v-if="editingKey === entryKey(entry)"
                :ref="(el) => (editingInput = el)"
                :data-test="`dest-path-input-${entryKey(entry)}`"
                :value="entry.destPath"
                @blur="commitEdit(entry, $event.target.value)"
                @keyup.enter="commitEdit(entry, $event.target.value)"
              />
              <span
                v-else
                :data-test="`dest-path-text-${entryKey(entry)}`"
                class="cursor-pointer text-blue-600 hover:underline"
                tabindex="0"
                @click="startEditing(entry)"
                @keyup.enter="startEditing(entry)"
              >
                {{ entry.destPath }}
              </span>
            </td>
            <td>{{ formatBytes(entry.size) }}</td>
            <td>
              <button type="button" :data-test="`remove-${entryKey(entry)}`" @click="remove(entry)">Remove</button>
            </td>
          </tr>
        </tbody>
      </table>
      <BaseField label="Destination host">
        <BaseSelect data-test="destination-select" v-model="destinationHost">
          <option value="" disabled>Select a destination host</option>
          <option v-for="client in clients.list" :key="client.hostname" :value="client.hostname">
            {{ client.hostname }}
          </option>
        </BaseSelect>
      </BaseField>
      <label class="flex items-center gap-2">
        <input type="checkbox" data-test="overwrite-checkbox" v-model="overwrite" />
        Overwrite existing files
      </label>
      <BaseButton data-test="verify-button" variant="secondary" :disabled="!canSubmit" @click="verify">
        Verify
      </BaseButton>
      <BaseButton data-test="restore-button" variant="primary" :disabled="!canSubmit" @click="restore">
        Restore
      </BaseButton>
    </StatusMessage>
    <!-- Outside StatusMessage on purpose: its slot only renders for a
         non-empty cart, but the error (e.g. "Nothing selected for restore.")
         and the results of an already-submitted restore must stay visible
         even once the cart is empty. -->
    <p v-if="submission.error" data-test="submission-error">{{ submission.error }}</p>
    <ul v-if="submission.results.length" data-test="submission-results">
      <li v-for="result in submission.results" :key="result.storeHost">
        <span v-if="result.status === 'success'">Started verification policy {{ result.policy.name }} from {{ result.storeHost }}</span>
        <span v-else>{{ result.storeHost }}: {{ result.message }}</span>
      </li>
    </ul>
  </div>
</template>
