import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  timeout: 120_000,
  // The restore-cart suite seeds real backend state through a shared UI
  // flow and scenarios build on that shared fixture -- parallel workers
  // would race against the same backend data and each other's navigation.
  fullyParallel: false,
  workers: 1,
  reporter: 'list',
  use: {
    baseURL: 'http://localhost:8091',
    // This repo's test-hook convention is data-test="...", not
    // Playwright's default data-testid -- every getByTestId() call in
    // this suite depends on this.
    testIdAttribute: 'data-test',
    trace: 'retain-on-failure',
  },
})
