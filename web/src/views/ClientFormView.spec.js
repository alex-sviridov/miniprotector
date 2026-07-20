import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientFormView from './ClientFormView.vue'
import { useClientsStore } from '../stores/clients'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientFormView, { global: { plugins: [pinia] } })
  return { wrapper, clients: useClientsStore() }
}

describe('ClientFormView', () => {
  afterEach(() => {
    push.mockReset()
  })

  it('submits an enroll request with the entered hostname and navigates to the new detail page', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })

    await wrapper.find('input[name="hostname"]').setValue('node-1')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()

    expect(clients.enroll).toHaveBeenCalledWith('node-1', [])
    expect(push).toHaveBeenCalledWith({ name: 'client-detail', params: { hostname: 'node-1' } })
  })

  it('adds and removes SAN rows, sending only non-empty trimmed values', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })

    await wrapper.find('[data-test="san-add"]').trigger('click')
    await wrapper.find('[data-test="san-add"]').trigger('click')
    const sanInputs = wrapper.findAll('[data-test="san-input"]')
    await sanInputs[0].setValue('alias.internal')
    await sanInputs[1].setValue('  ')
    await wrapper.find('input[name="hostname"]').setValue('node-1')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()

    expect(clients.enroll).toHaveBeenCalledWith('node-1', ['alias.internal'])
  })

  it('removes a SAN row via its remove button', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockResolvedValue({ hostname: 'node-1', token: 'tok-abc' })

    await wrapper.find('[data-test="san-add"]').trigger('click')
    await wrapper.find('[data-test="san-input"]').setValue('alias.internal')
    await wrapper.find('[data-test="san-remove"]').trigger('click')
    await wrapper.find('input[name="hostname"]').setValue('node-1')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()

    expect(clients.enroll).toHaveBeenCalledWith('node-1', [])
  })

  it('shows the store error message and keeps the entered hostname on submit failure', async () => {
    const { wrapper, clients } = mountView({ error: null })
    clients.enroll.mockRejectedValue(new Error('client node-1 already enrolled'))

    await wrapper.find('input[name="hostname"]').setValue('node-1')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()
    clients.error = 'client node-1 already enrolled'
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('client node-1 already enrolled')
    expect(wrapper.find('input[name="hostname"]').element.value).toBe('node-1')
    expect(push).not.toHaveBeenCalled()
  })
})
