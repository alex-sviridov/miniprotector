import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import PolicyFormView from './PolicyFormView.vue'
import { usePoliciesStore } from '../stores/policies'

const push = vi.fn()
let routeParams = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: routeParams }),
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { policies: state } })
  const wrapper = mount(PolicyFormView, { global: { plugins: [pinia] } })
  return { wrapper, policies: usePoliciesStore() }
}

describe('PolicyFormView', () => {
  afterEach(() => {
    push.mockReset()
    routeParams = {}
  })

  describe('create mode', () => {
    it('does not fetch an existing policy on mount', () => {
      routeParams = {}
      const { policies } = mountView({ error: null })
      expect(policies.fetchOne).not.toHaveBeenCalled()
    })

    it('submits a create request with the entered core fields', async () => {
      routeParams = {}
      const { wrapper, policies } = mountView({ error: null })
      policies.create.mockResolvedValue({ id: 'p9' })

      await wrapper.find('input[name="name"]').setValue('nightly-db-backup')
      await wrapper.find('input[name="rpo"]').setValue('1h')
      await wrapper.find('input[name="destination"]').setValue('store:8080')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.create).toHaveBeenCalledWith({
        name: 'nightly-db-backup',
        client_filters: { hostnames: [], labels: {} },
        object_filters: [],
        rpo: '1h',
        backup_window: [],
        destination: 'store:8080',
      })
      expect(push).toHaveBeenCalledWith('/policies/p9')
    })
  })

  describe('edit mode', () => {
    it('fetches and pre-populates the existing policy on mount', async () => {
      routeParams = { id: 'p1' }
      const { wrapper, policies } = mountView({ error: null })
      policies.fetchOne.mockResolvedValue({
        id: 'p1',
        name: 'nightly-db-backup',
        rpo: '1h',
        destination: 'store:8080',
        client_filters: { hostnames: [], labels: {} },
        object_filters: [],
        backup_window: [],
      })

      await wrapper.vm.$nextTick()
      await Promise.resolve()
      await wrapper.vm.$nextTick()

      expect(policies.fetchOne).toHaveBeenCalledWith('p1')
      expect(wrapper.find('input[name="name"]').element.value).toBe('nightly-db-backup')
      expect(wrapper.find('input[name="rpo"]').element.value).toBe('1h')
    })

    it('submits an update request addressed by the route id', async () => {
      routeParams = { id: 'p1' }
      const { wrapper, policies } = mountView({ error: null })
      policies.fetchOne.mockResolvedValue({
        id: 'p1',
        name: 'nightly-db-backup',
        rpo: '1h',
        destination: 'store:8080',
        client_filters: { hostnames: [], labels: {} },
        object_filters: [],
        backup_window: [],
      })
      policies.update.mockResolvedValue({ id: 'p1' })

      await wrapper.vm.$nextTick()
      await Promise.resolve()
      await wrapper.vm.$nextTick()

      await wrapper.find('input[name="rpo"]').setValue('2h')
      await wrapper.find('form').trigger('submit')
      await Promise.resolve()

      expect(policies.update).toHaveBeenCalledWith('p1', {
        name: 'nightly-db-backup',
        client_filters: { hostnames: [], labels: {} },
        object_filters: [],
        rpo: '2h',
        backup_window: [],
        destination: 'store:8080',
      })
      expect(push).toHaveBeenCalledWith('/policies/p1')
    })
  })

  it('shows the store error message and keeps entered values on submit failure', async () => {
    routeParams = {}
    const { wrapper, policies } = mountView({ error: null })
    policies.create.mockRejectedValue(new Error('name is required'))

    await wrapper.find('input[name="name"]').setValue('bad-policy')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()
    policies.error = 'name is required'
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('name is required')
    expect(wrapper.find('input[name="name"]').element.value).toBe('bad-policy')
    expect(push).not.toHaveBeenCalled()
  })
})
