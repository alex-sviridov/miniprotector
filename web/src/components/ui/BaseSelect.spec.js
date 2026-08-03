import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BaseSelect from './BaseSelect.vue'

describe('BaseSelect', () => {
  it('reflects modelValue in the rendered selection', () => {
    const wrapper = mount(BaseSelect, {
      props: { modelValue: 'b' },
      slots: { default: '<option value="a">A</option><option value="b">B</option>' },
    })
    expect(wrapper.find('select').element.value).toBe('b')
  })

  it('emits update:modelValue when the selection changes', async () => {
    const wrapper = mount(BaseSelect, {
      props: { modelValue: 'a' },
      slots: { default: '<option value="a">A</option><option value="b">B</option>' },
    })
    await wrapper.find('select').setValue('b')
    expect(wrapper.emitted('update:modelValue')[0]).toEqual(['b'])
  })

  it('passes through arbitrary attributes to the underlying select', () => {
    const wrapper = mount(BaseSelect, {
      props: { modelValue: '' },
      attrs: { required: true, 'data-test': 'storage-select' },
    })
    const select = wrapper.find('select')
    expect(select.attributes('required')).toBeDefined()
    expect(select.attributes('data-test')).toBe('storage-select')
  })
})
