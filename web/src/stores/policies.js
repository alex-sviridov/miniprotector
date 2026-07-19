import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

export const usePoliciesStore = defineStore('policies', {
  state: () => ({
    list: [],
    byId: {},
    loading: false,
    error: null,
  }),
  actions: {
    async fetchAll() {
      this.loading = true
      this.error = null
      try {
        const body = await apiFetch('/policies')
        this.list = body.data
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async fetchOne(id) {
      if (this.byId[id]) {
        this.error = null
        return this.byId[id]
      }
      this.loading = true
      this.error = null
      try {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
        this.byId[id] = policy
        return policy
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async create(input) {
      this.loading = true
      this.error = null
      try {
        const policy = await apiFetch('/policies', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        this.list.push(policy)
        this.byId[policy.id] = policy
        return policy
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async update(id, input) {
      this.loading = true
      this.error = null
      try {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        const idx = this.list.findIndex((p) => p.id === id)
        if (idx !== -1) this.list[idx] = policy
        this.byId[id] = policy
        return policy
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async remove(id) {
      this.loading = true
      this.error = null
      try {
        await apiFetch(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' })
        this.list = this.list.filter((p) => p.id !== id)
        delete this.byId[id]
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
  },
})
