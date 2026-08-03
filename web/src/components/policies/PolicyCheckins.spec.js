import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import PolicyCheckins from './PolicyCheckins.vue'

describe('PolicyCheckins', () => {
  it('renders checkins sorted by last_seen_at descending', () => {
    const wrapper = mount(PolicyCheckins, {
      props: {
        checkins: [
          { hostname: 'web-01', last_seen_at: 1752400000 },
          { hostname: 'web-02', last_seen_at: 1752400500 },
        ],
        loading: false,
        error: null,
      },
    })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('web-02')
    expect(rows[1].text()).toContain('web-01')
  })

  it('shows an empty-state message when there are no checkins', () => {
    const wrapper = mount(PolicyCheckins, { props: { checkins: [], loading: false, error: null } })
    expect(wrapper.text()).toContain('No hosts have checked in yet.')
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('emits refresh when the Refresh button is clicked', async () => {
    const wrapper = mount(PolicyCheckins, { props: { checkins: [], loading: false, error: null } })
    await wrapper.find('[data-test="checkins-refresh"]').trigger('click')
    expect(wrapper.emitted('refresh')).toHaveLength(1)
  })

  it('disables the Refresh button while loading', () => {
    const wrapper = mount(PolicyCheckins, { props: { checkins: [], loading: true, error: null } })
    expect(wrapper.find('[data-test="checkins-refresh"]').attributes('disabled')).toBeDefined()
  })

  it('shows the error message without clearing existing rows', () => {
    const wrapper = mount(PolicyCheckins, {
      props: {
        checkins: [{ hostname: 'web-01', last_seen_at: 1752400000 }],
        loading: false,
        error: 'network error',
      },
    })
    expect(wrapper.find('[data-test="checkins-error"]').text()).toBe('network error')
    expect(wrapper.text()).toContain('web-01')
  })
})
