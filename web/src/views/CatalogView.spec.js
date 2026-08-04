import { describe, it, expect } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'

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

  it('opens the versions modal for the row actually clicked, even after sorting reorders the table', async () => {
    const { wrapper, catalog } = mountView({})
    // Insertion order: "webserver" (1 version) first, "database" (2
    // versions) second — so row 0 is webserver *before* sorting. Sorting
    // ascending by Path puts "database" (/var/lib/...) before "webserver"
    // (/var/www/...), so row 0 becomes database *after* sorting. The old
    // simple-datatables integration mapped a clicked row's index back into
    // the pre-sort array, so clicking post-sort row 0 there would have
    // resolved to webserver (wrong, and single-version besides). Row-click
    // now hands back the actual clicked row object, so it must resolve to
    // database regardless of sort order.
    catalog.entries = [
      entry({ id: 3, source_host: 'webserver', path: '/var/www/index.html', store_created_at: 1752350000 }),
      entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db', store_created_at: 1752300000 }),
      entry({ id: 2, source_host: 'database', path: '/var/lib/dbdata/data.db', store_created_at: 1752400000 }),
    ]
    await search(wrapper)

    await wrapper.find('thead th button').trigger('click') // sorts by Path ascending
    await flushPromises()
    const sortedRows = wrapper.findAll('tbody tr')
    expect(sortedRows[0].text()).toContain('/var/lib/dbdata/data.db')

    await sortedRows[0].trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
  })

  it('does not open the versions modal when a single-version row is clicked', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.entries = [entry({ id: 1 })]
    await search(wrapper)

    await wrapper.find('tbody tr').trigger('click')
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
