// web/src/components/ui/RepeatableFieldList.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import RepeatableFieldList from './RepeatableFieldList.vue'

describe('RepeatableFieldList', () => {
  it('renders one row per item via the row slot', () => {
    const wrapper = mount(RepeatableFieldList, {
      props: { items: ['a', 'b'], addLabel: 'Add Hostname', testPrefix: 'hostname' },
      slots: {
        row: `<template #row="{ item, index }"><span :data-test="'item-' + index">{{ item }}</span></template>`,
      },
    })
    expect(wrapper.findAll('[data-test^="item-"]').map((n) => n.text())).toEqual(['a', 'b'])
  })

  it('pushes a new item from the newItem factory when Add is clicked', async () => {
    const items = []
    const wrapper = mount(RepeatableFieldList, {
      props: { items, newItem: () => 'x', addLabel: 'Add Hostname', testPrefix: 'hostname' },
    })
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    expect(items).toEqual(['x'])
  })

  it('defaults newItem to an empty string when none is provided', async () => {
    const items = []
    const wrapper = mount(RepeatableFieldList, {
      props: { items, addLabel: 'Add Hostname', testPrefix: 'hostname' },
    })
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    expect(items).toEqual([''])
  })

  it("splices an item out when its Remove button is clicked", async () => {
    const items = ['a', 'b']
    const wrapper = mount(RepeatableFieldList, {
      props: { items, addLabel: 'Add Hostname', testPrefix: 'hostname' },
      slots: { row: `<template #row="{ item }">{{ item }}</template>` },
    })
    await wrapper.findAll('[data-test="hostname-remove"]')[0].trigger('click')
    expect(items).toEqual(['b'])
  })

  it('uses a custom removeLabel when provided', () => {
    const wrapper = mount(RepeatableFieldList, {
      props: { items: ['a'], addLabel: 'Add Filter', removeLabel: 'Remove Filter', testPrefix: 'filter' },
    })
    expect(wrapper.find('[data-test="filter-remove"]').text()).toBe('Remove Filter')
  })
})
