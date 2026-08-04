import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import StorageView from './StorageView.vue'
import { useStoragePoliciesStore } from '../stores/storagePolicies'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { storagePolicies: state } })
  const wrapper = mount(StorageView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
  })
  return { wrapper, storagePolicies: useStoragePoliciesStore() }
}

describe('StorageView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('calls fetchAll on mount', () => {
    const { storagePolicies } = mountView({ list: [], loading: false, error: null })
    expect(storagePolicies.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('renders each storage policy in the table', () => {
    const { wrapper } = mountView({
      list: [
        {
          id: 's1',
          name: 'east-1-storage',
          client_filters: { hostnames: ['storage-east-1.internal'], labels: {} },
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage"}',
        },
      ],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('east-1-storage')
    expect(wrapper.text()).toContain('storage-east-1.internal')
    expect(wrapper.text()).toContain('9400')
    expect(wrapper.text()).toContain('filesystem')
  })

  it('shows an empty-state message when there are no storage policies', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.text()).toContain('No storage policies defined yet.')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('opens the modal in create mode when "New Storage Policy" is clicked', async () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    await wrapper.find('[data-test="storage-new"]').trigger('click')
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).props('policy')).toBeNull()
  })

  it("links each policy's name to its detail page", () => {
    const { wrapper } = mountView({
      list: [{ id: 's1', name: 'east-1-storage', port: 9400, config: '{}' }],
      loading: false,
      error: null,
    })
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'east-1-storage')
    expect(link.props('to')).toEqual({ name: 'storage-detail', params: { id: 's1' } })
  })

  it('calls create and closes the modal on save in create mode', async () => {
    const { wrapper, storagePolicies } = mountView({ list: [], loading: false, error: null })
    storagePolicies.create.mockResolvedValue({ id: 's2', name: 'new-storage' })
    await wrapper.find('[data-test="storage-new"]').trigger('click')

    const payload = { name: 'new-storage', port: 1, config: '{}', client_filters: { hostnames: [], labels: {} } }
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(storagePolicies.create).toHaveBeenCalledWith(payload)
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })

  it('keeps the modal open and shows the server error when create fails', async () => {
    const { wrapper, storagePolicies } = mountView({ list: [], loading: false, error: null })
    // Mirrors withRequest: the store records the error before the rejection
    // propagates to the caller.
    storagePolicies.create.mockImplementation(async () => {
      storagePolicies.error = 'name must contain alphanumeric characters'
      throw new Error('name must contain alphanumeric characters')
    })
    await wrapper.find('[data-test="storage-new"]').trigger('click')

    const payload = { name: '***', port: 1, config: '{}', client_filters: { hostnames: [], labels: {} } }
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(storagePolicies.create).toHaveBeenCalledWith(payload)
    const modal = wrapper.findComponent({ name: 'StorageEditModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('name must contain alphanumeric characters')
    expect(wrapper.text()).toContain('name must contain alphanumeric characters')
  })

  it('closes the modal without saving on close', async () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    await wrapper.find('[data-test="storage-new"]').trigger('click')
    await wrapper.findComponent({ name: 'StorageEditModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'StorageEditModal' }).exists()).toBe(false)
  })

  it('deletes a storage policy after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, storagePolicies } = mountView({
      list: [{ id: 's1', name: 'east-1-storage', port: 9400, config: '{}' }],
      loading: false,
      error: null,
    })

    await wrapper.find('[data-test="storage-delete-s1"]').trigger('click')

    expect(storagePolicies.remove).toHaveBeenCalledWith('s1')
  })

  it('does not delete when the confirm dialog is dismissed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { wrapper, storagePolicies } = mountView({
      list: [{ id: 's1', name: 'east-1-storage', port: 9400, config: '{}' }],
      loading: false,
      error: null,
    })

    await wrapper.find('[data-test="storage-delete-s1"]').trigger('click')

    expect(storagePolicies.remove).not.toHaveBeenCalled()
  })

  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Storage')
  })
})
