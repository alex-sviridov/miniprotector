import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DetailList from './DetailList.vue'

describe('DetailList', () => {
  it('renders each row as a term/definition pair', () => {
    const wrapper = mount(DetailList, {
      props: {
        rows: [
          { key: 'rpo', label: 'RPO', value: '1h' },
          { key: 'destination', label: 'Destination', value: 'store:8080' },
        ],
      },
    })
    expect(wrapper.findAll('dt').map((t) => t.text())).toEqual(['RPO', 'Destination'])
    expect(wrapper.findAll('dd').map((d) => d.text())).toEqual(['1h', 'store:8080'])
  })

  it("renders a named slot in place of a row's plain value", () => {
    const wrapper = mount(DetailList, {
      props: { rows: [{ key: 'objectFilters', label: 'Object Filters', value: '' }] },
      slots: { objectFilters: '<ul><li>/var/lib/dbdata</li></ul>' },
    })
    expect(wrapper.find('li').text()).toBe('/var/lib/dbdata')
  })
})
