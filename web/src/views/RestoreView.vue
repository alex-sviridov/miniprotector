<script setup>
import { useRestoreCartStore } from '../stores/restoreCart'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'

const restoreCart = useRestoreCartStore()

function label(entry) {
  return entry.host === null ? `${entry.path}/*` : `${entry.path} (${entry.host})`
}
</script>

<template>
  <div>
    <PageHeader title="Restore" :crumbs="[{ label: 'Restore' }]" />
    <StatusMessage :empty="restoreCart.entries.length === 0" empty-text="No files selected for restore yet.">
      <ul>
        <li v-for="entry in restoreCart.entries" :key="`${entry.host ?? ''}:${entry.path}`">
          {{ label(entry) }}
        </li>
      </ul>
    </StatusMessage>
  </div>
</template>
