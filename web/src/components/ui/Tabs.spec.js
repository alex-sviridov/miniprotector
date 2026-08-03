import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import Tabs from './Tabs.vue'

const replace = vi.fn()
let routeQuery = {}

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: routeQuery }),
  useRouter: () => ({ replace }),
}))

const TABS = [
  { key: 'details', label: 'Details' },
  { key: 'checkins', label: 'Check-ins' },
]

function mountTabs() {
  return mount(Tabs, {
    props: { tabs: TABS },
    slots: {
      details: '<p>details content</p>',
      checkins: '<p>checkins content</p>',
    },
  })
}

describe('Tabs', () => {
  afterEach(() => {
    replace.mockReset()
    routeQuery = {}
  })

  it('renders a button per tab', () => {
    const wrapper = mountTabs()
    expect(wrapper.find('[data-test="tab-details"]').text()).toBe('Details')
    expect(wrapper.find('[data-test="tab-checkins"]').text()).toBe('Check-ins')
  })

  it('defaults to the first tab when the query param is absent', () => {
    const wrapper = mountTabs()
    expect(wrapper.text()).toContain('details content')
    expect(wrapper.text()).not.toContain('checkins content')
  })

  it('defaults to the first tab when the query param matches no tab', () => {
    routeQuery = { tab: 'nonsense' }
    const wrapper = mountTabs()
    expect(wrapper.text()).toContain('details content')
  })

  it('shows the tab matching the query param', () => {
    routeQuery = { tab: 'checkins' }
    const wrapper = mountTabs()
    expect(wrapper.text()).toContain('checkins content')
    expect(wrapper.text()).not.toContain('details content')
  })

  it('replaces the route query with the clicked tab key, preserving other params', async () => {
    routeQuery = { foo: 'bar' }
    const wrapper = mountTabs()
    await wrapper.find('[data-test="tab-checkins"]').trigger('click')
    expect(replace).toHaveBeenCalledWith({ query: { foo: 'bar', tab: 'checkins' } })
  })
})
