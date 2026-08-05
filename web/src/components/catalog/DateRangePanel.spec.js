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

  it('updates receivedAfter/receivedBefore snapped to day boundaries when the picker emits a new range', async () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    const start = new Date(2026, 0, 10, 14, 30)
    const end = new Date(2026, 0, 15, 9, 0)
    await picker.vm.$emit('update:model-value', [start, end])

    const expectedAfter = Math.floor(new Date(2026, 0, 10, 0, 0, 0, 0).getTime() / 1000)
    const expectedBefore = Math.floor(new Date(2026, 0, 15, 23, 59, 59, 999).getTime() / 1000)

    expect(wrapper.emitted('update:receivedAfter')[0]).toEqual([expectedAfter])
    expect(wrapper.emitted('update:receivedBefore')[0]).toEqual([expectedBefore])
  })

  it('does not emit anything for a partial (single-date) selection', async () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    await picker.vm.$emit('update:model-value', [new Date(2026, 0, 10)])

    expect(wrapper.emitted('update:receivedAfter')).toBeFalsy()
    expect(wrapper.emitted('update:receivedBefore')).toBeFalsy()
  })

  it('disables partialRange on the picker via the range config object', () => {
    // @vuepic/vue-datepicker v14 exposes partialRange as a field of the
    // `range` prop's RangeConfig object, not as a standalone top-level
    // `partial-range` prop -- confirmed against the installed package's
    // type declarations (node_modules/@vuepic/vue-datepicker/dist/index.d.ts).
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    expect(picker.props('range')).toEqual({ partialRange: false })
  })

  it('includes a "Last 7 days" preset spanning exactly 7 days', () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    const presets = picker.props('presetDates')
    const week = presets.find((p) => p.label === 'Last 7 days')
    const [start, end] = week.value
    expect(end.getTime() - start.getTime()).toBe(7 * 24 * 60 * 60 * 1000)
  })

  it('has a "This month" preset starting on the 1st of the current month, distinct from "Last 30 days"', () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    const presets = picker.props('presetDates')
    const thisMonth = presets.find((p) => p.label === 'This month')
    const [start] = thisMonth.value
    const now = new Date()
    expect(start.getDate()).toBe(1)
    expect(start.getMonth()).toBe(now.getMonth())

    const last30 = presets.find((p) => p.label === 'Last 30 days')
    expect(thisMonth.value[0].getTime()).not.toBe(last30.value[0].getTime())
  })

  it('disables the time picker (date-only range)', () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    expect(picker.props('timeConfig')).toEqual({ enableTimePicker: false })
  })
})
