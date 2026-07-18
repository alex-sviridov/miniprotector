import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import App from './App.vue'

function mountApp(token) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { auth: { token } } })
  return mount(App, {
    global: {
      plugins: [pinia],
      stubs: { RouterView: true, RouterLink: true },
    },
  })
}

describe('App', () => {
  it('shows only the token gate when unauthenticated', () => {
    const wrapper = mountApp(null)
    expect(wrapper.find('form').exists()).toBe(true)
    expect(wrapper.find('nav').exists()).toBe(false)
  })

  it('shows the sidebar and content once authenticated', () => {
    const wrapper = mountApp('secret')
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.find('nav').exists()).toBe(true)
  })
})
