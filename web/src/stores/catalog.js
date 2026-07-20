import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

const MAX_PAGE_LIMIT = 500

function buildQuery(filters, startingAfter, limit) {
  const params = new URLSearchParams()
  if (filters.sourceHost) params.set('source_host', filters.sourceHost)
  if (filters.storeHost) params.set('store_host', filters.storeHost)
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  params.set('limit', String(limit))
  return params.toString()
}

export const useCatalogStore = defineStore('catalog', {
  state: () => ({
    filters: { sourceHost: '', storeHost: '', pattern: '' },
    entries: [],
    loading: false,
    error: null,
  }),
  actions: {
    async search(filters) {
      this.filters = { ...filters }
      try {
        await withRequest(this, async () => {
          const collected = []
          let startingAfter
          for (;;) {
            const qs = buildQuery(this.filters, startingAfter, MAX_PAGE_LIMIT)
            const body = await apiFetch(`/catalog?${qs}`)
            collected.push(...body.data)
            if (!body.has_more || body.data.length === 0) break
            startingAfter = body.data[body.data.length - 1].id
          }
          this.entries = collected
        })
      } catch {
        // withRequest already recorded this.error; discard any partial or
        // stale results rather than leaving a previous search's rows on screen.
        this.entries = []
      }
    },
  },
})
