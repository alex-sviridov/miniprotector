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

  it('search resets the cursor stack and fetches page 1 with filters', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: 'database', pattern: 'dbdata' })

    expect(apiFetch).toHaveBeenCalledWith('/catalog?source_host=database&pattern=dbdata')
    expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }])
    expect(catalog.hasMore).toBe(true)
    expect(catalog.canGoPrev).toBe(false)
  })

  it('nextPage requests starting_after the last entry id and pushes the cursor stack', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', pattern: '' })

    apiFetch.mockResolvedValue({ data: [{ id: 3 }, { id: 4 }], has_more: false })
    await catalog.nextPage()

    expect(apiFetch).toHaveBeenLastCalledWith('/catalog?starting_after=2')
    expect(catalog.entries).toEqual([{ id: 3 }, { id: 4 }])
    expect(catalog.canGoPrev).toBe(true)
  })

  it('prevPage pops the cursor stack and refetches the prior page', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', pattern: '' })
    apiFetch.mockResolvedValue({ data: [{ id: 3 }, { id: 4 }], has_more: false })
    await catalog.nextPage()

    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    await catalog.prevPage()

    expect(apiFetch).toHaveBeenLastCalledWith('/catalog')
    expect(catalog.canGoPrev).toBe(false)
  })

  it('nextPage does nothing when has_more is false', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }], has_more: false })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', pattern: '' })

    apiFetch.mockClear()
    await catalog.nextPage()

    expect(apiFetch).not.toHaveBeenCalled()
  })
})
