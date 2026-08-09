import { describe, it, expect } from 'vitest'
import { resolveFile, resolveFolderState } from './restoreRules'

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

describe('resolveFolderState', () => {
  it('is unchecked when nothing covers the path and nothing sits under it', () => {
    expect(resolveFolderState([], '/etc')).toBe('unchecked')
  })

  it('is checked when a rule fully covers the path with nothing underneath', () => {
    const rules = [{ path: '/etc', host: null, include: true }]
    expect(resolveFolderState(rules, '/etc')).toBe('checked')
  })

  it('is checked when covered by an ancestor rule, with nothing underneath', () => {
    const rules = [{ path: '/', host: null, include: true }]
    expect(resolveFolderState(rules, '/etc')).toBe('checked')
  })

  it('is indeterminate when a nested exception exists under a covering rule', () => {
    const rules = [
      { path: '/var', host: null, include: true },
      { path: '/var/log', host: null, include: false },
    ]
    expect(resolveFolderState(rules, '/var')).toBe('indeterminate')
  })

  it('is indeterminate when a file below is individually selected without covering the folder', () => {
    const rules = [{ path: '/var/log/access.log', host: 'web01', include: true }]
    expect(resolveFolderState(rules, '/var/log')).toBe('indeterminate')
    expect(resolveFolderState(rules, '/var')).toBe('indeterminate')
  })

  it('is indeterminate even when its own exact rule excludes it, if something nested re-includes', () => {
    const rules = [
      { path: '/var', host: null, include: true },
      { path: '/var/log', host: null, include: false },
      { path: '/var/log/nginx', host: null, include: true },
    ]
    expect(resolveFolderState(rules, '/var/log')).toBe('indeterminate')
  })

  it('is unaffected by sibling rules', () => {
    const rules = [{ path: '/home', host: null, include: true }]
    expect(resolveFolderState(rules, '/etc')).toBe('unchecked')
  })
})
