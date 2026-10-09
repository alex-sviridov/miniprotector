import { test, expect } from '@playwright/test'

// Enrolled nodes refresh their operating certificate every 15 minutes, and
// each refresh makes issuer record the bootstrap certificate's expiry, so by
// the time the demo lab is up every client shows a healthy "Nd left" badge
// (a fresh bootstrap certificate is about 90 days from expiry).
test('clients list and detail show the bootstrap certificate expiry', async ({ page, context }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  await page.goto('/clients')
  const row = page.locator('tbody tr', { hasText: 'database' })
  const badge = row.locator('[data-test="bootstrap-cert-badge"]')
  await expect(badge).toHaveText(/^\d+d left$/)
  await expect(badge).toHaveClass(/bg-emerald-50/)

  await row.getByRole('link', { name: 'database' }).click()
  await expect(page.locator('[data-test="bootstrap-cert-badge"]')).toHaveText(/^\d+d left$/)
  await expect(page.locator('[data-test="bootstrap-cert-expiry"]')).toContainText('expires')
  await expect(page.locator('[data-test="bootstrap-renewal"]')).toContainText(/Renewing normally|No report yet|Unknown/)
})
