import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestoreSubmissionStore } from './restoreSubmission'
import { useRestoreCartStore } from './restoreCart'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('restoreSubmission store', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-10T00:00:00.000Z'))
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('reports an error and makes no network calls when the cart is empty', async () => {
    const submission = useRestoreSubmissionStore()

    await submission.submit('web01')

    expect(apiFetch).not.toHaveBeenCalled()
    expect(submission.error).toBe('Nothing selected for restore.')
    expect(submission.results).toEqual([])
  })

  it('sends the full, unsplit rule list to the one store a folder rule touches', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 2, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.error).toBeNull()
    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
    ])
    expect(apiFetch).toHaveBeenCalledWith('/restore', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'restore-2026-08-10T00:00:00.000Z-store-a',
        client_filters: { hostnames: ['web01'], labels: {} },
        storage_policy_id: 's1',
        rules: cart.rules,
      }),
    })
  })

  it('creates one restore policy per distinct store, each carrying the same full rule list', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores?source_hosts=database')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      }
      if (path.startsWith('/catalog/stores?source_hosts=web01')) {
        return Promise.resolve({ data: [{ name: 'store-b', count: 1, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [
            { id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] },
            { id: 's2', port: 9090, checkins: [{ hostname: 'store-b', last_seen_at: 1 }] },
          ],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
      { storeHost: 'store-b', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-b' } },
    ])
    const restoreCalls = apiFetch.mock.calls.filter(([path]) => path === '/restore')
    expect(restoreCalls).toHaveLength(2)
    for (const [, opts] of restoreCalls) {
      expect(JSON.parse(opts.body).rules).toEqual(cart.rules)
    }
  })

  it('sets an error and makes no /restore call when the store-facets fetch rejects', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog/stores')) return Promise.reject(new Error('catalog unavailable'))
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await expect(submission.submit('web01')).resolves.toBeUndefined()

    expect(submission.error).toBe('catalog unavailable')
    expect(submission.results).toEqual([])
    expect(submission.submitting).toBe(false)
    expect(apiFetch).not.toHaveBeenCalledWith('/restore', expect.anything())
  })

  it('reports a storage-policy lookup failure and creates no policies', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') return Promise.reject(new Error('policy server down'))
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.error).toBe('Could not look up storage policies: policy server down')
    expect(submission.results).toEqual([])
    expect(apiFetch).not.toHaveBeenCalledWith('/restore', expect.anything())
  })

  it('reports a per-store error when a store has no matching storage policy, without blocking other stores', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores?source_hosts=database')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      }
      if (path.startsWith('/catalog/stores?source_hosts=web01')) {
        return Promise.resolve({ data: [{ name: 'store-b', count: 1, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
      { storeHost: 'store-b', status: 'error', message: 'No storage policy found for store-b' },
    ])
  })

  it('reports a per-store error when CreatePolicy fails, without blocking other stores', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores?source_hosts=database')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      }
      if (path.startsWith('/catalog/stores?source_hosts=web01')) {
        return Promise.resolve({ data: [{ name: 'store-b', count: 1, last_seen: 100 }] })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [
            { id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] },
            { id: 's2', port: 9090, checkins: [{ hostname: 'store-b', last_seen_at: 1 }] },
          ],
        })
      }
      if (path === '/restore') {
        const name = JSON.parse(opts.body).name
        if (name.endsWith('store-b')) return Promise.reject(new Error('name already exists'))
        return Promise.resolve({ id: 'r1', name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.results).toEqual([
      { storeHost: 'store-a', status: 'success', policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' } },
      { storeHost: 'store-b', status: 'error', message: 'name already exists' },
    ])
  })

  it('tracks submitting state across the whole flow', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')
    apiFetch.mockResolvedValue({ data: [] })

    const submission = useRestoreSubmissionStore()
    const pending = submission.submit('web01')
    expect(submission.submitting).toBe(true)
    await pending
    expect(submission.submitting).toBe(false)
  })
})
