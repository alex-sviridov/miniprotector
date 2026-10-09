import { test as base, expect } from '@playwright/test'

export const AUTH_HEADERS = { Authorization: 'Bearer dev-placeholder-token-change-me' }

// trackPolicy lets a test (or a helper it calls) register a policy id as
// soon as it's known. Teardown deletes every registered id after the test
// finishes, whether it passed, failed, or threw -- Playwright always runs
// fixture teardown, so this replaces the need for a manual try/finally at
// every call site.
export const test = base.extend({
  trackPolicy: async ({ request }, use) => {
    const ids = []
    await use((id) => {
      ids.push(id)
    })
    for (const id of ids) {
      let resp
      try {
        resp = await request.delete(`/api/v1/policies/${id}`, { headers: AUTH_HEADERS })
      } catch (err) {
        console.warn(`cleanup: failed to delete policy ${id}: ${err.message}`)
        continue
      }
      if (!resp.ok()) {
        console.warn(`cleanup: failed to delete policy ${id}, status ${resp.status()}`)
      }
    }
  },
})

export { expect }
