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

// distinctStoreHosts finds every store_host touched by any of entries'
// patterns -- a cheap facet query (bounded by distinct-store-count, not by
// how many files match), replacing the old full-file-pagination approach.
async function distinctStoreHosts(entries) {
  const hosts = new Set()
  for (const entry of entries) {
    const qs = buildStoreFacetsQuery(entry)
    const body = await apiFetch(`/catalog/stores?${qs}`)
    for (const facet of body.data) hosts.add(facet.name)
  }
  return [...hosts]
}

// storagePolicyIdForHost finds which storage policy's checkins include
// storeHost -- same cross-reference resolveStoreAddress used to do, but
// stopping at the policy id: policy-server finishes the resolution live
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

        const storeHosts = await distinctStoreHosts(positiveEntries)

        await storagePolicies.fetchAll()
        if (storagePolicies.error) {
          this.error = `Could not look up storage policies: ${storagePolicies.error}`
          return
        }

        const results = []
        for (const storeHost of storeHosts) {
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
              rules: cart.rules,
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
