import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

export const useJobsStore = defineStore('jobs', {
  state: () => ({
    list: [],
    loading: false,
    error: null,
    logs: [],
    logsLoading: false,
    logsError: null,
  }),
  actions: {
    async fetchAll() {
      this.loading = true
      this.error = null
      try {
        const body = await apiFetch('/jobs')
        this.list = body.data
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async fetchLogs(jobId) {
      this.logsLoading = true
      this.logsError = null
      try {
        const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs`)
        this.logs = body.data ?? []
      } catch (err) {
        this.logsError = err.message
      } finally {
        this.logsLoading = false
      }
    },
  },
})
