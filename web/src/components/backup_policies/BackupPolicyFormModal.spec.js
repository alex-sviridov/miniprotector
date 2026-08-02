import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import BackupPolicyFormModal from './BackupPolicyFormModal.vue'

describe('BackupPolicyFormModal', () => {
  it('renders empty fields in create mode', () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    expect(wrapper.find('input[name="name"]').element.value).toBe('')
    expect(wrapper.find('input[name="rpo"]').element.value).toBe('')
    expect(wrapper.find('input[name="destination"]').element.value).toBe('')
    expect(wrapper.findAll('[data-test="hostname-input"]')).toHaveLength(0)
  })

  it('pre-fills fields from the policy prop in edit mode', () => {
    const wrapper = mount(BackupPolicyFormModal, {
      props: {
        policy: {
          id: 'p1',
          name: 'nightly-db-backup',
          rpo: '1h',
          destination: 'store:8080',
          client_filters: { hostnames: ['database'], labels: { env: 'prod' } },
          object_filters: [{ id: 'f1', path: '/var/lib/dbdata', include: ['*.sql'], exclude: [] }],
          backup_window: ['0 2 * * *'],
        },
      },
    })
    expect(wrapper.find('input[name="name"]').element.value).toBe('nightly-db-backup')
    expect(wrapper.find('input[name="rpo"]').element.value).toBe('1h')
    expect(wrapper.find('input[name="destination"]').element.value).toBe('store:8080')
    expect(wrapper.find('[data-test="hostname-input"]').element.value).toBe('database')
    expect(wrapper.find('[data-test="label-key-input"]').element.value).toBe('env')
    expect(wrapper.find('[data-test="label-value-input"]').element.value).toBe('prod')
    expect(wrapper.find('[data-test="filter-path-input"]').element.value).toBe('/var/lib/dbdata')
    expect(wrapper.find('[data-test="filter-include-input"]').element.value).toBe('*.sql')
    expect(wrapper.find('[data-test="window-input"]').element.value).toBe('0 2 * * *')
  })

  it('emits close when Cancel is clicked', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('[data-test="backup-policy-cancel"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on Escape', () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('does not emit save when the name is blank', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('emits save with the built payload on valid submit', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('nightly-db-backup')
    await wrapper.find('input[name="rpo"]').setValue('1h')
    await wrapper.find('input[name="destination"]').setValue('store:8080')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({
      name: 'nightly-db-backup',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '1h',
      backup_window: [],
      destination: 'store:8080',
    })
  })

  it('adds and removes hostname rows, sending only non-empty trimmed values', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    const hostnameInputs = wrapper.findAll('[data-test="hostname-input"]')
    await hostnameInputs[0].setValue('database')
    await hostnameInputs[1].setValue('  ')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({ client_filters: { hostnames: ['database'], labels: {} } })
    )
  })

  it('adds a label row and sends it as a key/value map', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="label-add"]').trigger('click')
    await wrapper.find('[data-test="label-key-input"]').setValue('env')
    await wrapper.find('[data-test="label-value-input"]').setValue('prod')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({ client_filters: { hostnames: [], labels: { env: 'prod' } } })
    )
  })

  it('adds a backup window row', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="window-add"]').trigger('click')
    await wrapper.find('[data-test="window-input"]').setValue('0 2 * * *')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(expect.objectContaining({ backup_window: ['0 2 * * *'] }))
  })

  it('adds an object filter and splits comma-separated include/exclude into arrays', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="filter-add"]').trigger('click')
    await wrapper.find('[data-test="filter-path-input"]').setValue('/var/lib/dbdata')
    await wrapper.find('[data-test="filter-include-input"]').setValue('*.sql, *.dump')
    await wrapper.find('[data-test="filter-exclude-input"]').setValue('*.tmp')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({
        object_filters: [{ path: '/var/lib/dbdata', include: ['*.sql', '*.dump'], exclude: ['*.tmp'] }],
      })
    )
  })

  it('removes a row via its remove button', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="hostname-add"]').trigger('click')
    await wrapper.find('[data-test="hostname-input"]').setValue('database')
    await wrapper.find('[data-test="hostname-remove"]').trigger('click')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({ client_filters: { hostnames: [], labels: {} } })
    )
  })

  it('shows the server error message', () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null, serverError: 'name is required' } })
    expect(wrapper.text()).toContain('name is required')
  })

  it('emits run-now with the built payload when clicked with a valid form', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('input[name="name"]').setValue('oneoff')
    await wrapper.find('input[name="destination"]').setValue('store:8080')
    await wrapper.find('[data-test="backup-policy-run-now"]').trigger('click')

    expect(wrapper.emitted('run-now')).toHaveLength(1)
    expect(wrapper.emitted('run-now')[0][0]).toEqual({
      name: 'oneoff',
      client_filters: { hostnames: [], labels: {} },
      object_filters: [],
      rpo: '',
      backup_window: [],
      destination: 'store:8080',
    })
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('does not emit run-now when the name is blank', async () => {
    const wrapper = mount(BackupPolicyFormModal, { props: { policy: null } })
    await wrapper.find('[data-test="backup-policy-run-now"]').trigger('click')
    expect(wrapper.emitted('run-now')).toBeUndefined()
  })
})
