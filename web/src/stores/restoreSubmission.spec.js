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
        rules: [{ host: null, path: '/var/lib/dbdata', include: true }],
      }),
    })
  })

  // rulesForStore pulls the rules each store's CreatePolicy call carried,
  // keyed by the store host its generated policy name ends with.
  function rulesByStoreFromCalls() {
    const byStore = {}
    for (const [path, opts] of apiFetch.mock.calls) {
      if (path !== '/restore') continue
      const body = JSON.parse(opts.body)
      byStore[body.name.replace(/^restore-.*Z-/, '')] = body.rules
    }
    return byStore
  }

  // The failure this splitting exists to prevent: rwfs treats a file-level
  // rule that matches nothing on the store it is checking as a verification
  // failure, so telling store-b to verify a file that only ever lived on
  // store-a would fail store-b's one-shot task forever.
  it('creates one restore policy per distinct store, each carrying only its own file rules', async () => {
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
    expect(rulesByStoreFromCalls()).toEqual({
      'store-a': [{ path: '/var/lib/dbdata/dump.sql', host: 'database', include: true }],
      'store-b': [{ path: '/etc/hosts', host: 'web01', include: true }],
    })
  })

  it('sends folder rules to every store alongside each store\'s own file rules', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/srv/shared')
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores?pattern=%2Fsrv%2Fshared')) {
        return Promise.resolve({
          data: [
            { name: 'store-a', count: 1, last_seen: 100 },
            { name: 'store-b', count: 1, last_seen: 100 },
          ],
        })
      }
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

    const folderRule = { path: '/srv/shared', host: null, include: true }
    expect(rulesByStoreFromCalls()).toEqual({
      'store-a': [folderRule, { path: '/var/lib/dbdata/dump.sql', host: 'database', include: true }],
      'store-b': [folderRule, { path: '/etc/hosts', host: 'web01', include: true }],
    })
  })

  // An exclusion rule can only ever suppress a selection -- rwfs's
  // not-found scan skips it -- so it is safe on every store, and dropping
  // it would restore a file the user explicitly deselected.
  it('sends exclusion rules to every store', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/srv/shared')
    cart.toggleFile('web01', '/srv/shared/secret.env') // deselects one file under the folder
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')

    expect(cart.rules).toEqual([
      { path: '/srv/shared', host: null, include: true, destPath: '/srv/shared' },
      { path: '/srv/shared/secret.env', host: 'web01', include: false, destPath: '/srv/shared/secret.env' },
      { path: '/var/lib/dbdata/dump.sql', host: 'database', include: true, destPath: '/var/lib/dbdata/dump.sql' },
    ])

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores?pattern=%2Fsrv%2Fshared')) {
        return Promise.resolve({
          data: [
            { name: 'store-a', count: 1, last_seen: 100 },
            { name: 'store-b', count: 1, last_seen: 100 },
          ],
        })
      }
      if (path.startsWith('/catalog/stores?source_hosts=database')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
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

    const shared = [
      { path: '/srv/shared', host: null, include: true },
      { path: '/srv/shared/secret.env', host: 'web01', include: false },
    ]
    expect(rulesByStoreFromCalls()).toEqual({
      'store-a': [...shared, { path: '/var/lib/dbdata/dump.sql', host: 'database', include: true }],
      'store-b': shared,
    })
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

  it('includes dest_path on the wire only for a rule whose destPath differs from its path', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/nginx/nginx.conf')
    cart.setDestPath({ host: 'web01', path: '/etc/nginx/nginx.conf' }, '/etc/nginx/nginx.conf.bak')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
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

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules).toEqual([
      { host: 'web01', path: '/etc/nginx/nginx.conf', include: true, dest_path: '/etc/nginx/nginx.conf.bak' },
    ])
  })

  it('never sends storeHost or size on the wire', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', 'bwfs-1', 4096)

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
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

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules[0]).not.toHaveProperty('storeHost')
    expect(body.rules[0]).not.toHaveProperty('size')
    expect(body.rules[0]).not.toHaveProperty('dest_path')
  })
})
