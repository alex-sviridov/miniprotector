// web/src/views/PolicyDetailView.spec.js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import PolicyDetailView from './PolicyDetailView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: 'p1' } }),
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(PolicyDetailView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
  })
  return { wrapper, policies: usePoliciesStore() }
}

describe('PolicyDetailView', () => {
  afterEach(() => {
    push.mockReset()
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
    const editLink = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'Edit')
    expect(editLink.props('to')).toEqual({ name: 'policy-edit', params: { id: 'p1' } })
  })

  it('shows the store error message on a 404', () => {
    const { wrapper } = mountView({ byId: {}, loading: false, error: 'policy not found' })
    expect(wrapper.text()).toContain('policy not found')
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
})
