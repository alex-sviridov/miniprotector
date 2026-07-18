import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'

function mountView(state) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: { catalog: { cursorStack: [], ...state } },
  })
  const wrapper = mount(CatalogView, { global: { plugins: [pinia] } })
  return { wrapper, catalog: useCatalogStore() }
}

describe('CatalogView', () => {
  it('calls search with empty filters on mount', () => {
    const { catalog } = mountView({ entries: [], hasMore: false, loading: false, error: null })
    expect(catalog.search).toHaveBeenCalledWith({ sourceHost: '', storeHost: '', pattern: '' })
  })

  it('submits the filter form via search', async () => {
    const { wrapper, catalog } = mountView({ entries: [], hasMore: false, loading: false, error: null })
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('database')
    await inputs[1].setValue('bwfs-a')
    await inputs[2].setValue('dbdata')
    await wrapper.find('form').trigger('submit.prevent')
    expect(catalog.search).toHaveBeenLastCalledWith({ sourceHost: 'database', storeHost: 'bwfs-a', pattern: 'dbdata' })
  })

  it('disables Next when hasMore is false and Prev when canGoPrev is false', () => {
    const { wrapper } = mountView({
      entries: [{ id: 1, path: '/x', source_host: 'h', store_host: 's', size: 1, mode: '-rw', mod_time: 0 }],
      hasMore: false,
      loading: false,
      error: null,
    })
    const buttons = wrapper.findAll('button')
    const next = buttons.find((b) => b.text() === 'Next')
    const prev = buttons.find((b) => b.text() === 'Prev')
    expect(next.attributes('disabled')).toBeDefined()
    expect(prev.attributes('disabled')).toBeDefined()
  })

  it('clicking Next calls catalog.nextPage', async () => {
    const { wrapper, catalog } = mountView({
      entries: [{ id: 1, path: '/x', source_host: 'h', store_host: 's', size: 1, mode: '-rw', mod_time: 0 }],
      hasMore: true,
      loading: false,
      error: null,
    })
    const next = wrapper.findAll('button').find((b) => b.text() === 'Next')
    await next.trigger('click')
    expect(catalog.nextPage).toHaveBeenCalledTimes(1)
  })
})
