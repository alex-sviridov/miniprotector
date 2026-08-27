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
    // entryKey(entry) -> [{ status: 'submitting'|'success'|'error', jobId?, message?, mode }].
    // An array because one folder entry can fan out to more than one
    // storage host's policy, each with its own independent outcome --
    // almost always length 1 per mode for a file entry, which only ever
    // touches one store. Also holds at most one outcome per *mode*: a
    // Verify success and a Restore success for the same (host, path) are
    // two independent facts and both stay recorded. Persists across
    // submit() calls (not reset to {} each time) so a completed row's
    // status/link stays visible after the request that produced it
    // finishes -- see submit()'s already-succeeded filter below for how
    // re-submission is guarded (per mode) instead of relying on the cart
    // being emptied. Cleared per-entry via clearEntry(), called when the
    // entry is removed from the cart (see RestoreView.vue's remove()).
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
        // A success is only "already succeeded" for *this* mode -- a
        // completed Verify must never block a later Restore of the same
        // entry (or vice versa); see restoreCart's design doc for why
        // there's no other mode-vs-status coupling here.
        const alreadySucceeded = (entry) =>
          (this.entryStatus[entryKey(entry)] || []).some((s) => s.status === 'success' && s.mode === mode)
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

        // Drop any prior outcome recorded for *this* mode (a submitting
        // placeholder, or a stale error being retried) while preserving
        // outcomes recorded under a different mode -- e.g. an earlier
        // Verify success must survive a subsequent Restore submission for
        // the same entry.
        for (const entry of positiveEntries) {
          const key = entryKey(entry)
          const preserved = (this.entryStatus[key] || []).filter((s) => s.mode !== mode)
          this.entryStatus[key] = [...preserved, { status: 'submitting', mode }]
        }

        for (const [storeHost, rules] of rulesByStore) {
          const coveredEntries = entriesByStore.get(storeHost)
          const storagePolicyId = storagePolicyIdForHost(storagePolicies.list, storeHost)
          if (!storagePolicyId) {
            this.recordOutcome(coveredEntries, { status: 'error', message: `No storage policy found for ${storeHost}`, mode })
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
            this.recordOutcome(coveredEntries, { status: 'success', jobId: policy.job_id, mode })
          } catch (err) {
            this.recordOutcome(coveredEntries, { status: 'error', message: err.message, mode })
          }
        }

        // For entries that touched zero storage hosts, they were initialized to
        // 'submitting' (for this mode) but never passed to recordOutcome. Replace
        // that placeholder with an explicit error so they don't stay stuck at
        // 'submitting' forever, without disturbing any other-mode outcomes also
        // held for this entry.
        for (const entry of positiveEntries) {
          const key = entryKey(entry)
          const status = this.entryStatus[key] || []
          const stillSubmitting = status.find((s) => s.status === 'submitting' && s.mode === mode)
          if (stillSubmitting) {
            this.entryStatus[key] = status.map((s) =>
              s === stillSubmitting
                ? { status: 'error', message: 'No storage host found for this selection', mode }
                : s
            )
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
        this.entryStatus[key] = (this.entryStatus[key] || []).filter(
          (s) => !(s.status === 'submitting' && s.mode === outcome.mode)
        )
        this.entryStatus[key].push(outcome)
      }
    },

    // clearEntry drops all recorded outcomes (any mode) for one entry --
    // called when the entry itself is removed from the cart, so a stale
    // status/badge for a since-removed-and-possibly-re-added entry can't
    // linger (restoreCart and restoreSubmission are separate stores that
    // don't import each other, so this is wired at the call site instead;
    // see RestoreView.vue's remove()).
    clearEntry(entry) {
      delete this.entryStatus[entryKey(entry)]
    },
  },
})
