import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { useRestoreCartStore } from './restoreCart'
import { useStoragePoliciesStore } from './storagePolicies'
import { useRestorePoliciesStore } from './restorePolicies'

// distinctPositiveEntries returns cart.entries (the positively-selected
// top-level rules), deduped by (host, path) -- submitting the same
// top-level selection twice would otherwise issue a redundant facet query.
function distinctPositiveEntries(entries) {
  const seen = new Set()
  return entries.filter((e) => {
    const key = `${e.host ?? ''}:${e.path}`
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
// have. Three kinds of rule, three treatments:
//
//   - A host-specific (file) *include* rule goes only to the store(s) that
//     entry's own facet lookup found it on. This is the whole point: rwfs
//     reports a file-level include rule that matches no row as a
//     verification failure, so sending store-a's file to store-b would
//     make store-b's one-shot verification fail forever on a file it never
//     held.
//   - A host-agnostic (folder) include rule goes to every store. A folder
//     rule matching nothing on a given store is not an error (see rwfs's
//     not-found accounting, which skips host-agnostic rules), and a folder
//     may legitimately span stores.
//   - An *exclude* rule of either kind goes to every store. Exclusions can
//     only ever suppress a selection, never demand a file be present --
//     rwfs's not-found scan skips them explicitly -- so they are safe
//     everywhere, and dropping any of them would restore a file the user
//     deselected.
//
// Rule order is not significant to consumers: both restoreRules.js's
// resolveFile and rwfs's resolveRestoreFile resolve by specificity
// (exact-host rule, else longest matching ancestor folder rule), never by
// position in the list.
async function buildRulesByStore(positiveEntries, allRules) {
  const perEntryStores = await Promise.all(positiveEntries.map((e) => storesTouchedByEntry(e)))
  const allStores = new Set(perEntryStores.flat())

  const sharedRules = allRules.filter((r) => !r.include || !r.host)

  const fileRulesByStore = new Map()
  positiveEntries.forEach((entry, i) => {
    if (!entry.host) return
    for (const store of perEntryStores[i]) {
      if (!fileRulesByStore.has(store)) fileRulesByStore.set(store, [])
      fileRulesByStore.get(store).push(entry)
    }
  })

  const rulesByStore = new Map()
  for (const store of allStores) {
    rulesByStore.set(store, [...sharedRules, ...(fileRulesByStore.get(store) || [])])
  }
  return rulesByStore
}

// storagePolicyIdForHost finds which storage policy's checkins include
// storeHost -- the same cross-reference the now-removed resolveStoreAddress
// helper used to do, but stopping at the policy id: policy-server finishes
// the resolution live
// (see server.go's attachDestination), so staying stale is no longer a
// risk the frontend needs to avoid by resolving all the way to an address
// itself.
function storagePolicyIdForHost(storagePolicies, storeHost) {
  for (const policy of storagePolicies) {
    if ((policy.checkins || []).some((c) => c.hostname === storeHost)) return policy.id
  }
  return null
}

export const useRestoreSubmissionStore = defineStore('restoreSubmission', {
  state: () => ({
    submitting: false,
    results: [],
    error: null,
  }),
  actions: {
    async submit(destinationHost) {
      const cart = useRestoreCartStore()
      const storagePolicies = useStoragePoliciesStore()
      const restorePolicies = useRestorePoliciesStore()

      this.submitting = true
      this.results = []
      this.error = null

      try {
        const positiveEntries = distinctPositiveEntries(cart.entries)
        if (positiveEntries.length === 0) {
          this.error = 'Nothing selected for restore.'
          return
        }

        const rulesByStore = await buildRulesByStore(positiveEntries, cart.rules)

        await storagePolicies.fetchAll()
        if (storagePolicies.error) {
          this.error = `Could not look up storage policies: ${storagePolicies.error}`
          return
        }

        const results = []
        for (const [storeHost, rules] of rulesByStore) {
          const storagePolicyId = storagePolicyIdForHost(storagePolicies.list, storeHost)
          if (!storagePolicyId) {
            results.push({
              storeHost,
              status: 'error',
              message: `No storage policy found for ${storeHost}`,
            })
            continue
          }
          try {
            const name = `restore-${new Date().toISOString()}-${storeHost}`
            const policy = await restorePolicies.create({
              name,
              client_filters: { hostnames: [destinationHost], labels: {} },
              storage_policy_id: storagePolicyId,
              rules,
            })
            results.push({ storeHost, status: 'success', policy })
          } catch (err) {
            results.push({ storeHost, status: 'error', message: err.message })
          }
        }
        this.results = results
      } catch (err) {
        this.error = err.message
      } finally {
        this.submitting = false
      }
    },
  },
})
