import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import KeyValueEditor from './KeyValueEditor.vue'

function mountEditor(modelValue = {}) {
  return mount(KeyValueEditor, {
    props: { modelValue, label: 'Description', testPrefix: 'description' },
  })
}

describe('KeyValueEditor', () => {
  it('renders existing key/value pairs', () => {
    const wrapper = mountEditor({ owner: 'alice' })
    expect(wrapper.find('[data-test="description-key-input"]').element.value).toBe('owner')
    expect(wrapper.find('[data-test="description-value-input"]').element.value).toBe('alice')
  })

  it('Update button starts disabled with no changes', () => {
    const wrapper = mountEditor({ owner: 'alice' })
    expect(wrapper.find('[data-test="description-update"]').attributes('disabled')).toBeDefined()
  })

  it('Update button enables once a value changes, and emits the correct set/unset diff', async () => {
    const wrapper = mountEditor({ owner: 'alice', old: 'gone' })

    const valueInputs = wrapper.findAll('[data-test="description-value-input"]')
    await valueInputs[0].setValue('bob')
    await wrapper.findAll('[data-test="description-remove"]')[1].trigger('click')

    expect(wrapper.find('[data-test="description-update"]').attributes('disabled')).toBeUndefined()

    await wrapper.find('[data-test="description-update"]').trigger('click')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({ set: { owner: 'bob' }, unset: ['old'] })
  })

  it('adding a new key/value row and clicking Update sends it in set', async () => {
    const wrapper = mountEditor({})

    await wrapper.find('[data-test="description-add"]').trigger('click')
    await wrapper.find('[data-test="description-key-input"]').setValue('role')
    await wrapper.find('[data-test="description-value-input"]').setValue('db')
    await wrapper.find('[data-test="description-update"]').trigger('click')

    expect(wrapper.emitted('save')[0][0]).toEqual({ set: { role: 'db' }, unset: [] })
  })

  it('resets its draft when modelValue prop changes', async () => {
    const wrapper = mountEditor({ owner: 'alice' })
    await wrapper.find('[data-test="description-value-input"]').setValue('bob')
    expect(wrapper.find('[data-test="description-update"]').attributes('disabled')).toBeUndefined()

    await wrapper.setProps({ modelValue: { owner: 'carol' } })

    expect(wrapper.find('[data-test="description-value-input"]').element.value).toBe('carol')
    expect(wrapper.find('[data-test="description-update"]').attributes('disabled')).toBeDefined()
  })
})
