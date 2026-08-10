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
    expect(cart.rules).toEqual([{ path: '/etc/hosts', host: 'web01', include: true }])
    expect(cart.hasSelections).toBe(true)
    expect(cart.entries).toEqual([{ path: '/etc/hosts', host: 'web01', include: true }])
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
    expect(cart.rules).toEqual([{ path: '/var', host: null, include: true }])
    expect(cart.hasSelections).toBe(true)
  })

  it('entries excludes exception (include: false) rules', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/etc')
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toHaveLength(2)
    expect(cart.entries).toEqual([{ path: '/etc', host: null, include: true }])
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
