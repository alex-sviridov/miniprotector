// web/src/views/BackupPolicyView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import BackupPolicyView from './BackupPolicyView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()
const replace = vi.fn()
let routeQuery = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 'p1' }, query: routeQuery }),
  useRouter: () => ({ push, replace }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(BackupPolicyView, { global: { plugins: [pinia] } })
  return { wrapper, policies: usePoliciesStore() }
}

describe('BackupPolicyView', () => {
  afterEach(() => {
    push.mockReset()
    replace.mockReset()
    routeQuery = {}
    vi.restoreAllMocks()
  })

  it('calls fetchOne with the route id on mount', () => {
    const { policies } = mountView({ byId: {}, loading: false, error: null })
    expect(policies.fetchOne).toHaveBeenCalledWith('p1')
  })

  it('renders the cached policy record', () => {
    const { wrapper } = mountView({
      byId: {
        p1: {
          id: 'p1',
          name: 'nightly-db-backup',
          rpo: '1h',
          destination: 'store:8080',
          client_filters: { hostnames: ['database'], labels: {} },
          object_filters: [{ id: 'f1', path: '/var/lib/dbdata', include: [], exclude: [] }],
          backup_window: ['0 * * * *'],
        },
      },
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('nightly-db-backup')
    expect(wrapper.text()).toContain('1h')
    expect(wrapper.text()).toContain('/var/lib/dbdata')
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byId: {}, loading: false, error: 'policy not found' })
    expect(wrapper.text()).toContain('policy not found')
  })

  it('hides the Edit button while the policy has not loaded yet', () => {
    const { wrapper } = mountView({ byId: {}, loading: true, error: null })
    expect(wrapper.find('[data-test="policy-edit"]').exists()).toBe(false)
  })

  it('shows the Edit button once the policy has loaded', () => {
    const { wrapper } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    expect(wrapper.find('[data-test="policy-edit"]').exists()).toBe(true)
  })

  it('deletes the policy after confirming and navigates to the list', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.remove.mockResolvedValue(undefined)

    await wrapper.find('[data-test="policy-delete"]').trigger('click')
    await Promise.resolve()

    expect(policies.remove).toHaveBeenCalledWith('p1')
    expect(push).toHaveBeenCalledWith({ name: 'policies' })
  })

  it('opens the modal in edit mode when Edit is clicked', async () => {
    const policy = { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} }
    const { wrapper } = mountView({ byId: { p1: policy }, loading: false, error: null })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('policy')).toEqual(policy)
  })

  it('calls update and closes the modal on save', async () => {
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.update.mockResolvedValue({ id: 'p1', name: 'renamed' })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')

    const payload = {
      name: 'renamed',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      storage_policy_id: 'sp1',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(policies.update).toHaveBeenCalledWith('p1', payload)
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
  })

  it('refreshes check-ins from the server after a successful save', async () => {
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.update.mockResolvedValue({ id: 'p1', name: 'renamed' })
    policies.refresh.mockResolvedValue({ id: 'p1', name: 'renamed', checkins: [] })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')

    const payload = {
      name: 'renamed',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      storage_policy_id: 'sp1',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', payload)
    await nextTick()

    expect(policies.refresh).toHaveBeenCalledWith('p1')
  })

  it('keeps the modal open and shows the server error when update fails', async () => {
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.update.mockImplementation(async () => {
      policies.error = 'invalid glob pattern'
      throw new Error('invalid glob pattern')
    })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')

    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('save', { name: 'bad' })
    await nextTick()

    const modal = wrapper.findComponent({ name: 'BackupPolicyFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('invalid glob pattern')
  })

  it('calls runAdhoc, closes the modal, and navigates to jobs on run-now', async () => {
    const { wrapper, policies } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    policies.runAdhoc.mockResolvedValue({ id: 'adhoc1' })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')

    const payload = {
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      storage_policy_id: 'sp1',
    }
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('run-now', payload)
    await nextTick()

    expect(policies.runAdhoc).toHaveBeenCalledWith(payload)
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
    expect(push).toHaveBeenCalledWith({ name: 'jobs' })
  })

  it('closes the modal without saving on close', async () => {
    const { wrapper } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
      loading: false,
      error: null,
    })
    await wrapper.find('[data-test="policy-edit"]').trigger('click')
    await wrapper.findComponent({ name: 'BackupPolicyFormModal' }).vm.$emit('close')
    expect(wrapper.findComponent({ name: 'BackupPolicyFormModal' }).exists()).toBe(false)
  })

  it('shows both tab buttons once the policy has loaded', () => {
    const { wrapper } = mountView({
      byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {}, checkins: [] } },
      loading: false,
      error: null,
    })
    expect(wrapper.find('[data-test="tab-details"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="tab-checkins"]').exists()).toBe(true)
  })

  it('renders check-ins and wires Refresh to the store on the Checkins tab', async () => {
    routeQuery = { tab: 'checkins' }
    const { wrapper, policies } = mountView({
      byId: {
        p1: {
          id: 'p1',
          name: 'nightly-db-backup',
          object_filters: [],
          client_filters: {},
          checkins: [{ hostname: 'web-01', last_seen_at: 1752400000 }],
        },
      },
      loading: false,
      error: null,
      checkinsLoading: false,
      checkinsError: null,
    })
    expect(wrapper.text()).toContain('web-01')

    await wrapper.find('[data-test="checkins-refresh"]').trigger('click')
    expect(policies.refresh).toHaveBeenCalledWith('p1')
  })

  it('does not throw an unhandled rejection when a manual refresh fails', async () => {
    routeQuery = { tab: 'checkins' }
    const { wrapper, policies } = mountView({
      byId: {
        p1: {
          id: 'p1',
          name: 'nightly-db-backup',
          object_filters: [],
          client_filters: {},
          checkins: [],
        },
      },
      loading: false,
      error: null,
      checkinsLoading: false,
      checkinsError: null,
    })
    policies.refresh.mockRejectedValue(new Error('boom'))

    await expect(wrapper.find('[data-test="checkins-refresh"]').trigger('click')).resolves.not.toThrow()
    expect(policies.refresh).toHaveBeenCalledWith('p1')
  })
})
