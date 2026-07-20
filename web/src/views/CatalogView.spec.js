import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'

const { destroy, on, DataTable } = vi.hoisted(() => {
  const destroy = vi.fn()
  const on = vi.fn()
  const DataTable = vi.fn(() => ({ destroy, on }))
  return { destroy, on, DataTable }
})

vi.mock('simple-datatables', () => ({ DataTable }))

beforeEach(() => {
  DataTable.mockClear()
  destroy.mockClear()
  on.mockClear()
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
    initialState: { catalog: { entries: [], loading: false, error: null, ...state } },
  })
  const wrapper = mount(CatalogView, { global: { plugins: [pinia] } })
  return { wrapper, catalog: useCatalogStore() }
}

function selectRow(rowIndex) {
  const call = on.mock.calls.findLast(([event]) => event === 'datatable.selectrow')
  call[1](rowIndex)
}

async function search(wrapper, sourceHost = 'database') {
  await wrapper.findAll('input')[0].setValue(sourceHost)
  await wrapper.find('form').trigger('submit.prevent')
  await flushPromises()
}

describe('CatalogView', () => {
  it('does not fetch on mount', async () => {
    const { catalog } = mountView({})
    await flushPromises()
    expect(catalog.search).not.toHaveBeenCalled()
  })

  it('shows a prompt before any search has been run', () => {
    const { wrapper } = mountView({})
    expect(wrapper.text()).toContain('Enter a filter and search.')
  })

  it('disables Search when every filter field is empty', () => {
    const { wrapper } = mountView({})
    expect(wrapper.find('button[type="submit"]').attributes('disabled')).toBeDefined()
  })

  it('enables Search once a filter field has a value', async () => {
    const { wrapper } = mountView({})
    await wrapper.findAll('input')[0].setValue('database')
    expect(wrapper.find('button[type="submit"]').attributes('disabled')).toBeUndefined()
  })

  it('does not call search when submitted with every field empty', async () => {
    const { wrapper, catalog } = mountView({})
    await wrapper.find('form').trigger('submit.prevent')
    expect(catalog.search).not.toHaveBeenCalled()
  })

  it('submits the filter form via search once a field is filled', async () => {
    const { wrapper, catalog } = mountView({})
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('database')
    await inputs[1].setValue('bwfs-a')
    await inputs[2].setValue('dbdata')
    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()
    expect(catalog.search).toHaveBeenLastCalledWith({
      sourceHost: 'database',
      storeHost: 'bwfs-a',
      pattern: 'dbdata',
    })
  })

  it('shows a no-results message after a search returns nothing', async () => {
    const { wrapper } = mountView({})
    await search(wrapper)
    expect(wrapper.text()).toContain('No entries match this filter.')
  })

  it('groups entries sharing source_host and path into a single row with a version count', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [
      entry({ id: 1, store_created_at: 1752300000, size: 8004 }),
      entry({ id: 2, store_created_at: 1752400000, size: 8192 }),
    ]
    await search(wrapper)

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('/var/lib/dbdata/data.db')
    expect(rows[0].text()).toContain('8.0 KB')
    expect(rows[0].text()).toContain('2')
  })

  it('renders a single-version file without a version count', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [entry({ id: 1 })]
    await search(wrapper)

    const rows = wrapper.findAll('tbody tr')
    const cells = rows[0].findAll('td')
    expect(cells[cells.length - 1].text()).toBe('')
  })

  it('constructs simple-datatables with search disabled and a 25-row page size, and destroys it on unmount', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [entry({ id: 1 })]
    await search(wrapper)

    expect(DataTable).toHaveBeenCalledTimes(1)
    expect(DataTable.mock.calls[0][0].tagName).toBe('TABLE')
    expect(DataTable.mock.calls[0][1]).toEqual({ searchable: false, perPage: 25 })

    wrapper.unmount()
    expect(destroy).toHaveBeenCalledTimes(1)
  })

  it('opens the versions modal when a multi-version row is selected', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [
      entry({ id: 1, store_created_at: 1752300000 }),
      entry({ id: 2, store_created_at: 1752400000 }),
    ]
    await search(wrapper)

    selectRow(0)
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.fixed').exists()).toBe(true)
    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
  })

  it('does not open the versions modal when a single-version row is selected', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [entry({ id: 1 })]
    await search(wrapper)

    selectRow(0)
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('closes the versions modal via its Close button', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [
      entry({ id: 1, store_created_at: 1752300000 }),
      entry({ id: 2, store_created_at: 1752400000 }),
    ]
    await search(wrapper)

    selectRow(0)
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(true)

    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Close')
      .trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })
})
