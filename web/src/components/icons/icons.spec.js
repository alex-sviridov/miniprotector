import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import IconClients from './IconClients.vue'
import IconCatalog from './IconCatalog.vue'
import IconPolicies from './IconPolicies.vue'
import IconStorage from './IconStorage.vue'
import IconJobs from './IconJobs.vue'

const icons = { IconClients, IconCatalog, IconPolicies, IconStorage, IconJobs }

describe('icons', () => {
  for (const [name, component] of Object.entries(icons)) {
    it(`${name} renders a single 24x24 svg and forwards a passed class`, () => {
      const wrapper = mount(component, { attrs: { class: 'w-4 h-4' } })
      const svg = wrapper.find('svg')
      expect(svg.exists()).toBe(true)
      expect(svg.attributes('viewBox')).toBe('0 0 24 24')
      expect(svg.classes()).toContain('w-4')
      expect(svg.classes()).toContain('h-4')
    })
  }
})
