import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useAuthStore } from '../stores/auth'
import { apiFetch, ApiError } from './client'

describe('apiFetch', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    global.fetch = vi.fn()
  })

  it('attaches the bearer token from the auth store', async () => {
    const auth = useAuthStore()
    auth.setToken('secret-token')
    global.fetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ data: [] }) })

    await apiFetch('/clients')

    expect(global.fetch).toHaveBeenCalledWith(
      '/api/v1/clients',
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: 'Bearer secret-token' }),
      })
    )
  })

  it('does not attach an Authorization header when no token is set', async () => {
    global.fetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ data: [] }) })

    await apiFetch('/clients')

    const [, options] = global.fetch.mock.calls[0]
    expect(options.headers.Authorization).toBeUndefined()
  })

  it('returns parsed JSON on a 2xx response', async () => {
    global.fetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ data: [{ hostname: 'x' }] }) })

    const body = await apiFetch('/clients')

    expect(body).toEqual({ data: [{ hostname: 'x' }] })
  })

  it('throws an ApiError with the backend message on a non-2xx response', async () => {
    global.fetch.mockResolvedValue({ ok: false, status: 404, json: async () => ({ error: 'client not found' }) })

    await expect(apiFetch('/clients/unknown')).rejects.toMatchObject({
      status: 404,
      message: 'client not found',
    })
  })

  it('clears the stored token on a 401 response', async () => {
    const auth = useAuthStore()
    auth.setToken('stale-token')
    global.fetch.mockResolvedValue({ ok: false, status: 401, json: async () => ({ error: 'unauthorized' }) })

    await expect(apiFetch('/clients')).rejects.toBeInstanceOf(ApiError)
    expect(auth.token).toBeNull()
    expect(auth.error).toEqual(expect.any(String))
    expect(auth.error.length).toBeGreaterThan(0)
  })
})
