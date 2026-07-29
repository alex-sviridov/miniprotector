import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import LogLine from './LogLine.vue'

function mountLine(overrides = {}) {
  const line = {
    timestamp: 1752400000123456789,
    hostname: 'database',
    binary: 'brfs',
    line: JSON.stringify({ level: 'INFO', msg: 'started', job_id: 'backup:x:1' }),
    ...overrides,
  }
  return mount(LogLine, { props: { line } })
}

describe('LogLine', () => {
  it('renders the level, timestamp, binary@hostname, and message summary', () => {
    const wrapper = mountLine()

    expect(wrapper.find('[data-test="log-line-level"]').text()).toBe('INFO')
    expect(wrapper.find('[data-test="log-line-message"]').text()).toBe('started')
    expect(wrapper.text()).toContain('brfs@database')
  })

  it('colors the level badge by severity', () => {
    const info = mountLine({ line: JSON.stringify({ level: 'INFO', msg: 'x' }) })
    const error = mountLine({ line: JSON.stringify({ level: 'ERROR', msg: 'x' }) })

    expect(info.find('[data-test="log-line-level"]').classes()).toContain('bg-blue-100')
    expect(error.find('[data-test="log-line-level"]').classes()).toContain('bg-red-100')
  })

  it('keeps extra fields hidden until the row is clicked, then shows them', async () => {
    const wrapper = mountLine({
      line: JSON.stringify({ level: 'INFO', msg: 'started', job_id: 'backup:x:1', event: 'start' }),
    })

    expect(wrapper.find('[data-test="log-line-fields"]').exists()).toBe(false)

    await wrapper.find('[data-test="log-line-summary"]').trigger('click')

    const fields = wrapper.find('[data-test="log-line-fields"]')
    expect(fields.exists()).toBe(true)
    expect(fields.text()).toContain('job_id')
    expect(fields.text()).toContain('backup:x:1')
    expect(fields.text()).toContain('event')
    expect(fields.text()).toContain('start')
  })

  it('shows no expand affordance and does not toggle when there are no extra fields', async () => {
    const wrapper = mountLine({ line: JSON.stringify({ level: 'INFO', msg: 'started' }) })

    expect(wrapper.find('[data-test="log-line-caret"]').exists()).toBe(false)

    await wrapper.find('[data-test="log-line-summary"]').trigger('click')

    expect(wrapper.find('[data-test="log-line-fields"]').exists()).toBe(false)
  })

  it('falls back to the raw line text with a neutral badge when the line is not JSON', () => {
    const wrapper = mountLine({ line: 'not json at all' })

    expect(wrapper.find('[data-test="log-line-level"]').text()).toBe('—')
    expect(wrapper.find('[data-test="log-line-message"]').text()).toBe('not json at all')
    expect(wrapper.find('[data-test="log-line-caret"]').exists()).toBe(false)
  })

  it('stringifies a non-primitive field value instead of showing [object Object]', async () => {
    const wrapper = mountLine({
      line: JSON.stringify({ level: 'ERROR', msg: 'failed', error: { code: 'E1', retryable: true } }),
    })

    await wrapper.find('[data-test="log-line-summary"]').trigger('click')

    expect(wrapper.find('[data-test="log-line-fields"]').text()).toContain('{"code":"E1","retryable":true}')
  })
})
