<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useClientsStore } from '../stores/clients'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import BaseField from '../components/ui/BaseField.vue'
import BaseSelect from '../components/ui/BaseSelect.vue'

const restoreCart = useRestoreCartStore()
const clients = useClientsStore()
const submission = useRestoreSubmissionStore()

const destinationHost = ref('')

onMounted(() => {
  if (clients.list.length === 0) clients.fetchAll()
})

function label(entry) {
  return entry.host === null ? `${entry.path}/*` : `${entry.path} (${entry.host})`
}

function remove(entry) {
  restoreCart.removeEntry(entry)
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
      <ul>
        <li v-for="entry in restoreCart.entries" :key="`${entry.host ?? ''}:${entry.path}`">
          {{ label(entry) }}
          <button
            type="button"
            :data-test="`remove-${entry.host ?? ''}:${entry.path}`"
            @click="remove(entry)"
          >
            Remove
          </button>
        </li>
      </ul>
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
