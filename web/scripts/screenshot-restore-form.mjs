// Drives the running demo web UI to the Restore page with one real file
// staged, and saves a screenshot. Requires the demo stack to be up
// (`make demo-up`) and at least one file already backed up from `database`
// under /var/lib/dbdata, e.g.:
//   docker compose -f demo/docker-compose.yml exec database ./brfs /var/lib/dbdata --destination store:8080
//
// Usage: node web/scripts/screenshot-restore-form.mjs [output-path]

import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium, selectors } from '../node_modules/playwright-core/index.mjs'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const BASE_URL = process.env.MP_WEB_URL || 'http://localhost:8091'
const TOKEN = process.env.MP_API_TOKEN || 'dev-placeholder-token-change-me'
const OUT = path.resolve(process.argv[2] || path.join(__dirname, 'restore-form.png'))

// This repo's UI uses `data-test`, not Playwright's default `data-testid`
// (see web/playwright.config.js's `testIdAttribute`).
selectors.setTestIdAttribute('data-test')

const browser = await chromium.launch({ args: ['--no-sandbox'] })
const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
await context.addInitScript((token) => {
  localStorage.setItem('mp_api_token', token)
}, TOKEN)
const page = await context.newPage()

page.on('pageerror', (err) => console.error('PAGE ERROR:', err.message))

await page.goto(`${BASE_URL}/catalog`, { waitUntil: 'networkidle' })
await page.getByTestId('crumb-home').click()
await page.getByText('//', { exact: true }).click()

for (const segment of ['var', 'lib', 'dbdata']) {
  await page.getByText(`${segment}/`, { exact: true }).click()
}

const fileCheckbox = page.getByTestId('file-checkbox-database:/var/lib/dbdata/dump.sql')
await fileCheckbox.waitFor({ state: 'visible', timeout: 15000 })
await fileCheckbox.click()

await page.getByRole('link', { name: 'Restore' }).click()
await page.getByTestId('restore-row-database:/var/lib/dbdata/dump.sql').waitFor({ timeout: 10000 })

const destSelect = page.getByTestId('destination-select')
await destSelect.waitFor({ timeout: 10000 })
await destSelect.selectOption({ index: 1 })

await page.getByTestId('overwrite-checkbox').check()

await page.screenshot({ path: OUT, fullPage: true })
console.log('Screenshot saved to', OUT)

await browser.close()
