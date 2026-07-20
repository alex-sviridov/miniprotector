import { describe, it, expect } from 'vitest'
import { withRequest } from './helpers'

function fakeStore() {
  return { loading: false, error: null, logsLoading: false, logsError: null }
}

describe('withRequest', () => {
  it('sets loading true during the call and false after success, clearing error, and returns the result', async () => {
    const store = fakeStore()
    let loadingDuringCall
    const result = await withRequest(store, async () => {
      loadingDuringCall = store.loading
      return 'ok'
    })
    expect(loadingDuringCall).toBe(true)
    expect(store.loading).toBe(false)
    expect(store.error).toBeNull()
    expect(result).toBe('ok')
  })

  it('records the error message and rethrows by default on failure', async () => {
    const store = fakeStore()
    await expect(withRequest(store, () => Promise.reject(new Error('boom')))).rejects.toThrow('boom')
    expect(store.error).toBe('boom')
    expect(store.loading).toBe(false)
  })

  it('records the error but swallows it when rethrow is false', async () => {
    const store = fakeStore()
    await expect(
      withRequest(store, () => Promise.reject(new Error('boom')), { rethrow: false })
    ).resolves.toBeUndefined()
    expect(store.error).toBe('boom')
  })

  it('uses a custom loadingKey/errorKey pair instead of the defaults', async () => {
    const store = fakeStore()
    await withRequest(store, () => Promise.reject(new Error('boom')), {
      rethrow: false,
      loadingKey: 'logsLoading',
      errorKey: 'logsError',
    })
    expect(store.logsError).toBe('boom')
    expect(store.loading).toBe(false)
    expect(store.error).toBeNull()
  })

  it('clears a stale error at the start of a new call even before it resolves', () => {
    const store = fakeStore()
    store.error = 'stale error from an earlier action'
    withRequest(store, () => new Promise(() => {}))
    expect(store.error).toBeNull()
  })
})
