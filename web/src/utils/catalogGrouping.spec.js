import { describe, it, expect } from 'vitest'
import { groupEntriesByFile } from './catalogGrouping'

function entry(overrides) {
  return {
    id: 1,
    source_host: 'database',
    store_host: 'bwfs-east',
    job_id: 'backup:daily-db-backup:1',
    object_id: 'fs://database:f:/var/lib/dbdata/data.db:1752400000',
    ctime: 1752400000,
    store_created_at: 1752400000,
    received_at: 1752400010,
    path: '/var/lib/dbdata/data.db',
    size: 8192,
    mode: '-rw-r--r--',
    owner: 999,
    group: 999,
    mod_time: 1752400000,
    ...overrides,
  }
}

describe('groupEntriesByFile', () => {
  it('returns an empty array for no entries', () => {
    expect(groupEntriesByFile([])).toEqual([])
  })

  it('groups entries sharing source_host and path into one group', () => {
    const a = entry({ id: 1, store_created_at: 1752400000 })
    const b = entry({ id: 2, store_created_at: 1752300000 })
    const groups = groupEntriesByFile([a, b])
    expect(groups).toHaveLength(1)
    expect(groups[0].sourceHost).toBe('database')
    expect(groups[0].path).toBe('/var/lib/dbdata/data.db')
    expect(groups[0].versions).toHaveLength(2)
  })

  it('keeps entries with different paths or source hosts in separate groups', () => {
    const a = entry({ id: 1, path: '/var/lib/dbdata/data.db' })
    const b = entry({ id: 2, path: '/etc/passwd' })
    const c = entry({ id: 3, source_host: 'webserver' })
    const groups = groupEntriesByFile([a, b, c])
    expect(groups).toHaveLength(3)
  })

  it('sorts versions newest-first by store_created_at and picks the newest as representative', () => {
    const older = entry({ id: 1, store_created_at: 1752300000, size: 8004 })
    const newer = entry({ id: 2, store_created_at: 1752400000, size: 8192 })
    const groups = groupEntriesByFile([older, newer])
    expect(groups[0].versions.map((v) => v.id)).toEqual([2, 1])
    expect(groups[0].representative).toBe(newer)
  })

  it('a single-version file has a one-element versions array equal to its representative', () => {
    const only = entry({ id: 1 })
    const groups = groupEntriesByFile([only])
    expect(groups[0].versions).toEqual([only])
    expect(groups[0].representative).toBe(only)
  })
})
