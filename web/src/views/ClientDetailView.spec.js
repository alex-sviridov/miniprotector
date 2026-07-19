import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientDetailView from './ClientDetailView.vue'
import { useClientsStore } from '../stores/clients'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { hostname: 'webserver' } }),
}))

function baseClient(overrides = {}) {
  return {
    hostname: 'webserver',
    revoked: false,
    revoked_at: 0,
    last_seen_at: 123,
    sans: null,
    attributes: null,
    descriptions: null,
    ...overrides,
  }
}

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientDetailView, { global: { plugins: [pinia] } })
  return { wrapper, clients: useClientsStore() }
}

describe('ClientDetailView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('calls fetchOne with the route hostname on mount', () => {
    const { clients } = mountView({ byHostname: {}, loading: false, error: null, pendingToken: null })
    expect(clients.fetchOne).toHaveBeenCalledWith('webserver')
  })

  it('renders the cached client record', () => {
    const { wrapper } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: null,
    })
    expect(wrapper.text()).toContain('webserver')
    expect(wrapper.text()).toContain('No')
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byHostname: {}, loading: false, error: 'client not found', pendingToken: null })
    expect(wrapper.text()).toContain('client not found')
  })

  it('shows a Revoke button for a non-revoked client, and calls revoke after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ revoked: false }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    expect(wrapper.find('[data-test="revoke-button"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="unrevoke-button"]').exists()).toBe(false)

    await wrapper.find('[data-test="revoke-button"]').trigger('click')

    expect(clients.revoke).toHaveBeenCalledWith('webserver')
  })

  it('does not call revoke when the confirm dialog is dismissed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ revoked: false }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    await wrapper.find('[data-test="revoke-button"]').trigger('click')

    expect(clients.revoke).not.toHaveBeenCalled()
  })

  it('shows an Unrevoke button for a revoked client, and calls unrevoke after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ revoked: true }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    expect(wrapper.find('[data-test="unrevoke-button"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="revoke-button"]').exists()).toBe(false)

    await wrapper.find('[data-test="unrevoke-button"]').trigger('click')

    expect(clients.unrevoke).toHaveBeenCalledWith('webserver')
  })

  it('calls reenroll when Re-enroll is clicked', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: null,
    })
    clients.reenroll.mockResolvedValue({ hostname: 'webserver', token: 'tok-fresh' })

    await wrapper.find('[data-test="reenroll-button"]').trigger('click')

    expect(clients.reenroll).toHaveBeenCalledWith('webserver')
  })

  it('shows the token banner when pendingToken matches the route hostname on mount, and clears it', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: { hostname: 'webserver', token: 'tok-abc' },
    })
    await flushPromises()

    expect(wrapper.find('[data-test="token-banner"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="token-value"]').text()).toBe('tok-abc')
    expect(clients.pendingToken).toBeNull()
  })

  it('does not show the token banner when pendingToken is for a different hostname', async () => {
    const { wrapper } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: { hostname: 'other-host', token: 'tok-abc' },
    })
    await flushPromises()

    expect(wrapper.find('[data-test="token-banner"]').exists()).toBe(false)
  })

  it('does not show the token banner when pendingToken is null', async () => {
    const { wrapper } = mountView({
      byHostname: { webserver: baseClient() },
      loading: false,
      error: null,
      pendingToken: null,
    })
    await flushPromises()

    expect(wrapper.find('[data-test="token-banner"]').exists()).toBe(false)
  })

  it('calls updateDescription with the KeyValueEditor save payload', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ descriptions: { owner: 'alice' } }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    await wrapper.find('[data-test="description-value-input"]').setValue('bob')
    await wrapper.find('[data-test="description-update"]').trigger('click')

    expect(clients.updateDescription).toHaveBeenCalledWith('webserver', { owner: 'bob' }, [])
  })

  it('calls updateAttributes with the KeyValueEditor save payload', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ attributes: { role: 'db' } }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    await wrapper.find('[data-test="attribute-value-input"]').setValue('web')
    await wrapper.find('[data-test="attribute-update"]').trigger('click')

    expect(clients.updateAttributes).toHaveBeenCalledWith('webserver', { role: 'web' }, [])
  })

  it('calls updateSans with the SanListEditor save payload', async () => {
    const { wrapper, clients } = mountView({
      byHostname: { webserver: baseClient({ sans: ['old.internal'] }) },
      loading: false,
      error: null,
      pendingToken: null,
    })

    await wrapper.find('[data-test="san-add"]').trigger('click')
    const inputs = wrapper.findAll('[data-test="san-input"]')
    await inputs[1].setValue('new.internal')
    await wrapper.find('[data-test="san-update"]').trigger('click')

    expect(clients.updateSans).toHaveBeenCalledWith('webserver', ['new.internal'], [])
  })
})
