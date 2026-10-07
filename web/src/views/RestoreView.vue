<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useClientsStore } from '../stores/clients'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'
import { useJobsStore } from '../stores/jobs'
import { formatBytes, formatTimestamp } from '../utils/format'
import { entryKey } from '../utils/restoreRules'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import BaseField from '../components/ui/BaseField.vue'
import BaseSelect from '../components/ui/BaseSelect.vue'
import Badge from '../components/ui/Badge.vue'
import VersionsModal from '../components/VersionsModal.vue'
import { DAMAGED_TOOLTIP } from '../utils/damaged'
import RestoreConfirmModal from '../components/RestoreConfirmModal.vue'

// 2x agent's default 15-minute operating-refresh cadence
// (OperatingCertFetchIntervalSec, docs/components/agent.md) -- a host more
// than twice its own expected check-in interval overdue is reasonably
// "stale" rather than just between ticks.
const STALE_CHECKIN_SEC = 1800

const restoreCart = useRestoreCartStore()
const clients = useClientsStore()
const submission = useRestoreSubmissionStore()
const jobs = useJobsStore()

const destinationHost = ref('')
const overwrite = ref(false)
// Key of the entry currently being edited (see entryKey), or null when no
// destination-path cell is in edit mode. Only one cell can be edited at a
// time.
const editingKey = ref(null)
const editingInput = ref(null)
const versionsFor = ref(null) // the cart entry a version picker is open for, or null
const confirming = ref(false)

onMounted(() => {
  if (clients.list.length === 0) clients.fetchAll()
  jobs.connectJobsStream()
})

onUnmounted(() => {
  jobs.disconnectJobsStream()
})

function sourcePathLabel(entry) {
  return entry.host === null ? `${entry.path}/*` : entry.path
}

function remove(entry) {
  restoreCart.removeEntry(entry)
  submission.clearEntry(entry)
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

function openVersions(entry) {
  versionsFor.value = entry
}

function selectVersion(version) {
  restoreCart.setVersionWindow(
    { host: versionsFor.value.host, path: versionsFor.value.path },
    version.store_created_at,
    version.store_created_at
  )
  versionsFor.value = null
}

// Unlike CatalogView's useLatestVersion (Task 4), which resets to the
// *currently browsed* catalog filter window, this page has no live catalog
// filter in scope to fall back to -- entries here may have been selected
// under different browsing filters at different times. 0/0 wire-encodes as
// fully unbounded (see toWireRule's truthy check, Task 5), which resolves
// to a genuine "whatever's newest at restore time" -- the simplest correct
// meaning "latest" can have on this page.
function useLatestVersion() {
  restoreCart.setVersionWindow({ host: versionsFor.value.host, path: versionsFor.value.path }, 0, 0)
  versionsFor.value = null
}

function isPinned(entry) {
  return Boolean(entry.notBefore) && Boolean(entry.notAfter) && entry.notBefore === entry.notAfter
}

// capturedLabel must not claim the bare "Latest" when the entry is
// actually bounded by a real captured filter window -- an unbounded
// "Latest" is only true after an explicit "Use latest" reset (which sets
// notBefore/notAfter to 0/0, see useLatestVersion above). Every other
// unpinned entry defaults to the catalog's filter window at selection
// time (see restoreRules.js's toggle functions), a real
// [notBefore, notAfter] range -- if the file's actual last backup falls
// outside that window, the restore finds nothing, so the label needs to
// surface the cutoff rather than implying an unqualified "latest version,
// no matter when."
function capturedLabel(entry) {
  if (isPinned(entry)) return formatTimestamp(entry.notBefore)
  if (entry.notBefore && entry.notAfter && entry.notBefore !== entry.notAfter) {
    return `Latest through ${formatTimestamp(entry.notAfter)}`
  }
  return 'Latest'
}

const totalSize = computed(() => restoreCart.entries.reduce((sum, e) => sum + (e.size || 0), 0))
const pinnedCount = computed(() => restoreCart.entries.filter(isPinned).length)
// Folder rules are resolved by rwfs at restore time, so only file rules can be known-damaged.
const damagedCount = computed(() => restoreCart.entries.filter((e) => e.host !== null && e.damaged === true).length)

const destinationClient = computed(() => clients.list.find((c) => c.hostname === destinationHost.value))
const destinationStale = computed(() => {
  const client = destinationClient.value
  if (!client) return false
  return Math.floor(Date.now() / 1000) - client.last_seen_at > STALE_CHECKIN_SEC
})

const canSubmit = computed(
  () => restoreCart.hasSelections && destinationHost.value !== '' && !submission.submitting
)

function verify() {
  submission.submit(destinationHost.value, { mode: 'verify', overwrite: overwrite.value })
}

function openConfirm() {
  confirming.value = true
}

function confirmRestore() {
  confirming.value = false
  submission.submit(destinationHost.value, { mode: 'restore', overwrite: overwrite.value })
}

function statusesFor(entry) {
  return submission.entryStatus[entryKey(entry)] || []
}

function jobState(jobId) {
  return jobs.list.find((j) => j.job_id === jobId)?.state
}

function badgeVariant(status) {
  if (status.status === 'error') return 'bad'
  if (status.status === 'submitting') return 'neutral'
  const state = jobState(status.jobId)
  if (state === 'success') return 'ok'
  if (state === 'failure') return 'bad'
  return 'neutral'
}
</script>

<template>
  <div>
    <PageHeader title="Restore" :crumbs="[{ label: 'Restore' }]" />
    <StatusMessage :empty="restoreCart.entries.length === 0" empty-text="No files selected for restore yet.">
      <p data-test="cart-summary" class="mb-2">
        {{ restoreCart.entries.length }} item{{ restoreCart.entries.length === 1 ? '' : 's' }} selected, {{ formatBytes(totalSize) }}
      </p>
      <table data-test="restore-table" class="w-full text-left">
        <thead>
          <tr>
            <th class="font-medium">Source Host</th>
            <th class="font-medium">Source Path</th>
            <th class="font-medium">Captured</th>
            <th class="font-medium">Destination Path</th>
            <th class="font-medium">Size</th>
            <th class="font-medium">Status</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="entry in restoreCart.entries" :key="entryKey(entry)" :data-test="`restore-row-${entryKey(entry)}`">
            <td>{{ entry.host ?? '—' }}</td>
            <td>
              {{ sourcePathLabel(entry) }}
              <Badge
                v-if="entry.host !== null && entry.damaged"
                variant="bad"
                :data-test="`cart-damaged-${entryKey(entry)}`"
                :title="DAMAGED_TOOLTIP"
              >Damaged</Badge>
            </td>
            <td>
              <button
                type="button"
                :data-test="`captured-${entryKey(entry)}`"
                class="cursor-pointer text-blue-600 hover:underline"
                @click="openVersions(entry)"
              >
                {{ capturedLabel(entry) }}
              </button>
            </td>
            <td>
              <input
                v-if="editingKey === entryKey(entry)"
                :ref="(el) => (editingInput = el)"
                :data-test="`dest-path-input-${entryKey(entry)}`"
                :value="entry.destPath"
                @blur="commitEdit(entry, $event.target.value)"
                @keyup.enter="commitEdit(entry, $event.target.value)"
              />
              <template v-else>
                <span :data-test="`dest-path-text-${entryKey(entry)}`">{{ entry.destPath }}</span>
                <button
                  type="button"
                  :data-test="`edit-dest-path-${entryKey(entry)}`"
                  aria-label="Edit destination path"
                  @click="startEditing(entry)"
                >
                  ✏️
                </button>
              </template>
            </td>
            <td>{{ formatBytes(entry.size) }}</td>
            <td :data-test="`status-${entryKey(entry)}`">
              <Badge v-for="(status, i) in statusesFor(entry)" :key="i" :variant="badgeVariant(status)">
                <router-link v-if="status.jobId" :to="{ name: 'job-detail', params: { job_id: status.jobId } }">
                  {{ status.status === 'submitting' ? 'submitting…' : jobState(status.jobId) || 'submitted' }}
                </router-link>
                <span v-else>{{ status.status === 'submitting' ? 'submitting…' : status.message }}</span>
              </Badge>
            </td>
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
      <p v-if="destinationStale" data-test="destination-stale-warning" class="text-amber-700">
        This host hasn't checked in recently — the restore will wait until it comes online.
      </p>
      <label class="flex items-center gap-2">
        <input type="checkbox" data-test="overwrite-checkbox" v-model="overwrite" />
        Overwrite existing files
      </label>
      <p class="text-sm text-gray-600">
        Replaces files that already exist at the destination. Unchecked: existing files are left
        alone and skipped.
      </p>
      <BaseButton data-test="verify-button" variant="secondary" :disabled="!canSubmit" @click="verify">
        Verify
      </BaseButton>
      <p class="text-sm text-gray-600">Verify checks integrity only — writes nothing.</p>
      <BaseButton data-test="restore-button" variant="primary" :disabled="!canSubmit" @click="openConfirm">
        Restore
      </BaseButton>
      <p class="text-sm text-gray-600">Restore writes files to the destination.</p>
    </StatusMessage>
    <p v-if="submission.error" data-test="submission-error">{{ submission.error }}</p>
    <VersionsModal
      v-if="versionsFor"
      :path="versionsFor.path"
      :source-host="versionsFor.host"
      @close="versionsFor = null"
      @select-version="selectVersion"
      @use-latest="useLatestVersion"
    />
    <RestoreConfirmModal
      v-if="confirming"
      :file-count="restoreCart.entries.length"
      :total-size="totalSize"
      :destination-host="destinationHost"
      :overwrite="overwrite"
      :pinned-count="pinnedCount"
      :damaged-count="damagedCount"
      @confirm="confirmRestore"
      @cancel="confirming = false"
    />
  </div>
</template>
