import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientsListView from './ClientsListView.vue'
import { useClientsStore } from '../stores/clients'

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

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientsListView, {
    global: {
      plugins: [pinia],
      stubs: { RouterLink: { template: '<a :href="to"><slot /></a>', props: ['to'] } },
    },
  })
  return { wrapper, clients: useClientsStore() }
}

describe('ClientsListView', () => {
  it('calls fetchAll on mount', () => {
    const { clients } = mountView({ list: [], loading: false, error: null })
    expect(clients.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('renders each client with a link to its detail page', () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'webserver', revoked: false, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('webserver')
    expect(wrapper.find('a[href="/clients/webserver"]').exists()).toBe(true)
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('links to the enroll form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('a[href="/clients/new"]').exists()).toBe(true)
  })

  it('initializes simple-datatables on the rendered table once data loads, and destroys it on unmount', async () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'webserver', revoked: false, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    await flushPromises()

    expect(DataTable).toHaveBeenCalledTimes(1)
    expect(DataTable.mock.calls[0][0].tagName).toBe('TABLE')

    wrapper.unmount()
    expect(destroy).toHaveBeenCalledTimes(1)
  })
})
