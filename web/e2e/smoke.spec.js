import { test, expect } from '@playwright/test'

test('the demo web UI loads and accepts the placeholder token', async ({ page, context }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })
  await page.goto('/')
  // Authenticated shell renders the sidebar nav; the token-gate form
  // (rendered when unauthenticated) does not.
  await expect(page.locator('nav')).toBeVisible()
  await expect(page.locator('form')).toHaveCount(0)
})
