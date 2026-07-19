import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import JobsListView from './JobsListView.vue'
import { useJobsStore } from '../stores/jobs'

const { destroy, DataTable } = vi.hoisted(() => {
  const destroy = vi.fn()
  const DataTable = vi.fn(() => ({ destroy }))
  return { destroy, DataTable }
})

vi.mock('simple-datatables', () => ({ DataTable }))

beforeEach(() => {
  DataTable.mockClear()
  destroy.mockClear()
})

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { jobs: state } })
  const wrapper = mount(JobsListView, {
    global: {
      plugins: [pinia],
      stubs: { RouterLink: { template: '<a :href="to"><slot /></a>', props: ['to'] } },
    },
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
    expect(wrapper.find('a').attributes('href')).toBe('/jobs/backup:nightly:1752400000')
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
    expect(wrapper.text()).toContain('—')
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('initializes simple-datatables on the rendered table once data loads, and destroys it on unmount', async () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'a',
          kind: 'backup',
          source_host: 'h',
          store_host: null,
          started_at: 1,
          finished_at: null,
          state: 'in_progress',
        },
      ],
      loading: false,
      error: null,
    })
    await flushPromises()

    expect(DataTable).toHaveBeenCalledTimes(1)
    expect(DataTable.mock.calls[0][0].tagName).toBe('TABLE')

    wrapper.unmount()
    expect(destroy).toHaveBeenCalledTimes(1)
  })
})
