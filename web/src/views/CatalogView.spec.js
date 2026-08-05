import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'
import DateRangePanel from '../components/catalog/DateRangePanel.vue'
import FacetPanel from '../components/catalog/FacetPanel.vue'

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
    initialState: {
      catalog: {
        entries: [],
        loading: false,
        error: null,
        filters: { pattern: '', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
        clientFacets: [],
        clientFacetsError: null,
        jobFacets: [],
        jobFacetsError: null,
        ...state,
      },
    },
  })
  const wrapper = mount(CatalogView, {
    global: { plugins: [pinia], stubs: { DateRangePanel: true, FacetPanel: true } },
  })
  return { wrapper, catalog: useCatalogStore() }
}

describe('CatalogView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('fetches results and both facet lists on mount', () => {
    const { catalog } = mountView({})
    expect(catalog.search).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
  })

  it('opens the date panel by default', () => {
    const { wrapper } = mountView({})
    expect(wrapper.findComponent(DateRangePanel).exists()).toBe(true)
    expect(wrapper.findComponent(FacetPanel).exists()).toBe(false)
  })

  it('switches to the clients panel when its chip is clicked', async () => {
    const { wrapper } = mountView({})
    await wrapper.find('[data-test="chip-clients"]').trigger('click')
    expect(wrapper.findComponent(DateRangePanel).exists()).toBe(false)
    const panel = wrapper.findComponent(FacetPanel)
    expect(panel.exists()).toBe(true)
    expect(panel.props('nameLabel')).toBe('Client')
  })

  it('switches to the jobs panel when its chip is clicked', async () => {
    const { wrapper } = mountView({})
    await wrapper.find('[data-test="chip-jobs"]').trigger('click')
    const panel = wrapper.findComponent(FacetPanel)
    expect(panel.exists()).toBe(true)
    expect(panel.props('nameLabel')).toBe('Policy')
  })

  it('closes the open panel when its own chip is clicked again', async () => {
    const { wrapper } = mountView({})
    expect(wrapper.findComponent(DateRangePanel).exists()).toBe(true)
    await wrapper.find('[data-test="chip-date"]').trigger('click')
    expect(wrapper.findComponent(DateRangePanel).exists()).toBe(false)
  })

  it('re-fetches results and both facet lists when the date range changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.search.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.receivedAfter = 500
    await wrapper.vm.$nextTick()

    expect(catalog.search).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
  })

  it('re-fetches results and only the job facets when the client selection changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.search.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.sourceHosts.push('database')
    await wrapper.vm.$nextTick()

    expect(catalog.search).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).not.toHaveBeenCalled()
  })

  it('re-fetches results and only the client facets when the job selection changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.search.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.jobNames.push('nightly-db')
    await wrapper.vm.$nextTick()

    expect(catalog.search).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).not.toHaveBeenCalled()
  })

  it('debounces path input before searching', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.search.mockClear()

    await wrapper.find('[data-test="path-input"]').setValue('dbdata')
    expect(catalog.search).not.toHaveBeenCalled()

    vi.advanceTimersByTime(300)
    await flushPromises()
    expect(catalog.search).toHaveBeenCalledTimes(1)
  })

  it('shows a no-results message when there are no entries', () => {
    const { wrapper } = mountView({})
    expect(wrapper.text()).toContain('No entries match this filter.')
  })

  it('groups entries sharing source_host and path into a single row with a version count', () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, store_created_at: 1752300000, size: 8004 }),
        entry({ id: 2, store_created_at: 1752400000, size: 8192 }),
      ],
    })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('/var/lib/dbdata/data.db')
    expect(rows[0].text()).toContain('8.0 KB')
    expect(rows[0].text()).toContain('2')
  })

  it('renders a single-version file without a version count', () => {
    const { wrapper } = mountView({ entries: [entry({ id: 1 })] })
    const rows = wrapper.findAll('tbody tr')
    const cells = rows[0].findAll('td')
    expect(cells[cells.length - 1].text()).toBe('')
  })

  it('opens the versions modal for the row actually clicked, even after sorting reorders the table', async () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 3, source_host: 'webserver', path: '/var/www/index.html', store_created_at: 1752350000 }),
        entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db', store_created_at: 1752300000 }),
        entry({ id: 2, source_host: 'database', path: '/var/lib/dbdata/data.db', store_created_at: 1752400000 }),
      ],
    })

    await wrapper.find('thead th button').trigger('click')
    await flushPromises()
    const sortedRows = wrapper.findAll('tbody tr')
    expect(sortedRows[0].text()).toContain('/var/lib/dbdata/data.db')

    await sortedRows[0].trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
  })

  it('does not open the versions modal when a single-version row is clicked', async () => {
    const { wrapper } = mountView({ entries: [entry({ id: 1 })] })
    await wrapper.find('tbody tr').trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('closes the versions modal via its Close button', async () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, store_created_at: 1752300000 }),
        entry({ id: 2, store_created_at: 1752400000 }),
      ],
    })
    await wrapper.find('tbody tr').trigger('click')
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

  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({})
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Catalog')
  })
})
