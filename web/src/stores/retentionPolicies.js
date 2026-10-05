import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

const JSON_HEADERS = { 'Content-Type': 'application/json' }

// list is always in evaluation order: api-server returns retention policies
// sorted by their server-managed priority, and every mutation here keeps it
// that way (create appends, update replaces in place, reorder adopts the
// server's answer).
export const useRetentionPoliciesStore = defineStore('retentionPolicies', {
  state: () => ({
    list: [],
    loading: false,
    error: null,
    reordering: false,
  }),
  actions: {
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/policies?type=retention')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
    async create(input) {
      return withRequest(this, async () => {
        const policy = await apiFetch('/retention-policies', {
          method: 'POST',
          headers: JSON_HEADERS,
          body: JSON.stringify(input),
        })
        this.list.push(policy)
        return policy
      })
    },
    async update(id, input) {
      return withRequest(this, async () => {
        const policy = await apiFetch(`/retention-policies/${encodeURIComponent(id)}`, {
          method: 'PUT',
          headers: JSON_HEADERS,
          body: JSON.stringify(input),
        })
        const idx = this.list.findIndex((p) => p.id === id)
        if (idx !== -1) this.list[idx] = policy
        return policy
      })
    },
    async remove(id) {
      return withRequest(this, async () => {
        await apiFetch(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' })
        this.list = this.list.filter((p) => p.id !== id)
      })
    },
    // reorder applies ids (the COMPLETE list, in the desired order) to the
    // view immediately, then asks the server to make it so. The server
    // rejects an incomplete/stale list; on any failure the view is
    // refetched from the server (which also picks up whatever made the list
    // stale) and the error is reported -- never left half-applied.
    async reorder(ids) {
      if (ids.length === this.list.length && ids.every((id, i) => id === this.list[i].id)) return
      const byId = Object.fromEntries(this.list.map((p) => [p.id, p]))
      this.reordering = true
      this.error = null
      this.list = ids.map((id) => byId[id])
      try {
        const body = await apiFetch('/retention-policies/reorder', {
          method: 'POST',
          headers: JSON_HEADERS,
          body: JSON.stringify({ ids }),
        })
        this.list = body.data
      } catch (err) {
        await this.fetchAll()
        this.error = err.message
      } finally {
        this.reordering = false
      }
    },
  },
})
