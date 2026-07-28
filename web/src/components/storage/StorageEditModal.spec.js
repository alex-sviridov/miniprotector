import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import StorageEditModal from './StorageEditModal.vue'

describe('StorageEditModal', () => {
  it('renders empty fields in create mode', () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    expect(wrapper.find('[data-test="storage-name-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-hostname-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-port-input"]').element.value).toBe('')
    expect(wrapper.find('[data-test="storage-path-input"]').element.value).toBe('')
  })

  it('pre-fills fields from the policy prop in edit mode', () => {
    const wrapper = mount(StorageEditModal, {
      props: {
        policy: {
          id: 's1',
          name: 'east-1-storage',
          hostname: 'storage-east-1.internal',
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage"}',
        },
      },
    })
    expect(wrapper.find('[data-test="storage-name-input"]').element.value).toBe('east-1-storage')
    expect(wrapper.find('[data-test="storage-hostname-input"]').element.value).toBe('storage-east-1.internal')
    expect(wrapper.find('[data-test="storage-port-input"]').element.value).toBe('9400')
    expect(wrapper.find('[data-test="storage-path-input"]').element.value).toBe('/data/storage')
  })

  it('emits close when the Cancel button is clicked', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-cancel"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('emits close on Escape', () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('does not emit save when required fields are blank', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.text()).toContain('required')
  })

  it('emits save with the built payload on valid submit', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-name-input"]').setValue('east-1-storage')
    await wrapper.find('[data-test="storage-hostname-input"]').setValue('storage-east-1.internal')
    await wrapper.find('[data-test="storage-port-input"]').setValue('9400')
    await wrapper.find('[data-test="storage-path-input"]').setValue('/data/storage')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    expect(wrapper.emitted('save')[0][0]).toEqual({
      name: 'east-1-storage',
      hostname: 'storage-east-1.internal',
      port: 9400,
      config: JSON.stringify({ backend: 'filesystem', root: '/data/storage' }),
      client_filters: { hostnames: [], labels: {} },
    })
  })

  it('preserves unknown config keys when editing and saving', async () => {
    const wrapper = mount(StorageEditModal, {
      props: {
        policy: {
          id: 's1',
          name: 'east-1-storage',
          hostname: 'storage-east-1.internal',
          port: 9400,
          config: '{"backend": "filesystem", "root": "/data/storage", "compression": "zstd"}',
        },
      },
    })
    await wrapper.find('[data-test="storage-name-input"]').setValue('east-1-storage-renamed')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toHaveLength(1)
    const payload = wrapper.emitted('save')[0][0]
    expect(JSON.parse(payload.config)).toEqual({
      backend: 'filesystem',
      root: '/data/storage',
      compression: 'zstd',
    })
  })

  it('does not throw and falls back to defaults when config is the literal null', () => {
    const wrapper = mount(StorageEditModal, {
      props: {
        policy: {
          id: 's1',
          name: 'east-1-storage',
          hostname: 'storage-east-1.internal',
          port: 9400,
          config: 'null',
        },
      },
    })
    expect(wrapper.find('[data-test="storage-type-select"]').element.value).toBe('filesystem')
    expect(wrapper.find('[data-test="storage-path-input"]').element.value).toBe('')
  })

  it('rejects a port outside 1-65535', async () => {
    const wrapper = mount(StorageEditModal, { props: { policy: null } })
    await wrapper.find('[data-test="storage-name-input"]').setValue('x')
    await wrapper.find('[data-test="storage-hostname-input"]').setValue('h')
    await wrapper.find('[data-test="storage-port-input"]').setValue('70000')
    await wrapper.find('[data-test="storage-path-input"]').setValue('/data')
    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.text()).toContain('port')
  })
})
