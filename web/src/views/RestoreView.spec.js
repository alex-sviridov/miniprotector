import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RestoreView from './RestoreView.vue'

function mountView(rules) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { restoreCart: { rules } } })
  return mount(RestoreView, { global: { plugins: [pinia] } })
}

describe('RestoreView', () => {
  it('shows the empty state when the cart has no selections', () => {
    const wrapper = mountView([])
    expect(wrapper.text()).toContain('No files selected for restore yet.')
  })

  it('lists a folder wildcard rule as path/*', () => {
    const wrapper = mountView([{ path: '/var', host: null, include: true }])
    expect(wrapper.text()).toContain('/var/*')
  })

  it('lists a file rule as path (host)', () => {
    const wrapper = mountView([{ path: '/etc/hosts', host: 'web01', include: true }])
    expect(wrapper.text()).toContain('/etc/hosts (web01)')
  })

  it('omits exception (include: false) rules from the list', () => {
    const wrapper = mountView([
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ])
    expect(wrapper.text()).toContain('/etc/*')
    expect(wrapper.text()).not.toContain('/etc/hosts')
  })

  it('renders the page breadcrumb', () => {
    const wrapper = mountView([])
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Restore')
  })
})
