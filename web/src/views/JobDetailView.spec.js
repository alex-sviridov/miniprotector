import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import JobDetailView from './JobDetailView.vue'
import { useJobsStore } from '../stores/jobs'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { job_id: 'backup:nightly:1752400000' } }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { jobs: state } })
  const wrapper = mount(JobDetailView, { global: { plugins: [pinia] } })
  return { wrapper, jobs: useJobsStore() }
}

describe('JobDetailView', () => {
  it('calls fetchLogs with the route job_id on mount', () => {
    const { jobs } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(jobs.fetchLogs).toHaveBeenCalledWith('backup:nightly:1752400000')
  })

  it('renders the job id as the heading', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(wrapper.find('h1').text()).toBe('backup:nightly:1752400000')
  })

  it('renders each log line with its formatted timestamp, hostname, binary, and raw line', () => {
    const { wrapper } = mountView({
      logs: [
        { timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{"msg":"started"}' },
      ],
      logsLoading: false,
      logsError: null,
    })
    expect(wrapper.text()).toContain('database')
    expect(wrapper.text()).toContain('brfs')
    expect(wrapper.text()).toContain('{"msg":"started"}')
  })

  it('shows an empty-state message when no lines are returned', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(wrapper.text()).toContain('No log lines found')
  })

  it('shows the store error message on failure', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })
})
