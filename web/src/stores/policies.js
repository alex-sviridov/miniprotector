import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

export const usePoliciesStore = defineStore('policies', {
  state: () => ({
    list: [],
    byId: {},
    loading: false,
    error: null,
    checkinsLoading: false,
    checkinsError: null,
  }),
  actions: {
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/policies?type=backup')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
    async fetchOne(id) {
      if (this.byId[id]) {
        this.error = null
        return this.byId[id]
      }
      return withRequest(this, async () => {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
        this.byId[id] = policy
        return policy
      })
    },
    async refresh(id) {
      return withRequest(
        this,
        async () => {
          const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
          this.byId[id] = policy
          const idx = this.list.findIndex((p) => p.id === id)
          if (idx !== -1) this.list[idx] = policy
          return policy
        },
        { loadingKey: 'checkinsLoading', errorKey: 'checkinsError' }
      )
    },
    async create(input) {
      return withRequest(this, async () => {
        const policy = await apiFetch('/policies', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        this.list.push(policy)
        this.byId[policy.id] = policy
        return policy
      })
    },
    async update(id, input) {
      return withRequest(this, async () => {
        const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
        const idx = this.list.findIndex((p) => p.id === id)
        if (idx !== -1) this.list[idx] = policy
        this.byId[id] = policy
        return policy
      })
    },
    async remove(id) {
      return withRequest(this, async () => {
        await apiFetch(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' })
        this.list = this.list.filter((p) => p.id !== id)
        delete this.byId[id]
      })
    },
    async runAdhoc(payload) {
      return withRequest(this, async () => {
        return apiFetch('/policies/adhoc', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        })
      })
    },
  },
})
