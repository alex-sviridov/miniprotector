import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import PoliciesListView from './PoliciesListView.vue'
import { usePoliciesStore } from '../stores/policies'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(PoliciesListView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
  })
  return { wrapper, policies: usePoliciesStore() }
}

describe('PoliciesListView', () => {
  afterEach(() => {
    vi.restoreAllMocks()
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

  it('links to the create form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'New Policy')
    expect(link.props('to')).toEqual({ name: 'policy-new' })
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
})
