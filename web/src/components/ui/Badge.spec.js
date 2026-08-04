import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import Badge from './Badge.vue'

describe('Badge', () => {
  it('renders slot content', () => {
    const wrapper = mount(Badge, { slots: { default: 'Yes' } })
    expect(wrapper.text()).toBe('Yes')
  })

  it('defaults to the neutral variant', () => {
    const wrapper = mount(Badge)
    expect(wrapper.classes()).toContain('bg-gray-100')
  })

  it('applies ok variant classes', () => {
    const wrapper = mount(Badge, { props: { variant: 'ok' } })
    expect(wrapper.classes()).toContain('bg-emerald-50')
  })

  it('applies bad variant classes', () => {
    const wrapper = mount(Badge, { props: { variant: 'bad' } })
    expect(wrapper.classes()).toContain('bg-red-50')
  })
})
