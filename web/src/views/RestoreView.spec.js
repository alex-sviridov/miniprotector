// web/src/views/RestoreView.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RestoreView from './RestoreView.vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'

function mountView({ rules = [], clientsList = [], submission = {} } = {}) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: {
      restoreCart: { rules },
      clients: { list: clientsList },
      restoreSubmission: { submitting: false, results: [], error: null, ...submission },
    },
  })
  return mount(RestoreView, { global: { plugins: [pinia] } })
}

describe('RestoreView', () => {
  it('shows the empty state when the cart has no selections', () => {
    const wrapper = mountView()
    expect(wrapper.text()).toContain('No files selected for restore yet.')
  })

  it('lists a folder wildcard rule as path/*', () => {
    const wrapper = mountView({ rules: [{ path: '/var', host: null, include: true }] })
    expect(wrapper.text()).toContain('/var/*')
  })

  it('lists a file rule as path (host)', () => {
    const wrapper = mountView({ rules: [{ path: '/etc/hosts', host: 'web01', include: true }] })
    expect(wrapper.text()).toContain('/etc/hosts (web01)')
  })

  it('omits exception (include: false) rules from the list', () => {
    const wrapper = mountView({
      rules: [
        { path: '/etc', host: null, include: true },
        { path: '/etc/hosts', host: 'web01', include: false },
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
      rules: [{ path: '/var', host: null, include: true }],
      clientsList: [{ hostname: 'web01' }, { hostname: 'web02' }],
    })
    const options = wrapper.find('[data-test="destination-select"]').findAll('option')
    expect(options.map((o) => o.element.value)).toEqual(['', 'web01', 'web02'])
  })

  it('disables submit until the cart has a selection and a destination is chosen', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      clientsList: [{ hostname: 'web01' }],
    })
    expect(wrapper.find('[data-test="submit-restore"]').attributes('disabled')).toBeDefined()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')

    expect(wrapper.find('[data-test="submit-restore"]').attributes('disabled')).toBeUndefined()
  })

  it('clicking submit calls restoreSubmission.submit with the chosen destination', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      clientsList: [{ hostname: 'web01' }],
    })
    const submission = useRestoreSubmissionStore()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')
    await wrapper.find('[data-test="submit-restore"]').trigger('click')

    expect(submission.submit).toHaveBeenCalledWith('web01')
  })

  it('renders a successful submission result', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      submission: { results: [{ storeHost: 'store-a', status: 'success', policy: { name: 'restore-x' } }] },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain('restore-x')
  })

  it('renders a per-group submission error', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
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

  it('renders a submission-level error', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      submission: { error: 'Nothing selected for restore.' },
    })
    expect(wrapper.find('[data-test="submission-error"]').text()).toBe('Nothing selected for restore.')
  })
})
