// web/src/utils/storeAddress.spec.js
import { describe, it, expect } from 'vitest'
import { resolveStoreAddress } from './storeAddress'

describe('resolveStoreAddress', () => {
  it('returns host:port from the storage policy whose checkin hostname matches', () => {
    const storagePolicies = [
      { id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 100 }] },
      { id: 's2', port: 9090, checkins: [{ hostname: 'store-b', last_seen_at: 200 }] },
    ]
    expect(resolveStoreAddress(storagePolicies, 'store-b')).toBe('store-b:9090')
  })

  it('returns null when no storage policy has a matching checkin', () => {
    const storagePolicies = [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 100 }] }]
    expect(resolveStoreAddress(storagePolicies, 'store-missing')).toBeNull()
  })

  it('treats a storage policy with no checkins yet as not matching', () => {
    const storagePolicies = [{ id: 's1', port: 8080, checkins: [] }]
    expect(resolveStoreAddress(storagePolicies, 'store-a')).toBeNull()
  })

  it('treats a storage policy with an absent checkins field as not matching', () => {
    const storagePolicies = [{ id: 's1', port: 8080 }]
    expect(resolveStoreAddress(storagePolicies, 'store-a')).toBeNull()
  })
})
