import { describe, it, expect } from 'vitest'
import { resolveFile } from './restoreRules'

describe('resolveFile', () => {
  it('is false when no rule matches', () => {
    expect(resolveFile([], 'web01', '/etc/hosts')).toBe(false)
  })

  it('is true for an exact host-specific include rule', () => {
    const rules = [{ path: '/etc/hosts', host: 'web01', include: true }]
    expect(resolveFile(rules, 'web01', '/etc/hosts')).toBe(true)
  })

  it('does not apply a host-specific rule to a different host', () => {
    const rules = [{ path: '/etc/hosts', host: 'web01', include: true }]
    expect(resolveFile(rules, 'db02', '/etc/hosts')).toBe(false)
  })

  it('inherits from the nearest covering host-agnostic ancestor folder rule', () => {
    const rules = [{ path: '/etc', host: null, include: true }]
    expect(resolveFile(rules, 'web01', '/etc/hosts')).toBe(true)
  })

  it('an exact host-specific exception overrides an ancestor folder rule', () => {
    const rules = [
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ]
    expect(resolveFile(rules, 'web01', '/etc/hosts')).toBe(false)
    expect(resolveFile(rules, 'db02', '/etc/hosts')).toBe(true)
  })

  it('uses the longest (most specific) matching ancestor folder rule', () => {
    const rules = [
      { path: '/var', host: null, include: true },
      { path: '/var/log', host: null, include: false },
    ]
    expect(resolveFile(rules, 'web01', '/var/log/access.log')).toBe(false)
    expect(resolveFile(rules, 'web01', '/var/lib/data.db')).toBe(true)
  })
})
