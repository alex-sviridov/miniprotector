import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useAuthStore } from './auth'

describe('auth store', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('starts with no token when localStorage is empty', () => {
    const auth = useAuthStore()
    expect(auth.token).toBeNull()
    expect(auth.isAuthenticated).toBe(false)
  })

  it('reads an existing token from localStorage on init', () => {
    localStorage.setItem('mp_api_token', 'existing-token')
    const auth = useAuthStore()
    expect(auth.token).toBe('existing-token')
    expect(auth.isAuthenticated).toBe(true)
  })

  it('setToken stores the token in state and localStorage', () => {
    const auth = useAuthStore()
    auth.setToken('new-token')
    expect(auth.token).toBe('new-token')
    expect(localStorage.getItem('mp_api_token')).toBe('new-token')
  })

  it('clearToken removes the token from state and localStorage', () => {
    const auth = useAuthStore()
    auth.setToken('new-token')
    auth.clearToken()
    expect(auth.token).toBeNull()
    expect(localStorage.getItem('mp_api_token')).toBeNull()
  })
})
