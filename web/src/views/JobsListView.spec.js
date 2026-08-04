import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import JobsListView from './JobsListView.vue'
import { useJobsStore } from '../stores/jobs'

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { jobs: state } })
  const wrapper = mount(JobsListView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
  })
  return { wrapper, jobs: useJobsStore() }
}

describe('JobsListView', () => {
  it('calls fetchAll on mount', () => {
    const { jobs } = mountView({ list: [], loading: false, error: null })
    expect(jobs.fetchAll).toHaveBeenCalledTimes(1)
  })

  it('renders each job with a link to its detail page', () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'backup:nightly:1752400000',
          kind: 'backup',
          source_host: 'database',
          store_host: 'bwfs-east',
          started_at: 1752400000,
          finished_at: 1752400010,
          state: 'success',
        },
      ],
      loading: false,
      error: null,
    })
    expect(wrapper.text()).toContain('backup:nightly:1752400000')
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'backup:nightly:1752400000')
    expect(link.props('to')).toEqual({ name: 'job-detail', params: { job_id: 'backup:nightly:1752400000' } })
  })

  it('renders a dash for a null store_host and finished_at', () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'operating-refresh:1752400500',
          kind: 'operating-refresh',
          source_host: 'webserver',
          store_host: null,
          started_at: 1752400500,
          finished_at: null,
          state: 'in_progress',
        },
      ],
      loading: false,
      error: null,
    })
    const cells = wrapper.findAll('tbody td')
    expect(cells[3].text()).toBe('—')
    expect(cells[5].text()).toBe('—')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('shows an empty-state message when there are no jobs', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.text()).toContain('No jobs in the last 24h.')
  })

  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Jobs')
  })

  it('renders the State column as a green badge for a successful job', () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'backup:nightly:1752400000',
          kind: 'backup',
          source_host: 'database',
          store_host: 'bwfs-east',
          started_at: 1752400000,
          finished_at: 1752400010,
          state: 'success',
        },
      ],
      loading: false,
      error: null,
    })
    const stateCell = wrapper.findAll('tbody td')[6]
    expect(stateCell.find('span').classes()).toContain('bg-emerald-50')
  })

  it('renders the State column as a red badge for a failed job', () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'backup:nightly:1752400000',
          kind: 'backup',
          source_host: 'database',
          store_host: 'bwfs-east',
          started_at: 1752400000,
          finished_at: 1752400010,
          state: 'failure',
        },
      ],
      loading: false,
      error: null,
    })
    const stateCell = wrapper.findAll('tbody td')[6]
    expect(stateCell.find('span').classes()).toContain('bg-red-50')
  })
})
