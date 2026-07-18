import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientDetailView from './ClientDetailView.vue'
import { useClientsStore } from '../stores/clients'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { hostname: 'webserver' } }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientDetailView, { global: { plugins: [pinia] } })
  return { wrapper, clients: useClientsStore() }
}

describe('ClientDetailView', () => {
  it('calls fetchOne with the route hostname on mount', () => {
    const { clients } = mountView({ byHostname: {}, loading: false, error: null })
    expect(clients.fetchOne).toHaveBeenCalledWith('webserver')
  })

  it('renders the cached client record', () => {
    const { wrapper } = mountView({
      byHostname: {
        webserver: {
          hostname: 'webserver',
          revoked: false,
          revoked_at: 0,
          last_seen_at: 123,
          sans: null,
          attributes: null,
          descriptions: null,
        },
      },
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('webserver')
    expect(wrapper.text()).toContain('No')
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byHostname: {}, loading: false, error: 'client not found' })
    expect(wrapper.text()).toContain('client not found')
  })
})
