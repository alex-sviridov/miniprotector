import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BaseField from './BaseField.vue'

describe('BaseField', () => {
  it('renders the label and slot content', () => {
    const wrapper = mount(BaseField, {
      props: { label: 'Name' },
      slots: { default: '<input data-test="name-input" />' },
    })
    expect(wrapper.text()).toContain('Name')
    expect(wrapper.find('[data-test="name-input"]').exists()).toBe(true)
  })

  it('shows a required asterisk when required is true', () => {
    const wrapper = mount(BaseField, { props: { label: 'Name', required: true } })
    expect(wrapper.find('.text-red-600').exists()).toBe(true)
  })

  it('omits the asterisk when required is false (the default)', () => {
    const wrapper = mount(BaseField, { props: { label: 'Name' } })
    expect(wrapper.find('.text-red-600').exists()).toBe(false)
  })
})
