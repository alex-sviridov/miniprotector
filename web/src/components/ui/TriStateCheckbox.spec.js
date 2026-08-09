import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import TriStateCheckbox from './TriStateCheckbox.vue'

describe('TriStateCheckbox', () => {
  it('reflects the checked prop', () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: true, indeterminate: false } })
    expect(wrapper.find('input').element.checked).toBe(true)
  })

  it('reflects unchecked when checked is false', () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: false, indeterminate: false } })
    expect(wrapper.find('input').element.checked).toBe(false)
  })

  it('sets the DOM indeterminate property when indeterminate is true', () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: false, indeterminate: true } })
    expect(wrapper.find('input').element.indeterminate).toBe(true)
  })

  it('clears the DOM indeterminate property when indeterminate is false', () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: false, indeterminate: false } })
    expect(wrapper.find('input').element.indeterminate).toBe(false)
  })

  it('emits toggle on change', async () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: false, indeterminate: false } })
    await wrapper.find('input').setValue(true)
    expect(wrapper.emitted('toggle')).toHaveLength(1)
  })

  it('stops the click event from bubbling to an ancestor handler', async () => {
    const onClick = vi.fn()
    const wrapper = mount(
      defineComponent({
        components: { TriStateCheckbox },
        template: `<div @click="onClick"><TriStateCheckbox :checked="false" :indeterminate="false" /></div>`,
        setup() {
          return { onClick }
        },
      })
    )
    await wrapper.find('input').trigger('click')
    expect(onClick).not.toHaveBeenCalled()
  })
})
