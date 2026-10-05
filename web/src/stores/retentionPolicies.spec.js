import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRetentionPoliciesStore } from './retentionPolicies'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

const rule = (id, priority) => ({ id, name: id, retention: { backup_type: 'filesystem', path: `/${id}`, keep_seconds: 86400, priority } })

describe('retentionPolicies store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll loads retention policies and keeps the server order', async () => {
    apiFetch.mockResolvedValue({ data: [rule('b', 1), rule('a', 2)] })
    const store = useRetentionPoliciesStore()

    await store.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/policies?type=retention')
    expect(store.list.map((p) => p.id)).toEqual(['b', 'a'])
    expect(store.loading).toBe(false)
    expect(store.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const store = useRetentionPoliciesStore()

    await store.fetchAll()

    expect(store.error).toBe('boom')
    expect(store.list).toEqual([])
  })

  it('create posts to /retention-policies and appends the result', async () => {
    const store = useRetentionPoliciesStore()
    store.list = [rule('a', 1)]
    apiFetch.mockResolvedValue(rule('b', 2))

    const created = await store.create({ name: 'b' })

    expect(apiFetch).toHaveBeenCalledWith('/retention-policies', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'b' }),
    })
    expect(created.id).toBe('b')
    expect(store.list.map((p) => p.id)).toEqual(['a', 'b'])
  })

  it('create rethrows and records the error', async () => {
    apiFetch.mockRejectedValue(new Error('path must be absolute'))
    const store = useRetentionPoliciesStore()

    await expect(store.create({ name: 'x' })).rejects.toThrow('path must be absolute')
    expect(store.error).toBe('path must be absolute')
  })

  it('update PUTs to /retention-policies/{id} and replaces the row in place', async () => {
    const store = useRetentionPoliciesStore()
    store.list = [rule('a', 1), rule('b', 2)]
    apiFetch.mockResolvedValue({ ...rule('a', 1), name: 'renamed' })

    await store.update('a', { name: 'renamed' })

    expect(apiFetch).toHaveBeenCalledWith('/retention-policies/a', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'renamed' }),
    })
    expect(store.list.map((p) => p.name)).toEqual(['renamed', 'b'])
  })

  it('remove DELETEs via /policies/{id} and drops the row', async () => {
    const store = useRetentionPoliciesStore()
    store.list = [rule('a', 1), rule('b', 2)]
    apiFetch.mockResolvedValue(undefined)

    await store.remove('a')

    expect(apiFetch).toHaveBeenCalledWith('/policies/a', { method: 'DELETE' })
    expect(store.list.map((p) => p.id)).toEqual(['b'])
  })

  it('reorder shows the new order immediately, posts the id list, then adopts the server order', async () => {
    const store = useRetentionPoliciesStore()
    store.list = [rule('a', 1), rule('b', 2), rule('c', 3)]
    let resolve
    apiFetch.mockReturnValue(new Promise((r) => (resolve = r)))

    const pending = store.reorder(['c', 'a', 'b'])
    expect(store.list.map((p) => p.id)).toEqual(['c', 'a', 'b'])
    expect(store.reordering).toBe(true)

    resolve({ data: [rule('c', 1), rule('a', 2), rule('b', 3)] })
    await pending

    expect(apiFetch).toHaveBeenCalledWith('/retention-policies/reorder', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids: ['c', 'a', 'b'] }),
    })
    expect(store.list.map((p) => p.retention.priority)).toEqual([1, 2, 3])
    expect(store.reordering).toBe(false)
    expect(store.error).toBeNull()
  })

  it('reorder reverts to the server state and reports the error when rejected', async () => {
    const store = useRetentionPoliciesStore()
    store.list = [rule('a', 1), rule('b', 2)]
    apiFetch
      .mockRejectedValueOnce(new Error('ids must list every retention policy exactly once'))
      .mockResolvedValueOnce({ data: [rule('a', 1), rule('b', 2), rule('z', 3)] }) // refetch: someone added z

    await store.reorder(['b', 'a'])

    expect(apiFetch).toHaveBeenLastCalledWith('/policies?type=retention')
    expect(store.list.map((p) => p.id)).toEqual(['a', 'b', 'z'])
    expect(store.error).toBe('ids must list every retention policy exactly once')
    expect(store.reordering).toBe(false)
  })

  it('reorder with an unchanged order does not call the API', async () => {
    const store = useRetentionPoliciesStore()
    store.list = [rule('a', 1), rule('b', 2)]

    await store.reorder(['a', 'b'])

    expect(apiFetch).not.toHaveBeenCalled()
  })
})
