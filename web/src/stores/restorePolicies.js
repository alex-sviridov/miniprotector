import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

export const useRestorePoliciesStore = defineStore('restorePolicies', {
  state: () => ({
    loading: false,
    error: null,
  }),
  actions: {
    async create(input) {
      return withRequest(this, async () => {
        return await apiFetch('/restore', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
      })
    },
  },
})
