import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useCatalogStore } from './catalog'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('catalog store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetches a single page with filters and a limit of 500 when has_more is false', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: false })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: 'database', storeHost: 'bwfs-a', pattern: 'dbdata' })

    expect(apiFetch).toHaveBeenCalledWith(
      '/catalog?source_host=database&store_host=bwfs-a&pattern=dbdata&limit=500'
    )
    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }])
    expect(catalog.loading).toBe(false)
    expect(catalog.error).toBe(null)
  })

  it('loops starting_after until has_more is false, concatenating every page', async () => {
    apiFetch
      .mockResolvedValueOnce({ data: [{ id: 1 }, { id: 2 }], has_more: true })
      .mockResolvedValueOnce({ data: [{ id: 3 }, { id: 4 }], has_more: true })
      .mockResolvedValueOnce({ data: [{ id: 5 }], has_more: false })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })

    expect(apiFetch).toHaveBeenCalledTimes(3)
    expect(apiFetch).toHaveBeenNthCalledWith(1, '/catalog?limit=500')
    expect(apiFetch).toHaveBeenNthCalledWith(2, '/catalog?starting_after=2&limit=500')
    expect(apiFetch).toHaveBeenNthCalledWith(3, '/catalog?starting_after=4&limit=500')
    expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }, { id: 3 }, { id: 4 }, { id: 5 }])
  })

  it('stops looping when a page returns zero rows even if has_more is true', async () => {
    apiFetch.mockResolvedValue({ data: [], has_more: true })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(catalog.entries).toEqual([])
  })

  it('discards everything collected so far and sets error when a later page fails', async () => {
    apiFetch
      .mockResolvedValueOnce({ data: [{ id: 1 }], has_more: true })
      .mockRejectedValueOnce(new Error('boom'))
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })

    expect(catalog.entries).toEqual([])
    expect(catalog.error).toBe('boom')
    expect(catalog.loading).toBe(false)
  })

  it('sets loading true while the fetch loop is in flight', async () => {
    let resolveFirst
    apiFetch.mockReturnValue(
      new Promise((resolve) => {
        resolveFirst = resolve
      })
    )
    const catalog = useCatalogStore()

    const promise = catalog.search({ sourceHost: '', storeHost: '', pattern: '' })
    expect(catalog.loading).toBe(true)
    resolveFirst({ data: [], has_more: false })
    await promise
    expect(catalog.loading).toBe(false)
  })
})
