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

  it('fetchOne clears a stale error on a cache hit', async () => {
    apiFetch.mockResolvedValue({ hostname: 'webserver', revoked: false })
    const clients = useClientsStore()

    await clients.fetchOne('webserver')
    clients.error = 'stale error from an unrelated earlier action'

    const result = await clients.fetchOne('webserver')

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(result).toEqual({ hostname: 'webserver', revoked: false })
    expect(clients.error).toBeNull()
  })

  it('enroll posts hostname/sans, records a minimal list entry, and sets pendingToken', async () => {
    apiFetch.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })
    const clients = useClientsStore()

    const result = await clients.enroll('node-1', ['alias.internal'])

    expect(apiFetch).toHaveBeenCalledWith('/clients', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ hostname: 'node-1', sans: ['alias.internal'] }),
    })
    expect(result).toEqual({ hostname: 'node-1', token: 'tok-abc' })
    expect(clients.list).toEqual([{ hostname: 'node-1', revoked: false, revoked_at: 0, last_seen_at: 0 }])
    expect(clients.pendingToken).toEqual({ hostname: 'node-1', token: 'tok-abc' })
  })

  it('enroll records an error and rethrows on failure', async () => {
    apiFetch.mockRejectedValue(new Error('client node-1 already enrolled'))
    const clients = useClientsStore()

    await expect(clients.enroll('node-1', [])).rejects.toThrow('client node-1 already enrolled')
    expect(clients.error).toBe('client node-1 already enrolled')
    expect(clients.pendingToken).toBeNull()
  })

  it('reenroll posts sans, sets pendingToken, and does not touch list/byHostname', async () => {
    apiFetch.mockResolvedValue({ hostname: 'node-1', token: 'tok-fresh' })
    const clients = useClientsStore()

    const result = await clients.reenroll('node-1', ['override.internal'])

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/reenroll', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sans: ['override.internal'] }),
    })
    expect(result).toEqual({ hostname: 'node-1', token: 'tok-fresh' })
    expect(clients.pendingToken).toEqual({ hostname: 'node-1', token: 'tok-fresh' })
    expect(clients.list).toEqual([])
  })

  it('revoke posts to the revoke endpoint and updates byHostname and the matching list row', async () => {
    const updated = { hostname: 'node-1', revoked: true, revoked_at: 111, last_seen_at: 0 }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()
    clients.list = [{ hostname: 'node-1', revoked: false, revoked_at: 0, last_seen_at: 0 }]

    const result = await clients.revoke('node-1')

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/revoke', { method: 'POST' })
    expect(result).toEqual(updated)
    expect(clients.byHostname['node-1']).toEqual(updated)
    expect(clients.list[0]).toEqual(updated)
  })

  it('unrevoke posts to the unrevoke endpoint and updates the cache', async () => {
    const updated = { hostname: 'node-1', revoked: false, revoked_at: 0, last_seen_at: 0 }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()
    clients.list = [{ hostname: 'node-1', revoked: true, revoked_at: 111, last_seen_at: 0 }]

    await clients.unrevoke('node-1')

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/unrevoke', { method: 'POST' })
    expect(clients.byHostname['node-1']).toEqual(updated)
    expect(clients.list[0]).toEqual(updated)
  })

  it('updateDescription PATCHes set/unset and updates the cache', async () => {
    const updated = { hostname: 'node-1', descriptions: { owner: 'alice' } }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()

    const result = await clients.updateDescription('node-1', { owner: 'alice' }, ['old'])

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/description', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ set: { owner: 'alice' }, unset: ['old'] }),
    })
    expect(result).toEqual(updated)
    expect(clients.byHostname['node-1']).toEqual(updated)
  })

  it('updateAttributes PATCHes set/unset and updates the cache', async () => {
    const updated = { hostname: 'node-1', attributes: { role: 'db' } }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()

    await clients.updateAttributes('node-1', { role: 'db' }, [])

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/attributes', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ set: { role: 'db' }, unset: [] }),
    })
    expect(clients.byHostname['node-1']).toEqual(updated)
  })

  it('updateSans PATCHes add/remove and updates the cache', async () => {
    const updated = { hostname: 'node-1', sans: ['new.internal'] }
    apiFetch.mockResolvedValue(updated)
    const clients = useClientsStore()

    await clients.updateSans('node-1', ['new.internal'], ['old.internal'])

    expect(apiFetch).toHaveBeenCalledWith('/clients/node-1/sans', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ add: ['new.internal'], remove: ['old.internal'] }),
    })
    expect(clients.byHostname['node-1']).toEqual(updated)
  })
})
