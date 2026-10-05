import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import RetentionFormModal from './RetentionFormModal.vue'

const existing = {
  id: 'r1',
  name: 'keep-logs',
  client_filters: { hostnames: ['web-*'], labels: { env: 'prod' } },
  retention: { backup_type: 'filesystem', path: '/var/log', include: ['*.log'], keep_seconds: 7 * 86400, priority: 2 },
}

const find = (wrapper, name) => wrapper.find(`[data-test="${name}"]`)

async function fillValid(wrapper) {
  await find(wrapper, 'retention-name-input').setValue('keep-logs')
  await find(wrapper, 'retention-path-input').setValue('/var/log')
}

describe('RetentionFormModal', () => {
  it('renders defaults in create mode', () => {
    const wrapper = mount(RetentionFormModal, { props: { policy: null } })
    expect(wrapper.text()).toContain('New Retention Rule')
    expect(find(wrapper, 'retention-name-input').element.value).toBe('')
    expect(find(wrapper, 'retention-path-input').element.value).toBe('')
    expect(find(wrapper, 'retention-keep-days-input').element.value).toBe('30')
    expect(find(wrapper, 'retention-keep-forever').element.checked).toBe(false)
    expect(find(wrapper, 'retention-backup-type').element.value).toBe('filesystem')
  })

  it('pre-fills from the policy in edit mode', () => {
    const wrapper = mount(RetentionFormModal, { props: { policy: existing } })
    expect(wrapper.text()).toContain('Edit Retention Rule')
    expect(find(wrapper, 'retention-name-input').element.value).toBe('keep-logs')
    expect(find(wrapper, 'retention-path-input').element.value).toBe('/var/log')
    expect(find(wrapper, 'retention-keep-days-input').element.value).toBe('7')
    expect(find(wrapper, 'hostname-input').element.value).toBe('web-*')
    expect(find(wrapper, 'label-key-input').element.value).toBe('env')
    expect(find(wrapper, 'label-value-input').element.value).toBe('prod')
    expect(find(wrapper, 'include-input').element.value).toBe('*.log')
  })

  it('does not emit save and shows field errors when invalid', async () => {
    const wrapper = mount(RetentionFormModal, { props: { policy: null } })
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toBeUndefined()
    expect(find(wrapper, 'retention-error-name').text()).toMatch(/required/i)
    expect(find(wrapper, 'retention-error-path').text()).toMatch(/required/i)
  })

  it('rejects a relative path, a slash in an include glob, and a bad day count', async () => {
    const wrapper = mount(RetentionFormModal, { props: { policy: null } })
    await find(wrapper, 'retention-name-input').setValue('x')
    await find(wrapper, 'retention-path-input').setValue('var/log')
    await find(wrapper, 'include-add').trigger('click')
    await find(wrapper, 'include-input').setValue('sub/*.log')
    await find(wrapper, 'retention-keep-days-input').setValue('0')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toBeUndefined()
    expect(find(wrapper, 'retention-error-path').text()).toMatch(/absolute/i)
    expect(find(wrapper, 'retention-error-include').text()).toContain('/')
    expect(find(wrapper, 'retention-error-keepDays').exists()).toBe(true)
  })

  it('emits the API payload when valid', async () => {
    const wrapper = mount(RetentionFormModal, { props: { policy: null } })
    await fillValid(wrapper)
    await find(wrapper, 'hostname-add').trigger('click')
    await find(wrapper, 'hostname-input').setValue('web-*')
    await find(wrapper, 'include-add').trigger('click')
    await find(wrapper, 'include-input').setValue('*.log')
    await find(wrapper, 'retention-keep-days-input').setValue('14')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({
      name: 'keep-logs',
      client_filters: { hostnames: ['web-*'], labels: {} },
      retention: { backup_type: 'filesystem', path: '/var/log', include: ['*.log'], keep_seconds: 14 * 86400 },
    })
  })

  it('"Keep forever" disables the days input and sends keep_seconds 0', async () => {
    const wrapper = mount(RetentionFormModal, { props: { policy: null } })
    await fillValid(wrapper)
    await find(wrapper, 'retention-keep-forever').setValue(true)

    expect(find(wrapper, 'retention-keep-days-input').element.disabled).toBe(true)
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')[0][0].retention.keep_seconds).toBe(0)
  })

  it('shows the server error', () => {
    const wrapper = mount(RetentionFormModal, { props: { policy: null, serverError: 'backup_type "x" is not supported' } })
    expect(wrapper.text()).toContain('backup_type "x" is not supported')
  })

  it('emits close on Cancel and on Escape', async () => {
    const wrapper = mount(RetentionFormModal, { props: { policy: null } })
    await find(wrapper, 'retention-cancel').trigger('click')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(2)
  })
})
