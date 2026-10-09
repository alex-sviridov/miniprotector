import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import VersionsModal from './VersionsModal.vue'
import { useCatalogStore } from '../stores/catalog'

function mountModal(props, versions = []) {
  const pinia = createTestingPinia({ stubActions: false })
  const catalog = useCatalogStore(pinia)
  vi.spyOn(catalog, 'fetchPathVersions').mockResolvedValue(versions)
  const wrapper = mount(VersionsModal, { props, global: { plugins: [pinia] } })
  return { wrapper, catalog }
}

function version(overrides) {
  return {
    id: 1,
    source_host: 'database',
    store_created_at: 1752400000,
    size: 8192,
    mode: '-rw-r--r--',
    mod_time: 1752400000,
    job_id: 'backup:daily-db-backup:2',
    store_host: 'bwfs-east',
    ...overrides,
  }
}

describe('VersionsModal', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('fetches versions scoped to the given path and source host on mount', () => {
    const { catalog } = mountModal({ path: '/var/lib/dbdata/data.db', sourceHost: 'database' })
    expect(catalog.fetchPathVersions).toHaveBeenCalledWith('/var/lib/dbdata/data.db', 'database')
  })

  it('renders the heading with the source host for a file', async () => {
    const { wrapper } = mountModal({ path: '/var/lib/dbdata/data.db', sourceHost: 'database' })
    await flushPromises()
    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
  })

  it('renders the heading without a host for a folder', async () => {
    const { wrapper } = mountModal({ path: '/srv/shared', sourceHost: null })
    await flushPromises()
    expect(wrapper.text()).toContain('Versions of /srv/shared')
    expect(wrapper.text()).not.toContain('on ')
  })

  it('renders every version newest-first with a "Restore this version" button each', async () => {
    const { wrapper } = mountModal(
      { path: '/var/lib/dbdata/data.db', sourceHost: 'database' },
      [version({ id: 2, store_created_at: 300 }), version({ id: 1, store_created_at: 200 })]
    )
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].find('[data-test="restore-version-2"]').exists()).toBe(true)
    expect(rows[1].find('[data-test="restore-version-1"]').exists()).toBe(true)
  })

  it('emits select-version with the chosen version on click', async () => {
    const v = version({ id: 1 })
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' }, [v])
    await flushPromises()
    await wrapper.get('[data-test="restore-version-1"]').trigger('click')
    expect(wrapper.emitted('select-version')).toEqual([[v]])
  })

  it('emits use-latest when the "Use latest" button is clicked', async () => {
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' })
    await flushPromises()
    await wrapper.get('[data-test="use-latest"]').trigger('click')
    expect(wrapper.emitted('use-latest')).toHaveLength(1)
  })

  it('use-latest carries the damaged state of the newest version (false when there are none)', async () => {
    const damagedLatest = mountModal({ path: '/etc/hosts', sourceHost: 'web01' }, [
      version({ id: 2, store_created_at: 200, damaged: true }),
      version({ id: 1, store_created_at: 100, damaged: false }),
    ])
    await flushPromises()
    await damagedLatest.wrapper.get('[data-test="use-latest"]').trigger('click')
    expect(damagedLatest.wrapper.emitted('use-latest')).toEqual([[true]])

    const healthyLatest = mountModal({ path: '/etc/hosts', sourceHost: 'web01' }, [
      version({ id: 2, store_created_at: 200 }),
      version({ id: 1, store_created_at: 100, damaged: true }),
    ])
    await flushPromises()
    await healthyLatest.wrapper.get('[data-test="use-latest"]').trigger('click')
    expect(healthyLatest.wrapper.emitted('use-latest')).toEqual([[false]])

    const none = mountModal({ path: '/etc/hosts', sourceHost: 'web01' }, [])
    await flushPromises()
    await none.wrapper.get('[data-test="use-latest"]').trigger('click')
    expect(none.wrapper.emitted('use-latest')).toEqual([[false]])
  })

  it('shows a multi-host note only when the fetched versions span more than one source host', async () => {
    const single = await mountModal({ path: '/srv/shared', sourceHost: null }, [version({ source_host: 'a' })])
    await flushPromises()
    expect(single.wrapper.find('[data-test="multi-host-note"]').exists()).toBe(false)

    const multi = mountModal({ path: '/srv/shared', sourceHost: null }, [
      version({ id: 1, source_host: 'a' }),
      version({ id: 2, source_host: 'b' }),
    ])
    await flushPromises()
    expect(multi.wrapper.find('[data-test="multi-host-note"]').exists()).toBe(true)
  })

  it('shows an error message when the fetch rejects', async () => {
    const pinia = createTestingPinia({ stubActions: false })
    const catalog = useCatalogStore(pinia)
    vi.spyOn(catalog, 'fetchPathVersions').mockRejectedValue(new Error('catalog unavailable'))
    const wrapper = mount(VersionsModal, { props: { path: '/etc/hosts', sourceHost: 'web01' }, global: { plugins: [pinia] } })
    await flushPromises()
    expect(wrapper.get('[data-test="versions-error"]').text()).toBe('catalog unavailable')
  })

  it('renders a dash for a zero timestamp instead of the literal value', async () => {
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' }, [
      version({ store_created_at: 0, mod_time: 0 }),
    ])
    await flushPromises()
    const cells = wrapper.findAll('tbody td')
    expect(cells[0].text()).toBe('—')
  })

  it('emits close when the Close button is clicked', async () => {
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' })
    await flushPromises()
    await wrapper.get('[data-test="close"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on a backdrop click and on Escape', async () => {
    const { wrapper } = mountModal({ path: '/etc/hosts', sourceHost: 'web01' })
    await flushPromises()
    await wrapper.find('.fixed').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(2)
  })

  it('shows a Damaged badge with a warning tooltip only on damaged versions, and keeps restore enabled', async () => {
    const { wrapper } = mountModal(
      { path: '/var/lib/dbdata/data.db', sourceHost: 'database' },
      [version({ id: 1, damaged: true }), version({ id: 2, damaged: false }), version({ id: 3 })]
    )
    await flushPromises()
    const badge = wrapper.get('[data-test="version-damaged-1"]')
    expect(badge.text()).toBe('Damaged')
    expect(badge.attributes('title')).toBe('Backup data for this version is damaged; restore may fail.')
    expect(wrapper.find('[data-test="version-damaged-2"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="version-damaged-3"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="restore-version-1"]').attributes('disabled')).toBeUndefined()
  })
})
