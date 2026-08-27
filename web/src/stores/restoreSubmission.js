import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { useRestoreCartStore } from './restoreCart'
import { useStoragePoliciesStore } from './storagePolicies'
import { useRestorePoliciesStore } from './restorePolicies'
import { entryKey } from '../utils/restoreRules'

// distinctPositiveEntries returns cart.entries (the positively-selected
// top-level rules), deduped by (host, path) -- submitting the same
// top-level selection twice would otherwise issue a redundant facet query.
function distinctPositiveEntries(entries) {
  const seen = new Set()
  return entries.filter((e) => {
    const key = entryKey(e)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function buildStoreFacetsQuery(entry) {
  const params = new URLSearchParams()
  if (entry.host) params.set('source_hosts', entry.host)
  params.set('pattern', entry.path)
  return params.toString()
}

// storesTouchedByEntry finds every store_host holding data matching one
// entry's pattern -- a cheap facet query (bounded by distinct-store-count,
// not by how many files match), replacing the old full-file-pagination
// approach.
async function storesTouchedByEntry(entry) {
  const qs = buildStoreFacetsQuery(entry)
  const body = await apiFetch(`/catalog/stores?${qs}`)
  return body.data.map((f) => f.name)
}

// buildRulesByStore groups the cart's rules per store, so each store's
// restore policy is told to verify only what that store could actually
// have, and (entriesByStore) which cart entries that policy's outcome
// should be reported against -- see restoreSubmission's own module doc at
// the top of submit() below. Three kinds of rule, three treatments:
//
//   - A host-specific (file) *include* rule goes only to the store(s) that
//     entry's own facet lookup found it on.
//   - A host-agnostic (folder) include rule goes to every store.
//   - An *exclude* rule of either kind goes to every store.
//
// Rule order is not significant to consumers: both restoreRules.js's
// resolveFile and rwfs's resolveRestoreFile resolve by specificity
// (exact-host rule, else longest matching ancestor folder rule), never by
// position in the list.
async function buildRulesByStore(positiveEntries, allRules) {
  const perEntryStores = await Promise.all(positiveEntries.map((e) => storesTouchedByEntry(e)))
  const allStores = new Set(perEntryStores.flat())

  const sharedRules = allRules.filter((r) => !r.include || !r.host)
  const sharedFolderEntries = positiveEntries.filter((e) => e.host === null)

  const fileEntriesByStore = new Map()
  positiveEntries.forEach((entry, i) => {
    if (!entry.host) return
    for (const store of perEntryStores[i]) {
      if (!fileEntriesByStore.has(store)) fileEntriesByStore.set(store, [])
      fileEntriesByStore.get(store).push(entry)
    }
  })

  const rulesByStore = new Map()
  const entriesByStore = new Map()
  for (const store of allStores) {
    const fileEntries = fileEntriesByStore.get(store) || []
    rulesByStore.set(store, [...sharedRules, ...fileEntries])
    entriesByStore.set(store, [...sharedFolderEntries, ...fileEntries])
  }
  return { rulesByStore, entriesByStore }
}

// toWireRule strips the cart's client-only display fields (storeHost, size
// -- see restoreCart.js's toggleFile) and omits dest_path entirely when
// it's unchanged from path (the "no rename" case). not_before/not_after
// are only ever meaningful on an included rule (see policyserver.proto's
// RestoreRule doc) -- an excluded rule can carry a stale window from
// whatever the box's *last checked* state set, so it's deliberately never
// sent for one.
function toWireRule(rule) {
  const wire = { host: rule.host, path: rule.path, include: rule.include }
  if (rule.destPath && rule.destPath !== rule.path) wire.dest_path = rule.destPath
  if (rule.include && rule.notBefore) wire.not_before = rule.notBefore
  if (rule.include && rule.notAfter) wire.not_after = rule.notAfter
  return wire
}

function storagePolicyIdForHost(storagePolicies, storeHost) {
  for (const policy of storagePolicies) {
    if ((policy.checkins || []).some((c) => c.hostname === storeHost)) return policy.id
  }
  return null
}

export const useRestoreSubmissionStore = defineStore('restoreSubmission', {
  state: () => ({
    submitting: false,
    // entryKey(entry) -> [{ status: 'submitting'|'success'|'error', jobId?, message? }].
    // An array because one folder entry can fan out to more than one
    // storage host's policy, each with its own independent outcome --
    // almost always length 1 for a file entry, which only ever touches
    // one store. Persists across submit() calls (not reset to {} each
    // time) so a completed row's status/link stays visible after the
    // request that produced it finishes -- see submit()'s
    // already-succeeded filter below for how re-submission is guarded
    // instead of relying on the cart being emptied.
    entryStatus: {},
    error: null,
  }),
  actions: {
    async submit(destinationHost, { mode, overwrite }) {
      const cart = useRestoreCartStore()
      const storagePolicies = useStoragePoliciesStore()
      const restorePolicies = useRestorePoliciesStore()

      this.submitting = true
      this.error = null

      try {
        const alreadySucceeded = (entry) =>
          (this.entryStatus[entryKey(entry)] || []).some((s) => s.status === 'success')
        const positiveEntries = distinctPositiveEntries(cart.entries).filter((e) => !alreadySucceeded(e))
        if (positiveEntries.length === 0) {
          this.error = 'Nothing selected for restore.'
          return
        }

        const { rulesByStore, entriesByStore } = await buildRulesByStore(positiveEntries, cart.rules)

        await storagePolicies.fetchAll()
        if (storagePolicies.error) {
          this.error = `Could not look up storage policies: ${storagePolicies.error}`
          return
        }

        for (const entry of positiveEntries) this.entryStatus[entryKey(entry)] = [{ status: 'submitting' }]

        for (const [storeHost, rules] of rulesByStore) {
          const coveredEntries = entriesByStore.get(storeHost)
          const storagePolicyId = storagePolicyIdForHost(storagePolicies.list, storeHost)
          if (!storagePolicyId) {
            this.recordOutcome(coveredEntries, { status: 'error', message: `No storage policy found for ${storeHost}` })
            continue
          }
          try {
            const name = `restore-${new Date().toISOString()}-${storeHost}`
            const policy = await restorePolicies.create({
              name,
              client_filters: { hostnames: [destinationHost], labels: {} },
              storage_policy_id: storagePolicyId,
              rules: rules.map(toWireRule),
              mode,
              overwrite,
            })
            this.recordOutcome(coveredEntries, { status: 'success', jobId: policy.job_id })
          } catch (err) {
            this.recordOutcome(coveredEntries, { status: 'error', message: err.message })
          }
        }
      } catch (err) {
        this.error = err.message
      } finally {
        this.submitting = false
      }
    },

    recordOutcome(entries, outcome) {
      for (const entry of entries) {
        const key = entryKey(entry)
        this.entryStatus[key] = (this.entryStatus[key] || []).filter((s) => s.status !== 'submitting')
        this.entryStatus[key].push(outcome)
      }
    },
  },
})
