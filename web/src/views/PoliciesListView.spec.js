import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import PoliciesListView from './PoliciesListView.vue'
import { usePoliciesStore } from '../stores/policies'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(PoliciesListView, {
    global: {
      plugins: [pinia],
      stubs: { RouterLink: { template: '<a :href="to"><slot /></a>', props: ['to'] } },
    },
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
      list: [{ id: 'p1', name: 'nightly-db-backup' }],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('nightly-db-backup')
    expect(wrapper.find('a[href="/policies/p1"]').exists()).toBe(true)
  })

  it('links to the create form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('a[href="/policies/new"]').exists()).toBe(true)
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('deletes a policy after confirming', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const { wrapper, policies } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup' }],
      loading: false,
      error: null,
    })

    await wrapper.find('button').trigger('click')

    expect(policies.remove).toHaveBeenCalledWith('p1')
  })

  it('does not delete when the confirm dialog is dismissed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const { wrapper, policies } = mountView({
      list: [{ id: 'p1', name: 'nightly-db-backup' }],
      loading: false,
      error: null,
    })

    await wrapper.find('button').trigger('click')

    expect(policies.remove).not.toHaveBeenCalled()
  })
})
