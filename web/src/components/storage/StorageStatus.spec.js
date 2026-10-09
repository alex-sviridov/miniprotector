import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StorageStatus from './StorageStatus.vue'

const report = (over = {}) => ({
  hostname: 'store-1',
  port: 9400,
  state: 'online',
  disk_total_bytes: 1000,
  disk_used_bytes: 400,
  active_connections: 3,
  in_progress_jobs: 1,
  uptime_seconds: 7200,
  reported_at: '2026-10-05T10:00:00Z',
  ...over,
})

describe('StorageStatus', () => {
  it('shows an empty message when nothing has reported', () => {
    const w = mount(StorageStatus, { props: { reports: [] } })
    expect(w.find('[data-test="status-empty"]').exists()).toBe(true)
  })

  it('shows nothing while the first fetch is pending', () => {
    const w = mount(StorageStatus, { props: {} })
    expect(w.find('[data-test="status-empty"]').exists()).toBe(false)
  })

  it('renders state, disk usage and connections', () => {
    const w = mount(StorageStatus, { props: { reports: [report()] } })
    expect(w.find('[data-test="status-state"]').text()).toBe('online')
    expect(w.find('[data-test="status-disk"]').text()).toContain('40%')
    expect(w.find('[data-test="status-connections"]').text()).toBe('3')
    expect(w.find('[data-test="status-jobs"]').text()).toBe('1')
  })

  it('turns the disk bar amber then red as it fills', () => {
    const amber = mount(StorageStatus, { props: { reports: [report({ disk_used_bytes: 900 })] } })
    expect(amber.find('[data-test="status-disk-bar"]').classes()).toContain('bg-amber-500')
    const red = mount(StorageStatus, { props: { reports: [report({ disk_used_bytes: 990 })] } })
    expect(red.find('[data-test="status-disk-bar"]').classes()).toContain('bg-red-500')
  })

  it('renders one card per node', () => {
    const w = mount(StorageStatus, { props: { reports: [report(), report({ hostname: 'store-2' })] } })
    expect(w.findAll('[data-test="status-card"]')).toHaveLength(2)
  })

  it('emits refresh', async () => {
    const w = mount(StorageStatus, { props: { reports: [] } })
    await w.find('[data-test="status-refresh"]').trigger('click')
    expect(w.emitted('refresh')).toHaveLength(1)
  })
})
