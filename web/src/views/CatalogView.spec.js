import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'
import { useRestoreCartStore } from '../stores/restoreCart'
import DateRangePanel from '../components/catalog/DateRangePanel.vue'
import FacetPanel from '../components/catalog/FacetPanel.vue'
import DirectoryPathBar from '../components/catalog/DirectoryPathBar.vue'

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
    parent_directory: '/var/lib/dbdata',
    short_filename: 'data.db',
    size: 8192,
    mode: '-rw-r--r--',
    owner: 999,
    group: 999,
    mod_time: 1752400000,
    ...overrides,
  }
}

function mountView(state, restoreCartState = {}) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: {
      catalog: {
        currentPath: null,
        entries: [],
        loading: false,
        error: null,
        filters: { pattern: '', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
        clientFacets: [],
        clientFacetsError: null,
        jobFacets: [],
        jobFacetsError: null,
        directoryChildren: [],
        directoryChildrenLoading: false,
        directoryChildrenError: null,
        ...state,
      },
      restoreCart: { rules: [], ...restoreCartState },
    },
  })
  const wrapper = mount(CatalogView, {
    global: { plugins: [pinia], stubs: { DateRangePanel: true, FacetPanel: true } },
  })
  return { wrapper, catalog: useCatalogStore(), restoreCart: useRestoreCartStore() }
}

describe('CatalogView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('refreshes and fetches both facet lists on mount', () => {
    const { catalog } = mountView({})
    expect(catalog.refresh).toHaveBeenCalledTimes(1)
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

  it('re-refreshes and re-fetches both facet lists when the date range changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.refresh.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.receivedAfter = 500
    await wrapper.vm.$nextTick()

    expect(catalog.refresh).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
  })

  it('re-refreshes and only re-fetches job facets when the client selection changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.refresh.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.sourceHosts.push('database')
    await wrapper.vm.$nextTick()

    expect(catalog.refresh).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).not.toHaveBeenCalled()
  })

  it('re-refreshes and only re-fetches client facets when the job selection changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.refresh.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.jobNames.push('nightly-db')
    await wrapper.vm.$nextTick()

    expect(catalog.refresh).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).not.toHaveBeenCalled()
  })

  it('debounces path input before refreshing', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.refresh.mockClear()

    await wrapper.find('[data-test="path-input"]').setValue('dbdata')
    expect(catalog.refresh).not.toHaveBeenCalled()

    vi.advanceTimersByTime(300)
    await flushPromises()
    expect(catalog.refresh).toHaveBeenCalledTimes(1)
  })

  it('shows a no-results message when there are no entries or folders', () => {
    const { wrapper } = mountView({})
    expect(wrapper.text()).toContain('No entries match this filter.')
  })

  it('renders folder rows above file rows when browsing', () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      directoryChildren: [{ path: '/var/lib/dbdata/backups', name: 'backups', file_count: 3, last_seen: 1752400010, has_children: false }],
      entries: [entry({ id: 1 })],
    })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('backups/')
    expect(rows[1].text()).toContain('data.db')
  })

  it('navigates into a folder when its row is clicked', async () => {
    const { wrapper, catalog } = mountView({
      directoryChildren: [{ path: '/var', name: 'var', file_count: 0, last_seen: 0, has_children: true }],
    })
    await wrapper.find('tbody tr').trigger('click')
    expect(catalog.navigateTo).toHaveBeenCalledWith('/var')
  })

  it('shows the directory path bar while browsing', () => {
    const { wrapper } = mountView({ currentPath: '/var/lib' })
    expect(wrapper.find('[data-test="directory-path-bar"]').exists()).toBe(true)
  })

  it('hides the directory path bar during pattern search', () => {
    const { wrapper } = mountView({
      filters: { pattern: 'dbdata', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
    })
    expect(wrapper.find('[data-test="directory-path-bar"]').exists()).toBe(false)
  })

  it('does not render stale folder rows during pattern search, even if directoryChildren is still populated', () => {
    const { wrapper } = mountView({
      filters: { pattern: 'dbdata', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
      directoryChildren: [{ path: '/var', name: 'var', file_count: 0, last_seen: 0, has_children: true }],
    })
    expect(wrapper.text()).not.toContain('var/')
    expect(wrapper.findAll('tbody tr').filter((r) => r.text().includes('var/'))).toHaveLength(0)
  })

  it('navigates home when the path bar emits a null path', async () => {
    const { wrapper, catalog } = mountView({ currentPath: '/var/lib' })
    await wrapper.findComponent(DirectoryPathBar).vm.$emit('navigate', null)
    expect(catalog.navigateHome).toHaveBeenCalled()
  })

  it('navigates to a crumb path when the path bar emits it', async () => {
    const { wrapper, catalog } = mountView({ currentPath: '/var/lib' })
    await wrapper.findComponent(DirectoryPathBar).vm.$emit('navigate', '/var')
    expect(catalog.navigateTo).toHaveBeenCalledWith('/var')
  })

  it('groups entries sharing source_host and path into a single row with a version count', () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [
        entry({ id: 1, store_created_at: 1752300000, size: 8004 }),
        entry({ id: 2, store_created_at: 1752400000, size: 8192 }),
      ],
    })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('data.db')
    expect(rows[0].text()).toContain('8.0 KB')
    expect(rows[0].text()).toContain('2')
  })

  it('renders a single-version file without a version count', () => {
    const { wrapper } = mountView({ currentPath: '/var/lib/dbdata', entries: [entry({ id: 1 })] })
    const rows = wrapper.findAll('tbody tr')
    const cells = rows[0].findAll('td')
    expect(cells[cells.length - 1].text()).toBe('')
  })

  it('opens the versions modal for the row actually clicked, even after sorting reorders the table', async () => {
    const { wrapper } = mountView({
      filters: { pattern: 'x', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
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
    const { wrapper } = mountView({ currentPath: '/var/lib/dbdata', entries: [entry({ id: 1 })] })
    await wrapper.find('tbody tr').trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('closes the versions modal via its Close button', async () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
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

  it('renders a checkbox for a file row reflecting its restore-cart state', () => {
    const { wrapper } = mountView(
      { currentPath: '/var/lib/dbdata', entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db' })] },
      { rules: [{ path: '/var/lib/dbdata/data.db', host: 'database', include: true }] }
    )
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    expect(checkbox.element.checked).toBe(true)
  })

  it('renders an unchecked checkbox for a file row not in the restore cart', () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db' })],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    expect(checkbox.element.checked).toBe(false)
  })

  it('clicking a file checkbox calls restoreCart.toggleFile and does not navigate', async () => {
    const { wrapper, catalog, restoreCart } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db' })],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    // jsdom only runs a checkbox's native input/change cascade from a
    // 'click' when the element is attached to `document` (mount() here
    // uses a detached div), so 'change' is triggered explicitly to
    // exercise TriStateCheckbox's @change listener the way a real
    // browser click would. The 'click' trigger still exercises the
    // component's @click.stop, which is what keeps this from navigating.
    await checkbox.trigger('click')
    await checkbox.trigger('change')
    expect(restoreCart.toggleFile).toHaveBeenCalledWith('database', '/var/lib/dbdata/data.db')
    expect(catalog.navigateTo).not.toHaveBeenCalled()
  })

  it('renders a checked checkbox for a folder row fully covered by a wildcard rule', () => {
    const { wrapper } = mountView(
      {
        currentPath: '/var',
        directoryChildren: [{ path: '/var/log', name: 'log', file_count: 3, last_seen: 1752400010, has_children: false }],
      },
      { rules: [{ path: '/var/log', host: null, include: true }] }
    )
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    expect(checkbox.element.checked).toBe(true)
    expect(checkbox.element.indeterminate).toBe(false)
  })

  it('renders an indeterminate checkbox for a folder row with a nested exception', () => {
    const { wrapper } = mountView(
      {
        currentPath: '/var',
        directoryChildren: [{ path: '/var/log', name: 'log', file_count: 3, last_seen: 1752400010, has_children: true }],
      },
      {
        rules: [
          { path: '/var/log', host: null, include: true },
          { path: '/var/log/access.log', host: 'web01', include: false },
        ],
      }
    )
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    expect(checkbox.element.indeterminate).toBe(true)
  })

  it('clicking a folder checkbox calls restoreCart.toggleFolder and does not navigate into it', async () => {
    const { wrapper, catalog, restoreCart } = mountView({
      currentPath: '/var',
      directoryChildren: [{ path: '/var/log', name: 'log', file_count: 3, last_seen: 1752400010, has_children: false }],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    // See the comment on the analogous file-checkbox test above: 'change'
    // is triggered explicitly since jsdom won't cascade it from 'click'
    // on a detached element.
    await checkbox.trigger('click')
    await checkbox.trigger('change')
    expect(restoreCart.toggleFolder).toHaveBeenCalledWith('/var/log')
    expect(catalog.navigateTo).not.toHaveBeenCalled()
  })

  it('sets a data-test attribute identifying each row\'s checkbox', () => {
    const { wrapper } = mountView({
      currentPath: '/var',
      directoryChildren: [{ path: '/var/lib', name: 'lib', file_count: 0, last_seen: 0, has_children: true }],
    })
    expect(wrapper.find('[data-test="folder-checkbox-/var/lib"]').exists()).toBe(true)
  })

  it('sets a data-test attribute identifying a file row\'s checkbox', () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql' })],
    })
    expect(wrapper.find('[data-test="file-checkbox-database:/var/lib/dbdata/dump.sql"]').exists()).toBe(true)
  })
})
