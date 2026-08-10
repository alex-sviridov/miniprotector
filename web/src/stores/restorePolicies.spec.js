import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestorePoliciesStore } from './restorePolicies'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('restorePolicies store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('create posts to /restore and returns the created policy', async () => {
    const created = { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' }
    apiFetch.mockResolvedValue(created)
    const restorePolicies = useRestorePoliciesStore()

    const input = {
      name: created.name,
      client_filters: { hostnames: ['web01'], labels: {} },
      source_store: 'store-a:8080',
      config: '{"files":[{"source_host":"database","path":"/var/lib/dbdata/dump.sql"}]}',
    }
    const result = await restorePolicies.create(input)

    expect(apiFetch).toHaveBeenCalledWith('/restore', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(created)
    expect(restorePolicies.loading).toBe(false)
    expect(restorePolicies.error).toBeNull()
  })

  it('create records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('source_store must be a valid host:port'))
    const restorePolicies = useRestorePoliciesStore()

    await expect(restorePolicies.create({ name: 'x' })).rejects.toThrow(
      'source_store must be a valid host:port'
    )
    expect(restorePolicies.error).toBe('source_store must be a valid host:port')
  })
})
