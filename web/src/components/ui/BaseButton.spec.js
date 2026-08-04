import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import BaseButton from './BaseButton.vue'

describe('BaseButton', () => {
  it('renders slot content and defaults to type=button with secondary styling', () => {
    const wrapper = mount(BaseButton, { slots: { default: 'Click me' } })
    expect(wrapper.text()).toBe('Click me')
    expect(wrapper.attributes('type')).toBe('button')
    expect(wrapper.classes()).toContain('border')
  })

  it('applies primary variant classes', () => {
    const wrapper = mount(BaseButton, { props: { variant: 'primary' } })
    expect(wrapper.classes()).toContain('bg-blue-600')
  })

  it('applies danger variant classes', () => {
    const wrapper = mount(BaseButton, { props: { variant: 'danger' } })
    expect(wrapper.classes()).toContain('text-red-600')
  })

  it('passes through type and disabled as native attributes', () => {
    const wrapper = mount(BaseButton, { props: { type: 'submit' }, attrs: { disabled: true } })
    expect(wrapper.attributes('type')).toBe('submit')
    expect(wrapper.attributes('disabled')).toBeDefined()
  })

  it('passes through data-test and additional classes', () => {
    const wrapper = mount(BaseButton, { attrs: { 'data-test': 'revoke-button', class: 'mt-2' } })
    expect(wrapper.attributes('data-test')).toBe('revoke-button')
    expect(wrapper.classes()).toContain('mt-2')
  })

  it('renders as a router-link when the to prop is set, keeping the variant classes', () => {
    const wrapper = mount(BaseButton, {
      props: { to: { name: 'client-new' }, variant: 'primary' },
      slots: { default: 'New Client' },
      global: { stubs: { RouterLink: RouterLinkStub } },
    })
    const link = wrapper.findComponent(RouterLinkStub)
    expect(link.exists()).toBe(true)
    expect(link.props('to')).toEqual({ name: 'client-new' })
    expect(wrapper.classes()).toContain('bg-blue-600')
    expect(wrapper.find('button').exists()).toBe(false)
  })
})
