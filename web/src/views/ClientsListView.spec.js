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

  it('shows a bootstrap cert badge per expiry state', () => {
    const now = Math.floor(Date.now() / 1000)
    const day = 86400
    const { wrapper } = mountView({
      list: [
        { hostname: 'fresh', revoked: false, last_seen_at: 0, bootstrap_not_after: now + 80 * day },
        { hostname: 'soon', revoked: false, last_seen_at: 0, bootstrap_not_after: now + 20 * day },
        { hostname: 'critical', revoked: false, last_seen_at: 0, bootstrap_not_after: now + 3 * day },
        { hostname: 'gone', revoked: false, last_seen_at: 0, bootstrap_not_after: now - day },
        { hostname: 'new', revoked: false, last_seen_at: 0 },
      ],
      loading: false,
      error: null,
    })
    const badges = wrapper.findAll('[data-test="bootstrap-cert-badge"]')
    expect(badges.map((b) => b.text())).toEqual(['79d left', '19d left', '2d left', 'Expired', '—'])
    expect(badges[0].classes()).toContain('bg-emerald-50')
    expect(badges[1].classes()).toContain('bg-amber-50')
    expect(badges[2].classes()).toContain('bg-red-50')
    expect(badges[3].classes()).toContain('bg-red-50')
    expect(badges[4].classes()).toContain('bg-gray-100')
  })
})
