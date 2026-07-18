import { defineStore } from 'pinia'

const STORAGE_KEY = 'mp_api_token'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    token: localStorage.getItem(STORAGE_KEY) || null,
    error: null,
  }),
  getters: {
    isAuthenticated: (state) => !!state.token,
  },
  actions: {
    setToken(token) {
      this.token = token
      this.error = null
      localStorage.setItem(STORAGE_KEY, token)
    },
    clearToken(reason = null) {
      this.token = null
      this.error = reason
      localStorage.removeItem(STORAGE_KEY)
    },
  },
})
