import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { usePoliciesStore } from './policies'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('policies store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll populates the list from the API', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 'p1', name: 'nightly' }] })
    const policies = usePoliciesStore()

    await policies.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/policies?type=backup')
    expect(policies.list).toEqual([{ id: 'p1', name: 'nightly' }])
    expect(policies.loading).toBe(false)
    expect(policies.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const policies = usePoliciesStore()

    await policies.fetchAll()

    expect(policies.error).toBe('boom')
    expect(policies.list).toEqual([])
  })

  it('fetchOne fetches and caches a policy by id', async () => {
    apiFetch.mockResolvedValue({ id: 'p1', name: 'nightly' })
    const policies = usePoliciesStore()

    const first = await policies.fetchOne('p1')
    const second = await policies.fetchOne('p1')

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(apiFetch).toHaveBeenCalledWith('/policies/p1')
    expect(first).toEqual({ id: 'p1', name: 'nightly' })
    expect(second).toEqual(first)
  })

  it('fetchOne records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('policy not found'))
    const policies = usePoliciesStore()

    await expect(policies.fetchOne('missing')).rejects.toThrow('policy not found')
    expect(policies.error).toBe('policy not found')
  })

  it('fetchOne clears a stale checkinsError on a cache miss', async () => {
    apiFetch.mockResolvedValue({ id: 'p1', name: 'nightly' })
    const policies = usePoliciesStore()
    policies.checkinsError = 'stale error from a previous policy'

    await policies.fetchOne('p1')

    expect(policies.checkinsError).toBeNull()
  })

  it('fetchOne clears a stale checkinsError on a cache hit', async () => {
    apiFetch.mockResolvedValue({ id: 'p1', name: 'nightly' })
    const policies = usePoliciesStore()
    await policies.fetchOne('p1')
    policies.checkinsError = 'stale error from a previous policy'

    await policies.fetchOne('p1')

    expect(policies.checkinsError).toBeNull()
  })

  it('refresh always refetches, bypassing the byId cache', async () => {
    apiFetch.mockResolvedValueOnce({ id: 'p1', name: 'nightly', checkins: [] })
    const policies = usePoliciesStore()
    await policies.fetchOne('p1')

    apiFetch.mockResolvedValueOnce({
      id: 'p1',
      name: 'nightly',
      checkins: [{ hostname: 'web-01', last_seen_at: 123 }],
    })
    const result = await policies.refresh('p1')

    expect(apiFetch).toHaveBeenCalledTimes(2)
    expect(apiFetch).toHaveBeenNthCalledWith(2, '/policies/p1')
    expect(result.checkins).toEqual([{ hostname: 'web-01', last_seen_at: 123 }])
    expect(policies.byId.p1).toEqual(result)
  })

  it('refresh updates the matching list entry when present', async () => {
    apiFetch.mockResolvedValueOnce({ data: [{ id: 'p1', name: 'nightly' }] })
    const policies = usePoliciesStore()
    await policies.fetchAll()

    apiFetch.mockResolvedValueOnce({ id: 'p1', name: 'nightly-renamed' })
    await policies.refresh('p1')

    expect(policies.list).toEqual([{ id: 'p1', name: 'nightly-renamed' }])
  })

  it('refresh tracks checkinsLoading separately from loading', async () => {
    let resolveFetch
    apiFetch.mockReturnValue(
      new Promise((resolve) => {
        resolveFetch = resolve
      })
    )
    const policies = usePoliciesStore()

    const pending = policies.refresh('p1')
    expect(policies.checkinsLoading).toBe(true)
    expect(policies.loading).toBe(false)
    resolveFetch({ id: 'p1' })
    await pending
    expect(policies.checkinsLoading).toBe(false)
  })

  it('refresh records the error on checkinsError (not error) and rethrows', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const policies = usePoliciesStore()

    await expect(policies.refresh('p1')).rejects.toThrow('boom')
    expect(policies.checkinsError).toBe('boom')
    expect(policies.error).toBeNull()
  })

  it('create posts the input and adds the result to list and byId', async () => {
    const created = { id: 'p2', name: 'weekly' }
    apiFetch.mockResolvedValue(created)
    const policies = usePoliciesStore()

    const input = { name: 'weekly' }
    const result = await policies.create(input)

    expect(apiFetch).toHaveBeenCalledWith('/policies', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(created)
    expect(policies.list).toEqual([created])
    expect(policies.byId.p2).toEqual(created)
  })

  it('create records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('name is required'))
    const policies = usePoliciesStore()

    await expect(policies.create({ name: '' })).rejects.toThrow('name is required')
    expect(policies.error).toBe('name is required')
    expect(policies.list).toEqual([])
  })

  it('update puts the input and replaces the entry in list and byId', async () => {
    const original = { id: 'p1', name: 'nightly' }
    const updated = { id: 'p1', name: 'nightly-renamed' }
    apiFetch.mockResolvedValueOnce({ data: [original] })
    const policies = usePoliciesStore()
    await policies.fetchAll()

    apiFetch.mockResolvedValueOnce(updated)
    const input = { name: 'nightly-renamed' }
    const result = await policies.update('p1', input)

    expect(apiFetch).toHaveBeenCalledWith('/policies/p1', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(updated)
    expect(policies.list).toEqual([updated])
    expect(policies.byId.p1).toEqual(updated)
  })

  it('update records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('invalid glob pattern'))
    const policies = usePoliciesStore()

    await expect(policies.update('p1', { name: 'x' })).rejects.toThrow('invalid glob pattern')
    expect(policies.error).toBe('invalid glob pattern')
  })

  it('remove deletes and drops the entry from list and byId', async () => {
    apiFetch.mockResolvedValueOnce({ data: [{ id: 'p1', name: 'nightly' }] })
    const policies = usePoliciesStore()
    await policies.fetchAll()
    policies.byId.p1 = { id: 'p1', name: 'nightly' }

    apiFetch.mockResolvedValueOnce(null)
    await policies.remove('p1')

    expect(apiFetch).toHaveBeenCalledWith('/policies/p1', { method: 'DELETE' })
    expect(policies.list).toEqual([])
    expect(policies.byId.p1).toBeUndefined()
  })

  it('remove records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('policy not found'))
    const policies = usePoliciesStore()

    await expect(policies.remove('missing')).rejects.toThrow('policy not found')
    expect(policies.error).toBe('policy not found')
  })

  it('runAdhoc posts to the adhoc endpoint without touching list or byId', async () => {
    const created = { id: 'adhoc1', name: 'adhoc_oneoff' }
    apiFetch.mockResolvedValue(created)
    const policies = usePoliciesStore()

    const input = {
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
    const result = await policies.runAdhoc(input)

    expect(apiFetch).toHaveBeenCalledWith('/policies/adhoc', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(created)
    expect(policies.list).toEqual([])
    expect(policies.byId).toEqual({})
  })

  it('runAdhoc records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('destination is required'))
    const policies = usePoliciesStore()

    await expect(policies.runAdhoc({ name: 'oneoff' })).rejects.toThrow('destination is required')
    expect(policies.error).toBe('destination is required')
  })
})
