<script setup>
import { computed, onMounted, ref } from 'vue'
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
// Key of the entry currently being edited (see entryKey), or null when no
// destination-path cell is in edit mode. Only one cell can be edited at a
// time.
const editingKey = ref(null)

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
}

function commitEdit(entry, value) {
  restoreCart.setDestPath(entry, value)
  editingKey.value = null
}

const canSubmit = computed(
  () => restoreCart.hasSelections && destinationHost.value !== '' && !submission.submitting
)

function submit() {
  submission.submit(destinationHost.value)
}
</script>

<template>
  <div>
    <PageHeader title="Restore" :crumbs="[{ label: 'Restore' }]" />
    <StatusMessage :empty="restoreCart.entries.length === 0" empty-text="No files selected for restore yet.">
      <table data-test="restore-table">
        <thead>
          <tr>
            <th>Storage Host</th>
            <th>Source Host</th>
            <th>Source Path</th>
            <th>Destination Path</th>
            <th>Size</th>
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
                :data-test="`dest-path-input-${entryKey(entry)}`"
                :value="entry.destPath"
                @blur="commitEdit(entry, $event.target.value)"
                @keyup.enter="commitEdit(entry, $event.target.value)"
              />
              <span v-else :data-test="`dest-path-text-${entryKey(entry)}`" @click="startEditing(entry)">
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
      <BaseButton data-test="submit-restore" variant="primary" :disabled="!canSubmit" @click="submit">
        Submit restore
      </BaseButton>
    </StatusMessage>
    <!-- Outside StatusMessage on purpose: its slot only renders for a
         non-empty cart, but the error (e.g. "Nothing selected for restore.")
         and the results of an already-submitted restore must stay visible
         even once the cart is empty. -->
    <p v-if="submission.error" data-test="submission-error">{{ submission.error }}</p>
    <ul v-if="submission.results.length" data-test="submission-results">
      <li v-for="result in submission.results" :key="result.storeHost">
        <span v-if="result.status === 'success'">Created {{ result.policy.name }} from {{ result.storeHost }}</span>
        <span v-else>{{ result.storeHost }}: {{ result.message }}</span>
      </li>
    </ul>
  </div>
</template>
