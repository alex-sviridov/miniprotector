import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import FacetPanel from './FacetPanel.vue'

const facets = [
  { name: 'database', count: 3, last_seen: 1752400000 },
  { name: 'webserver', count: 1, last_seen: 1752400010 },
]

function mountPanel(props = {}) {
  return mount(FacetPanel, {
    props: { facets, error: null, nameLabel: 'Client', countLabel: 'Entries in range', selected: [], ...props },
  })
}

describe('FacetPanel', () => {
  it('renders one row per facet with the given column labels', () => {
    const wrapper = mountPanel()
    expect(wrapper.text()).toContain('Client')
    expect(wrapper.text()).toContain('Entries in range')
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('database')
  })

  it('checks the checkbox for a name already in `selected`', () => {
    const wrapper = mountPanel({ selected: ['database'] })
    const checkbox = wrapper.find('tbody tr th.vgt-checkbox-col input[type="checkbox"]')
    expect(checkbox.element.checked).toBe(true)
  })

  it('leaves other checkboxes unchecked', () => {
    const wrapper = mountPanel({ selected: ['database'] })
    const checkboxes = wrapper.findAll('tbody tr th.vgt-checkbox-col input[type="checkbox"]')
    expect(checkboxes[1].element.checked).toBe(false)
  })

  it('emits update:selected with the new set of names when a checkbox is toggled', async () => {
    const wrapper = mountPanel()
    const vgtComponent = wrapper.findComponent({ name: 'vue-good-table' })
    const row = vgtComponent.vm.processedRows[0].children[0]
    vgtComponent.vm.onCheckboxClicked(row, 0, {})
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('update:selected')).toBeTruthy()
    expect(wrapper.emitted('update:selected').at(-1)[0]).toEqual(['database'])
  })

  it('shows the error message when present', () => {
    const wrapper = mountPanel({ error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })
})
