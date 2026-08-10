import { describe, it, expect } from 'vitest'
import { filterResolved, collapseToLatestVersion, groupByStore } from './restoreResolve'

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

describe('collapseToLatestVersion', () => {
  function version(overrides) {
    return {
      id: 1,
      source_host: 'database',
      path: '/var/lib/dbdata/data.db',
      store_host: 'store-a',
      store_created_at: 1752400000,
      ...overrides,
    }
  }

  it('returns an empty array for no entries', () => {
    expect(collapseToLatestVersion([])).toEqual([])
  })

  it('collapses many versions of one file to the single newest-version row', () => {
    const oldest = version({ id: 10, store_created_at: 1752200000 })
    const middle = version({ id: 11, store_created_at: 1752300000 })
    const newest = version({ id: 12, store_created_at: 1752400000 })
    expect(collapseToLatestVersion([oldest, newest, middle])).toEqual([newest])
  })

  it('keeps the newest version even when versions of one file live on different stores', () => {
    const older = version({ id: 20, store_created_at: 1752300000, store_host: 'store-a' })
    const newer = version({ id: 21, store_created_at: 1752400000, store_host: 'store-b' })
    const collapsed = collapseToLatestVersion([older, newer])
    expect(collapsed).toEqual([newer])
    expect(collapsed[0].store_host).toBe('store-b')
  })

  it('keeps the same path on different source hosts as separate files', () => {
    const a = version({ id: 30, source_host: 'web01', path: '/etc/hosts' })
    const b = version({ id: 31, source_host: 'web02', path: '/etc/hosts' })
    expect(collapseToLatestVersion([a, b])).toEqual([a, b])
  })

  it('keeps distinct paths on one source host as separate files', () => {
    const a = version({ id: 40, path: '/var/lib/dbdata/dump.sql' })
    const b = version({ id: 41, path: '/var/lib/dbdata/schema.sql' })
    expect(collapseToLatestVersion([a, b])).toEqual([a, b])
  })

  it('leaves a group that later feeds groupByStore with one file per path', () => {
    const entries = [
      version({ id: 50, path: '/a', store_created_at: 1752300000, store_host: 'store-a' }),
      version({ id: 51, path: '/a', store_created_at: 1752400000, store_host: 'store-b' }),
      version({ id: 52, path: '/b', store_created_at: 1752400000, store_host: 'store-b' }),
    ]
    expect(groupByStore(collapseToLatestVersion(entries))).toEqual([
      {
        storeHost: 'store-b',
        files: [
          { sourceHost: 'database', path: '/a' },
          { sourceHost: 'database', path: '/b' },
        ],
      },
    ])
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
