import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import Sidebar from './Sidebar.vue'

describe('Sidebar', () => {
  it('links to each top-level named route', () => {
    const wrapper = mount(Sidebar, { global: { stubs: { RouterLink: RouterLinkStub } } })
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links.map((l) => l.props('to'))).toEqual([
      { name: 'clients' },
      { name: 'catalog' },
      { name: 'policies' },
      { name: 'storage' },
      { name: 'jobs' },
    ])
  })

  it('renders a brand header and an icon before each nav label', () => {
    const wrapper = mount(Sidebar, { global: { stubs: { RouterLink: RouterLinkStub } } })
    expect(wrapper.text()).toContain('Miniprotector')
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links).toHaveLength(5)
    links.forEach((link) => {
      expect(link.find('svg').exists()).toBe(true)
    })
  })
})
