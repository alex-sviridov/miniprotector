<script setup>
import { onMounted, computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'
import KeyValueEditor from '../components/KeyValueEditor.vue'
import SanListEditor from '../components/SanListEditor.vue'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DetailList from '../components/ui/DetailList.vue'
import BaseButton from '../components/ui/BaseButton.vue'

const route = useRoute()
const clients = useClientsStore()
const hostname = computed(() => route.params.hostname)
const client = computed(() => clients.byHostname[hostname.value])

const showToken = ref(false)
const tokenValue = ref('')

function checkPendingToken() {
  if (clients.pendingToken && clients.pendingToken.hostname === hostname.value) {
    tokenValue.value = clients.pendingToken.token
    showToken.value = true
    clients.pendingToken = null
  }
}

onMounted(async () => {
  try {
    await clients.fetchOne(hostname.value)
  } catch {
    // error already recorded on clients.error by the store
  }
  checkPendingToken()
})

async function confirmRevoke() {
  if (window.confirm(`Revoke ${hostname.value}?`)) {
    try {
      await clients.revoke(hostname.value)
    } catch {
      // error already recorded on clients.error by the store
    }
  }
}
async function confirmUnrevoke() {
  if (window.confirm(`Unrevoke ${hostname.value}?`)) {
    try {
      await clients.unrevoke(hostname.value)
    } catch {
      // error already recorded on clients.error by the store
    }
  }
}
async function reenroll() {
  try {
    await clients.reenroll(hostname.value)
    checkPendingToken()
  } catch {
    // error already recorded on clients.error by the store
  }
}

async function copyToken() {
  try {
    await navigator.clipboard.writeText(tokenValue.value)
  } catch {
    // clipboard access can fail (insecure context, denied permission);
    // the token is still visible in the banner for manual copying
  }
}

async function saveDescription({ set, unset }) {
  try {
    await clients.updateDescription(hostname.value, set, unset)
  } catch {
    // error already recorded on clients.error by the store
  }
}
async function saveAttributes({ set, unset }) {
  try {
    await clients.updateAttributes(hostname.value, set, unset)
  } catch {
    // error already recorded on clients.error by the store
  }
}
async function saveSans({ add, remove }) {
  try {
    await clients.updateSans(hostname.value, add, remove)
  } catch {
    // error already recorded on clients.error by the store
  }
}

const detailRows = computed(() => {
  if (!client.value) return []
  return [
    { key: 'revoked', label: 'Revoked', value: client.value.revoked ? 'Yes' : 'No' },
    { key: 'revokedAt', label: 'Revoked At', value: formatTimestamp(client.value.revoked_at) || '—' },
    { key: 'lastSeen', label: 'Last Seen', value: formatTimestamp(client.value.last_seen_at) || 'Never' },
  ]
})
</script>

<template>
  <div>
    <PageHeader :title="hostname" :crumbs="[{ label: 'Clients', to: { name: 'clients' } }, { label: hostname }]" />
    <StatusMessage :loading="clients.loading" :error="clients.error">
      <template v-if="client">
        <div v-if="showToken" data-test="token-banner" class="bg-yellow-50 border border-yellow-400 rounded p-3 mb-4">
          <p class="font-medium">Enrollment token (shown once):</p>
          <code data-test="token-value" class="block bg-white border rounded px-2 py-1 my-1 break-all">{{ tokenValue }}</code>
          <BaseButton variant="secondary" class="mr-2" @click="copyToken">Copy</BaseButton>
          <BaseButton variant="secondary" @click="showToken = false">Dismiss</BaseButton>
          <p class="text-sm text-gray-600 mt-1">This token won't be shown again — relay it to the node now.</p>
        </div>

        <div class="mb-4 flex gap-2">
          <BaseButton v-if="!client.revoked" data-test="revoke-button" variant="danger" @click="confirmRevoke">
            Revoke
          </BaseButton>
          <BaseButton v-else data-test="unrevoke-button" variant="secondary" @click="confirmUnrevoke">
            Unrevoke
          </BaseButton>
          <BaseButton data-test="reenroll-button" variant="secondary" @click="reenroll">Re-enroll</BaseButton>
        </div>

        <DetailList :rows="detailRows" class="mb-6" />

        <KeyValueEditor
          :model-value="client.descriptions || {}"
          label="Description"
          test-prefix="description"
          class="mb-6"
          @save="saveDescription"
        />
        <KeyValueEditor
          :model-value="client.attributes || {}"
          label="Attributes"
          test-prefix="attribute"
          class="mb-6"
          @save="saveAttributes"
        />
        <SanListEditor :model-value="client.sans || []" @save="saveSans" />
      </template>
    </StatusMessage>
  </div>
</template>
