import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useStoragePoliciesStore } from './storagePolicies'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('storagePolicies store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll populates the list from the API filtered to storage', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 's1', name: 'east-1-storage' }] })
    const storagePolicies = useStoragePoliciesStore()

    await storagePolicies.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/policies?type=storage')
    expect(storagePolicies.list).toEqual([{ id: 's1', name: 'east-1-storage' }])
    expect(storagePolicies.loading).toBe(false)
    expect(storagePolicies.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const storagePolicies = useStoragePoliciesStore()

    await storagePolicies.fetchAll()

    expect(storagePolicies.error).toBe('boom')
    expect(storagePolicies.list).toEqual([])
  })

  it('fetchOne fetches and caches a storage policy by id', async () => {
    apiFetch.mockResolvedValue({ id: 's1', name: 'east-1-storage' })
    const storagePolicies = useStoragePoliciesStore()

    const first = await storagePolicies.fetchOne('s1')
    const second = await storagePolicies.fetchOne('s1')

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(apiFetch).toHaveBeenCalledWith('/policies/s1')
    expect(first).toEqual({ id: 's1', name: 'east-1-storage' })
    expect(second).toEqual(first)
  })

  it('refresh always refetches, bypassing the byId cache', async () => {
    apiFetch.mockResolvedValueOnce({ id: 's1', name: 'east-1-storage', checkins: [] })
    const storagePolicies = useStoragePoliciesStore()
    await storagePolicies.fetchOne('s1')

    apiFetch.mockResolvedValueOnce({
      id: 's1',
      name: 'east-1-storage',
      checkins: [{ hostname: 'storage-east-1', last_seen_at: 456 }],
    })
    const result = await storagePolicies.refresh('s1')

    expect(apiFetch).toHaveBeenCalledTimes(2)
    expect(apiFetch).toHaveBeenNthCalledWith(2, '/policies/s1')
    expect(result.checkins).toEqual([{ hostname: 'storage-east-1', last_seen_at: 456 }])
    expect(storagePolicies.byId.s1).toEqual(result)
  })

  it('refresh updates the matching list entry when present', async () => {
    apiFetch.mockResolvedValueOnce({ data: [{ id: 's1', name: 'east-1-storage' }] })
    const storagePolicies = useStoragePoliciesStore()
    await storagePolicies.fetchAll()

    apiFetch.mockResolvedValueOnce({ id: 's1', name: 'east-1-storage-renamed' })
    await storagePolicies.refresh('s1')

    expect(storagePolicies.list).toEqual([{ id: 's1', name: 'east-1-storage-renamed' }])
  })

  it('refresh tracks checkinsLoading separately from loading', async () => {
    let resolveFetch
    apiFetch.mockReturnValue(
      new Promise((resolve) => {
        resolveFetch = resolve
      })
    )
    const storagePolicies = useStoragePoliciesStore()

    const pending = storagePolicies.refresh('s1')
    expect(storagePolicies.checkinsLoading).toBe(true)
    expect(storagePolicies.loading).toBe(false)
    resolveFetch({ id: 's1' })
    await pending
    expect(storagePolicies.checkinsLoading).toBe(false)
  })

  it('refresh records the error on checkinsError (not error) and rethrows', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const storagePolicies = useStoragePoliciesStore()

    await expect(storagePolicies.refresh('s1')).rejects.toThrow('boom')
    expect(storagePolicies.checkinsError).toBe('boom')
    expect(storagePolicies.error).toBeNull()
  })

  it('create posts to /storage-policies and adds the result to list and byId', async () => {
    const created = { id: 's2', name: 'east-2-storage' }
    apiFetch.mockResolvedValue(created)
    const storagePolicies = useStoragePoliciesStore()

    const input = { name: 'east-2-storage', hostname: 'h', port: 9400, config: '{}' }
    const result = await storagePolicies.create(input)

    expect(apiFetch).toHaveBeenCalledWith('/storage-policies', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(created)
    expect(storagePolicies.list).toEqual([created])
    expect(storagePolicies.byId.s2).toEqual(created)
  })

  it('create records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('hostname is required'))
    const storagePolicies = useStoragePoliciesStore()

    await expect(storagePolicies.create({ name: 'x' })).rejects.toThrow('hostname is required')
    expect(storagePolicies.error).toBe('hostname is required')
    expect(storagePolicies.list).toEqual([])
  })

  it('update puts to /storage-policies/{id} and replaces the entry in list and byId', async () => {
    const original = { id: 's1', name: 'east-1-storage' }
    const updated = { id: 's1', name: 'east-1-storage-renamed' }
    apiFetch.mockResolvedValueOnce({ data: [original] })
    const storagePolicies = useStoragePoliciesStore()
    await storagePolicies.fetchAll()

    apiFetch.mockResolvedValueOnce(updated)
    const input = { name: 'east-1-storage-renamed', hostname: 'h', port: 9400, config: '{}' }
    const result = await storagePolicies.update('s1', input)

    expect(apiFetch).toHaveBeenCalledWith('/storage-policies/s1', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(updated)
    expect(storagePolicies.list).toEqual([updated])
    expect(storagePolicies.byId.s1).toEqual(updated)
  })

  it('update records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('port must be between 1 and 65535'))
    const storagePolicies = useStoragePoliciesStore()

    await expect(storagePolicies.update('s1', { name: 'x' })).rejects.toThrow('port must be between 1 and 65535')
    expect(storagePolicies.error).toBe('port must be between 1 and 65535')
  })

  it('remove deletes via /policies/{id} and drops the entry from list and byId', async () => {
    apiFetch.mockResolvedValueOnce({ data: [{ id: 's1', name: 'east-1-storage' }] })
    const storagePolicies = useStoragePoliciesStore()
    await storagePolicies.fetchAll()
    storagePolicies.byId.s1 = { id: 's1', name: 'east-1-storage' }

    apiFetch.mockResolvedValueOnce(null)
    await storagePolicies.remove('s1')

    expect(apiFetch).toHaveBeenCalledWith('/policies/s1', { method: 'DELETE' })
    expect(storagePolicies.list).toEqual([])
    expect(storagePolicies.byId.s1).toBeUndefined()
  })

  it('remove records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('policy not found'))
    const storagePolicies = useStoragePoliciesStore()

    await expect(storagePolicies.remove('missing')).rejects.toThrow('policy not found')
    expect(storagePolicies.error).toBe('policy not found')
  })
})
