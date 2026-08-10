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
