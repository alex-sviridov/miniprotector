import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestoreSubmissionStore } from './restoreSubmission'
import { useRestoreCartStore } from './restoreCart'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

function mockStorageAndRestore({ store = 'store-a', policyId = 's1', restoreOk = true, restoreError } = {}) {
  apiFetch.mockImplementation((path, opts) => {
    if (path.startsWith('/catalog/stores')) return Promise.resolve({ data: [{ name: store, count: 1, last_seen: 100 }] })
    if (path === '/policies?type=storage') {
      return Promise.resolve({ data: [{ id: policyId, port: 8080, checkins: [{ hostname: store, last_seen_at: 1 }] }] })
    }
    if (path === '/restore') {
      if (!restoreOk) return Promise.reject(new Error(restoreError))
      return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name, job_id: 'restore:r1:1' })
    }
    throw new Error(`unexpected apiFetch call: ${path}`)
  })
}

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
    await submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(apiFetch).not.toHaveBeenCalled()
    expect(submission.error).toBe('Nothing selected for restore.')
    expect(submission.entryStatus).toEqual({})
  })

  it('records a success status carrying the job_id for a single-store entry', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    mockStorageAndRestore()

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'success', jobId: 'restore:r1:1' }])
  })

  it('records a per-store error without blocking other stores, keyed by their own entries', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores?source_hosts=database')) return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      if (path.startsWith('/catalog/stores?source_hosts=web01')) return Promise.resolve({ data: [{ name: 'store-b', count: 1, last_seen: 100 }] })
      if (path === '/policies?type=storage') {
        return Promise.resolve({ data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }] })
      }
      if (path === '/restore') return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name, job_id: 'restore:r1:1' })
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    expect(submission.entryStatus['database:/var/lib/dbdata/dump.sql']).toEqual([{ status: 'success', jobId: 'restore:r1:1' }])
    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'error', message: 'No storage policy found for store-b' }])
  })

  it('includes not_before/not_after on the wire only for an included rule that has them', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', undefined, undefined, 1000, 2000)
    mockStorageAndRestore()

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules).toEqual([{ host: 'web01', path: '/etc/hosts', include: true, not_before: 1000, not_after: 2000 }])
  })

  it('omits not_before/not_after from an exclusion rule even if present', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/srv/shared', 1000, 2000)
    cart.toggleFile('web01', '/srv/shared/secret.env', undefined, undefined, 1000, 2000) // deselect -> exclusion rule
    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }] })
      if (path === '/policies?type=storage') return Promise.resolve({ data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }] })
      if (path === '/restore') return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name, job_id: 'restore:r1:1' })
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    const exclusion = body.rules.find((r) => r.include === false)
    expect(exclusion).toEqual({ host: 'web01', path: '/srv/shared/secret.env', include: false })
  })

  it('excludes an already-succeeded entry from a later submit, without dropping still-eligible entries', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    mockStorageAndRestore()
    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(apiFetch).toHaveBeenCalledTimes(3) // stores facet, storage policies, /restore

    apiFetch.mockClear()
    cart.toggleFile('web01', '/etc/nginx/nginx.conf')
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules).toEqual([{ host: 'web01', path: '/etc/nginx/nginx.conf', include: true }])
  })

  it('retries a previously-failed entry on a later submit', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    mockStorageAndRestore({ restoreOk: false, restoreError: 'transient failure' })
    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'error', message: 'transient failure' }])

    mockStorageAndRestore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'success', jobId: 'restore:r1:1' }])
  })

  it('tracks submitting state across the whole flow', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')
    apiFetch.mockResolvedValue({ data: [] })

    const submission = useRestoreSubmissionStore()
    const pending = submission.submit('web01', { mode: 'verify', overwrite: false })
    expect(submission.submitting).toBe(true)
    await pending
    expect(submission.submitting).toBe(false)
  })

  it('sends mode and overwrite through on every per-store /restore call', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')
    mockStorageAndRestore()

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'restore', overwrite: true })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.mode).toBe('restore')
    expect(body.overwrite).toBe(true)
  })

  it('never sends storeHost, size, or notBefore/notAfter-as-camelCase on the wire', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', 'bwfs-1', 4096, 1000, 1000)
    mockStorageAndRestore()

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    const restoreCall = apiFetch.mock.calls.find(([path]) => path === '/restore')
    const body = JSON.parse(restoreCall[1].body)
    expect(body.rules[0]).not.toHaveProperty('storeHost')
    expect(body.rules[0]).not.toHaveProperty('size')
    expect(body.rules[0]).not.toHaveProperty('notBefore')
    expect(body.rules[0]).not.toHaveProperty('notAfter')
  })

  it('reports an error for an entry that touches zero storage hosts', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores')) return Promise.resolve({ data: [] }) // zero stores found
      if (path === '/policies?type=storage') return Promise.resolve({ data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }] })
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    expect(submission.entryStatus['web01:/etc/hosts']).toEqual([{ status: 'error', message: 'No storage host found for this selection' }])
    expect(submission.error).toBeNull()
    expect(submission.submitting).toBe(false)
  })

  it('tracks a folder entry fanning out to two stores with different outcomes', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog/stores?pattern=%2Fvar%2Flib%2Fdbdata')) {
        return Promise.resolve({ data: [{ name: 'store-a', count: 1, last_seen: 100 }, { name: 'store-b', count: 1, last_seen: 100 }] })
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
        if (name.endsWith('store-a')) return Promise.resolve({ id: 'r1', name, job_id: 'restore:r1:1' })
        if (name.endsWith('store-b')) return Promise.reject(new Error('store-b unavailable'))
        throw new Error(`unexpected store: ${name}`)
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01', { mode: 'verify', overwrite: false })

    expect(submission.entryStatus[':/var/lib/dbdata']).toHaveLength(2)
    expect(submission.entryStatus[':/var/lib/dbdata']).toContainEqual({ status: 'success', jobId: 'restore:r1:1' })
    expect(submission.entryStatus[':/var/lib/dbdata']).toContainEqual({ status: 'error', message: 'store-b unavailable' })
  })
})
