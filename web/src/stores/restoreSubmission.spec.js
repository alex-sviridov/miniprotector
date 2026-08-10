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

  it('resolves a folder rule to catalog entries, groups by store, and creates one restore policy', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog')) {
        return Promise.resolve({
          data: [
            { id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' },
            { id: 2, source_host: 'database', path: '/var/lib/dbdata/schema.sql', store_host: 'store-a' },
          ],
          has_more: false,
        })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.error).toBeNull()
    expect(submission.results).toEqual([
      {
        storeHost: 'store-a',
        status: 'success',
        policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' },
      },
    ])
    expect(apiFetch).toHaveBeenCalledWith('/restore', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'restore-2026-08-10T00:00:00.000Z-store-a',
        client_filters: { hostnames: ['web01'], labels: {} },
        source_store: 'store-a:8080',
        config: JSON.stringify({
          files: [
            { source_host: 'database', path: '/var/lib/dbdata/dump.sql' },
            { source_host: 'database', path: '/var/lib/dbdata/schema.sql' },
          ],
        }),
      }),
    })
  })

  it('collapses a file\'s many catalog versions to one entry per path in the submitted policy', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    // One file backed up nightly is one /catalog row per version, all sharing
    // (source_host, path) but with distinct ids -- and here the older version
    // even sits on a different store than the latest one.
    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog')) {
        return Promise.resolve({
          data: [
            {
              id: 1,
              source_host: 'database',
              path: '/var/lib/dbdata/dump.sql',
              store_host: 'store-b',
              store_created_at: 1752200000,
            },
            {
              id: 2,
              source_host: 'database',
              path: '/var/lib/dbdata/dump.sql',
              store_host: 'store-a',
              store_created_at: 1752300000,
            },
            {
              id: 3,
              source_host: 'database',
              path: '/var/lib/dbdata/dump.sql',
              store_host: 'store-a',
              store_created_at: 1752400000,
            },
            {
              id: 4,
              source_host: 'database',
              path: '/var/lib/dbdata/schema.sql',
              store_host: 'store-a',
              store_created_at: 1752400000,
            },
          ],
          has_more: false,
        })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    const restoreCalls = apiFetch.mock.calls.filter(([path]) => path === '/restore')
    expect(restoreCalls).toHaveLength(1)
    const files = JSON.parse(JSON.parse(restoreCalls[0][1].body).config).files
    expect(files).toEqual([
      { source_host: 'database', path: '/var/lib/dbdata/dump.sql' },
      { source_host: 'database', path: '/var/lib/dbdata/schema.sql' },
    ])
    expect(submission.error).toBeNull()
  })

  it('sets error when a catalog fetch rejects, without throwing out of submit', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog')) return Promise.reject(new Error('catalog unavailable'))
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await expect(submission.submit('web01')).resolves.toBeUndefined()

    expect(submission.error).toBe('catalog unavailable')
    expect(submission.results).toEqual([])
    expect(submission.submitting).toBe(false)
    expect(apiFetch).not.toHaveBeenCalledWith('/restore', expect.anything())
  })

  it('reports a storage-policy lookup failure and processes no groups', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog')) {
        return Promise.resolve({
          data: [
            {
              id: 1,
              source_host: 'database',
              path: '/var/lib/dbdata/dump.sql',
              store_host: 'store-a',
              store_created_at: 1752400000,
            },
          ],
          has_more: false,
        })
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

  it('reports a per-group error when a store has no resolvable address, without blocking other groups', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog?source_host=database')) {
        return Promise.resolve({
          data: [{ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' }],
          has_more: false,
        })
      }
      if (path.startsWith('/catalog?source_host=web01')) {
        return Promise.resolve({
          data: [{ id: 2, source_host: 'web01', path: '/etc/hosts', store_host: 'store-b' }],
          has_more: false,
        })
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
      {
        storeHost: 'store-a',
        status: 'success',
        policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' },
      },
      { storeHost: 'store-b', status: 'error', message: 'No reachable storage node found for store-b' },
    ])
  })

  it('reports a per-group error when CreatePolicy fails, without blocking other groups', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog?source_host=database')) {
        return Promise.resolve({
          data: [{ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' }],
          has_more: false,
        })
      }
      if (path.startsWith('/catalog?source_host=web01')) {
        return Promise.resolve({
          data: [{ id: 2, source_host: 'web01', path: '/etc/hosts', store_host: 'store-b' }],
          has_more: false,
        })
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
      {
        storeHost: 'store-a',
        status: 'success',
        policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' },
      },
      { storeHost: 'store-b', status: 'error', message: 'name already exists' },
    ])
  })

  it('paginates catalog fetches until has_more is false', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    let call = 0
    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog')) {
        call += 1
        if (call === 1) {
          expect(path).not.toContain('starting_after')
          return Promise.resolve({
            data: [{ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' }],
            has_more: true,
          })
        }
        expect(path).toContain('starting_after=1')
        return Promise.resolve({
          data: [{ id: 2, source_host: 'database', path: '/var/lib/dbdata/schema.sql', store_host: 'store-a' }],
          has_more: false,
        })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') return Promise.resolve({ id: 'r1', name: 'x' })
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(call).toBe(2)
    expect(submission.results[0].status).toBe('success')
  })

  it('tracks submitting state across the whole flow', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')
    apiFetch.mockResolvedValue({ data: [], has_more: false })

    const submission = useRestoreSubmissionStore()
    const pending = submission.submit('web01')
    expect(submission.submitting).toBe(true)
    await pending
    expect(submission.submitting).toBe(false)
  })
})
