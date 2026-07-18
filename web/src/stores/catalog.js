import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

function buildQuery(filters, startingAfter) {
  const params = new URLSearchParams()
  if (filters.sourceHost) params.set('source_host', filters.sourceHost)
  if (filters.storeHost) params.set('store_host', filters.storeHost)
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  return params.toString()
}

export const useCatalogStore = defineStore('catalog', {
  state: () => ({
    filters: { sourceHost: '', storeHost: '', pattern: '' },
    cursorStack: [],
    entries: [],
    hasMore: false,
    loading: false,
    error: null,
  }),
  getters: {
    canGoPrev: (state) => state.cursorStack.length > 0,
  },
  actions: {
    async _fetchPage(startingAfter) {
      this.loading = true
      this.error = null
      try {
        const qs = buildQuery(this.filters, startingAfter)
        const body = await apiFetch(`/catalog${qs ? `?${qs}` : ''}`)
        this.entries = body.data
        this.hasMore = body.has_more
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async search(filters) {
      this.filters = { ...filters }
      this.cursorStack = []
      await this._fetchPage(undefined)
    },
    async nextPage() {
      if (!this.hasMore || this.entries.length === 0) return
      const lastId = this.entries[this.entries.length - 1].id
      this.cursorStack.push(lastId)
      await this._fetchPage(lastId)
    },
    async prevPage() {
      if (this.cursorStack.length === 0) return
      this.cursorStack.pop()
      const prevCursor = this.cursorStack[this.cursorStack.length - 1]
      await this._fetchPage(prevCursor)
    },
  },
})
