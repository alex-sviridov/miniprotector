import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import Sidebar from './Sidebar.vue'

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/clients', name: 'clients', component: { template: '<div />' } },
      { path: '/catalog', name: 'catalog', component: { template: '<div />' } },
      { path: '/policies', name: 'policies', component: { template: '<div />' } },
      { path: '/storage', name: 'storage', component: { template: '<div />' } },
      { path: '/jobs', name: 'jobs', component: { template: '<div />' } },
    ],
  })
}

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

  it('marks the current route\'s nav link active and leaves the others inactive', async () => {
    const router = makeRouter()
    router.push({ name: 'policies' })
    await router.isReady()

    const wrapper = mount(Sidebar, { global: { plugins: [router], stubs: { RouterLink: false } } })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links).toHaveLength(5)

    const activeLink = links[2]
    expect(activeLink.text()).toContain('Policies')
    expect(activeLink.classes()).toEqual(
      expect.arrayContaining(['bg-slate-800', 'text-white', 'border-l-4', 'border-blue-500', 'pl-2']),
    )
    expect(activeLink.classes()).not.toContain('pl-3')

    const inactiveLinks = [links[0], links[1], links[3], links[4]]
    inactiveLinks.forEach((link) => {
      expect(link.classes()).toEqual(expect.arrayContaining(['text-slate-300', 'pl-3']))
      expect(link.classes()).not.toContain('bg-slate-800')
      expect(link.classes()).not.toContain('border-l-4')
      expect(link.classes()).not.toContain('pl-2')
    })
  })
})
