import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'
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
  const catalog = useCatalogStore()
  vi.spyOn(catalog, 'fetchPathVersions').mockResolvedValue([])
  const wrapper = mount(CatalogView, {
    global: { plugins: [pinia], stubs: { DateRangePanel: true, FacetPanel: true } },
  })
  return { wrapper, catalog, restoreCart: useRestoreCartStore(), submission: useRestoreSubmissionStore() }
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

  it('shows each file row\'s representative captured timestamp as a clickable "Captured" cell', () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01', store_created_at: 1752400000 })] }).wrapper
    const cell = wrapper.get('[data-test="captured-web01:/etc/hosts"]')
    expect(cell.text()).not.toBe('')
  })

  it('reflects an already-pinned version in a file row\'s Captured cell, instead of the representative default', () => {
    const wrapper = mountView(
      { entries: [entry({ path: '/etc/hosts', source_host: 'web01', store_created_at: 1752400000 })] },
      { rules: [{ host: 'web01', path: '/etc/hosts', include: true, destPath: '/etc/hosts', notBefore: 555, notAfter: 555 }] }
    ).wrapper
    const cell = wrapper.get('[data-test="captured-web01:/etc/hosts"]')
    expect(cell.text()).toBe(new Date(555 * 1000).toLocaleString())
    expect(cell.text()).not.toBe(new Date(1752400000 * 1000).toLocaleString())
  })

  it('falls back to the representative default in a file row\'s Captured cell when no pin exists', () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01', store_created_at: 1752400000 })] }).wrapper
    const cell = wrapper.get('[data-test="captured-web01:/etc/hosts"]')
    expect(cell.text()).toBe(new Date(1752400000 * 1000).toLocaleString())
  })

  it('reflects an already-pinned version in a folder row\'s Captured cell, instead of last_seen', () => {
    const wrapper = mountView(
      { directoryChildren: [{ path: '/var', name: 'var', file_count: 3, last_seen: 1752400010 }] },
      { rules: [{ host: null, path: '/var', include: true, destPath: '/var', notBefore: 555, notAfter: 555 }] }
    ).wrapper
    const cell = wrapper.get('[data-test="captured-/var"]')
    expect(cell.text()).toBe(new Date(555 * 1000).toLocaleString())
  })

  it('opens the version picker for the row whose Captured cell was clicked, scoped to that file\'s host', async () => {
    const wrapper = mountView({
      entries: [
        entry({ path: '/etc/hosts', source_host: 'web01' }),
        entry({ path: '/etc/hostname', source_host: 'web01' }),
      ],
    }).wrapper
    await wrapper.get('[data-test="captured-web01:/etc/hostname"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'VersionsModal' })
    expect(modal.props('path')).toBe('/etc/hostname')
    expect(modal.props('sourceHost')).toBe('web01')
  })

  it('opens the version picker for a folder row scoped host-agnostically (sourceHost null)', async () => {
    const wrapper = mountView({ directoryChildren: [{ path: '/var', name: 'var', file_count: 3, last_seen: 100 }] }).wrapper
    await wrapper.get('[data-test="captured-/var"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'VersionsModal' })
    expect(modal.props('path')).toBe('/var')
    expect(modal.props('sourceHost')).toBe(null)
  })

  it('closes the version picker via its close event', async () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] }).wrapper
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'VersionsModal' }).exists()).toBe(false)
  })

  it('selecting a version selects the file (if not already) and pins its window to that exact instant', async () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] }).wrapper
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 555, store_host: 'bwfs-1', size: 10 })
    expect(restoreCart.toggleFile).toHaveBeenCalledWith('web01', '/etc/hosts', 'bwfs-1', 10, expect.any(Number), expect.any(Number), false)
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith({ host: 'web01', path: '/etc/hosts' }, 555, 555)
  })

  it('picking a version for a file only covered by an ancestor folder rule creates a real pinned rule at the exact path, instead of silently no-op-ing', async () => {
    const wrapper = mountView(
      { entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] },
      { rules: [{ host: null, path: '/etc', include: true, destPath: '/etc' }] } // ancestor-only coverage, no exact rule at /etc/hosts
    ).wrapper
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 555, store_host: 'bwfs-1', size: 10 })
    // Not toggleFile: the path already resolves as selected (via the
    // ancestor), so toggling would flip it to an exclusion instead of
    // materializing the implicit selection into a real rule.
    expect(restoreCart.toggleFile).not.toHaveBeenCalled()
    expect(restoreCart.ensureFileSelected).toHaveBeenCalledWith('web01', '/etc/hosts', 'bwfs-1', 10, expect.any(Number), expect.any(Number), false)
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith({ host: 'web01', path: '/etc/hosts' }, 555, 555)
  })

  it('picking a version for a folder only covered by an ancestor folder rule creates a real pinned rule at the exact path', async () => {
    const wrapper = mountView(
      { directoryChildren: [{ path: '/var/lib/db', name: 'db', file_count: 3, last_seen: 100 }] },
      { rules: [{ host: null, path: '/var', include: true, destPath: '/var' }] } // ancestor-only coverage, no exact rule at /var/lib/db
    ).wrapper
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-/var/lib/db"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 555 })
    expect(restoreCart.toggleFolder).not.toHaveBeenCalled()
    expect(restoreCart.ensureFolderSelected).toHaveBeenCalledWith('/var/lib/db', expect.any(Number), expect.any(Number))
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith({ host: null, path: '/var/lib/db' }, 555, 555)
  })

  it('selecting a version for an already-selected file does not re-toggle it', async () => {
    const wrapper = mountView(
      { entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] },
      { rules: [{ host: 'web01', path: '/etc/hosts', include: true, destPath: '/etc/hosts' }] }
    ).wrapper
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 555 })
    expect(restoreCart.toggleFile).not.toHaveBeenCalled()
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith({ host: 'web01', path: '/etc/hosts' }, 555, 555)
  })

  it('using latest resets the window to the current catalog filter range', async () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] }).wrapper
    const restoreCart = useRestoreCartStore()
    const catalog = useCatalogStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('use-latest')
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith(
      { host: 'web01', path: '/etc/hosts' },
      catalog.filters.receivedAfter,
      catalog.filters.receivedBefore
    )
  })

  it('checking a file box passes the current catalog filter range as its default version window', async () => {
    const { wrapper, restoreCart, catalog } = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01' })] })
    await wrapper.get('[data-test="file-checkbox-web01:/etc/hosts"]').trigger('click')
    await wrapper.find('tbody tr input[type="checkbox"]').trigger('change')
    expect(restoreCart.toggleFile).toHaveBeenCalledWith(
      'web01', '/etc/hosts', 'bwfs-east', 8192, catalog.filters.receivedAfter, catalog.filters.receivedBefore, false
    )
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
    expect(restoreCart.toggleFile).toHaveBeenCalledWith('database', '/var/lib/dbdata/data.db', 'bwfs-east', 8192, 1000, 2000, false)
    expect(catalog.navigateTo).not.toHaveBeenCalled()
  })

  it('clicking a file checkbox also clears any stale submission status recorded for that entry', async () => {
    const { wrapper, submission } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db' })],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    await checkbox.trigger('click')
    await checkbox.trigger('change')
    expect(submission.clearEntry).toHaveBeenCalledWith({ host: 'database', path: '/var/lib/dbdata/data.db' })
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
    expect(restoreCart.toggleFolder).toHaveBeenCalledWith('/var/log', 1000, 2000)
    expect(catalog.navigateTo).not.toHaveBeenCalled()
  })

  it('clicking a folder checkbox also clears any stale submission status recorded for that entry', async () => {
    const { wrapper, submission } = mountView({
      currentPath: '/var',
      directoryChildren: [{ path: '/var/log', name: 'log', file_count: 3, last_seen: 1752400010, has_children: false }],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    await checkbox.trigger('click')
    await checkbox.trigger('change')
    expect(submission.clearEntry).toHaveBeenCalledWith({ host: null, path: '/var/log' })
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

  it('shows a Damaged badge on a file row only when its latest version is damaged', () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, source_host: 'web01', path: '/a', store_created_at: 100, damaged: true }),
        entry({ id: 2, source_host: 'web01', path: '/a', store_created_at: 200, damaged: false }),
        entry({ id: 3, source_host: 'web01', path: '/b', store_created_at: 100, damaged: false }),
        entry({ id: 4, source_host: 'web01', path: '/b', store_created_at: 200, damaged: true }),
      ],
    })
    expect(wrapper.find('[data-test="file-damaged-web01:/a"]').exists()).toBe(false)
    const badge = wrapper.get('[data-test="file-damaged-web01:/b"]')
    expect(badge.text()).toBe('Damaged')
    expect(badge.attributes('title')).toContain('damaged')
  })

  it('checking a damaged file passes the damaged flag to the cart', async () => {
    const { wrapper, restoreCart, catalog } = mountView({
      entries: [entry({ path: '/etc/hosts', source_host: 'web01', damaged: true })],
    })
    await wrapper.get('[data-test="file-checkbox-web01:/etc/hosts"]').trigger('click')
    await wrapper.find('tbody tr input[type="checkbox"]').trigger('change')
    expect(restoreCart.toggleFile).toHaveBeenCalledWith(
      'web01', '/etc/hosts', 'bwfs-east', 8192, catalog.filters.receivedAfter, catalog.filters.receivedBefore, true
    )
  })

  it('picking a version refreshes the cart rule damaged flag from that version; using latest from the latest row', async () => {
    const wrapper = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01', damaged: true })] }).wrapper
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 555, damaged: false })
    expect(restoreCart.setDamaged).toHaveBeenCalledWith({ host: 'web01', path: '/etc/hosts' }, false)
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('use-latest')
    expect(restoreCart.setDamaged).toHaveBeenLastCalledWith({ host: 'web01', path: '/etc/hosts' }, true)
  })

  it('using latest for a file not in the current rows clears the damaged flag', async () => {
    const { wrapper, catalog } = mountView({ entries: [entry({ path: '/etc/hosts', source_host: 'web01', damaged: true })] })
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    catalog.entries = []
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('use-latest')
    expect(restoreCart.setDamaged).toHaveBeenLastCalledWith({ host: 'web01', path: '/etc/hosts' }, false)
  })
})
