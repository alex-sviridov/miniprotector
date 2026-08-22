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

  it('renders in input order (no default sort) when defaultSort is not set', () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    const trs = wrapper.findAll('tbody tr')
    expect(trs[0].text()).toContain('a')
    expect(trs[1].text()).toContain('b')
  })

  it('renders sorted by defaultSort even though the input rows are unsorted', async () => {
    // Regression: a live-upserted row appended to the end of an otherwise
    // unsorted rows array (e.g. the jobs list) must still land where the
    // configured default sort would put it, not wherever it happened to
    // be pushed -- see JobsListView.vue's started_at desc usage.
    const wrapper = mount(DataTable, {
      props: { columns, rows, defaultSort: { field: 'size', type: 'desc' } },
    })
    // vue-good-table-next applies initialSortBy in its own mounted() hook,
    // via a child ref -- the reactive re-render lands one tick later.
    await wrapper.vm.$nextTick()
    const trs = wrapper.findAll('tbody tr')
    expect(trs[0].text()).toContain('b')
    expect(trs[1].text()).toContain('a')
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

  it('does not render checkboxes by default', () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    expect(wrapper.find('th.vgt-checkbox-col').exists()).toBe(false)
  })

  it('renders row checkboxes when selectable is true', () => {
    const wrapper = mount(DataTable, { props: { columns, rows, selectable: true } })
    expect(wrapper.findAll('tbody tr th.vgt-checkbox-col input[type="checkbox"]')).toHaveLength(2)
  })

  it('emits selection-change with the selected row objects', async () => {
    const wrapper = mount(DataTable, { props: { columns, rows, selectable: true } })
    const vgtComponent = wrapper.findComponent({ name: 'vue-good-table' })
    const row = vgtComponent.vm.processedRows[0].children[0]
    vgtComponent.vm.onCheckboxClicked(row, 0, {})
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('selection-change')).toBeTruthy()
    const lastCall = wrapper.emitted('selection-change').at(-1)[0]
    expect(lastCall).toHaveLength(1)
    expect(lastCall[0]).toMatchObject({ id: 1, name: 'a' })
  })
})
