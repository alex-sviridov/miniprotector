import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import BackupPoliciesView from './BackupPoliciesView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(BackupPoliciesView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
  })
  return { wrapper, policies: usePoliciesStore() }
}

describe('BackupPoliciesView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    push.mockReset()
  })

  it('calls fetchAll on mount', () => {
    const { policies } = mountView({ list: [], loading: false, error: null })
    expect(policies.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('renders each policy with a link to its detail page', () => {
    const { wrapper } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup', rpo: '1h', destination: 'store:8080' }],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('nightly-db-backup')
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'nightly-db-backup')
    expect(link.props('to')).toEqual({ name: 'policy-detail', params: { id: 'p1' } })
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('shows an empty-state message when there are no policies', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.text()).toContain('No policies defined yet.')
  })

  it('deletes a policy after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, policies } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup', rpo: '1h', destination: 'store:8080' }],
      loading: false,
      error: null,
    })
    await wrapper.find('[data-test="policy-delete"]').trigger('click')
    expect(policies.remove).toHaveBeenCalledWith('p1')
  })

  it('does not delete when the confirm dialog is dismissed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { wrapper, policies } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup', rpo: '1h', destination: 'store:8080' }],
      loading: false,
      error: null,
    })
    await wrapper.find('[data-test="policy-delete"]').trigger('click')
    expect(policies.remove).not.toHaveBeenCalled()
  })

  it('opens the modal in create mode when "New backup" is clicked', async () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    await wrapper.find('[data-test="policy-new"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('policy')).toBeNull()
  })

  it('calls create, closes the modal, and navigates to the detail page on save', async () => {
    const { wrapper, policies } = mountView({ list: [], loading: false, error: null })
    policies.create.mockResolvedValue({ id: 'p9', name: 'nightly-db-backup' })
    await wrapper.find('[data-test="policy-new"]').trigger('click')

    const payload = {
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(policies.create).toHaveBeenCalledWith(payload)
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
    expect(push).toHaveBeenCalledWith({ name: 'policy-detail', params: { id: 'p9' } })
  })

  it('keeps the modal open and shows the server error when create fails', async () => {
    const { wrapper, policies } = mountView({ list: [], loading: false, error: null })
    policies.create.mockImplementation(async () => {
      policies.error = 'name is required'
      throw new Error('name is required')
    })
    await wrapper.find('[data-test="policy-new"]').trigger('click')

    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', { name: '' })
    await nextTick()

    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('name is required')
  })

  it('closes the modal without saving on close', async () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    await wrapper.find('[data-test="policy-new"]').trigger('click')
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
  })

  it('calls runAdhoc, closes the modal, and navigates to jobs on run-now', async () => {
    const { wrapper, policies } = mountView({ list: [], loading: false, error: null })
    policies.runAdhoc.mockResolvedValue({ id: 'adhoc1' })
    await wrapper.find('[data-test="policy-new"]').trigger('click')

    const payload = {
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '',
      backup_window: [],
      destination: 'store:8080',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('run-now', payload)
    await nextTick()

    expect(policies.runAdhoc).toHaveBeenCalledWith(payload)
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
    expect(push).toHaveBeenCalledWith({ name: 'jobs' })
  })

  it('keeps the modal open and shows the server error when run-now fails', async () => {
    const { wrapper, policies } = mountView({ list: [], loading: false, error: null })
    policies.runAdhoc.mockImplementation(async () => {
      policies.error = 'destination is required'
      throw new Error('destination is required')
    })
    await wrapper.find('[data-test="policy-new"]').trigger('click')

    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('run-now', { name: 'oneoff' })
    await nextTick()

    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('destination is required')
    expect(push).not.toHaveBeenCalledWith({ name: 'jobs' })
  })
})
