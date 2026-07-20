import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StatusMessage from './StatusMessage.vue'

describe('StatusMessage', () => {
  it('shows Loading when loading is true', () => {
    const wrapper = mount(StatusMessage, { props: { loading: true, error: null, empty: false } })
    expect(wrapper.text()).toBe('Loading...')
  })

  it('shows the error message when error is set', () => {
    const wrapper = mount(StatusMessage, { props: { loading: false, error: 'boom', empty: false } })
    expect(wrapper.text()).toBe('boom')
  })

  it('shows the empty text when empty is true', () => {
    const wrapper = mount(StatusMessage, {
      props: { loading: false, error: null, empty: true, emptyText: 'No clients enrolled yet.' },
    })
    expect(wrapper.text()).toBe('No clients enrolled yet.')
  })

  it('renders the default slot when none of loading/error/empty apply', () => {
    const wrapper = mount(StatusMessage, {
      props: { loading: false, error: null, empty: false },
      slots: { default: '<table><tbody><tr><td>row</td></tr></tbody></table>' },
    })
    expect(wrapper.find('td').text()).toBe('row')
  })

  it('prioritizes loading over error and empty', () => {
    const wrapper = mount(StatusMessage, { props: { loading: true, error: 'boom', empty: true } })
    expect(wrapper.text()).toBe('Loading...')
  })

  it('prioritizes error over empty', () => {
    const wrapper = mount(StatusMessage, { props: { loading: false, error: 'boom', empty: true } })
    expect(wrapper.text()).toBe('boom')
  })
})
