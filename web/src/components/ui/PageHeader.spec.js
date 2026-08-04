// web/src/components/ui/PageHeader.spec.js
import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import PageHeader from './PageHeader.vue'

describe('PageHeader', () => {
  it('renders the title as an h1', () => {
    const wrapper = mount(PageHeader, { props: { title: 'Clients' } })
    expect(wrapper.find('h1').text()).toBe('Clients')
  })

  it('renders default slot content below the header row', () => {
    const wrapper = mount(PageHeader, {
      props: { title: 'Clients' },
      slots: { default: '<p>body</p>' },
    })
    expect(wrapper.find('p').text()).toBe('body')

    // Verify ordering: h1 should appear before p in the DOM
    const html = wrapper.html()
    const h1Index = html.indexOf('<h1')
    const pIndex = html.indexOf('<p')
    expect(h1Index).toBeGreaterThanOrEqual(0)
    expect(pIndex).toBeGreaterThanOrEqual(0)
    expect(h1Index).toBeLessThan(pIndex)
  })

  it('renders the actions slot when provided', () => {
    const wrapper = mount(PageHeader, {
      props: { title: 'Clients' },
      slots: { actions: '<button>New Client</button>' },
    })
    expect(wrapper.find('button').text()).toBe('New Client')
  })

  it('does not render an actions wrapper when no actions slot is given', () => {
    const wrapper = mount(PageHeader, { props: { title: 'Clients' } })
    expect(wrapper.find('[data-test="page-header-actions"]').exists()).toBe(false)
  })

  it('renders no breadcrumb when crumbs is omitted', () => {
    const wrapper = mount(PageHeader, { props: { title: 'Clients' } })
    expect(wrapper.find('[data-test="breadcrumb"]').exists()).toBe(false)
  })

  it('renders breadcrumb segments in order, linking all but the last', () => {
    const wrapper = mount(PageHeader, {
      props: {
        title: 'webserver',
        crumbs: [{ label: 'Clients', to: { name: 'clients' } }, { label: 'webserver' }],
      },
      global: { stubs: { RouterLink: RouterLinkStub } },
    })
    const crumb = wrapper.find('[data-test="breadcrumb"]')
    expect(crumb.text()).toBe('Clients / webserver')
    const link = crumb.findComponent(RouterLinkStub)
    expect(link.props('to')).toEqual({ name: 'clients' })
    expect(link.text()).toBe('Clients')
  })
})
