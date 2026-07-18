import { defineStore } from 'pinia'

const STORAGE_KEY = 'mp_api_token'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    token: localStorage.getItem(STORAGE_KEY) || null,
  }),
  getters: {
    isAuthenticated: (state) => !!state.token,
  },
  actions: {
    setToken(token) {
      this.token = token
      localStorage.setItem(STORAGE_KEY, token)
    },
    clearToken() {
      this.token = null
      localStorage.removeItem(STORAGE_KEY)
    },
  },
})
