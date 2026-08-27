import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import RestoreConfirmModal from './RestoreConfirmModal.vue'

function renderModal(overrides = {}) {
  return mount(RestoreConfirmModal, {
    props: { fileCount: 3, totalSize: 1024, destinationHost: 'web-03', overwrite: false, pinnedCount: 0, ...overrides },
  })
}

describe('RestoreConfirmModal', () => {
  it('renders the file count, size, and destination', () => {
    const wrapper = renderModal()
    const text = wrapper.get('[data-test="confirm-summary"]').text()
    expect(text).toContain('3 items')
    expect(text).toContain('1.0 KB')
    expect(text).toContain('web-03')
  })

  it('uses singular "item" for a single file', () => {
    const wrapper = renderModal({ fileCount: 1 })
    expect(wrapper.get('[data-test="confirm-summary"]').text()).toContain('1 item ')
  })

  it('shows the overwrite callout only when overwrite is true', () => {
    expect(renderModal({ overwrite: false }).find('[data-test="confirm-overwrite"]').exists()).toBe(false)
    expect(renderModal({ overwrite: true }).find('[data-test="confirm-overwrite"]').exists()).toBe(true)
  })

  it('shows the pinned-version callout only when pinnedCount is greater than zero', () => {
    expect(renderModal({ pinnedCount: 0 }).find('[data-test="confirm-pinned"]').exists()).toBe(false)
    const wrapper = renderModal({ pinnedCount: 2 })
    expect(wrapper.get('[data-test="confirm-pinned"]').text()).toContain('2 items pinned')
  })

  it('emits confirm and cancel from their respective buttons', async () => {
    const wrapper = renderModal()
    await wrapper.get('[data-test="confirm-restore"]').trigger('click')
    expect(wrapper.emitted('confirm')).toHaveLength(1)
    await wrapper.get('[data-test="confirm-cancel"]').trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(1)
  })

  it('emits cancel on Escape and on a backdrop click', async () => {
    const wrapper = renderModal()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('cancel')).toHaveLength(1)
    await wrapper.get('.fixed').trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(2)
  })
})
