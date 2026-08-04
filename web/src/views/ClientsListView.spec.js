import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientsListView from './ClientsListView.vue'
import { useClientsStore } from '../stores/clients'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientsListView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
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
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'webserver')
    expect(link.props('to')).toEqual({ name: 'client-detail', params: { hostname: 'webserver' } })
  })

  it('renders Revoked as Yes/No and a Never fallback for an unset Last Seen', () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'webserver', revoked: true, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    const cells = wrapper.findAll('tbody td')
    expect(cells[1].text()).toBe('Yes')
    expect(cells[2].text()).toBe('Never')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('shows an empty-state message when there are no clients', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.text()).toContain('No clients enrolled yet.')
  })

  it('links to the enroll form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'New Client')
    expect(link.props('to')).toEqual({ name: 'client-new' })
  })

  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Clients')
  })

  it('renders the Revoked column as a red badge when the client is revoked', () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'legacy', revoked: true, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    const revokedCell = wrapper.findAll('tbody td')[1]
    expect(revokedCell.find('span').classes()).toContain('bg-red-50')
  })

  it('renders the Revoked column as a green badge when the client is not revoked', () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'active-host', revoked: false, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    const revokedCell = wrapper.findAll('tbody td')[1]
    expect(revokedCell.find('span').classes()).toContain('bg-emerald-50')
  })
})
