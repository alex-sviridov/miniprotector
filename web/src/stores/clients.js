import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

export const useClientsStore = defineStore('clients', {
  state: () => ({
    list: [],
    byHostname: {},
    loading: false,
    error: null,
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
  },
})
