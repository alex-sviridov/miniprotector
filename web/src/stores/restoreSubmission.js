import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { useRestoreCartStore } from './restoreCart'
import { useStoragePoliciesStore } from './storagePolicies'
import { useRestorePoliciesStore } from './restorePolicies'
import { filterResolved, collapseToLatestVersion, groupByStore } from '../utils/restoreResolve'
import { resolveStoreAddress } from '../utils/storeAddress'

const MAX_PAGE_LIMIT = 500

function buildCatalogQuery(entry, startingAfter) {
  const params = new URLSearchParams()
  if (entry.host) params.set('source_host', entry.host)
  params.set('pattern', entry.path)
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  params.set('limit', String(MAX_PAGE_LIMIT))
  return params.toString()
}

// fetchCandidateEntries over-fetches (pattern is a substring match, not an
// anchored prefix match) on purpose -- filterResolved (restoreResolve.js)
// is what decides real inclusion, using the same path-segment logic the
// catalog UI's own checkboxes already rely on.
async function fetchCandidateEntries(entry) {
  const collected = []
  let startingAfter
  for (;;) {
    const qs = buildCatalogQuery(entry, startingAfter)
    const body = await apiFetch(`/catalog?${qs}`)
    collected.push(...body.data)
    if (!body.has_more || body.data.length === 0) break
    startingAfter = body.data[body.data.length - 1].id
  }
  return collected
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
        const positiveEntries = cart.entries
        if (positiveEntries.length === 0) {
          this.error = 'Nothing selected for restore.'
          return
        }

        const candidateLists = await Promise.all(positiveEntries.map(fetchCandidateEntries))
        const candidates = collapseToLatestVersion(candidateLists.flat())
        const resolved = filterResolved(cart.rules, candidates)
        const groups = groupByStore(resolved)

        await storagePolicies.fetchAll()
        // Without this check every group would be reported as having no
        // reachable storage node, blaming the stores for what is really a
        // failed policy lookup.
        if (storagePolicies.error) {
          this.error = `Could not look up storage policies: ${storagePolicies.error}`
          return
        }

        const results = []
        for (const group of groups) {
          const address = resolveStoreAddress(storagePolicies.list, group.storeHost)
          if (!address) {
            results.push({
              storeHost: group.storeHost,
              status: 'error',
              message: `No reachable storage node found for ${group.storeHost}`,
            })
            continue
          }
          const name = `restore-${new Date().toISOString()}-${group.storeHost}`
          try {
            const policy = await restorePolicies.create({
              name,
              client_filters: { hostnames: [destinationHost], labels: {} },
              source_store: address,
              config: JSON.stringify({
                files: group.files.map((f) => ({ source_host: f.sourceHost, path: f.path })),
              }),
            })
            results.push({ storeHost: group.storeHost, status: 'success', policy })
          } catch (err) {
            results.push({ storeHost: group.storeHost, status: 'error', message: err.message })
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
