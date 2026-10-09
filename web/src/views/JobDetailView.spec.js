import { describe, it, expect, vi, beforeEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import JobDetailView from './JobDetailView.vue'
import { useJobsStore } from '../stores/jobs'

const { getJobId, setJobId } = vi.hoisted(() => {
  let jobId = 'backup:nightly:1752400000'
  return { getJobId: () => jobId, setJobId: (v) => { jobId = v } }
})

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { job_id: getJobId() } }),
}))

// JobDetailView always wires useAutoFollow, which observes a sentinel via
// IntersectionObserver as soon as it mounts -- jsdom doesn't implement it,
// so every test that mounts the view (not just the follow/jump-to-latest
// ones) needs a stub in place, or the composable's watcher throws.
let observedCallback

class MockIntersectionObserver {
  constructor(cb) {
    observedCallback = cb
  }
  observe() {}
  disconnect() {}
}

beforeEach(() => {
  observedCallback = null
  vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
  setJobId('backup:nightly:1752400000')
})

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { jobs: state } })
  const wrapper = mount(JobDetailView, { global: { plugins: [pinia] } })
  return { wrapper, jobs: useJobsStore() }
}

describe('JobDetailView', () => {
  it('calls connectLogsStream with the route job_id on mount', () => {
    const { jobs } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(jobs.connectLogsStream).toHaveBeenCalledWith('backup:nightly:1752400000')
  })

  it('renders the job id as the heading', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(wrapper.find('h1').text()).toBe('backup:nightly:1752400000')
  })

  it('renders each log line via LogLine with timestamp, hostname, binary, and message', () => {
    const { wrapper } = mountView({
      logs: [
        { timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{"msg":"started"}' },
      ],
      logsLoading: false,
      logsError: null,
    })
    expect(wrapper.text()).toContain('database')
    expect(wrapper.text()).toContain('brfs')
    expect(wrapper.text()).toContain('started')
    expect(wrapper.text()).not.toContain('{"msg":"started"}')
  })

  it('shows an empty-state message when no lines are returned', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: null })
    expect(wrapper.text()).toContain('No log lines found')
  })

  it('shows the store error message on failure', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('renders a breadcrumb back to the jobs list', () => {
    const pinia = createTestingPinia({
      stubActions: true,
      initialState: { jobs: { logs: [], logsLoading: false, logsError: null } },
    })
    const wrapper = mount(JobDetailView, {
      global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
    })
    const crumb = wrapper.find('[data-test="breadcrumb"]')
    expect(crumb.text()).toBe('Jobs / backup:nightly:1752400000')
    expect(crumb.findComponent(RouterLinkStub).props('to')).toEqual({ name: 'jobs' })
  })

  it('shows a "Load older" button when hasOlderLogs is true, and calls loadOlder on click', async () => {
    const { wrapper, jobs } = mountView({ logs: [], logsLoading: false, logsError: null, hasOlderLogs: true })
    const button = wrapper.find('[data-test="load-older"]')
    expect(button.exists()).toBe(true)

    await button.trigger('click')

    expect(jobs.loadOlder).toHaveBeenCalledWith('backup:nightly:1752400000')
  })

  it('hides the "Load older" button when hasOlderLogs is false', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: null, hasOlderLogs: false })
    expect(wrapper.find('[data-test="load-older"]').exists()).toBe(false)
  })

  it('disables the "Load older" button while a page is in flight', () => {
    const { wrapper } = mountView({
      logs: [],
      logsLoading: false,
      logsError: null,
      hasOlderLogs: true,
      logsOlderLoading: true,
    })
    const button = wrapper.find('[data-test="load-older"]')
    expect(button.attributes('disabled')).toBeDefined()
    expect(button.text()).toContain('Loading')
  })

  it('renders a "load older" failure inline, without replacing the log list', () => {
    const { wrapper } = mountView({
      logs: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{"msg":"still here"}' }],
      logsLoading: false,
      logsError: null,
      hasOlderLogs: true,
      logsOlderError: 'upstream 502',
    })
    expect(wrapper.find('[data-test="load-older-error"]').text()).toBe('upstream 502')
    expect(wrapper.text()).toContain('still here')
  })

  it("keeps each LogLine's expanded state attached to its own line, not its array position, when older lines are prepended", async () => {
    const { wrapper, jobs } = mountView({
      logs: [
        { timestamp: 200, hostname: 'h', binary: 'brfs', line: JSON.stringify({ level: 'INFO', msg: 'second', extra: 'x' }) },
      ],
      logsLoading: false,
      logsError: null,
    })

    await wrapper.find('[data-test="log-line-summary"]').trigger('click')

    jobs.logs.unshift({
      timestamp: 100,
      hostname: 'h',
      binary: 'brfs',
      line: JSON.stringify({ level: 'INFO', msg: 'first', extra: 'y' }),
    })
    await nextTick()

    const items = wrapper.findAll('li')
    expect(items[0].text()).toContain('first')
    expect(items[0].find('[data-test="log-line-fields"]').exists()).toBe(false)
    expect(items[1].text()).toContain('second')
    expect(items[1].find('[data-test="log-line-fields"]').exists()).toBe(true)
  })

  describe('follow / jump-to-latest wiring', () => {
    it('calls setFollowing on the store when the sentinel leaves view, and shows a jump-to-latest button with a new-line count', async () => {
      const { wrapper, jobs } = mountView({
        logs: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }],
        logsLoading: false,
        logsError: null,
      })
      await nextTick()

      observedCallback([{ isIntersecting: false }])
      await nextTick()
      expect(jobs.setFollowing).toHaveBeenCalledWith(false)

      // A live line arriving is a tailSeq bump, which is what the counter
      // keys off (see the tailSeq test below).
      jobs.logs.push({ timestamp: 200, hostname: 'h', binary: 'brfs', line: '{}' })
      jobs.tailSeq++
      await nextTick()

      const jumpButton = wrapper.find('[data-test="jump-to-latest"]')
      expect(jumpButton.exists()).toBe(true)
      expect(jumpButton.text()).toContain('1 new line')
    })

    it('does not count paged-in history as new lines', async () => {
      // loadOlder unshifts older lines without touching tailSeq, so a
      // reader who scrolled up to page history in must not be offered a
      // button that scrolls them back down.
      const { wrapper, jobs } = mountView({
        logs: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }],
        logsLoading: false,
        logsError: null,
      })
      await nextTick()

      observedCallback([{ isIntersecting: false }])
      await nextTick()

      jobs.logs.unshift(
        { timestamp: 10, hostname: 'h', binary: 'brfs', line: '{}' },
        { timestamp: 20, hostname: 'h', binary: 'brfs', line: '{}' }
      )
      await nextTick()

      expect(wrapper.find('[data-test="jump-to-latest"]').exists()).toBe(false)
    })

    it('auto-scrolls the sentinel back into view when a live line arrives while following', async () => {
      // Every appended line pushes the zero-height sentinel below the
      // fold; without this the view would drop out of follow mode almost
      // immediately and the store's eviction cap would stop engaging.
      const { wrapper, jobs } = mountView({
        logs: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }],
        logsLoading: false,
        logsError: null,
      })
      await nextTick()

      const scrollIntoView = vi.fn()
      wrapper.find('[data-test="scroll-sentinel"]').element.scrollIntoView = scrollIntoView

      jobs.logs.push({ timestamp: 200, hostname: 'h', binary: 'brfs', line: '{}' })
      jobs.tailSeq++
      await nextTick()

      expect(scrollIntoView).toHaveBeenCalledWith({ block: 'end' })
    })

    it('does not auto-scroll when the reader has scrolled away from the tail', async () => {
      const { wrapper, jobs } = mountView({
        logs: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }],
        logsLoading: false,
        logsError: null,
      })
      await nextTick()

      const scrollIntoView = vi.fn()
      wrapper.find('[data-test="scroll-sentinel"]').element.scrollIntoView = scrollIntoView

      observedCallback([{ isIntersecting: false }])
      await nextTick()

      jobs.logs.push({ timestamp: 200, hostname: 'h', binary: 'brfs', line: '{}' })
      jobs.tailSeq++
      await nextTick()

      expect(scrollIntoView).not.toHaveBeenCalled()
    })
  })

  describe('restore/verify summary banner', () => {
    function logLine(binary, fields) {
      return { timestamp: 1, hostname: 'h', binary, line: JSON.stringify(fields) }
    }

    it('renders no banner for a non-restore/verify job', () => {
      setJobId('backup:nightly:1752400000')
      const { wrapper, jobs } = mountView({ logs: [] })
      expect(wrapper.find('[data-test="restore-summary-banner"]').exists()).toBe(false)
    })

    it('shows an in-progress banner when no finish line has arrived yet', () => {
      setJobId('restore:x:1')
      const { wrapper } = mountView({ logs: [] })
      expect(wrapper.get('[data-test="restore-summary-banner"]').text()).toContain('in progress')
    })

    it('shows a success banner with file counts for a finished restore job', () => {
      setJobId('restore:x:1')
      const { wrapper } = mountView({
        logs: [
          logLine('rwfs', { msg: 'restore complete', files_written: 12, bytes_written: 4400000000, skipped: 0 }),
          logLine('agent', { msg: 'policy execution completed', event: 'finish', status: 'success' }),
        ],
      })
      const text = wrapper.get('[data-test="restore-summary-banner"]').text()
      expect(text).toContain('12 files written')
      expect(text).toContain('0 skipped')
    })

    it('shows a failure banner without counts for a failed restore job', () => {
      setJobId('restore:x:1')
      const { wrapper } = mountView({
        logs: [logLine('agent', { msg: 'policy execution completed', event: 'finish', status: 'failure' })],
      })
      expect(wrapper.get('[data-test="restore-summary-banner"]').text()).toContain('Restore failed')
    })

    it('shows a success banner with verified/warnings counts for a finished verify job', () => {
      setJobId('verify:x:1')
      const { wrapper } = mountView({
        logs: [
          logLine('rwfs', { msg: 'summary', verified: 8, warnings: 1 }),
          logLine('agent', { msg: 'policy execution completed', event: 'finish', status: 'success' }),
        ],
      })
      const text = wrapper.get('[data-test="restore-summary-banner"]').text()
      expect(text).toContain('8 files verified')
      expect(text).toContain('1 warning')
    })
  })

  describe('store maintenance (cleanup/vacuum) summary banner', () => {
    function logLine(fields) {
      return { timestamp: 1, hostname: 'bwfs-east', binary: 'bwfs', line: JSON.stringify(fields) }
    }
    const banner = (wrapper) => wrapper.get('[data-test="maintenance-summary-banner"]').text()

    it('renders no maintenance banner for other job kinds', () => {
      setJobId('backup:nightly:1752400000')
      const { wrapper } = mountView({ logs: [] })
      expect(wrapper.find('[data-test="maintenance-summary-banner"]').exists()).toBe(false)
    })

    it('shows in progress until the finish line arrives', () => {
      setJobId('cleanup:bwfs-east:1752400000')
      const { wrapper } = mountView({
        logs: [logLine({ msg: 'store cleanup started', event: 'start', dry_run: false })],
      })
      expect(banner(wrapper)).toContain('Cleanup in progress')
    })

    it('summarizes a finished cleanup', () => {
      setJobId('cleanup:bwfs-east:1752400000')
      const { wrapper } = mountView({
        logs: [
          logLine({ msg: 'store cleanup started', event: 'start' }),
          logLine({ msg: 'store cleanup completed', event: 'finish', status: 'success', dry_run: false, versions_expired: 1200, deletion_log_pruned: 30, duration: 2_500_000_000 }),
        ],
      })
      const text = banner(wrapper)
      expect(text).toContain('Cleanup complete')
      expect(text).toContain('1200 versions deleted')
      expect(text).toContain('30 deletion-log entries pruned')
      expect(text).toContain('2.5 s')
    })

    it('says so when a cleanup was a dry run and deleted nothing', () => {
      setJobId('cleanup:bwfs-east:1752400000')
      const { wrapper } = mountView({
        logs: [logLine({ msg: 'store cleanup completed', event: 'finish', status: 'success', dry_run: true, versions_expired: 1, deletion_log_pruned: 0, duration: 1_000_000 })],
      })
      const text = banner(wrapper)
      expect(text).toContain('Dry run')
      expect(text).toContain('1 version would be deleted')
      expect(text).not.toContain('pruned')
    })

    it('summarizes a finished vacuum with what was reclaimed', () => {
      setJobId('vacuum:bwfs-east:1752400000')
      const { wrapper } = mountView({
        logs: [
          logLine({
            msg: 'store vacuum completed', event: 'finish', status: 'success',
            incomplete_file_data_removed: 1, orphaned_file_data_removed: 4, orphaned_chunk_links_removed: 9,
            orphaned_chunks_removed: 7, bytes_reclaimed: 3_758_096_384, duration: 61_000_000_000,
          }),
        ],
      })
      const text = banner(wrapper)
      expect(text).toContain('Vacuum complete')
      expect(text).toContain('7 chunks removed')
      expect(text).toContain('5 file data')
      expect(text).toContain('3.5 GB')
      expect(text).toContain('1 min 1 s')
    })

    it('shows the error for a failed run', () => {
      setJobId('vacuum:bwfs-east:1752400000')
      const { wrapper } = mountView({
        logs: [logLine({ msg: 'store vacuum failed', level: 'ERROR', event: 'finish', status: 'failure', error: 'disk I/O error', duration: 1_000_000 })],
      })
      const text = banner(wrapper)
      expect(text).toContain('Vacuum failed')
      expect(text).toContain('disk I/O error')
    })

    it('does not use the restore/verify banner', () => {
      setJobId('cleanup:bwfs-east:1752400000')
      const { wrapper } = mountView({ logs: [] })
      expect(wrapper.find('[data-test="restore-summary-banner"]').exists()).toBe(false)
    })
  })
})
