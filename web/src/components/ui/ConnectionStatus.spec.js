import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ConnectionStatus from './ConnectionStatus.vue'

describe('ConnectionStatus', () => {
  it('renders the label for each known status', () => {
    for (const [status, label] of [
      ['live', 'Live'],
      ['connecting', 'Connecting…'],
      ['reconnecting', 'Reconnecting…'],
      ['polling', 'Live updates unavailable — refreshing every 10s'],
      ['finished', 'Finished'],
    ]) {
      const wrapper = mount(ConnectionStatus, { props: { status } })
      expect(wrapper.text()).toContain(label)
    }
  })
})
