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
