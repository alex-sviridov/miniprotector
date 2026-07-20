import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DataTable from './DataTable.vue'

const columns = [
  { label: 'Name', field: 'name', sortable: true },
  { label: 'Size', field: 'size', sortable: true, type: 'number', formatFn: (v) => `${v} B` },
]
const rows = [
  { id: 1, name: 'a', size: 10 },
  { id: 2, name: 'b', size: 20 },
]

describe('DataTable', () => {
  it('renders one row per item with formatted cell values', () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    const trs = wrapper.findAll('tbody tr')
    expect(trs).toHaveLength(2)
    expect(trs[0].text()).toContain('a')
    expect(trs[0].text()).toContain('10 B')
  })

  it('shows a search box by default', () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    expect(wrapper.find('input.vgt-input').exists()).toBe(true)
  })

  it('hides the search box when searchEnabled is false', () => {
    const wrapper = mount(DataTable, { props: { columns, rows, searchEnabled: false } })
    expect(wrapper.find('input.vgt-input').exists()).toBe(false)
  })

  it('emits row-click with the clicked row object, not an index', async () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    await wrapper.findAll('tbody tr')[1].trigger('click')
    expect(wrapper.emitted('row-click')).toHaveLength(1)
    expect(wrapper.emitted('row-click')[0][0]).toMatchObject({ id: 2, name: 'b' })
  })

  it('lets the caller override cell rendering via the table-row slot', () => {
    const wrapper = mount(DataTable, {
      props: { columns, rows },
      slots: {
        'table-row': `<template #table-row="{ column, row, formattedRow }">
          <a v-if="column.field === 'name'" :href="'/x/' + row.name">{{ row.name }}</a>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>`,
      },
    })
    expect(wrapper.find('a[href="/x/a"]').exists()).toBe(true)
  })
})
