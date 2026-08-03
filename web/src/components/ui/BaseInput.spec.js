import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BaseInput from './BaseInput.vue'

describe('BaseInput', () => {
  it('renders the modelValue and emits update:modelValue on input', async () => {
    const wrapper = mount(BaseInput, { props: { modelValue: 'x' } })
    expect(wrapper.find('input').element.value).toBe('x')
    await wrapper.find('input').setValue('y')
    expect(wrapper.emitted('update:modelValue')[0]).toEqual(['y'])
  })

  it('passes through arbitrary attributes to the underlying input', () => {
    const wrapper = mount(BaseInput, {
      props: { modelValue: '' },
      attrs: { type: 'number', required: true, 'data-test': 'port-input', pattern: '[0-9]+' },
    })
    const input = wrapper.find('input')
    expect(input.attributes('type')).toBe('number')
    expect(input.attributes('required')).toBeDefined()
    expect(input.attributes('data-test')).toBe('port-input')
    expect(input.attributes('pattern')).toBe('[0-9]+')
  })

  it('applies the shared input styling', () => {
    const wrapper = mount(BaseInput, { props: { modelValue: '' } })
    expect(wrapper.find('input').classes()).toContain('border')
  })
})
