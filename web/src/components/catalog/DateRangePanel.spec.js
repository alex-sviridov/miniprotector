import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DateRangePanel from './DateRangePanel.vue'
import { VueDatePicker } from '@vuepic/vue-datepicker'

function mountPanel(props = {}) {
  return mount(DateRangePanel, {
    props: { receivedAfter: 1000, receivedBefore: 2000, ...props },
    global: { stubs: { VueDatePicker: true } },
  })
}

describe('DateRangePanel', () => {
  it("passes the current range as VueDatePicker's model value", () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    expect(picker.props('modelValue')).toEqual([new Date(1000 * 1000), new Date(2000 * 1000)])
  })

  it('updates receivedAfter/receivedBefore when the picker emits a new range', async () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    await picker.vm.$emit('update:model-value', [new Date(5000 * 1000), new Date(6000 * 1000)])

    expect(wrapper.emitted('update:receivedAfter')[0]).toEqual([5000])
    expect(wrapper.emitted('update:receivedBefore')[0]).toEqual([6000])
  })

  it('includes a "Last 7 days" preset spanning exactly 7 days', () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    const presets = picker.props('presetDates')
    const week = presets.find((p) => p.label === 'Last 7 days')
    const [start, end] = week.value
    expect(end.getTime() - start.getTime()).toBe(7 * 24 * 60 * 60 * 1000)
  })

  it('disables the time picker (date-only range)', () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    expect(picker.props('timeConfig')).toEqual({ enableTimePicker: false })
  })
})
