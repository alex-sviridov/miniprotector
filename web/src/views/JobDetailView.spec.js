import { describe, it, expect, vi, beforeEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import JobDetailView from './JobDetailView.vue'
import { useJobsStore } from '../stores/jobs'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { job_id: 'backup:nightly:1752400000' } }),
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
})
