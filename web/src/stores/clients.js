import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

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
      this.loading = true
      this.error = null
      try {
        const body = await apiFetch('/clients')
        this.list = body.data
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async fetchOne(hostname) {
      if (this.byHostname[hostname]) {
        this.error = null
        return this.byHostname[hostname]
      }
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}`)
        this.byHostname[hostname] = client
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async enroll(hostname, sans) {
      this.loading = true
      this.error = null
      try {
        const result = await apiFetch('/clients', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ hostname, sans }),
        })
        this.list.push({ hostname: result.hostname, revoked: false, revoked_at: 0, last_seen_at: 0 })
        this.pendingToken = { hostname: result.hostname, token: result.token }
        return result
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async reenroll(hostname, sans) {
      this.loading = true
      this.error = null
      try {
        const result = await apiFetch(`/clients/${encodeURIComponent(hostname)}/reenroll`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sans }),
        })
        this.pendingToken = { hostname: result.hostname, token: result.token }
        return result
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async revoke(hostname) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/revoke`, { method: 'POST' })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async unrevoke(hostname) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/unrevoke`, { method: 'POST' })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async updateDescription(hostname, set, unset) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/description`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ set, unset }),
        })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async updateAttributes(hostname, set, unset) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/attributes`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ set, unset }),
        })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
    },
    async updateSans(hostname, add, remove) {
      this.loading = true
      this.error = null
      try {
        const client = await apiFetch(`/clients/${encodeURIComponent(hostname)}/sans`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ add, remove }),
        })
        this.updateCache(client)
        return client
      } catch (err) {
        this.error = err.message
        throw err
      } finally {
        this.loading = false
      }
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
