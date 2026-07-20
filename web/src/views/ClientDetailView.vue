<script setup>
import { onMounted, computed, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useClientsStore } from '../stores/clients'
import { formatTimestamp } from '../utils/format'
import KeyValueEditor from '../components/KeyValueEditor.vue'
import SanListEditor from '../components/SanListEditor.vue'

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
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">{{ hostname }}</h1>
    <p v-if="clients.loading">Loading...</p>
    <p v-else-if="clients.error" class="text-red-600">{{ clients.error }}</p>
    <template v-else-if="client">
      <div v-if="showToken" data-test="token-banner" class="bg-yellow-50 border border-yellow-400 rounded p-3 mb-4">
        <p class="font-medium">Enrollment token (shown once):</p>
        <code data-test="token-value" class="block bg-white border rounded px-2 py-1 my-1 break-all">{{ tokenValue }}</code>
        <button type="button" @click="copyToken" class="border rounded px-2 py-1 mr-2">Copy</button>
        <button type="button" @click="showToken = false" class="border rounded px-2 py-1">Dismiss</button>
        <p class="text-sm text-gray-600 mt-1">This token won't be shown again — relay it to the node now.</p>
      </div>

      <div class="mb-4 space-x-2">
        <button
          v-if="!client.revoked"
          type="button"
          data-test="revoke-button"
          @click="confirmRevoke"
          class="border rounded px-3 py-1"
        >
          Revoke
        </button>
        <button v-else type="button" data-test="unrevoke-button" @click="confirmUnrevoke" class="border rounded px-3 py-1">
          Unrevoke
        </button>
        <button type="button" data-test="reenroll-button" @click="reenroll" class="border rounded px-3 py-1">
          Re-enroll
        </button>
      </div>

      <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 mb-6">
        <dt class="font-medium">Revoked</dt>
        <dd>{{ client.revoked ? 'Yes' : 'No' }}</dd>
        <dt class="font-medium">Revoked At</dt>
        <dd>{{ formatTimestamp(client.revoked_at) || '—' }}</dd>
        <dt class="font-medium">Last Seen</dt>
        <dd>{{ formatTimestamp(client.last_seen_at) || 'Never' }}</dd>
      </dl>

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
  </div>
</template>
