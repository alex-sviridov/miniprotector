# Restore Cart Submission Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator submit the restore cart from `/restore`, turning its selected rules into one or more created `"restore"` policies.

**Architecture:** Pure frontend work in `web/`. A submit action resolves the cart's rules into concrete catalog entries via the existing `GET /api/v1/catalog`, groups them by the physical `store_host` each file actually lives on, resolves each group's dial address from a matching `"storage"` policy's checked-in hostname + port (`GET /api/v1/policies?type=storage`), and creates one `"restore"` policy per group (`POST /api/v1/restore`). No backend, proto, or bwfs change.

**Tech Stack:** Vue 3 (`<script setup>`), Pinia stores, Vitest + `@vue/test-utils` + `@pinia/testing` for tests. All new network calls go through the existing `apiFetch` helper (`web/src/api/client.js`).

## Global Constraints

- No changes to `policy-server`, `api-server`, `bwfs`, or any `.proto` file — every endpoint this plan uses already exists.
- Every new/modified store follows this codebase's existing pattern: state `{ loading, error, ... }`, actions wrapped in `withRequest` (`web/src/stores/helpers.js`) for loading/error tracking, `apiFetch` for all HTTP calls.
- Every step that touches restore-cart rule resolution reuses `resolveFile` from `web/src/utils/restoreRules.js` rather than reimplementing path-matching — this is both less code and avoids a real bug class (a naive substring check would treat `/var/lib/dbdata2` as matching a rule for `/var/lib/dbdata`; `resolveFile`'s real path-segment ancestor walk doesn't).
- Per this project's documentation rules (`.claude/CLAUDE.md`): `docs/components/web.md` and `CHANGELOG.md` must be updated before this is considered mergeable — that's Task 7, not optional cleanup.

---

## File Structure

New files:
- `web/src/utils/restoreResolve.js` — pure functions: filter fetched catalog entries down to what the cart's rules currently resolve as included, dedupe, group by `store_host`.
- `web/src/utils/restoreResolve.spec.js`
- `web/src/utils/storeAddress.js` — pure function: resolve a `store_host` to a dialable `host:port` from a list of `"storage"` policies.
- `web/src/utils/storeAddress.spec.js`
- `web/src/stores/restorePolicies.js` — minimal Pinia store: `create(input)` posting to `POST /restore`.
- `web/src/stores/restorePolicies.spec.js`
- `web/src/stores/restoreSubmission.js` — orchestration Pinia store: fetches candidate catalog entries per cart rule, resolves/groups them, resolves each group's store address, and creates one restore policy per group, tracking `submitting`/`results`/`error`.
- `web/src/stores/restoreSubmission.spec.js`

Modified files:
- `web/src/stores/restoreCart.js` — add a `removeEntry(entry)` action.
- `web/src/stores/restoreCart.spec.js` — cover it.
- `web/src/views/RestoreView.vue` — redesign from a placeholder list into a working submit form.
- `web/src/views/RestoreView.spec.js` — extend for remove/destination/submit/results behavior.
- `docs/components/web.md` — describe the submission flow, cross-link the new design doc.
- `CHANGELOG.md` — dated entry.

Build order: the two pure `utils/` modules first (no dependencies), then the two new stores (`restorePolicies` has no dependencies beyond `apiFetch`; `restoreSubmission` depends on both utils modules plus `restoreCart`/`storagePolicies`/`restorePolicies`), then the small `restoreCart` addition, then the view that ties everything together, then docs.

---

### Task 1: `restoreResolve.js` — resolve, dedupe, and group catalog entries

**Files:**
- Create: `web/src/utils/restoreResolve.js`
- Test: `web/src/utils/restoreResolve.spec.js`

**Interfaces:**
- Consumes: `resolveFile(rules, host, path)` from `web/src/utils/restoreRules.js` (already exists — returns `true`/`false` for whether `(host, path)` is currently selected by the rule list, using real path-segment ancestor matching, not substring matching).
- Produces:
  - `filterResolved(rules, entries)` — `entries` is an array of raw `/catalog` DTOs (snake_case fields: `id`, `source_host`, `path`, `store_host`, ...). Returns the subset where `resolveFile(rules, entry.source_host, entry.path) === true`.
  - `dedupeById(entries)` — returns `entries` with repeat `id`s dropped, keeping first occurrence.
  - `groupByStore(entries)` — returns `[{ storeHost, files: [{ sourceHost, path }] }]`, groups sorted by `storeHost` ascending, files within a group sorted by `sourceHost` then `path` ascending. Consumed by Task 5 (`restoreSubmission.js`).

- [ ] **Step 1: Write the failing tests**

```js
// web/src/utils/restoreResolve.spec.js
import { describe, it, expect } from 'vitest'
import { filterResolved, dedupeById, groupByStore } from './restoreResolve'

describe('filterResolved', () => {
  it('keeps an entry covered by a folder wildcard rule', () => {
    const rules = [{ path: '/var/lib/dbdata', host: null, include: true }]
    const entries = [{ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' }]
    expect(filterResolved(rules, entries)).toEqual(entries)
  })

  it('drops an entry only sharing a path prefix as a substring, not a real path segment', () => {
    const rules = [{ path: '/var/lib/dbdata', host: null, include: true }]
    const entries = [{ id: 1, source_host: 'database', path: '/var/lib/dbdata2/other.log', store_host: 'store-a' }]
    expect(filterResolved(rules, entries)).toEqual([])
  })

  it('drops an entry excluded by a more specific exception rule', () => {
    const rules = [
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ]
    const entries = [
      { id: 1, source_host: 'web01', path: '/etc/hosts', store_host: 'store-a' },
      { id: 2, source_host: 'web01', path: '/etc/passwd', store_host: 'store-a' },
    ]
    expect(filterResolved(rules, entries)).toEqual([entries[1]])
  })

  it('keeps a file rule scoped to its exact host, drops the same path from a different host', () => {
    const rules = [{ path: '/etc/hosts', host: 'web01', include: true }]
    const entries = [
      { id: 1, source_host: 'web01', path: '/etc/hosts', store_host: 'store-a' },
      { id: 2, source_host: 'web02', path: '/etc/hosts', store_host: 'store-a' },
    ]
    expect(filterResolved(rules, entries)).toEqual([entries[0]])
  })
})

describe('dedupeById', () => {
  it('drops repeat entries with the same id, keeping the first occurrence', () => {
    const a = { id: 1, path: '/a' }
    const b = { id: 1, path: '/a' }
    const c = { id: 2, path: '/b' }
    expect(dedupeById([a, b, c])).toEqual([a, c])
  })
})

describe('groupByStore', () => {
  it('groups entries by store_host', () => {
    const entries = [
      { id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' },
      { id: 2, source_host: 'web01', path: '/etc/hosts', store_host: 'store-b' },
    ]
    expect(groupByStore(entries)).toEqual([
      { storeHost: 'store-a', files: [{ sourceHost: 'database', path: '/var/lib/dbdata/dump.sql' }] },
      { storeHost: 'store-b', files: [{ sourceHost: 'web01', path: '/etc/hosts' }] },
    ])
  })

  it('splits one source host across two stores into two groups', () => {
    const entries = [
      { id: 1, source_host: 'database', path: '/a', store_host: 'store-a' },
      { id: 2, source_host: 'database', path: '/b', store_host: 'store-b' },
    ]
    expect(groupByStore(entries)).toEqual([
      { storeHost: 'store-a', files: [{ sourceHost: 'database', path: '/a' }] },
      { storeHost: 'store-b', files: [{ sourceHost: 'database', path: '/b' }] },
    ])
  })

  it('sorts groups by storeHost and files within a group by sourceHost then path', () => {
    const entries = [
      { id: 1, source_host: 'web02', path: '/b', store_host: 'store-b' },
      { id: 2, source_host: 'web01', path: '/z', store_host: 'store-a' },
      { id: 3, source_host: 'web01', path: '/a', store_host: 'store-a' },
    ]
    expect(groupByStore(entries)).toEqual([
      {
        storeHost: 'store-a',
        files: [
          { sourceHost: 'web01', path: '/a' },
          { sourceHost: 'web01', path: '/z' },
        ],
      },
      { storeHost: 'store-b', files: [{ sourceHost: 'web02', path: '/b' }] },
    ])
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/utils/restoreResolve.spec.js`
Expected: FAIL — `restoreResolve.js` doesn't exist yet.

- [ ] **Step 3: Write the implementation**

```js
// web/src/utils/restoreResolve.js
// Turns a set of raw /catalog entries into what the restore cart's rules
// actually resolve as selected -- reuses resolveFile (restoreRules.js)
// rather than a substring/prefix check of our own, so an unrelated file
// like /var/lib/dbdata2/x is never swept in by a rule for /var/lib/dbdata:
// resolveFile walks real path segments, a substring check would not.
import { resolveFile } from './restoreRules'

export function filterResolved(rules, entries) {
  return entries.filter((entry) => resolveFile(rules, entry.source_host, entry.path))
}

export function dedupeById(entries) {
  const seen = new Set()
  const result = []
  for (const entry of entries) {
    if (seen.has(entry.id)) continue
    seen.add(entry.id)
    result.push(entry)
  }
  return result
}

// groupByStore groups resolved entries by the physical bwfs node they're
// stored on (store_host) -- this is what makes "one restore policy per
// store" possible: a single source host's files can in principle live on
// more than one store over time, and grouping at the file level (rather
// than trying to pick one store per source host) handles that for free.
export function groupByStore(entries) {
  const byStore = new Map()
  for (const entry of entries) {
    if (!byStore.has(entry.store_host)) byStore.set(entry.store_host, [])
    byStore.get(entry.store_host).push({ sourceHost: entry.source_host, path: entry.path })
  }
  return Array.from(byStore.entries())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([storeHost, files]) => ({
      storeHost,
      files: [...files].sort(
        (a, b) => a.sourceHost.localeCompare(b.sourceHost) || a.path.localeCompare(b.path)
      ),
    }))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/utils/restoreResolve.spec.js`
Expected: PASS (10 tests)

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add web/src/utils/restoreResolve.js web/src/utils/restoreResolve.spec.js
git commit -m "feat: add restore cart resolve/group utilities"
```

---

### Task 2: `storeAddress.js` — resolve a store_host to a dialable address

**Files:**
- Create: `web/src/utils/storeAddress.js`
- Test: `web/src/utils/storeAddress.spec.js`

**Interfaces:**
- Consumes: nothing — pure function over plain data.
- Produces: `resolveStoreAddress(storagePolicies, storeHost)` — `storagePolicies` is the array `usePoliciesStore`-style list format returned by `GET /policies?type=storage` (each item has `port` and `checkins: [{ hostname, last_seen_at }]`, per `policyDTO` in `src/cmd/api-server/policies.go`). Returns `"<storeHost>:<port>"` for the first storage policy with a checkin whose `hostname` equals `storeHost`, or `null` if none match. Consumed by Task 5 (`restoreSubmission.js`).

- [ ] **Step 1: Write the failing tests**

```js
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/utils/storeAddress.spec.js`
Expected: FAIL — `storeAddress.js` doesn't exist yet.

- [ ] **Step 3: Write the implementation**

```js
// web/src/utils/storeAddress.js
// Mirrors policy-server's own attachDestination computation
// (src/cmd/policy-server/server.go) client-side: a storage policy's
// dialable address is its checked-in hostname paired with its own port.
// No endpoint returns this pre-joined for a storage policy, so it's
// computed here from data the app already fetches.
export function resolveStoreAddress(storagePolicies, storeHost) {
  for (const policy of storagePolicies) {
    const checkin = (policy.checkins || []).find((c) => c.hostname === storeHost)
    if (checkin) return `${storeHost}:${policy.port}`
  }
  return null
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/utils/storeAddress.spec.js`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add web/src/utils/storeAddress.js web/src/utils/storeAddress.spec.js
git commit -m "feat: add store_host to dial-address resolution"
```

---

### Task 3: `restorePolicies.js` store — create a restore policy

**Files:**
- Create: `web/src/stores/restorePolicies.js`
- Test: `web/src/stores/restorePolicies.spec.js`

**Interfaces:**
- Consumes: `apiFetch` (`web/src/api/client.js`), `withRequest` (`web/src/stores/helpers.js`) — both already exist and are used identically by `web/src/stores/policies.js`/`storagePolicies.js`.
- Produces: Pinia store `useRestorePoliciesStore`, state `{ loading, error }`, action `create(input)` — `POST`s `input` (already-shaped request body) to `/restore`, returns the created policy DTO on success, sets `error` and rethrows on failure. Consumed by Task 5 (`restoreSubmission.js`).

- [ ] **Step 1: Write the failing tests**

```js
// web/src/stores/restorePolicies.spec.js
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestorePoliciesStore } from './restorePolicies'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('restorePolicies store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('create posts to /restore and returns the created policy', async () => {
    const created = { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' }
    apiFetch.mockResolvedValue(created)
    const restorePolicies = useRestorePoliciesStore()

    const input = {
      name: created.name,
      client_filters: { hostnames: ['web01'], labels: {} },
      source_store: 'store-a:8080',
      config: '{"files":[{"source_host":"database","path":"/var/lib/dbdata/dump.sql"}]}',
    }
    const result = await restorePolicies.create(input)

    expect(apiFetch).toHaveBeenCalledWith('/restore', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    expect(result).toEqual(created)
    expect(restorePolicies.loading).toBe(false)
    expect(restorePolicies.error).toBeNull()
  })

  it('create records and rethrows an error on failure', async () => {
    apiFetch.mockRejectedValue(new Error('source_store must be a valid host:port'))
    const restorePolicies = useRestorePoliciesStore()

    await expect(restorePolicies.create({ name: 'x' })).rejects.toThrow(
      'source_store must be a valid host:port'
    )
    expect(restorePolicies.error).toBe('source_store must be a valid host:port')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/stores/restorePolicies.spec.js`
Expected: FAIL — `restorePolicies.js` doesn't exist yet.

- [ ] **Step 3: Write the implementation**

```js
// web/src/stores/restorePolicies.js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

export const useRestorePoliciesStore = defineStore('restorePolicies', {
  state: () => ({
    loading: false,
    error: null,
  }),
  actions: {
    async create(input) {
      return withRequest(this, async () => {
        return await apiFetch('/restore', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(input),
        })
      })
    },
  },
})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/stores/restorePolicies.spec.js`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add web/src/stores/restorePolicies.js web/src/stores/restorePolicies.spec.js
git commit -m "feat: add restorePolicies store"
```

---

### Task 4: `restoreCart.js` — add `removeEntry`

**Files:**
- Modify: `web/src/stores/restoreCart.js`
- Modify: `web/src/stores/restoreCart.spec.js`

**Interfaces:**
- Consumes: existing `toggleFolder(path)`/`toggleFile(host, path)` actions already on this store.
- Produces: new action `removeEntry(entry)` — `entry` is a `{ path, host, include }` item as returned by this store's `entries` getter. Dispatches to `toggleFolder`/`toggleFile` based on `entry.host`. Consumed by Task 6 (`RestoreView.vue`).

- [ ] **Step 1: Write the failing tests**

Add to `web/src/stores/restoreCart.spec.js` (inside the existing `describe('restoreCart store', ...)` block, after the last existing `it`):

```js
  it('removeEntry unsets a folder wildcard entry', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')
    cart.removeEntry({ path: '/var', host: null, include: true })
    expect(cart.rules).toEqual([])
  })

  it('removeEntry unsets a file entry', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    cart.removeEntry({ path: '/etc/hosts', host: 'web01', include: true })
    expect(cart.rules).toEqual([])
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/stores/restoreCart.spec.js`
Expected: FAIL — `cart.removeEntry is not a function`

- [ ] **Step 3: Write the implementation**

In `web/src/stores/restoreCart.js`, add `removeEntry` to the `actions` object (alongside the existing `toggleFile`/`toggleFolder`):

```js
    removeEntry(entry) {
      if (entry.host === null) this.toggleFolder(entry.path)
      else this.toggleFile(entry.host, entry.path)
    },
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/stores/restoreCart.spec.js`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add web/src/stores/restoreCart.js web/src/stores/restoreCart.spec.js
git commit -m "feat: add removeEntry action to restoreCart store"
```

---

### Task 5: `restoreSubmission.js` store — resolve, group, and submit

**Files:**
- Create: `web/src/stores/restoreSubmission.js`
- Test: `web/src/stores/restoreSubmission.spec.js`

**Interfaces:**
- Consumes:
  - `apiFetch` (`web/src/api/client.js`)
  - `useRestoreCartStore` (`web/src/stores/restoreCart.js`) — reads `.rules` and `.entries` (the `include: true` subset)
  - `useStoragePoliciesStore` (`web/src/stores/storagePolicies.js`) — calls `.fetchAll()`, reads `.list`
  - `useRestorePoliciesStore` (Task 3) — calls `.create(input)`
  - `filterResolved`, `dedupeById`, `groupByStore` (Task 1, `web/src/utils/restoreResolve.js`)
  - `resolveStoreAddress` (Task 2, `web/src/utils/storeAddress.js`)
- Produces: Pinia store `useRestoreSubmissionStore`, state `{ submitting: boolean, results: Array, error: string|null }`. Action `submit(destinationHost)`:
  - If the cart has no `include: true` entries, sets `error = 'Nothing selected for restore.'` and returns without any network calls.
  - Otherwise fetches candidate catalog entries for every cart entry, resolves/dedupes/groups them, resolves each group's store address, and creates one restore policy per group, writing `results` as an array of either `{ storeHost, status: 'success', policy }` or `{ storeHost, status: 'error', message }` — one item per group, in `groupByStore`'s sorted order. A group whose address can't be resolved, or whose `create()` call fails, doesn't stop the others. Consumed by Task 6 (`RestoreView.vue`).

- [ ] **Step 1: Write the failing tests**

```js
// web/src/stores/restoreSubmission.spec.js
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestoreSubmissionStore } from './restoreSubmission'
import { useRestoreCartStore } from './restoreCart'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('restoreSubmission store', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-10T00:00:00.000Z'))
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('reports an error and makes no network calls when the cart is empty', async () => {
    const submission = useRestoreSubmissionStore()

    await submission.submit('web01')

    expect(apiFetch).not.toHaveBeenCalled()
    expect(submission.error).toBe('Nothing selected for restore.')
    expect(submission.results).toEqual([])
  })

  it('resolves a folder rule to catalog entries, groups by store, and creates one restore policy', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog')) {
        return Promise.resolve({
          data: [
            { id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' },
            { id: 2, source_host: 'database', path: '/var/lib/dbdata/schema.sql', store_host: 'store-a' },
          ],
          has_more: false,
        })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.error).toBeNull()
    expect(submission.results).toEqual([
      {
        storeHost: 'store-a',
        status: 'success',
        policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' },
      },
    ])
    expect(apiFetch).toHaveBeenCalledWith('/restore', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: 'restore-2026-08-10T00:00:00.000Z-store-a',
        client_filters: { hostnames: ['web01'], labels: {} },
        source_store: 'store-a:8080',
        config: JSON.stringify({
          files: [
            { source_host: 'database', path: '/var/lib/dbdata/dump.sql' },
            { source_host: 'database', path: '/var/lib/dbdata/schema.sql' },
          ],
        }),
      }),
    })
  })

  it('reports a per-group error when a store has no resolvable address, without blocking other groups', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog?source_host=database')) {
        return Promise.resolve({
          data: [{ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' }],
          has_more: false,
        })
      }
      if (path.startsWith('/catalog?source_host=web01')) {
        return Promise.resolve({
          data: [{ id: 2, source_host: 'web01', path: '/etc/hosts', store_host: 'store-b' }],
          has_more: false,
        })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') {
        return Promise.resolve({ id: 'r1', name: JSON.parse(opts.body).name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.results).toEqual([
      {
        storeHost: 'store-a',
        status: 'success',
        policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' },
      },
      { storeHost: 'store-b', status: 'error', message: 'No reachable storage node found for store-b' },
    ])
  })

  it('reports a per-group error when CreatePolicy fails, without blocking other groups', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('database', '/var/lib/dbdata/dump.sql')
    cart.toggleFile('web01', '/etc/hosts')

    apiFetch.mockImplementation((path, opts) => {
      if (path.startsWith('/catalog?source_host=database')) {
        return Promise.resolve({
          data: [{ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' }],
          has_more: false,
        })
      }
      if (path.startsWith('/catalog?source_host=web01')) {
        return Promise.resolve({
          data: [{ id: 2, source_host: 'web01', path: '/etc/hosts', store_host: 'store-b' }],
          has_more: false,
        })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [
            { id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] },
            { id: 's2', port: 9090, checkins: [{ hostname: 'store-b', last_seen_at: 1 }] },
          ],
        })
      }
      if (path === '/restore') {
        const name = JSON.parse(opts.body).name
        if (name.endsWith('store-b')) return Promise.reject(new Error('name already exists'))
        return Promise.resolve({ id: 'r1', name })
      }
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(submission.results).toEqual([
      {
        storeHost: 'store-a',
        status: 'success',
        policy: { id: 'r1', name: 'restore-2026-08-10T00:00:00.000Z-store-a' },
      },
      { storeHost: 'store-b', status: 'error', message: 'name already exists' },
    ])
  })

  it('paginates catalog fetches until has_more is false', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')

    let call = 0
    apiFetch.mockImplementation((path) => {
      if (path.startsWith('/catalog')) {
        call += 1
        if (call === 1) {
          expect(path).not.toContain('starting_after')
          return Promise.resolve({
            data: [{ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql', store_host: 'store-a' }],
            has_more: true,
          })
        }
        expect(path).toContain('starting_after=1')
        return Promise.resolve({
          data: [{ id: 2, source_host: 'database', path: '/var/lib/dbdata/schema.sql', store_host: 'store-a' }],
          has_more: false,
        })
      }
      if (path === '/policies?type=storage') {
        return Promise.resolve({
          data: [{ id: 's1', port: 8080, checkins: [{ hostname: 'store-a', last_seen_at: 1 }] }],
        })
      }
      if (path === '/restore') return Promise.resolve({ id: 'r1', name: 'x' })
      throw new Error(`unexpected apiFetch call: ${path}`)
    })

    const submission = useRestoreSubmissionStore()
    await submission.submit('web01')

    expect(call).toBe(2)
    expect(submission.results[0].status).toBe('success')
  })

  it('tracks submitting state across the whole flow', async () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var/lib/dbdata')
    apiFetch.mockResolvedValue({ data: [], has_more: false })

    const submission = useRestoreSubmissionStore()
    const pending = submission.submit('web01')
    expect(submission.submitting).toBe(true)
    await pending
    expect(submission.submitting).toBe(false)
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: FAIL — `restoreSubmission.js` doesn't exist yet.

- [ ] **Step 3: Write the implementation**

```js
// web/src/stores/restoreSubmission.js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { useRestoreCartStore } from './restoreCart'
import { useStoragePoliciesStore } from './storagePolicies'
import { useRestorePoliciesStore } from './restorePolicies'
import { filterResolved, dedupeById, groupByStore } from '../utils/restoreResolve'
import { resolveStoreAddress } from '../utils/storeAddress'

const MAX_PAGE_LIMIT = 500

function buildCatalogQuery(entry, startingAfter) {
  const params = new URLSearchParams()
  if (entry.host) params.set('source_host', entry.host)
  params.set('pattern', entry.path)
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  params.set('limit', String(MAX_PAGE_LIMIT))
  return params.toString()
}

// fetchCandidateEntries over-fetches (pattern is a substring match, not an
// anchored prefix match) on purpose -- filterResolved (restoreResolve.js)
// is what decides real inclusion, using the same path-segment logic the
// catalog UI's own checkboxes already rely on.
async function fetchCandidateEntries(entry) {
  const collected = []
  let startingAfter
  for (;;) {
    const qs = buildCatalogQuery(entry, startingAfter)
    const body = await apiFetch(`/catalog?${qs}`)
    collected.push(...body.data)
    if (!body.has_more || body.data.length === 0) break
    startingAfter = body.data[body.data.length - 1].id
  }
  return collected
}

export const useRestoreSubmissionStore = defineStore('restoreSubmission', {
  state: () => ({
    submitting: false,
    results: [],
    error: null,
  }),
  actions: {
    async submit(destinationHost) {
      const cart = useRestoreCartStore()
      const storagePolicies = useStoragePoliciesStore()
      const restorePolicies = useRestorePoliciesStore()

      this.submitting = true
      this.results = []
      this.error = null

      try {
        const positiveEntries = cart.entries
        if (positiveEntries.length === 0) {
          this.error = 'Nothing selected for restore.'
          return
        }

        const candidateLists = await Promise.all(positiveEntries.map(fetchCandidateEntries))
        const candidates = dedupeById(candidateLists.flat())
        const resolved = filterResolved(cart.rules, candidates)
        const groups = groupByStore(resolved)

        await storagePolicies.fetchAll()

        const results = []
        for (const group of groups) {
          const address = resolveStoreAddress(storagePolicies.list, group.storeHost)
          if (!address) {
            results.push({
              storeHost: group.storeHost,
              status: 'error',
              message: `No reachable storage node found for ${group.storeHost}`,
            })
            continue
          }
          const name = `restore-${new Date().toISOString()}-${group.storeHost}`
          try {
            const policy = await restorePolicies.create({
              name,
              client_filters: { hostnames: [destinationHost], labels: {} },
              source_store: address,
              config: JSON.stringify({
                files: group.files.map((f) => ({ source_host: f.sourceHost, path: f.path })),
              }),
            })
            results.push({ storeHost: group.storeHost, status: 'success', policy })
          } catch (err) {
            results.push({ storeHost: group.storeHost, status: 'error', message: err.message })
          }
        }
        this.results = results
      } finally {
        this.submitting = false
      }
    },
  },
})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/stores/restoreSubmission.spec.js`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add web/src/stores/restoreSubmission.js web/src/stores/restoreSubmission.spec.js
git commit -m "feat: add restoreSubmission store"
```

---

### Task 6: `RestoreView.vue` — remove, destination picker, submit, results

**Files:**
- Modify: `web/src/views/RestoreView.vue`
- Modify: `web/src/views/RestoreView.spec.js`

**Interfaces:**
- Consumes: `useRestoreCartStore` (`.entries`, `.hasSelections`, `.removeEntry(entry)`), `useClientsStore` (`web/src/stores/clients.js`, `.list` — array of `{ hostname, ... }`, `.fetchAll()`), `useRestoreSubmissionStore` (Task 5 — `.submitting`, `.results`, `.error`, `.submit(destinationHost)`), and UI components `PageHeader`, `StatusMessage`, `BaseButton`, `BaseField`, `BaseSelect` (all existing, `web/src/components/ui/`).
- Produces: the `/restore` page. No new interfaces for other tasks to consume — this is the last task before docs.

- [ ] **Step 1: Write the failing tests**

Replace `web/src/views/RestoreView.spec.js` entirely with:

```js
// web/src/views/RestoreView.spec.js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RestoreView from './RestoreView.vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'

function mountView({ rules = [], clientsList = [], submission = {} } = {}) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: {
      restoreCart: { rules },
      clients: { list: clientsList },
      restoreSubmission: { submitting: false, results: [], error: null, ...submission },
    },
  })
  return mount(RestoreView, { global: { plugins: [pinia] } })
}

describe('RestoreView', () => {
  it('shows the empty state when the cart has no selections', () => {
    const wrapper = mountView()
    expect(wrapper.text()).toContain('No files selected for restore yet.')
  })

  it('lists a folder wildcard rule as path/*', () => {
    const wrapper = mountView({ rules: [{ path: '/var', host: null, include: true }] })
    expect(wrapper.text()).toContain('/var/*')
  })

  it('lists a file rule as path (host)', () => {
    const wrapper = mountView({ rules: [{ path: '/etc/hosts', host: 'web01', include: true }] })
    expect(wrapper.text()).toContain('/etc/hosts (web01)')
  })

  it('omits exception (include: false) rules from the list', () => {
    const wrapper = mountView({
      rules: [
        { path: '/etc', host: null, include: true },
        { path: '/etc/hosts', host: 'web01', include: false },
      ],
    })
    expect(wrapper.text()).toContain('/etc/*')
    expect(wrapper.text()).not.toContain('/etc/hosts')
  })

  it('renders the page breadcrumb', () => {
    const wrapper = mountView()
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Restore')
  })

  it('removing an entry calls restoreCart.removeEntry with that entry', async () => {
    const entry = { path: '/var', host: null, include: true }
    const wrapper = mountView({ rules: [entry] })
    const cart = useRestoreCartStore()

    await wrapper.find('[data-test="remove-:/var"]').trigger('click')

    expect(cart.removeEntry).toHaveBeenCalledWith(entry)
  })

  it('populates the destination select from the clients store', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      clientsList: [{ hostname: 'web01' }, { hostname: 'web02' }],
    })
    const options = wrapper.find('[data-test="destination-select"]').findAll('option')
    expect(options.map((o) => o.element.value)).toEqual(['', 'web01', 'web02'])
  })

  it('disables submit until the cart has a selection and a destination is chosen', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      clientsList: [{ hostname: 'web01' }],
    })
    expect(wrapper.find('[data-test="submit-restore"]').attributes('disabled')).toBeDefined()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')

    expect(wrapper.find('[data-test="submit-restore"]').attributes('disabled')).toBeUndefined()
  })

  it('clicking submit calls restoreSubmission.submit with the chosen destination', async () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      clientsList: [{ hostname: 'web01' }],
    })
    const submission = useRestoreSubmissionStore()

    await wrapper.find('[data-test="destination-select"]').setValue('web01')
    await wrapper.find('[data-test="submit-restore"]').trigger('click')

    expect(submission.submit).toHaveBeenCalledWith('web01')
  })

  it('renders a successful submission result', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      submission: { results: [{ storeHost: 'store-a', status: 'success', policy: { name: 'restore-x' } }] },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain('restore-x')
  })

  it('renders a per-group submission error', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      submission: {
        results: [
          { storeHost: 'store-b', status: 'error', message: 'No reachable storage node found for store-b' },
        ],
      },
    })
    expect(wrapper.find('[data-test="submission-results"]').text()).toContain(
      'No reachable storage node found for store-b'
    )
  })

  it('renders a submission-level error', () => {
    const wrapper = mountView({
      rules: [{ path: '/var', host: null, include: true }],
      submission: { error: 'Nothing selected for restore.' },
    })
    expect(wrapper.find('[data-test="submission-error"]').text()).toBe('Nothing selected for restore.')
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npx vitest run src/views/RestoreView.spec.js`
Expected: FAIL — new `data-test` hooks (`remove-...`, `destination-select`, `submit-restore`, `submission-results`, `submission-error`) don't exist in the current placeholder view yet.

- [ ] **Step 3: Write the implementation**

Replace `web/src/views/RestoreView.vue` entirely with:

```vue
<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRestoreCartStore } from '../stores/restoreCart'
import { useClientsStore } from '../stores/clients'
import { useRestoreSubmissionStore } from '../stores/restoreSubmission'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import BaseField from '../components/ui/BaseField.vue'
import BaseSelect from '../components/ui/BaseSelect.vue'

const restoreCart = useRestoreCartStore()
const clients = useClientsStore()
const submission = useRestoreSubmissionStore()

const destinationHost = ref('')

onMounted(() => {
  if (clients.list.length === 0) clients.fetchAll()
})

function label(entry) {
  return entry.host === null ? `${entry.path}/*` : `${entry.path} (${entry.host})`
}

function remove(entry) {
  restoreCart.removeEntry(entry)
}

const canSubmit = computed(
  () => restoreCart.hasSelections && destinationHost.value !== '' && !submission.submitting
)

function submit() {
  submission.submit(destinationHost.value)
}
</script>

<template>
  <div>
    <PageHeader title="Restore" :crumbs="[{ label: 'Restore' }]" />
    <StatusMessage :empty="restoreCart.entries.length === 0" empty-text="No files selected for restore yet.">
      <ul>
        <li v-for="entry in restoreCart.entries" :key="`${entry.host ?? ''}:${entry.path}`">
          {{ label(entry) }}
          <button
            type="button"
            :data-test="`remove-${entry.host ?? ''}:${entry.path}`"
            @click="remove(entry)"
          >
            Remove
          </button>
        </li>
      </ul>
      <BaseField label="Destination host">
        <BaseSelect data-test="destination-select" v-model="destinationHost">
          <option value="" disabled>Select a destination host</option>
          <option v-for="client in clients.list" :key="client.hostname" :value="client.hostname">
            {{ client.hostname }}
          </option>
        </BaseSelect>
      </BaseField>
      <BaseButton data-test="submit-restore" variant="primary" :disabled="!canSubmit" @click="submit">
        Submit restore
      </BaseButton>
      <p v-if="submission.error" data-test="submission-error">{{ submission.error }}</p>
      <ul v-if="submission.results.length" data-test="submission-results">
        <li v-for="result in submission.results" :key="result.storeHost">
          <span v-if="result.status === 'success'">Created {{ result.policy.name }} from {{ result.storeHost }}</span>
          <span v-else>{{ result.storeHost }}: {{ result.message }}</span>
        </li>
      </ul>
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npx vitest run src/views/RestoreView.spec.js`
Expected: PASS (12 tests)

- [ ] **Step 5: Run the full web test suite to check for regressions**

Run: `cd web && npx vitest run`
Expected: PASS — all suites, including `src/stores/restoreCart.spec.js`, `src/components/Sidebar.spec.js` (unaffected — the sidebar highlight logic wasn't touched), and everything from Tasks 1-5.

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector
git add web/src/views/RestoreView.vue web/src/views/RestoreView.spec.js
git commit -m "feat: wire restore cart submission into RestoreView"
```

---

### Task 7: Documentation

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing (docs only).
- Produces: nothing consumed by other tasks — this is the final task.

- [ ] **Step 1: Update `docs/components/web.md`**

Replace the existing `/restore` bullet (currently: "`/restore` — placeholder list of everything currently staged in the restore cart (folder selections as `path/*`, file selections as `path (host)`); no actions yet, just a preview of what `/catalog`'s checkboxes have accumulated. The sidebar's Restore link highlights whenever the cart is non-empty.") with:

```markdown
- `/restore` — lists everything currently staged in the restore cart (folder selections as
  `path/*`, file selections as `path (host)`), each with a Remove button that unstages it (toggles
  the same rule back off, via `restoreCart.removeEntry`). Picking a destination host (from the
  enrolled-client list, `useClientsStore`) and clicking Submit resolves the cart's rules into
  concrete catalog entries (`GET /catalog`), groups them by the physical `store_host` each file is
  actually stored on, resolves each group's dial address from a matching `"storage"` policy's
  checked-in hostname + port, and creates one `"restore"` policy per group (`POST /restore`) — so a
  selection spanning files backed up to more than one storage destination becomes multiple
  policies, each scoped to just the files that live there. Results (created policy, or a per-group
  error such as an unresolvable store address) render inline; one group failing doesn't block the
  others. Submission is still terminal: nothing yet consumes a `"restore"` policy (see
  `docs/superpowers/specs/2026-08-09-restore-policy-type-design.md`) — this only creates it. The
  sidebar's Restore link still highlights whenever the cart is non-empty.
```

Add a new line to the "See Also" list, immediately after the existing `Design: restore cart` entry:

```markdown
- [Design: restore cart submission](../superpowers/specs/2026-08-10-restore-cart-submission-design.md)
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Add to the top of `CHANGELOG.md`, immediately after the `# Changelog` header and its intro line (above the existing `## 2026-08-10 — reject comma in object filter include/exclude patterns` entry):

```markdown
## 2026-08-10 — restore cart submission

The restore cart's `/restore` page can now actually be submitted, not just previewed. Each cart
entry gets a Remove button, and picking a destination host and clicking Submit resolves the cart's
rules into concrete catalog files, groups them by which physical `bwfs` store each one actually
lives on, and creates one `"restore"` policy per store (`POST /api/v1/restore`) — the config format
that policy type's own design left open. This is still frontend-only groundwork: no backend change
was needed (every step uses REST endpoints that already existed), and nothing yet consumes a
created `"restore"` policy — `agent` fetching and executing them is future work.
```

- [ ] **Step 3: Commit**

```bash
cd /home/alex/miniprotector
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document restore cart submission"
```

---

## Self-Review Notes

- **Spec coverage:** config format (flat `{files: [{source_host, path}]}`, Task 5's `submit`) ✓; resolve step reusing `resolveFile` (Task 1) ✓; group-by-store (Task 1's `groupByStore`) ✓; store address resolution via storage-policy checkins+port (Task 2) ✓; one `POST /restore` per group with per-group results (Task 5) ✓; Remove button (Task 4/6) ✓; destination picker from `useClientsStore` (Task 6) ✓; docs (`docs/components/web.md`, `CHANGELOG.md`, Task 7) ✓. `docs/ARCHITECTURE.md` and `README.md` are explicitly no-change per the spec, so no task touches them.
- **Placeholder scan:** no TBD/TODO markers; every step has runnable code, not a description of code.
- **Type/signature consistency checked across tasks:** `filterResolved(rules, entries)`/`groupByStore(entries)` (Task 1) called with exactly those signatures in Task 5. `resolveStoreAddress(storagePolicies, storeHost)` (Task 2) called the same way in Task 5. `restorePolicies.create(input)` (Task 3) called with the exact body shape in Task 5, matching what Task 3's own test asserts against. `restoreCart.removeEntry(entry)` (Task 4) called the same way in Task 6. `restoreSubmission.submit(destinationHost)` (Task 5) called the same way in Task 6.
