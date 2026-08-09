import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { createTestingPinia } from '@pinia/testing'
import Sidebar from './Sidebar.vue'

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/clients', name: 'clients', component: { template: '<div />' } },
      { path: '/catalog', name: 'catalog', component: { template: '<div />' } },
      { path: '/restore', name: 'restore', component: { template: '<div />' } },
      { path: '/policies', name: 'policies', component: { template: '<div />' } },
      { path: '/storage', name: 'storage', component: { template: '<div />' } },
      { path: '/jobs', name: 'jobs', component: { template: '<div />' } },
    ],
  })
}

function mountSidebar({ router, hasSelections = false } = {}) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: { restoreCart: { rules: hasSelections ? [{ path: '/etc', host: null, include: true }] : [] } },
  })
  const plugins = [pinia]
  const stubs = { RouterLink: RouterLinkStub }
  if (router) {
    plugins.push(router)
    stubs.RouterLink = false
  }
  return mount(Sidebar, { global: { plugins, stubs } })
}

describe('Sidebar', () => {
  it('links to each top-level named route', () => {
    const wrapper = mountSidebar()
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links.map((l) => l.props('to'))).toEqual([
      { name: 'clients' },
      { name: 'catalog' },
      { name: 'restore' },
      { name: 'policies' },
      { name: 'storage' },
      { name: 'jobs' },
    ])
  })

  it('renders a brand header and an icon before each nav label', () => {
    const wrapper = mountSidebar()
    expect(wrapper.text()).toContain('Miniprotector')
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links).toHaveLength(6)
    links.forEach((link) => {
      expect(link.find('svg').exists()).toBe(true)
    })
  })

  it("marks the current route's nav link active and leaves the others inactive", async () => {
    const router = makeRouter()
    router.push({ name: 'policies' })
    await router.isReady()

    const wrapper = mountSidebar({ router })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links).toHaveLength(6)

    const activeLink = links[3]
    expect(activeLink.text()).toContain('Policies')
    expect(activeLink.classes()).toEqual(
      expect.arrayContaining(['bg-slate-800', 'text-white', 'border-l-4', 'border-blue-500', 'pl-2'])
    )
    expect(activeLink.classes()).not.toContain('pl-3')

    const inactiveLinks = [links[0], links[1], links[2], links[4], links[5]]
    inactiveLinks.forEach((link) => {
      expect(link.classes()).toEqual(expect.arrayContaining(['text-slate-300', 'pl-3']))
      expect(link.classes()).not.toContain('bg-slate-800')
      expect(link.classes()).not.toContain('border-l-4')
      expect(link.classes()).not.toContain('pl-2')
    })
  })

  it('does not highlight the Restore link when the cart is empty', async () => {
    const router = makeRouter()
    router.push({ name: 'clients' })
    await router.isReady()

    const wrapper = mountSidebar({ router, hasSelections: false })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links[2].text()).toContain('Restore')
    expect(links[2].classes()).not.toContain('text-blue-400')
  })

  it('highlights the Restore link when the cart has selections', async () => {
    const router = makeRouter()
    router.push({ name: 'clients' })
    await router.isReady()

    const wrapper = mountSidebar({ router, hasSelections: true })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links[2].text()).toContain('Restore')
    expect(links[2].classes()).toContain('text-blue-400')
  })

  it('does not highlight Restore when it is the active route, even with selections', async () => {
    const router = makeRouter()
    router.push({ name: 'restore' })
    await router.isReady()

    const wrapper = mountSidebar({ router, hasSelections: true })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links[2].classes()).toContain('bg-slate-800')
    expect(links[2].classes()).not.toContain('text-blue-400')
  })
})
