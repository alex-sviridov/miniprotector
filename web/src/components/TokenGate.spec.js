import { describe, it, expect, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { useAuthStore } from '../stores/auth'
import TokenGate from './TokenGate.vue'

describe('TokenGate', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('renders the token form when no token is stored', () => {
    const wrapper = mount(TokenGate)
    expect(wrapper.find('form').exists()).toBe(true)
  })

  it('hides the token form once a token is set', () => {
    const auth = useAuthStore()
    auth.setToken('secret')
    const wrapper = mount(TokenGate)
    expect(wrapper.find('form').exists()).toBe(false)
  })

  it('submitting the form stores the entered token', async () => {
    const auth = useAuthStore()
    const wrapper = mount(TokenGate)
    await wrapper.find('input').setValue('typed-token')
    await wrapper.find('form').trigger('submit.prevent')
    expect(auth.token).toBe('typed-token')
  })
})
