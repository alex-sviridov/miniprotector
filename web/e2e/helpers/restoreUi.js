import { expect, AUTH_HEADERS } from './test.js'

// goToCatalogHome navigates to the Catalog page's root via real in-app
// clicks (sidebar link -> Home breadcrumb -> the synthetic "/" root row),
// never page.goto()/reload -- a real browser navigation tears down the
// page's whole Pinia JS context (restoreCart's selection state is
// deliberately in-memory only, see docs/superpowers/specs/2026-08-09-
// restore-cart-design.md), silently wiping any selection a scenario has
// already built up across earlier steps.
export async function goToCatalogHome(page) {
  await page.getByRole('link', { name: 'Catalog' }).click()
  await page.getByTestId('crumb-home').click()
  // The catalog's directory tree has a synthetic root "/" folder row
  // between Home and the first real path segment (parent_path="" returns
  // {name: "/"} before e.g. "var" appears as its child) -- confirmed live
  // against /api/v1/catalog/directories/children. Renders as "//" since
  // the row template appends "/" to row.name.
  await page.getByText('//', { exact: true }).click()
}

// drillInto navigates from the catalog home into pathSegments, one real
// row click per segment -- the shared "sidebar -> home -> root -> each
// segment" sequence every restore scenario in this suite uses to reach a
// fixture directory.
export async function drillInto(page, pathSegments) {
  await goToCatalogHome(page)
  for (const segment of pathSegments) {
    await page.getByText(`${segment}/`, { exact: true }).click()
  }
}

// submitAndTrackPolicy clicks a submission button on /restore (Verify or
// Restore), waits for that entry's per-row status link to appear
// (RestoreView.vue, Task 7), extracts the job_id from its href, confirms
// the job's kind prefix matches the submitted mode, looks up the created
// restore/verify policy by that job_id (policyDTO now serializes it --
// it's been on the underlying pb.Policy message all along, field 22;
// policy-server sets it synchronously at creation time, the REST layer
// just never exposed it until this branch's authorized fix), and
// registers the policy with trackPolicy for teardown.
//
// Returns { jobId, policy } -- most callers only need one or the other.
export async function submitAndTrackPolicy(page, { entryKey, buttonTestId, mode, confirm = false, trackPolicy }) {
  await page.getByTestId(buttonTestId).click()
  if (confirm) await page.getByTestId('confirm-restore').click() // Task 6's pre-restore confirmation modal

  const statusLink = page.getByTestId(`status-${entryKey}`).getByRole('link')
  await expect(statusLink).toBeVisible({ timeout: 30_000 })
  const href = await statusLink.getAttribute('href') // "/jobs/<job_id>", from RestoreView's router-link
  const jobId = href.replace(/^\/jobs\//, '')
  // policy-server's restorePolicyJobID (write.go) prefixes every job id
  // with its mode ("restore:"/"verify:") -- confirms the status link
  // really points at a job of the submitted mode before it's used below.
  expect(jobId).toMatch(new RegExp(`^${mode}:`))

  const policiesResp = await page.request.get('/api/v1/policies?type=restore', { headers: AUTH_HEADERS })
  const { data: policies } = await policiesResp.json()
  const policy = policies.find((p) => p.job_id === jobId)
  expect(policy).toBeTruthy()
  trackPolicy(policy.id)

  return { jobId, policy }
}
