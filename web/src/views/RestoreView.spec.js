// web/src/views/RestoreView.spec.js
import { describe, it, expect } from 'vitest'
import { nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RestoreView from './RestoreView.vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'

function mountView({ rules = [], clientsList = [], submission = {}, attachTo } = {}) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: {
      restoreCart: { rules },
      clients: { list: clientsList },
      restoreSubmission: { submitting: false, results: [], error: null, ...submission },
    },
  })
  return mount(RestoreView, { global: { plugins: [pinia] }, ...(attachTo ? { attachTo } : {}) })
}

describe('RestoreView', () => {
  it('shows the empty state when the cart has no selections', () => {
    const wrapper = mountView()
    expect(wrapper.text()).toContain('No files selected for restore yet.')
  })

  it('lists a folder wildcard rule\'s source path as path/*', () => {
    const wrapper = mountView({ rules: [{ path: '/var', host: null, include: true, destPath: '/var' }] })
    expect(wrapper.text()).toContain('/var/*')
  })

  it('shows storage host, source host, source path, and size in separate columns for a file rule', () => {
    const wrapper = mountView({
      rules: [
        {
          path: '/etc/hosts',
          host: 'web01',
          include: true,
          destPath: '/etc/hosts',
          storeHost: 'bwfs-1',
          size: 4096,
        },
      ],
    })
    const cells = wrapper.find('[data-test="restore-row-web01:/etc/hosts"]').findAll('td')
    expect(cells[0].text()).toBe('bwfs-1')
    expect(cells[1].text()).toBe('web01')
    expect(cells[2].text()).toBe('/etc/hosts')
    expect(cells[4].text()).toBe('4.0 KB')
  })

  it('shows dashes for storage host, source host, and size on a folder rule', () => {
    const wrapper = mountView({ rules: [{ path: '/var', host: null, include: true, destPath: '/var' }] })
    const cells = wrapper.find('[data-test="restore-row-:/var"]').findAll('td')
    expect(cells[0].text()).toBe('—')
    expect(cells[1].text()).toBe('—')
    expect(cells[4].text()).toBe('—')
  })

  it('omits exception (include: false) rules from the list', () => {
    const wrapper = mountView({
      rules: [
        { path: '/etc', host: null, include: true, destPath: '/etc' },
        { path: '/etc/hosts', host: 'web01', include: false, destPath: '/etc/hosts' },
      ],
    })
    expect(wrapper.text()).toContain('/etc/*')
    expect(wrapper.text()).not.toContain('/etc/hosts')
  })

  it('renders the page breadcrumb', () => {
    const wrapper = mountView()
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Restore')
  })

  it('removing an entry calls restoreCart.removeEntry with that entry', async () => {
    const entry = { path: '/var', host: null, include: true }
    const wrapper = mountView({ rules: [entry] })
    const cart = useRestoreCartStore()

    await wrapper.find('[data-test="remove-:/var"]').trigger('click')

    expect(cart.removeEntry).toHaveBeenCalledWith(entry)
  })

  it('populates the destination select from the clients store', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }, { hostname: 'web02' }],
    })
    const options = wrapper.find('[data-test="destination-select"]').findAll('option')
    expect(options.map((o) => o.element.value)).toEqual(['', 'web01', 'web02'])
  })

  it('disables submit until the cart has a selection and a destination is chosen', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }],
    })
    expect(wrapper.find('[data-test="submit-restore"]').attributes('disabled')).toBeDefined()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')

    expect(wrapper.find('[data-test="submit-restore"]').attributes('disabled')).toBeUndefined()
  })

  it('clicking submit calls restoreSubmission.submit with the chosen destination', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      clientsList: [{ hostname: 'web01' }],
    })
    const submission = useRestoreSubmissionStore()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')
    await wrapper.find('[data-test="submit-restore"]').trigger('click')

    expect(submission.submit).toHaveBeenCalledWith('web01')
  })

  it('renders a successful submission result', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      submission: { results: [{ storeHost: 'store-a', status: 'success', policy: { name: 'restore-x' } }] },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain('restore-x')
  })

  it('renders a per-group submission error', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true, destPath: '/var' }],
      submission: {
        results: [
          { storeHost: 'store-b', status: 'error', message: 'No reachable storage node found for store-b' },
        ],
      },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain(
      'No reachable storage node found for store-b'
    )
  })

  it('renders a submission-level error while the cart is empty', () => {
    const wrapper = mountView({ submission: { error: 'Nothing selected for restore.' } })
    expect(wrapper.text()).toContain('No files selected for restore yet.')
    expect(wrapper.find('[data-test="submission-error"]').text()).toBe('Nothing selected for restore.')
  })

  it('keeps submission results visible after the cart is emptied', () => {
    const wrapper = mountView({
      submission: { results: [{ storeHost: 'store-a', status: 'success', policy: { name: 'restore-x' } }] },
    })
    expect(wrapper.text()).toContain('No files selected for restore yet.')
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain('restore-x')
  })

  it('shows the destination path as plain text by default, prefilled to the source path', () => {
    const wrapper = mountView({
      rules: [{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }],
    })
    expect(wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').text()).toBe('/etc/hosts')
    expect(wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]').exists()).toBe(false)
  })

  it('clicking the destination path shows an editable input prefilled with the current value', async () => {
    const wrapper = mountView({
      rules: [{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }],
    })

    await wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').trigger('click')

    const input = wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]')
    expect(input.exists()).toBe(true)
    expect(input.element.value).toBe('/etc/hosts')
  })

  it('committing an edited destination path calls restoreCart.setDestPath and exits edit mode', async () => {
    const entry = { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }
    const wrapper = mountView({ rules: [entry] })
    const cart = useRestoreCartStore()

    await wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]')
    await input.setValue('/etc/hosts.bak')
    await input.trigger('blur')

    expect(cart.setDestPath).toHaveBeenCalledWith(entry, '/etc/hosts.bak')
    expect(wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').exists()).toBe(true)
  })

  it('pressing Enter in the destination path input commits the edit', async () => {
    const entry = { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }
    const wrapper = mountView({ rules: [entry] })
    const cart = useRestoreCartStore()

    await wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]')
    await input.setValue('/etc/hosts.bak')
    await input.trigger('keyup.enter')

    expect(cart.setDestPath).toHaveBeenCalledWith(entry, '/etc/hosts.bak')
  })

  it('does not double-commit when Enter removes the focused input, which then blurs', async () => {
    const entry = { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }
    const wrapper = mountView({ rules: [entry] })
    const cart = useRestoreCartStore()

    await wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]')
    await input.setValue('/etc/hosts.bak')
    await input.trigger('keyup.enter')
    // Simulates the native blur a browser fires when a focused element is
    // removed from the DOM -- jsdom/vue-test-utils don't reproduce this
    // automatically, so it's triggered explicitly here to exercise the guard.
    await input.trigger('blur')

    expect(cart.setDestPath).toHaveBeenCalledTimes(1)
  })

  it('focuses the destination path input when editing starts', async () => {
    const wrapper = mountView({
      rules: [{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }],
      attachTo: document.body,
    })
    await wrapper.find('[data-test="dest-path-text-web01:/etc/hosts"]').trigger('click')
    await nextTick()

    const input = wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]')
    expect(input.element).toBe(document.activeElement)

    wrapper.unmount()
  })
})
