import { describe, it, expect } from 'vitest'
import { splitPath, pathCrumbs } from './pathSplit'

describe('splitPath', () => {
  it('splits a nested unix path', () => {
    expect(splitPath('/var/lib/dbdata')).toEqual({ parentPath: '/var/lib', name: 'dbdata' })
  })

  it('splits a unix root-level path', () => {
    expect(splitPath('/data.db')).toEqual({ parentPath: '/', name: 'data.db' })
  })

  it('splits a windows nested path', () => {
    expect(splitPath('C:\\Users\\alice\\Documents')).toEqual({ parentPath: 'C:\\Users\\alice', name: 'Documents' })
  })

  it('splits a windows drive-root path', () => {
    expect(splitPath('C:\\file.txt')).toEqual({ parentPath: 'C:\\', name: 'file.txt' })
  })

  it('returns empty parent and name for empty input', () => {
    expect(splitPath('')).toEqual({ parentPath: '', name: '' })
  })
})

describe('pathCrumbs', () => {
  it('returns root-first crumbs for a nested unix path', () => {
    expect(pathCrumbs('/var/lib/dbdata')).toEqual([
      { path: '/', name: '/' },
      { path: '/var', name: 'var' },
      { path: '/var/lib', name: 'lib' },
      { path: '/var/lib/dbdata', name: 'dbdata' },
    ])
  })

  it('returns a single crumb for the unix root itself', () => {
    expect(pathCrumbs('/')).toEqual([{ path: '/', name: '/' }])
  })

  it('returns root-first crumbs for a windows drive path', () => {
    expect(pathCrumbs('C:\\Users\\alice')).toEqual([
      { path: 'C:\\', name: 'C:\\' },
      { path: 'C:\\Users', name: 'Users' },
      { path: 'C:\\Users\\alice', name: 'alice' },
    ])
  })
})
