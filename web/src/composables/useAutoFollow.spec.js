import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { useAutoFollow } from './useAutoFollow'

let observedCallback

class MockIntersectionObserver {
  constructor(callback) {
    observedCallback = callback
  }
  observe() {}
  disconnect() {}
}

beforeEach(() => {
  observedCallback = null
  vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
})

function mountHarness(initialCount) {
  const TestComponent = defineComponent({
    props: { count: { type: Number, required: true } },
    setup(props) {
      return { ...useAutoFollow(() => props.count) }
    },
    template: '<div><div ref="sentinel" /></div>',
  })
  return mount(TestComponent, { props: { count: initialCount } })
}

describe('useAutoFollow', () => {
  it('starts following and flips to not-following when the sentinel leaves view', async () => {
    const wrapper = mountHarness(0)
    await nextTick()
    expect(wrapper.vm.isFollowing).toBe(true)

    observedCallback([{ isIntersecting: false }])
    await nextTick()

    expect(wrapper.vm.isFollowing).toBe(false)
  })

  it('counts new lines that arrive while not following, and clears the count on returning to the bottom', async () => {
    const wrapper = mountHarness(5)
    await nextTick()
    observedCallback([{ isIntersecting: false }])
    await nextTick()

    await wrapper.setProps({ count: 8 })
    expect(wrapper.vm.newLineCount).toBe(3)

    observedCallback([{ isIntersecting: true }])
    await nextTick()
    expect(wrapper.vm.newLineCount).toBe(0)
  })

  it('scrollToBottom scrolls the sentinel element into view', async () => {
    const wrapper = mountHarness(0)
    await nextTick()
    const scrollIntoView = vi.fn()
    wrapper.vm.sentinel.scrollIntoView = scrollIntoView

    wrapper.vm.scrollToBottom()

    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'end' })
  })
})
