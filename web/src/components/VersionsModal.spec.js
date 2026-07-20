import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import VersionsModal from './VersionsModal.vue'

function group(overrides) {
  return {
    sourceHost: 'database',
    path: '/var/lib/dbdata/data.db',
    versions: [
      {
        id: 2,
        store_created_at: 1752400000,
        size: 8192,
        mode: '-rw-r--r--',
        mod_time: 1752400000,
        job_id: 'backup:daily-db-backup:2',
        store_host: 'bwfs-east',
      },
      {
        id: 1,
        store_created_at: 1752300000,
        size: 8004,
        mode: '-rw-r--r--',
        mod_time: 1752300000,
        job_id: 'backup:daily-db-backup:1',
        store_host: 'bwfs-east',
      },
    ],
    ...overrides,
  }
}

describe('VersionsModal', () => {
  it('renders the heading and every version newest-first', () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })

    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('8.0 KB')
    expect(rows[0].text()).toContain('backup:daily-db-backup:2')
    expect(rows[1].text()).toContain('backup:daily-db-backup:1')
  })

  it('renders a dash for a zero timestamp instead of the literal value', () => {
    const wrapper = mount(VersionsModal, {
      props: {
        group: group({
          versions: [
            {
              id: 1,
              store_created_at: 0,
              size: 100,
              mode: '-rw-r--r--',
              mod_time: 0,
              job_id: 'j',
              store_host: 'bwfs-east',
            },
          ],
        }),
      },
    })
    const cells = wrapper.findAll('tbody td')
    expect(cells[0].text()).toBe('—')
    expect(cells[3].text()).toBe('—')
  })

  it('emits close when the Close button is clicked', async () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on a backdrop click', async () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })
    await wrapper.find('.fixed').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('does not emit close when clicking inside the modal body', async () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })
    await wrapper.find('table').trigger('click')
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('emits close on Escape, and stops listening after unmount', () => {
    const wrapper = mount(VersionsModal, { props: { group: group() } })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    const closeEvents = wrapper.emitted('close')
    expect(closeEvents).toHaveLength(1)

    wrapper.unmount()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(closeEvents).toHaveLength(1)
  })
})
