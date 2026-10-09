import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RestoreView from './RestoreView.vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'
import { useCatalogStore } from '../stores/catalog'
import { useJobsStore } from '../stores/jobs'

function mountView(initialState = {}) {
  const pinia = createTestingPinia({ stubActions: true, initialState })
  // Without this, opening a real VersionsModal (see the "captured" tests
  // below) leaves its fetchPathVersions call auto-stubbed to resolve
  // `undefined` instead of an array, and VersionsModal's own
  // spansMultipleHosts computed then throws on `.map()` of that
  // `undefined` as an unhandled rejection -- see CatalogView.spec.js's
  // mountView for the same fix applied to the same underlying problem.
  const catalog = useCatalogStore()
  vi.spyOn(catalog, 'fetchPathVersions').mockResolvedValue([])
  const wrapper = mount(RestoreView, {
    global: { plugins: [pinia], stubs: { 'router-link': { template: '<a><slot /></a>' } } },
  })
  return { wrapper, pinia }
}

const fileEntry = { host: 'web01', path: '/etc/hosts', include: true, destPath: '/etc/hosts', size: 100 }
const folderEntry = { host: null, path: '/var', include: true, destPath: '/var' }
const pinnedEntry = { host: 'web01', path: '/etc/nginx.conf', include: true, destPath: '/etc/nginx.conf', notBefore: 555, notAfter: 555 }
const unboundedEntry = { host: 'web01', path: '/etc/resolved.conf', include: true, destPath: '/etc/resolved.conf', notBefore: 0, notAfter: 0 }
const windowedEntry = { host: 'web01', path: '/etc/motd', include: true, destPath: '/etc/motd', notBefore: 1000, notAfter: 2000 }

describe('RestoreView', () => {
  it('shows the empty state when the cart has no selections', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [] } })
    expect(wrapper.text()).toContain('No files selected for restore yet.')
  })

  it('shows a summary line with item count and total size', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry, folderEntry] } })
    expect(wrapper.get('[data-test="cart-summary"]').text()).toContain('2 items selected')
  })

  it('never renders a storage/store host anywhere on the page', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    expect(wrapper.text()).not.toMatch(/store.?host/i)
  })

  it('shows the pinned timestamp for an entry pinned to a specific version', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [pinnedEntry] } })
    expect(wrapper.get('[data-test="captured-web01:/etc/nginx.conf"]').text()).not.toBe('Latest')
  })

  it('shows bare "Latest" only for a genuinely unbounded entry (explicit "Use latest" reset, notBefore/notAfter both 0)', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [unboundedEntry] } })
    expect(wrapper.get('[data-test="captured-web01:/etc/resolved.conf"]').text()).toBe('Latest')
  })

  it('does not claim bare "Latest" for an entry bounded by a real captured filter window -- shows the window\'s upper-bound date instead', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [windowedEntry] } })
    const text = wrapper.get('[data-test="captured-web01:/etc/motd"]').text()
    expect(text).not.toBe('Latest')
    expect(text).toContain(new Date(2000 * 1000).toLocaleString())
  })

  it('clicking a Captured cell opens the version picker scoped to that entry', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    const modal = wrapper.findComponent({ name: 'VersionsModal' })
    expect(modal.props('path')).toBe('/etc/hosts')
    expect(modal.props('sourceHost')).toBe('web01')
  })

  it('selecting a version from the picker calls setVersionWindow for that entry', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
    await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 999 })
    expect(restoreCart.setVersionWindow).toHaveBeenCalledWith({ host: 'web01', path: '/etc/hosts' }, 999, 999)
  })

  it('shows an edit control for the destination path instead of relying on clicking plain text', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    expect(wrapper.get('[data-test="edit-dest-path-web01:/etc/hosts"]').exists()).toBe(true)
  })

  it('clicking the edit control opens an editable input prefilled with the current value', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    await wrapper.get('[data-test="edit-dest-path-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.get('[data-test="dest-path-input-web01:/etc/hosts"]')
    expect(input.element.value).toBe('/etc/hosts')
  })

  it('committing an edited destination path calls restoreCart.setDestPath and exits edit mode', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="edit-dest-path-web01:/etc/hosts"]').trigger('click')
    const input = wrapper.get('[data-test="dest-path-input-web01:/etc/hosts"]')
    await input.setValue('/etc/hosts.bak')
    await input.trigger('keyup.enter')
    expect(restoreCart.setDestPath).toHaveBeenCalledWith(fileEntry, '/etc/hosts.bak')
    expect(wrapper.find('[data-test="dest-path-input-web01:/etc/hosts"]').exists()).toBe(false)
  })

  it('shows overwrite and verify/restore helper copy', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    expect(wrapper.text()).toContain('Replaces files that already exist at the destination')
    expect(wrapper.text()).toContain('Verify checks integrity only')
    expect(wrapper.text()).toContain('Restore writes files to the destination')
  })

  it('shows a stale-checkin warning when the selected destination host has not checked in recently', async () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      clients: { list: [{ hostname: 'web-03', last_seen_at: 0 }] },
    })
    const select = wrapper.get('[data-test="destination-select"]')
    await select.setValue('web-03')
    expect(wrapper.get('[data-test="destination-stale-warning"]').exists()).toBe(true)
  })

  it('shows no stale-checkin warning for a recently-checked-in destination host', async () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      clients: { list: [{ hostname: 'web-03', last_seen_at: Math.floor(Date.now() / 1000) }] },
    })
    const select = wrapper.get('[data-test="destination-select"]')
    await select.setValue('web-03')
    expect(wrapper.find('[data-test="destination-stale-warning"]').exists()).toBe(false)
  })

  it('clicking Verify calls restoreSubmission.submit directly, without a confirmation step', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] }, clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] } })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="verify-button"]').trigger('click')
    expect(submission.submit).toHaveBeenCalledWith('web-03', { mode: 'verify', overwrite: false })
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).exists()).toBe(false)
  })

  it('clicking Restore opens the confirmation modal instead of submitting immediately', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] }, clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] } })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    expect(submission.submit).not.toHaveBeenCalled()
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).exists()).toBe(true)
  })

  it('confirming the modal submits with mode restore and the checked overwrite flag, and closes it', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] }, clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] } })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="overwrite-checkbox"]').setValue(true)
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    await wrapper.findComponent({ name: 'RestoreConfirmModal' }).vm.$emit('confirm')
    expect(submission.submit).toHaveBeenCalledWith('web-03', { mode: 'restore', overwrite: true })
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).exists()).toBe(false)
  })

  it('cancelling the modal submits nothing and closes it', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] }, clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] } })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    await wrapper.findComponent({ name: 'RestoreConfirmModal' }).vm.$emit('cancel')
    expect(submission.submit).not.toHaveBeenCalled()
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).exists()).toBe(false)
  })

  it('passes the pinned-entry count into the confirmation modal', async () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry, pinnedEntry] },
      clients: { list: [{ hostname: 'web-03', last_seen_at: 1 }] },
    })
    await wrapper.get('[data-test="destination-select"]').setValue('web-03')
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).props('pinnedCount')).toBe(1)
  })

  it("renders a submitting badge, then a success badge linking to the entry's job", () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      restoreSubmission: { entryStatus: { 'web01:/etc/hosts': [{ status: 'success', jobId: 'restore:r1:1' }] } },
      jobs: { list: [{ job_id: 'restore:r1:1', state: 'success' }] },
    })
    const link = wrapper.get('[data-test="status-web01:/etc/hosts"] a')
    expect(link.attributes('href') || link.exists()).toBeTruthy()
    expect(wrapper.get('[data-test="status-web01:/etc/hosts"]').text()).toContain('success')
  })

  it('renders an error message inline for an entry whose submission failed', () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      restoreSubmission: { entryStatus: { 'web01:/etc/hosts': [{ status: 'error', message: 'No storage policy found for store-b' }] } },
    })
    expect(wrapper.get('[data-test="status-web01:/etc/hosts"]').text()).toContain('No storage policy found for store-b')
  })

  it('removing an entry calls restoreCart.removeEntry with that entry', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    const restoreCart = useRestoreCartStore()
    await wrapper.get('[data-test="remove-web01:/etc/hosts"]').trigger('click')
    expect(restoreCart.removeEntry).toHaveBeenCalledWith(fileEntry)
  })

  it('removing an entry also clears its recorded submission status, so a re-added entry starts fresh', async () => {
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry] },
      restoreSubmission: { entryStatus: { 'web01:/etc/hosts': [{ status: 'success', jobId: 'restore:r1:1', mode: 'verify' }] } },
    })
    const submission = useRestoreSubmissionStore()
    await wrapper.get('[data-test="remove-web01:/etc/hosts"]').trigger('click')
    expect(submission.clearEntry).toHaveBeenCalledWith(fileEntry)
  })

  it('disables verify and restore until the cart has a selection and a destination is chosen', async () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    expect(wrapper.get('[data-test="verify-button"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="destination-select"]').setValue('web01')
    expect(wrapper.get('[data-test="verify-button"]').attributes('disabled')).toBeUndefined()
  })

  it('disconnects the jobs stream on unmount', () => {
    const { wrapper } = mountView({ restoreCart: { rules: [fileEntry] } })
    const jobs = useJobsStore()
    wrapper.unmount()
    expect(jobs.disconnectJobsStream).toHaveBeenCalledTimes(1)
  })

  it('shows a Damaged badge on damaged file rows only, never on folder rules', () => {
    const damagedEntry = { ...fileEntry, path: '/etc/bad', destPath: '/etc/bad', damaged: true }
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry, damagedEntry, { ...folderEntry, damaged: true }] },
    })
    const badge = wrapper.get('[data-test="cart-damaged-web01:/etc/bad"]')
    expect(badge.text()).toBe('Damaged')
    expect(badge.attributes('title')).toContain('damaged')
    expect(wrapper.find('[data-test="cart-damaged-web01:/etc/hosts"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="cart-damaged-:/var"]').exists()).toBe(false)
  })

  it('passes the damaged file-rule count into the confirmation modal (folders and excluded rules not counted)', async () => {
    const dmg = { ...fileEntry, path: '/etc/bad', destPath: '/etc/bad', damaged: true }
    const excluded = { ...fileEntry, path: '/etc/x', destPath: '/etc/x', damaged: true, include: false }
    const { wrapper } = mountView({
      restoreCart: { rules: [fileEntry, dmg, excluded, { ...folderEntry, damaged: true }] },
      clients: { list: [{ hostname: 'web01', last_seen_at: Math.floor(Date.now() / 1000) }] },
    })
    wrapper.vm.destinationHost = 'web01'
    await wrapper.get('[data-test="destination-select"]').setValue('web01')
    await wrapper.get('[data-test="restore-button"]').trigger('click')
    expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).props('damagedCount')).toBe(1)
  })

  describe('damaged flag when changing version on the cart page', () => {
    const dmg = { ...fileEntry, damaged: true }

    function mountReal(rules) {
      const pinia = createTestingPinia({ stubActions: false, initialState: { restoreCart: { rules } } })
      vi.spyOn(useCatalogStore(), 'fetchPathVersions').mockResolvedValue([])
      const wrapper = mount(RestoreView, {
        global: { plugins: [pinia], stubs: { 'router-link': { template: '<a><slot /></a>' } } },
      })
      return { wrapper, cart: useRestoreCartStore() }
    }
    async function pick(wrapper, event, payload) {
      await wrapper.get('[data-test="captured-web01:/etc/hosts"]').trigger('click')
      await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit(event, payload)
    }

    it('pinning an older healthy version clears the badge and the confirm count', async () => {
      const { wrapper } = mountReal([dmg])
      await pick(wrapper, 'select-version', { store_created_at: 5, damaged: false })
      expect(wrapper.find('[data-test="cart-damaged-web01:/etc/hosts"]').exists()).toBe(false)
      await wrapper.get('[data-test="destination-select"]').setValue('web01')
      await wrapper.get('[data-test="restore-button"]').trigger('click')
      expect(wrapper.findComponent({ name: 'RestoreConfirmModal' }).props('damagedCount')).toBe(0)
    })

    it('pinning a damaged older version shows the badge', async () => {
      const { wrapper } = mountReal([fileEntry])
      await pick(wrapper, 'select-version', { store_created_at: 5, damaged: true })
      expect(wrapper.find('[data-test="cart-damaged-web01:/etc/hosts"]').exists()).toBe(true)
    })

    it('use latest takes the damaged state of the latest version from the modal', async () => {
      const { wrapper, cart } = mountReal([dmg])
      await pick(wrapper, 'use-latest', true)
      expect(cart.rules[0].damaged).toBe(true)
      expect(wrapper.find('[data-test="cart-damaged-web01:/etc/hosts"]').exists()).toBe(true)
    })

    it('use latest of a healthy latest version clears the flag; no payload falls back to healthy', async () => {
      const { wrapper, cart } = mountReal([dmg])
      await pick(wrapper, 'use-latest', false)
      expect(cart.rules[0].damaged).toBe(false)
      expect(wrapper.find('[data-test="cart-damaged-web01:/etc/hosts"]').exists()).toBe(false)

      await pick(wrapper, 'select-version', { store_created_at: 5, damaged: true })
      await pick(wrapper, 'use-latest')
      expect(cart.rules[0].damaged).toBe(false)
    })

    it('does not touch folder rules', async () => {
      const { wrapper, cart } = mountReal([folderEntry])
      await wrapper.get('[data-test="captured-:/var"]').trigger('click')
      await wrapper.findComponent({ name: 'VersionsModal' }).vm.$emit('select-version', { store_created_at: 5, damaged: true })
      expect(cart.rules[0]).not.toHaveProperty('damaged')
    })
  })
})
