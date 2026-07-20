import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

export const useClientsStore = defineStore('clients', {
  state: () => ({
    list: [],
    byHostname: {},
    loading: false,
    error: null,
    pendingToken: null,
  }),
  actions: {
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/clients')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
    async fetchOne(hostname) {
      if (this.byHostname[hostname]) {
        this.error = null
        return this.byHostname[hostname]
      }
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}`)
        this.byHostname[hostname] = client
        return client
      })
    },
    async enroll(hostname, sans) {
      return withRequest(this, async () => {
        const result = await apiFetch('/clients', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hostname, sans }),
        })
        this.list.push({ hostname: result.hostname, revoked: false, revoked_at: 0, last_seen_at: 0 })
        this.pendingToken = { hostname: result.hostname, token: result.token }
        return result
      })
    },
    async reenroll(hostname, sans) {
      return withRequest(this, async () => {
        const result = await apiFetch(`/clients/${encodeURIComponent(hostname)}/reenroll`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sans }),
        })
        this.pendingToken = { hostname: result.hostname, token: result.token }
        return result
      })
    },
    async revoke(hostname) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/revoke`, { method: 'POST' })
        this.updateCache(client)
        return client
      })
    },
    async unrevoke(hostname) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/unrevoke`, { method: 'POST' })
        this.updateCache(client)
        return client
      })
    },
    async updateDescription(hostname, set, unset) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/description`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ set, unset }),
        })
        this.updateCache(client)
        return client
      })
    },
    async updateAttributes(hostname, set, unset) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/attributes`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ set, unset }),
        })
        this.updateCache(client)
        return client
      })
    },
    async updateSans(hostname, add, remove) {
      return withRequest(this, async () => {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/sans`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ add, remove }),
        })
        this.updateCache(client)
        return client
      })
    },
    // updateCache writes a fresh client record (from revoke/unrevoke/update*
    // responses) into both byHostname and the matching list row, so every
    // view reading either stays in sync without a refetch.
    updateCache(client) {
      this.byHostname[client.hostname] = client
      const idx = this.list.findIndex((c) => c.hostname === client.hostname)
      if (idx !== -1) this.list[idx] = client
    },
  },
})
