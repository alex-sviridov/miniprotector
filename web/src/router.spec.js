import { describe, it, expect } from 'vitest'
import { router } from './router'

const EXPECTED_NAMES = [
  'home',
  'clients',
  'client-new',
  'client-detail',
  'catalog',
  'policies',
  'policy-detail',
  'storage',
  'storage-detail',
  'jobs',
  'job-detail',
]

describe('router', () => {
  it('gives every route a unique, expected name', () => {
    const names = router.getRoutes().map((r) => r.name)
    expect(new Set(names).size).toBe(names.length)
    expect(names.sort()).toEqual([...EXPECTED_NAMES].sort())
  })

  it('lazily resolves each route to its view component', async () => {
    for (const route of router.getRoutes()) {
      const resolved = await route.components.default()
      expect(resolved.default).toBeDefined()
    }
  })
})
