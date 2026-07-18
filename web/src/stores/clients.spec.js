import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useClientsStore } from './clients'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('clients store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll populates the list from the API', async () => {
    apiFetch.mockResolvedValue({ data: [{ hostname: 'webserver' }] })
    const clients = useClientsStore()

    await clients.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/clients')
    expect(clients.list).toEqual([{ hostname: 'webserver' }])
    expect(clients.loading).toBe(false)
    expect(clients.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const clients = useClientsStore()

    await clients.fetchAll()

    expect(clients.error).toBe('boom')
    expect(clients.list).toEqual([])
  })

  it('fetchOne fetches and caches a client by hostname', async () => {
    apiFetch.mockResolvedValue({ hostname: 'webserver', revoked: false })
    const clients = useClientsStore()

    const first = await clients.fetchOne('webserver')
    const second = await clients.fetchOne('webserver')

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(apiFetch).toHaveBeenCalledWith('/clients/webserver')
    expect(first).toEqual({ hostname: 'webserver', revoked: false })
    expect(second).toEqual(first)
  })

  it('fetchOne records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('client not found'))
    const clients = useClientsStore()

    await expect(clients.fetchOne('missing')).rejects.toThrow('client not found')
    expect(clients.error).toBe('client not found')
  })
})
