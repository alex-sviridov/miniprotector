import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TagInput from './TagInput.vue'

function mountTagInput(items = []) {
  const wrapper = mount(TagInput, { props: { items, testPrefix: 'pattern' } })
  return { wrapper, items }
}

async function type(wrapper, text) {
  await wrapper.find('[data-test="pattern-input"]').setValue(text)
}

async function pressKey(wrapper, key) {
  await wrapper.find('[data-test="pattern-input"]').trigger('keydown', { key })
}

describe('TagInput', () => {
  it('renders existing items as chips on mount', () => {
    const { wrapper } = mountTagInput(['*.sql', '*.log'])
    const texts = wrapper.findAll('[data-test="pattern-chip"]').map((n) => n.text())
    expect(texts[0]).toContain('*.sql')
    expect(texts[1]).toContain('*.log')
  })

  it('adds a chip and clears the input on Enter', async () => {
    const { wrapper, items } = mountTagInput([])
    await type(wrapper, '*.sql')
    await pressKey(wrapper, 'Enter')
    expect(wrapper.findAll('[data-test="pattern-chip"]')).toHaveLength(1)
    expect(wrapper.find('[data-test="pattern-input"]').element.value).toBe('')
    expect(items).toEqual(['*.sql'])
  })

  it('adds a chip on comma', async () => {
    const { wrapper, items } = mountTagInput([])
    await type(wrapper, '*.sql')
    await pressKey(wrapper, ',')
    expect(items).toEqual(['*.sql'])
  })

  it('commits leftover text on blur', async () => {
    const { wrapper, items } = mountTagInput([])
    await type(wrapper, '*.sql')
    await wrapper.find('[data-test="pattern-input"]').trigger('blur')
    expect(items).toEqual(['*.sql'])
  })

  it('ignores empty/whitespace-only commits', async () => {
    const { wrapper, items } = mountTagInput([])
    await type(wrapper, '   ')
    await pressKey(wrapper, 'Enter')
    expect(wrapper.findAll('[data-test="pattern-chip"]')).toHaveLength(0)
    expect(items).toEqual([])
  })

  it('removes the last chip on backspace when the input is empty', async () => {
    const { wrapper, items } = mountTagInput(['*.sql', '*.log'])
    await pressKey(wrapper, 'Backspace')
    expect(items).toEqual(['*.sql'])
  })

  it('does not remove a chip on backspace when the input has text', async () => {
    const { wrapper, items } = mountTagInput(['*.sql'])
    await type(wrapper, 'x')
    await pressKey(wrapper, 'Backspace')
    expect(items).toEqual(['*.sql'])
  })

  it('removes a specific chip via its remove button', async () => {
    const { wrapper, items } = mountTagInput(['*.sql', '*.log'])
    await wrapper.findAll('[data-test="pattern-chip-remove"]')[0].trigger('click')
    expect(items).toEqual(['*.log'])
  })

  it('flags a syntactically invalid pattern and reports invalid', async () => {
    const { wrapper } = mountTagInput([])
    await type(wrapper, '[abc')
    await pressKey(wrapper, 'Enter')
    expect(wrapper.find('[data-test="pattern-chip"]').classes()).toContain('border-red-500')
    expect(wrapper.vm.isValid()).toBe(false)
  })

  it('flags a pattern that overlaps an existing one in the list', async () => {
    const { wrapper } = mountTagInput(['/var/log'])
    await type(wrapper, '/var/log/app')
    await pressKey(wrapper, 'Enter')
    const chips = wrapper.findAll('[data-test="pattern-chip"]')
    expect(chips[1].classes()).toContain('border-red-500')
    expect(wrapper.vm.isValid()).toBe(false)
  })

  it('does not flag unrelated patterns that merely share a text prefix', async () => {
    const { wrapper } = mountTagInput(['/var/log'])
    await type(wrapper, '/var/logs')
    await pressKey(wrapper, 'Enter')
    const chips = wrapper.findAll('[data-test="pattern-chip"]')
    expect(chips[1].classes()).not.toContain('border-red-500')
    expect(wrapper.vm.isValid()).toBe(true)
  })

  it('reports valid when all chips are valid and non-conflicting', async () => {
    const { wrapper } = mountTagInput(['*.sql'])
    await type(wrapper, '*.log')
    await pressKey(wrapper, 'Enter')
    expect(wrapper.vm.isValid()).toBe(true)
  })
})
