import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DirectoryPathBar from './DirectoryPathBar.vue'

describe('DirectoryPathBar', () => {
  it('shows only Home when currentPath is null', () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: null } })
    expect(wrapper.text()).toBe('Home')
  })

  it('renders root-to-leaf crumbs for a nested unix path, only the last one non-clickable', () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: '/var/lib/dbdata' } })
    const clickable = wrapper.findAll('[data-test="crumb"]').map((c) => c.text())
    expect(clickable).toEqual(['/', 'var', 'lib'])
    expect(wrapper.find('[data-test="crumb-current"]').text()).toBe('dbdata')
  })

  it('renders a windows drive-rooted path correctly', () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: 'C:\\Users\\alice\\Documents' } })
    const clickable = wrapper.findAll('[data-test="crumb"]').map((c) => c.text())
    expect(clickable).toEqual(['C:\\', 'Users', 'alice'])
    expect(wrapper.find('[data-test="crumb-current"]').text()).toBe('Documents')
  })

  it('emits navigate with null when Home is clicked', async () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: '/var/lib' } })
    await wrapper.find('[data-test="crumb-home"]').trigger('click')
    expect(wrapper.emitted('navigate')).toEqual([[null]])
  })

  it('emits navigate with the crumb path when an intermediate crumb is clicked', async () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: '/var/lib/dbdata' } })
    const crumbs = wrapper.findAll('[data-test="crumb"]')
    await crumbs[1].trigger('click') // "var"
    expect(wrapper.emitted('navigate')).toEqual([['/var']])
  })
})
