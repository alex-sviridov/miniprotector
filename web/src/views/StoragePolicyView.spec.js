// web/src/views/StoragePolicyView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import StoragePolicyView from './StoragePolicyView.vue'
import { useStoragePoliciesStore } from '../stores/storagePolicies'

const push = vi.fn()
const replace = vi.fn()
let routeQuery = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 's1' }, query: routeQuery }),
  useRouter: () => ({ push, replace }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { storagePolicies: state } })
  const wrapper = mount(StoragePolicyView, { global: { plugins: [pinia] } })
  return { wrapper, storagePolicies: useStoragePoliciesStore() }
}

describe('StoragePolicyView', () => {
  afterEach(() => {
    push.mockReset()
    replace.mockReset()
    routeQuery = {}
    vi.restoreAllMocks()
  })

  it('calls fetchOne with the route id on mount', () => {
    const { storagePolicies } = mountView({ byId: {}, loading: false, error: null })
    expect(storagePolicies.fetchOne).toHaveBeenCalledWith('s1')
  })

  it('renders the cached storage policy record', () => {
    const { wrapper } = mountView({
      byId: {
        s1: {
          id: 's1',
          name: 'east-1-storage',
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage"}',
          client_filters: { hostnames: ['storage-east-1.internal'], labels: {} },
          checkins: [],
        },
      },
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('east-1-storage')
    expect(wrapper.text()).toContain('storage-east-1.internal')
    expect(wrapper.text()).toContain('9400')
    expect(wrapper.text()).toContain('filesystem')
    expect(wrapper.text()).toContain('/data/storage')
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byId: {}, loading: false, error: 'policy not found' })
    expect(wrapper.text()).toContain('policy not found')
  })

  it('shows both tab buttons once the policy has loaded', () => {
    const { wrapper } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {}, checkins: [] } },
      loading: false,
      error: null,
    })
    expect(wrapper.find('[data-test="tab-details"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="tab-checkins"]').exists()).toBe(true)
  })

  it('renders check-ins and wires Refresh to the store on the Checkins tab', async () => {
    routeQuery = { tab: 'checkins' }
    const { wrapper, storagePolicies } = mountView({
      byId: {
        s1: {
          id: 's1',
          name: 'east-1-storage',
          config: '{}',
          client_filters: {},
          checkins: [{ hostname: 'storage-east-1', last_seen_at: 1752400000 }],
        },
      },
      loading: false,
      error: null,
      checkinsLoading: false,
      checkinsError: null,
    })
    expect(wrapper.text()).toContain('storage-east-1')

    await wrapper.find('[data-test="checkins-refresh"]').trigger('click')
    expect(storagePolicies.refresh).toHaveBeenCalledWith('s1')
  })

  it('does not throw an unhandled rejection when a manual refresh fails', async () => {
    routeQuery = { tab: 'checkins' }
    const { wrapper, storagePolicies } = mountView({
      byId: {
        s1: {
          id: 's1',
          name: 'east-1-storage',
          config: '{}',
          client_filters: {},
          checkins: [],
        },
      },
      loading: false,
      error: null,
      checkinsLoading: false,
      checkinsError: null,
    })
    storagePolicies.refresh.mockRejectedValue(new Error('boom'))

    await expect(wrapper.find('[data-test="checkins-refresh"]').trigger('click')).resolves.not.toThrow()
    expect(storagePolicies.refresh).toHaveBeenCalledWith('s1')
  })

  it('deletes the policy after confirming and navigates to the storage list', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, storagePolicies } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    storagePolicies.remove.mockResolvedValue(undefined)

    await wrapper.find('[data-test="storage-policy-delete"]').trigger('click')
    await Promise.resolve()

    expect(storagePolicies.remove).toHaveBeenCalledWith('s1')
    expect(push).toHaveBeenCalledWith({ name: 'storage' })
  })

  it('opens the edit modal pre-filled with the policy when Edit is clicked', async () => {
    const policy = { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} }
    const { wrapper } = mountView({ byId: { s1: policy }, loading: false, error: null })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'StorageEditModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('policy')).toEqual(policy)
  })

  it('calls update and closes the modal on save', async () => {
    const { wrapper, storagePolicies } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    storagePolicies.update.mockResolvedValue({ id: 's1', name: 'renamed' })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')

    const payload = { name: 'renamed', port: 9400, config: '{}', client_filters: { hostnames: [], labels: {} } }
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(storagePolicies.update).toHaveBeenCalledWith('s1', payload)
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })

  it('refreshes check-ins from the server after a successful save', async () => {
    const { wrapper, storagePolicies } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    storagePolicies.update.mockResolvedValue({ id: 's1', name: 'renamed' })
    storagePolicies.refresh.mockResolvedValue({ id: 's1', name: 'renamed', checkins: [] })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')

    const payload = { name: 'renamed', port: 9400, config: '{}', client_filters: { hostnames: [], labels: {} } }
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(storagePolicies.refresh).toHaveBeenCalledWith('s1')
  })

  it('keeps the modal open and shows the server error when update fails', async () => {
    const { wrapper, storagePolicies } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    storagePolicies.update.mockImplementation(async () => {
      storagePolicies.error = 'port must be between 1 and 65535'
      throw new Error('port must be between 1 and 65535')
    })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')

    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', { name: 'bad' })
    await nextTick()

    const modal = wrapper.findComponent({ name: 'StorageEditModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('port must be between 1 and 65535')
  })

  it('closes the modal without saving on close', async () => {
    const { wrapper } = mountView({
      byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
      loading: false,
      error: null,
    })
    await wrapper.find('[data-test="storage-policy-edit"]').trigger('click')
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })
})
