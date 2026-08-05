import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useCatalogStore } from './catalog'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

const DAY = 24 * 60 * 60

describe('catalog store', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-05T00:00:00Z'))
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('defaults the date range to the last 7 days', () => {
    const catalog = useCatalogStore()
    const now = Math.floor(Date.now() / 1000)
    expect(catalog.filters.receivedBefore).toBe(now)
    expect(catalog.filters.receivedAfter).toBe(now - 7 * DAY)
  })

  describe('search', () => {
    it('fetches a single page using the current filters and a limit of 500', async () => {
      apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: false })
      const catalog = useCatalogStore()
      catalog.filters.pattern = 'dbdata'
      catalog.filters.sourceHosts = ['database']
      catalog.filters.jobNames = ['nightly-db']

      await catalog.search()

      const now = Math.floor(Date.now() / 1000)
      expect(apiFetch).toHaveBeenCalledWith(
        `/catalog?received_after=${now - 7 * DAY}&received_before=${now}&source_hosts=database&job_names=nightly-db&pattern=dbdata&limit=500`
      )
      expect(apiFetch).toHaveBeenCalledTimes(1)
      expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }])
      expect(catalog.loading).toBe(false)
      expect(catalog.error).toBe(null)
    })

    it('loops starting_after until has_more is false, concatenating every page', async () => {
      apiFetch
        .mockResolvedValueOnce({ data: [{ id: 1 }, { id: 2 }], has_more: true })
        .mockResolvedValueOnce({ data: [{ id: 3 }], has_more: false })
      const catalog = useCatalogStore()

      await catalog.search()

      expect(apiFetch).toHaveBeenCalledTimes(2)
      expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }, { id: 3 }])
    })

    it('stops looping when a page returns zero rows even if has_more is true', async () => {
      apiFetch.mockResolvedValue({ data: [], has_more: true })
      const catalog = useCatalogStore()

      await catalog.search()

      expect(apiFetch).toHaveBeenCalledTimes(1)
      expect(catalog.entries).toEqual([])
    })

    it('discards everything collected so far and sets error when a later page fails', async () => {
      apiFetch
        .mockResolvedValueOnce({ data: [{ id: 1 }], has_more: true })
        .mockRejectedValueOnce(new Error('boom'))
      const catalog = useCatalogStore()

      await catalog.search()

      expect(catalog.entries).toEqual([])
      expect(catalog.error).toBe('boom')
      expect(catalog.loading).toBe(false)
    })

    it('discards a stale response that resolves after a later search was issued (last-issued wins, not last-completed)', async () => {
      let resolveFirst
      let resolveSecond
      apiFetch
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              resolveFirst = resolve
            })
        )
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              resolveSecond = resolve
            })
        )
      const catalog = useCatalogStore()

      const firstSearch = catalog.search()
      const secondSearch = catalog.search()

      // The second (last-issued) call resolves first...
      resolveSecond({ data: [{ id: 'second' }], has_more: false })
      await secondSearch

      // ...and the first (stale) call resolves after -- it must not clobber
      // the second call's results.
      resolveFirst({ data: [{ id: 'first' }], has_more: false })
      await firstSearch

      expect(catalog.entries).toEqual([{ id: 'second' }])
    })

    it('sets loading true while the fetch loop is in flight', async () => {
      let resolveFirst
      apiFetch.mockReturnValue(
        new Promise((resolve) => {
          resolveFirst = resolve
        })
      )
      const catalog = useCatalogStore()

      const promise = catalog.search()
      expect(catalog.loading).toBe(true)
      resolveFirst({ data: [], has_more: false })
      await promise
      expect(catalog.loading).toBe(false)
    })
  })

  describe('fetchClientFacets', () => {
    it('queries /catalog/clients excluding the sourceHosts filter', async () => {
      apiFetch.mockResolvedValue({ data: [{ name: 'database', count: 3, last_seen: 123 }] })
      const catalog = useCatalogStore()
      catalog.filters.sourceHosts = ['database']
      catalog.filters.jobNames = ['nightly-db']

      await catalog.fetchClientFacets()

      const now = Math.floor(Date.now() / 1000)
      expect(apiFetch).toHaveBeenCalledWith(
        `/catalog/clients?received_after=${now - 7 * DAY}&received_before=${now}&job_names=nightly-db`
      )
      expect(catalog.clientFacets).toEqual([{ name: 'database', count: 3, last_seen: 123 }])
    })

    it('sets clientFacetsError without touching the results error on failure', async () => {
      apiFetch.mockRejectedValue(new Error('boom'))
      const catalog = useCatalogStore()

      await catalog.fetchClientFacets()

      expect(catalog.clientFacetsError).toBe('boom')
      expect(catalog.error).toBe(null)
    })
  })

  describe('fetchJobFacets', () => {
    it('queries /catalog/jobs excluding the jobNames filter', async () => {
      apiFetch.mockResolvedValue({ data: [{ name: 'nightly-db', count: 7, last_seen: 123 }] })
      const catalog = useCatalogStore()
      catalog.filters.sourceHosts = ['database']
      catalog.filters.jobNames = ['nightly-db']

      await catalog.fetchJobFacets()

      const now = Math.floor(Date.now() / 1000)
      expect(apiFetch).toHaveBeenCalledWith(
        `/catalog/jobs?received_after=${now - 7 * DAY}&received_before=${now}&source_hosts=database`
      )
      expect(catalog.jobFacets).toEqual([{ name: 'nightly-db', count: 7, last_seen: 123 }])
    })
  })
})
