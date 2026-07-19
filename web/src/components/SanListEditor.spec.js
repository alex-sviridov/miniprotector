import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import SanListEditor from './SanListEditor.vue'

describe('SanListEditor', () => {
  it('renders existing SANs', () => {
    const wrapper = mount(SanListEditor, { props: { modelValue: ['old.internal'] } })
    expect(wrapper.find('[data-test="san-input"]').element.value).toBe('old.internal')
  })

  it('Update button starts disabled with no changes', () => {
    const wrapper = mount(SanListEditor, { props: { modelValue: ['old.internal'] } })
    expect(wrapper.find('[data-test="san-update"]').attributes('disabled')).toBeDefined()
  })

  it('adding and removing SANs enables Update and emits the correct add/remove diff', async () => {
    const wrapper = mount(SanListEditor, { props: { modelValue: ['old.internal'] } })

    await wrapper.find('[data-test="san-add"]').trigger('click')
    const inputs = wrapper.findAll('[data-test="san-input"]')
    await inputs[1].setValue('new.internal')
    await wrapper.find('[data-test="san-remove"]').trigger('click')

    expect(wrapper.find('[data-test="san-update"]').attributes('disabled')).toBeUndefined()

    await wrapper.find('[data-test="san-update"]').trigger('click')

    expect(wrapper.emitted('save')[0][0]).toEqual({ add: ['new.internal'], remove: ['old.internal'] })
  })

  it('resets its draft when modelValue prop changes', async () => {
    const wrapper = mount(SanListEditor, { props: { modelValue: ['old.internal'] } })
    await wrapper.find('[data-test="san-add"]').trigger('click')
    expect(wrapper.find('[data-test="san-update"]').attributes('disabled')).toBeUndefined()

    await wrapper.setProps({ modelValue: ['new.internal'] })

    expect(wrapper.findAll('[data-test="san-input"]')).toHaveLength(1)
    expect(wrapper.find('[data-test="san-input"]').element.value).toBe('new.internal')
    expect(wrapper.find('[data-test="san-update"]').attributes('disabled')).toBeDefined()
  })
})
