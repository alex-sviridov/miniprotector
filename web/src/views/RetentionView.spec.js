import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RetentionView from './RetentionView.vue'
import { useRetentionPoliciesStore } from '../stores/retentionPolicies'

const rule = (id, priority, extra = {}) => ({
  id,
  name: `rule-${id}`,
  client_filters: { hostnames: ['web-*'], labels: {} },
  retention: { backup_type: 'filesystem', path: `/${id}`, include: [], keep_seconds: 7 * 86400, priority },
  ...extra,
})

function mountView(list = [rule('a', 1), rule('b', 2), rule('c', 3)], state = {}) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: { retentionPolicies: { list, loading: false, error: null, reordering: false, ...state } },
  })
  const wrapper = mount(RetentionView, {
    global: { plugins: [pinia], stubs: { RouterLink: { template: '<a><slot /></a>' } } },
  })
  return { wrapper, store: useRetentionPoliciesStore() }
}

const find = (wrapper, name) => wrapper.find(`[data-test="${name}"]`)

describe('RetentionView', () => {
  beforeEach(() => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('fetches the rules on mount', () => {
    const { store } = mountView()
    expect(store.fetchAll).toHaveBeenCalled()
  })

  it('renders one row per rule in order, with keep, type, path and target', () => {
    const { wrapper } = mountView([
      rule('a', 1, { retention: { backup_type: 'filesystem', path: '/var/log', include: ['*.log'], keep_seconds: 0, priority: 1 } }),
      rule('b', 2),
    ])
    const rows = wrapper.findAll('[data-test^="retention-row-"]')
    expect(rows.map((r) => r.attributes('data-test'))).toEqual(['retention-row-a', 'retention-row-b'])
    expect(rows[0].text()).toContain('rule-a')
    expect(rows[0].text()).toContain('Filesystem')
    expect(rows[0].text()).toContain('/var/log')
    expect(rows[0].text()).toContain('*.log')
    expect(rows[0].text()).toContain('Forever')
    expect(rows[0].text()).toContain('web-*')
    expect(rows[1].text()).toContain('7 days')
    expect(rows[0].find('[data-test="retention-position-a"]').text()).toBe('1')
    expect(rows[1].find('[data-test="retention-position-b"]').text()).toBe('2')
  })

  it('always shows the fixed built-in default row last, which is not reorderable', () => {
    const { wrapper } = mountView()
    const def = find(wrapper, 'retention-default-row')
    expect(def.exists()).toBe(true)
    expect(def.text()).toMatch(/everything else/i)
    expect(def.text()).toMatch(/built-in default/i)
    expect(def.attributes('draggable')).toBeUndefined()
    expect(def.find('button').exists()).toBe(false)
    const all = wrapper.findAll('[data-test^="retention-row-"], [data-test="retention-default-row"]')
    expect(all[all.length - 1].attributes('data-test')).toBe('retention-default-row')
  })

  it('shows an empty-state note plus the default row when there are no rules', () => {
    const { wrapper } = mountView([])
    expect(wrapper.text()).toMatch(/no retention rules/i)
    expect(find(wrapper, 'retention-default-row').exists()).toBe(true)
  })

  it('move-down / move-up buttons call reorder with the new id order', async () => {
    const { wrapper, store } = mountView()

    await find(wrapper, 'retention-down-a').trigger('click')
    expect(store.reorder).toHaveBeenLastCalledWith(['b', 'a', 'c'])

    await find(wrapper, 'retention-up-c').trigger('click')
    expect(store.reorder).toHaveBeenLastCalledWith(['a', 'c', 'b'])
  })

  it('disables move-up on the first row and move-down on the last, and everything while a reorder is in flight', () => {
    const { wrapper } = mountView()
    expect(find(wrapper, 'retention-up-a').element.disabled).toBe(true)
    expect(find(wrapper, 'retention-down-c').element.disabled).toBe(true)
    expect(find(wrapper, 'retention-down-a').element.disabled).toBe(false)

    const busy = mountView(undefined, { reordering: true })
    expect(find(busy.wrapper, 'retention-down-a').element.disabled).toBe(true)
    expect(find(busy.wrapper, 'retention-up-b').element.disabled).toBe(true)
  })

  it('dragging a row onto another reorders', async () => {
    const { wrapper, store } = mountView()

    await find(wrapper, 'retention-row-a').trigger('dragstart')
    await find(wrapper, 'retention-row-c').trigger('dragover')
    await find(wrapper, 'retention-row-c').trigger('drop')
    expect(store.reorder).toHaveBeenLastCalledWith(['b', 'c', 'a'])

    await find(wrapper, 'retention-row-c').trigger('dragstart')
    await find(wrapper, 'retention-row-a').trigger('drop')
    expect(store.reorder).toHaveBeenLastCalledWith(['c', 'a', 'b'])
  })

  it('dropping a row on itself, or without a drag in progress, does nothing', async () => {
    const { wrapper, store } = mountView()
    await find(wrapper, 'retention-row-b').trigger('drop')
    await find(wrapper, 'retention-row-a').trigger('dragstart')
    await find(wrapper, 'retention-row-a').trigger('drop')
    expect(store.reorder).not.toHaveBeenCalled()
  })

  it('shows a store error while keeping the list visible', () => {
    const { wrapper } = mountView(undefined, { error: 'ids must list every retention policy exactly once' })
    expect(wrapper.text()).toContain('ids must list every retention policy exactly once')
    expect(wrapper.findAll('[data-test^="retention-row-"]')).toHaveLength(3)
  })

  it('opens the create modal and saves through the store', async () => {
    const { wrapper, store } = mountView()
    await find(wrapper, 'retention-new').trigger('click')
    const modal = wrapper.findComponent({ name: 'RetentionFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('policy')).toBeNull()

    modal.vm.$emit('save', { name: 'x' })
    await flushPromises()

    expect(store.create).toHaveBeenCalledWith({ name: 'x' })
    expect(wrapper.findComponent({ name: 'RetentionFormModal' }).exists()).toBe(false)
  })

  it('keeps the modal open and shows the server error when create fails', async () => {
    const { wrapper, store } = mountView()
    store.create.mockImplementation(async () => {
      store.error = 'path must be absolute'
      throw new Error('path must be absolute')
    })
    await find(wrapper, 'retention-new').trigger('click')
    wrapper.findComponent({ name: 'RetentionFormModal' }).vm.$emit('save', { name: 'x' })
    await flushPromises()

    const modal = wrapper.findComponent({ name: 'RetentionFormModal' })
    expect(modal.exists()).toBe(true)
    expect(modal.props('serverError')).toBe('path must be absolute')
  })

  it('edit opens the modal with the policy and saves via update', async () => {
    const { wrapper, store } = mountView()
    await find(wrapper, 'retention-edit-b').trigger('click')
    const modal = wrapper.findComponent({ name: 'RetentionFormModal' })
    expect(modal.props('policy').id).toBe('b')

    modal.vm.$emit('save', { name: 'renamed' })
    await flushPromises()

    expect(store.update).toHaveBeenCalledWith('b', { name: 'renamed' })
  })

  it('delete confirms first and removes via the store', async () => {
    const { wrapper, store } = mountView()
    window.confirm.mockReturnValueOnce(false)
    await find(wrapper, 'retention-delete-a').trigger('click')
    expect(store.remove).not.toHaveBeenCalled()

    await find(wrapper, 'retention-delete-a').trigger('click')
    expect(store.remove).toHaveBeenCalledWith('a')
  })
})
