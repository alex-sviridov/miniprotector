import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'

const { destroy, DataTable } = vi.hoisted(() => {
  const destroy = vi.fn()
  const DataTable = vi.fn(() => ({ destroy }))
  return { destroy, DataTable }
})

vi.mock('simple-datatables', () => ({ DataTable }))

beforeEach(() => {
  DataTable.mockClear()
  destroy.mockClear()
})

function entry(overrides) {
  return {
    id: 1,
    source_host: 'database',
    store_host: 'bwfs-east',
    job_id: 'backup:daily-db-backup:1',
    object_id: 'fs://database:f:/var/lib/dbdata/data.db:1752400000',
    ctime: 1752400000,
    store_created_at: 1752400000,
    received_at: 1752400010,
    path: '/var/lib/dbdata/data.db',
    size: 8192,
    mode: '-rw-r--r--',
    owner: 999,
    group: 999,
    mod_time: 1752400000,
    ...overrides,
  }
}

function mountView(state) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: { catalog: { cursorStack: [], ...state } },
  })
  const wrapper = mount(CatalogView, { global: { plugins: [pinia] } })
  return { wrapper, catalog: useCatalogStore() }
}

describe('CatalogView', () => {
  it('calls search with empty filters on mount', async () => {
    const { catalog } = mountView({ entries: [], hasMore: false, loading: false, error: null })
    await flushPromises()
    expect(catalog.search).toHaveBeenCalledWith({ sourceHost: '', storeHost: '', pattern: '' })
  })

  it('submits the filter form via search', async () => {
    const { wrapper, catalog } = mountView({ entries: [], hasMore: false, loading: false, error: null })
    await flushPromises()
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('database')
    await inputs[1].setValue('bwfs-a')
    await inputs[2].setValue('dbdata')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()
    expect(catalog.search).toHaveBeenLastCalledWith({ sourceHost: 'database', storeHost: 'bwfs-a', pattern: 'dbdata' })
  })

  it('disables Next when hasMore is false and Prev when canGoPrev is false', async () => {
    const { wrapper } = mountView({
      entries: [entry({ id: 1 })],
      hasMore: false,
      loading: false,
      error: null,
    })
    await flushPromises()
    const buttons = wrapper.findAll('button')
    const next = buttons.find((b) => b.text() === 'Next')
    const prev = buttons.find((b) => b.text() === 'Prev')
    expect(next.attributes('disabled')).toBeDefined()
    expect(prev.attributes('disabled')).toBeDefined()
  })

  it('clicking Next calls catalog.nextPage', async () => {
    const { wrapper, catalog } = mountView({
      entries: [entry({ id: 1 })],
      hasMore: true,
      loading: false,
      error: null,
    })
    await flushPromises()
    const next = wrapper.findAll('button').find((b) => b.text() === 'Next')
    await next.trigger('click')
    await flushPromises()
    expect(catalog.nextPage).toHaveBeenCalledTimes(1)
  })

  it('groups entries sharing source_host and path into a single row with a version count', async () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, store_created_at: 1752300000, size: 8004 }),
        entry({ id: 2, store_created_at: 1752400000, size: 8192 }),
      ],
      hasMore: false,
      loading: false,
      error: null,
    })
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('/var/lib/dbdata/data.db')
    expect(rows[0].text()).toContain('8192') // representative is the newest version
    expect(rows[0].text()).toContain('2')
  })

  it('renders a single-version file without a version count', async () => {
    const { wrapper } = mountView({
      entries: [entry({ id: 1 })],
      hasMore: false,
      loading: false,
      error: null,
    })
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    const cells = rows[0].findAll('td')
    expect(cells[cells.length - 1].text()).toBe('')
  })

  it('initializes simple-datatables on the rendered table once data loads, and destroys it on unmount', async () => {
    const { wrapper } = mountView({
      entries: [entry({ id: 1 })],
      hasMore: false,
      loading: false,
      error: null,
    })
    await flushPromises()

    expect(DataTable).toHaveBeenCalledTimes(1)
    expect(DataTable.mock.calls[0][0].tagName).toBe('TABLE')

    wrapper.unmount()
    expect(destroy).toHaveBeenCalledTimes(1)
  })

  it('destroys and recreates simple-datatables when Next is clicked', async () => {
    const { wrapper } = mountView({
      entries: [entry({ id: 1 })],
      hasMore: true,
      loading: false,
      error: null,
    })
    await flushPromises()
    expect(DataTable).toHaveBeenCalledTimes(1)

    const next = wrapper.findAll('button').find((b) => b.text() === 'Next')
    await next.trigger('click')
    await flushPromises()

    expect(destroy).toHaveBeenCalledTimes(1)
    expect(DataTable).toHaveBeenCalledTimes(2)
  })

  it('shows the store error message when present', async () => {
    const { wrapper } = mountView({ entries: [], hasMore: false, loading: false, error: 'boom' })
    await flushPromises()
    expect(wrapper.text()).toContain('boom')
  })

  it('opens a modal listing versions newest-first when the version count is clicked', async () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, store_created_at: 1752300000, size: 8004, job_id: 'backup:daily-db-backup:1' }),
        entry({ id: 2, store_created_at: 1752400000, size: 8192, job_id: 'backup:daily-db-backup:2' }),
      ],
      hasMore: false,
      loading: false,
      error: null,
    })
    await flushPromises()

    await wrapper.find('tbody button').trigger('click')

    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
    const versionRows = wrapper.findAll('.fixed tbody tr')
    expect(versionRows).toHaveLength(2)
    expect(versionRows[0].text()).toContain('8192')
    expect(versionRows[0].text()).toContain('backup:daily-db-backup:2')
    expect(versionRows[1].text()).toContain('8004')
  })

  it('closes the modal via the Close button', async () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, store_created_at: 1752300000 }),
        entry({ id: 2, store_created_at: 1752400000 }),
      ],
      hasMore: false,
      loading: false,
      error: null,
    })
    await flushPromises()
    await wrapper.find('tbody button').trigger('click')
    expect(wrapper.find('.fixed').exists()).toBe(true)

    await wrapper.findAll('button').find((b) => b.text() === 'Close').trigger('click')
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('closes the modal via backdrop click', async () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, store_created_at: 1752300000 }),
        entry({ id: 2, store_created_at: 1752400000 }),
      ],
      hasMore: false,
      loading: false,
      error: null,
    })
    await flushPromises()
    await wrapper.find('tbody button').trigger('click')

    await wrapper.find('.fixed').trigger('click')
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('closes the modal on Escape', async () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, store_created_at: 1752300000 }),
        entry({ id: 2, store_created_at: 1752400000 }),
      ],
      hasMore: false,
      loading: false,
      error: null,
    })
    await flushPromises()
    await wrapper.find('tbody button').trigger('click')

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })
})
