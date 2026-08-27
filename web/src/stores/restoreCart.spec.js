import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestoreCartStore } from './restoreCart'

describe('restoreCart store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('starts with no rules', () => {
    const cart = useRestoreCartStore()
    expect(cart.rules).toEqual([])
    expect(cart.hasSelections).toBe(false)
    expect(cart.entries).toEqual([])
  })

  it('toggleFile adds a rule and updates hasSelections/entries', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toEqual([{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }])
    expect(cart.hasSelections).toBe(true)
    expect(cart.entries).toEqual([{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }])
  })

  it('toggleFile twice returns to no rules', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toEqual([])
    expect(cart.hasSelections).toBe(false)
  })

  it('toggleFolder adds a wildcard rule', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')
    expect(cart.rules).toEqual([{ path: '/var', host: null, include: true, destPath: '/var' }])
    expect(cart.hasSelections).toBe(true)
  })

  it('entries excludes exception (include: false) rules', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/etc')
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toHaveLength(2)
    expect(cart.entries).toEqual([{ path: '/etc', host: null, include: true, destPath: '/etc' }])
  })

  it('toggleFile threads storeHost and size onto the created rule when passed', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', 'bwfs-1', 4096)
    expect(cart.rules).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts', storeHost: 'bwfs-1', size: 4096 },
    ])
  })

  it('setDestPath updates the matching rule and leaves others untouched', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    cart.toggleFolder('/var')

    cart.setDestPath({ host: 'web01', path: '/etc/hosts' }, '/etc/hosts.bak')

    expect(cart.rules).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts.bak' },
      { path: '/var', host: null, include: true, destPath: '/var' },
    ])
  })

  it('setDestPath on a folder rule updates its destPath', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')

    cart.setDestPath({ host: null, path: '/var' }, '/var_recovered')

    expect(cart.rules).toEqual([{ path: '/var', host: null, include: true, destPath: '/var_recovered' }])
  })

  it('setDestPath is a no-op when no rule matches', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')

    cart.setDestPath({ host: 'web02', path: '/nope' }, '/renamed')

    expect(cart.rules).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' },
    ])
  })

  it('toggleFile threads notBefore/notAfter onto the created rule when passed', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts', 'bwfs-1', 4096, 1000, 2000)
    expect(cart.rules).toEqual([
      {
        path: '/etc/hosts',
        host: 'web01',
        include: true,
        destPath: '/etc/hosts',
        storeHost: 'bwfs-1',
        size: 4096,
        notBefore: 1000,
        notAfter: 2000,
      },
    ])
  })

  it('toggleFolder threads notBefore/notAfter onto the created rule when passed', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var', 1000, 2000)
    expect(cart.rules).toEqual([
      { path: '/var', host: null, include: true, destPath: '/var', notBefore: 1000, notAfter: 2000 },
    ])
  })

  it('setVersionWindow updates the matching rule and leaves others untouched', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    cart.toggleFolder('/var')

    cart.setVersionWindow({ host: 'web01', path: '/etc/hosts' }, 1000, 1000)

    expect(cart.rules).toEqual([
      { path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts', notBefore: 1000, notAfter: 1000 },
      { path: '/var', host: null, include: true, destPath: '/var' },
    ])
  })

  it('setVersionWindow on a folder rule (host: null) updates its window', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')

    cart.setVersionWindow({ host: null, path: '/var' }, 500, 600)

    expect(cart.rules).toEqual([{ path: '/var', host: null, include: true, destPath: '/var', notBefore: 500, notAfter: 600 }])
  })

  it('setVersionWindow is a no-op when no rule matches', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')

    cart.setVersionWindow({ host: 'web02', path: '/nope' }, 1, 2)

    expect(cart.rules).toEqual([{ path: '/etc/hosts', host: 'web01', include: true, destPath: '/etc/hosts' }])
  })

  it('ensureFileSelected creates an exact rule for a file only covered by an ancestor folder rule', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')

    cart.ensureFileSelected('web01', '/var/lib/db/dump.sql', 'bwfs-1', 4096, 100, 100)

    expect(cart.rules).toEqual([
      { path: '/var', host: null, include: true, destPath: '/var' },
      {
        path: '/var/lib/db/dump.sql',
        host: 'web01',
        include: true,
        destPath: '/var/lib/db/dump.sql',
        storeHost: 'bwfs-1',
        size: 4096,
        notBefore: 100,
        notAfter: 100,
      },
    ])
  })

  it('ensureFolderSelected creates an exact rule for a folder only covered by an ancestor folder rule', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')

    cart.ensureFolderSelected('/var/lib/db', 100, 100)

    expect(cart.rules).toEqual([
      { path: '/var', host: null, include: true, destPath: '/var' },
      { path: '/var/lib/db', host: null, include: true, destPath: '/var/lib/db', notBefore: 100, notAfter: 100 },
    ])
  })

  it('removeEntry unsets a folder wildcard entry', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')
    cart.removeEntry({ path: '/var', host: null, include: true })
    expect(cart.rules).toEqual([])
  })

  it('removeEntry unsets a file entry', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    cart.removeEntry({ path: '/etc/hosts', host: 'web01', include: true })
    expect(cart.rules).toEqual([])
  })
})
