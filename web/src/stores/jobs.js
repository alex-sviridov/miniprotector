import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

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
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/jobs')
          this.list = body.data
        },
        { rethrow: false }
      )
    },
    async fetchLogs(jobId) {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs`)
          this.logs = body.data ?? []
        },
        { rethrow: false, loadingKey: 'logsLoading', errorKey: 'logsError' }
      )
    },
  },
})
