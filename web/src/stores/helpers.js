export async function withRequest(store, fn, { rethrow = true, loadingKey = 'loading', errorKey = 'error' } = {}) {
  store[loadingKey] = true
  store[errorKey] = null
  try {
    return await fn()
  } catch (err) {
    store[errorKey] = err.message
    if (rethrow) throw err
  } finally {
    store[loadingKey] = false
  }
}
