import { describe, it, expect } from 'vitest'
import { filterResolved, dedupeById, groupByStore } from './restoreResolve'

describe('filterResolved', () => {
  it('keeps an entry covered by a folder wildcard rule', () => {
    const rules = [{ path: '/var/lib/dbdata', host: null, include: true }]
    const entries = [{ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' }]
    expect(filterResolved(rules, entries)).toEqual(entries)
  })

  it('drops an entry only sharing a path prefix as a substring, not a real path segment', () => {
    const rules = [{ path: '/var/lib/dbdata', host: null, include: true }]
    const entries = [{ id: 1, source_host: 'database', path: '/var/lib/dbdata2/other.log', store_host: 'store-a' }]
    expect(filterResolved(rules, entries)).toEqual([])
  })

  it('drops an entry excluded by a more specific exception rule', () => {
    const rules = [
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ]
    const entries = [
      { id: 1, source_host: 'web01', path: '/etc/hosts', store_host: 'store-a' },
      { id: 2, source_host: 'web01', path: '/etc/passwd', store_host: 'store-a' },
    ]
    expect(filterResolved(rules, entries)).toEqual([entries[1]])
  })

  it('keeps a file rule scoped to its exact host, drops the same path from a different host', () => {
    const rules = [{ path: '/etc/hosts', host: 'web01', include: true }]
    const entries = [
      { id: 1, source_host: 'web01', path: '/etc/hosts', store_host: 'store-a' },
      { id: 2, source_host: 'web02', path: '/etc/hosts', store_host: 'store-a' },
    ]
    expect(filterResolved(rules, entries)).toEqual([entries[0]])
  })
})

describe('dedupeById', () => {
  it('drops repeat entries with the same id, keeping the first occurrence', () => {
    const a = { id: 1, path: '/a' }
    const b = { id: 1, path: '/a' }
    const c = { id: 2, path: '/b' }
    expect(dedupeById([a, b, c])).toEqual([a, c])
  })
})

describe('groupByStore', () => {
  it('groups entries by store_host', () => {
    const entries = [
      { id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' },
      { id: 2, source_host: 'web01', path: '/etc/hosts', store_host: 'store-b' },
    ]
    expect(groupByStore(entries)).toEqual([
      { storeHost: 'store-a', files: [{ sourceHost: 'database', path: '/var/lib/dbdata/dump.sql' }] },
      { storeHost: 'store-b', files: [{ sourceHost: 'web01', path: '/etc/hosts' }] },
    ])
  })

  it('splits one source host across two stores into two groups', () => {
    const entries = [
      { id: 1, source_host: 'database', path: '/a', store_host: 'store-a' },
      { id: 2, source_host: 'database', path: '/b', store_host: 'store-b' },
    ]
    expect(groupByStore(entries)).toEqual([
      { storeHost: 'store-a', files: [{ sourceHost: 'database', path: '/a' }] },
      { storeHost: 'store-b', files: [{ sourceHost: 'database', path: '/b' }] },
    ])
  })

  it('sorts groups by storeHost and files within a group by sourceHost then path', () => {
    const entries = [
      { id: 1, source_host: 'web02', path: '/b', store_host: 'store-b' },
      { id: 2, source_host: 'web01', path: '/z', store_host: 'store-a' },
      { id: 3, source_host: 'web01', path: '/a', store_host: 'store-a' },
    ]
    expect(groupByStore(entries)).toEqual([
      {
        storeHost: 'store-a',
        files: [
          { sourceHost: 'web01', path: '/a' },
          { sourceHost: 'web01', path: '/z' },
        ],
      },
      { storeHost: 'store-b', files: [{ sourceHost: 'web02', path: '/b' }] },
    ])
  })
})
