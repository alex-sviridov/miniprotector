import { test, expect } from '@playwright/test'
import { seedRestoreCartCatalogData } from './helpers/policySeeding.js'

test.describe.configure({ mode: 'serial' })

test('restore cart selection', async ({ page, context }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page)
  const [firstFile, secondFile] = files
  const segments = dirPath.split('/').filter(Boolean) // ['var', 'lib', 'dbdata']

  // IMPORTANT: every navigation from here on must be a client-side SPA
  // navigation (clicking an in-app link/row), never page.goto()/page.reload().
  // restoreCart's selection state is deliberately in-memory only (no
  // persistence, by design -- see docs/superpowers/specs/2026-08-09-restore-
  // cart-design.md), which lives in the page's live JS context. page.goto()
  // is a real browser navigation: it reloads the document and tears down
  // that JS context (all Pinia stores, not just restoreCart), silently
  // wiping every selection made in an earlier step. Clicking a <router-link>
  // (the sidebar, breadcrumbs, table rows) never reloads the document, so
  // selection state survives across scenarios the way it would for a real
  // user browsing the app. (Task 3's seeding helper uses page.goto() freely
  // for its own polling loops -- that's fine there, because it all happens
  // before any restore-cart selection exists to lose.)

  async function goToCatalogHome(page) {
    await page.getByRole('link', { name: 'Catalog' }).click()
    await page.getByTestId('crumb-home').click()
    // The catalog's directory tree has a synthetic root "/" folder row
    // between Home and the first real path segment (parent_path="" returns
    // {name: "/"} before e.g. "var" appears as its child) -- confirmed live
    // against /api/v1/catalog/directories/children (see Task 3's
    // policySeeding.js for the same fix applied to its own drill-down).
    // Renders as "//" since the row template appends "/" to row.name.
    await page.getByText('//', { exact: true }).click()
  }

  async function drillInto(page, pathSegments) {
    await goToCatalogHome(page)
    for (const segment of pathSegments) {
      await page.getByText(`${segment}/`, { exact: true }).click()
    }
  }

  await test.step('selecting a file checks it, highlights the sidebar, and lists it on /restore', async () => {
    await drillInto(page, segments)
    const fileCheckbox = page.getByTestId(`file-checkbox-${sourceHost}:${dirPath}/${firstFile}`)
    await fileCheckbox.click()
    await expect(fileCheckbox).toBeChecked()

    const restoreLink = page.getByRole('link', { name: 'Restore' })
    await expect(restoreLink).toHaveClass(/text-blue-400/)

    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByText(`${dirPath}/${firstFile} (${sourceHost})`)).toBeVisible()
  })

  await test.step('selecting the parent folder pre-checks its children on drill-down', async () => {
    await drillInto(page, segments.slice(0, -1)) // up to dirPath's parent
    const folderCheckbox = page.getByTestId(`folder-checkbox-${dirPath}`)
    await folderCheckbox.click()
    await expect(folderCheckbox).toBeChecked()

    await page.getByText(`${segments[segments.length - 1]}/`, { exact: true }).click() // drill into dirPath
    for (const file of files) {
      await expect(page.getByTestId(`file-checkbox-${sourceHost}:${dirPath}/${file}`)).toBeChecked()
    }

    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByText(`${dirPath}/*`)).toBeVisible()
  })

  await test.step('unchecking a nested file creates an exception, shown as indeterminate on the parent', async () => {
    await drillInto(page, segments) // back into dirPath
    const secondFileCheckbox = page.getByTestId(`file-checkbox-${sourceHost}:${dirPath}/${secondFile}`)
    await secondFileCheckbox.click()
    await expect(secondFileCheckbox).not.toBeChecked()

    await drillInto(page, segments.slice(0, -1)) // up to dirPath's parent
    const folderCheckbox = page.getByTestId(`folder-checkbox-${dirPath}`)
    await expect(folderCheckbox).toHaveJSProperty('indeterminate', true)

    // the exception itself is never shown -- only the wildcard entry
    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByText(`${dirPath}/*`)).toBeVisible()
    await expect(page.getByText(`${dirPath}/${secondFile}`)).toHaveCount(0)
  })

  await test.step('unchecking the folder clears the sidebar highlight and empties /restore', async () => {
    await drillInto(page, segments.slice(0, -1))
    const folderCheckbox = page.getByTestId(`folder-checkbox-${dirPath}`)

    // Coming out of the previous step, the folder is indeterminate (a
    // nested exception exists). A click on an indeterminate checkbox
    // always resolves to fully checked first (clearing that exception),
    // per restoreRules.js's toggleFolder -- checked/unchecked is the only
    // pair that toggles directly. So this scenario needs two clicks to
    // reach fully unchecked.
    await folderCheckbox.click()
    await expect(folderCheckbox).toBeChecked()

    await folderCheckbox.click()
    await expect(folderCheckbox).not.toBeChecked()

    const restoreLink = page.getByRole('link', { name: 'Restore' })
    await expect(restoreLink).not.toHaveClass(/text-blue-400/)

    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByText('No files selected for restore yet.')).toBeVisible()
  })
})
