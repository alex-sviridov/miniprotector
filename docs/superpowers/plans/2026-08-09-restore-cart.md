# Restore Cart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user check files and folders in the catalog UI to stage them for restore, with the selection reflected as a highlighted sidebar link and a placeholder list on a new `/restore` page. UI-only — no restore execution.

**Architecture:** A prefix-matching rule engine (pure functions in `web/src/utils/restoreRules.js`) resolves any file's or folder's selection state from a small list of `{ path, host, include }` rules, so selecting an entire folder never requires enumerating its contents. A thin Pinia store (`stores/restoreCart.js`) holds the rule list and exposes mutation actions; `CatalogView.vue` reads resolved state directly from the rules for each visible row via the pure functions (kept out of the store's action layer specifically so `@pinia/testing`'s `stubActions: true` — already relied on throughout this codebase's tests — doesn't stub reads along with writes).

**Tech Stack:** Vue 3 `<script setup>`, Pinia, Vitest + `@vue/test-utils` + `@pinia/testing`, Tailwind utility classes (no new dependencies).

## Global Constraints

- Selection state is in-memory only (plain Pinia store state, no persistence plugin) — resets on page reload.
- A cart file entry is keyed by `(source_host, path)`, never a specific version id — it always means "restore whatever the latest version is," per `docs/superpowers/specs/2026-08-09-restore-cart-design.md`.
- A cart folder entry (`host: null`) applies across every source host, matching how folder rows are already computed host-agnostically by `ListDirectoryChildren`.
- No restore execution, no backend changes, no cart persistence, no remove/clear controls in the restore view, no bulk select-all — all explicitly out of scope for this pass.
- Follow existing repo conventions exactly: `<script setup>` SFCs, Pinia option-store style (`defineStore(name, { state, getters, actions })`) as used in `stores/catalog.js`, Vitest `describe`/`it` with `@pinia/testing`'s `createTestingPinia`.

---

## Task 1: Selection rule engine

**Files:**
- Create: `web/src/utils/restoreRules.js`
- Test: `web/src/utils/restoreRules.spec.js`

**Interfaces:**
- Consumes: `pathCrumbs(path) -> [{ path, name }]` from `web/src/utils/pathSplit.js` (already exists — returns a path's ancestor chain root-first, including the path itself as the last element; handles Unix/Windows/UNC shapes).
- Produces (used by Tasks 2 and 7):
  - `resolveFile(rules, host, path) -> boolean`
  - `resolveFolderState(rules, path) -> 'checked' | 'unchecked' | 'indeterminate'`
  - `toggleFile(rules, host, path) -> newRulesArray`
  - `toggleFolder(rules, path) -> newRulesArray`
  - A rule shape: `{ path: string, host: string|null, include: boolean }`.

This is the highest-risk part of the feature — the rest of the plan is straightforward wiring around it. Build it in four small red/green cycles, one per function, each ending in its own commit.

- [ ] **Step 1: Write failing tests for `resolveFile`**

Create `web/src/utils/restoreRules.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { resolveFile } from './restoreRules'

describe('resolveFile', () => {
  it('is false when no rule matches', () => {
    expect(resolveFile([], 'web01', '/etc/hosts')).toBe(false)
  })

  it('is true for an exact host-specific include rule', () => {
    const rules = [{ path: '/etc/hosts', host: 'web01', include: true }]
    expect(resolveFile(rules, 'web01', '/etc/hosts')).toBe(true)
  })

  it('does not apply a host-specific rule to a different host', () => {
    const rules = [{ path: '/etc/hosts', host: 'web01', include: true }]
    expect(resolveFile(rules, 'db02', '/etc/hosts')).toBe(false)
  })

  it('inherits from the nearest covering host-agnostic ancestor folder rule', () => {
    const rules = [{ path: '/etc', host: null, include: true }]
    expect(resolveFile(rules, 'web01', '/etc/hosts')).toBe(true)
  })

  it('an exact host-specific exception overrides an ancestor folder rule', () => {
    const rules = [
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ]
    expect(resolveFile(rules, 'web01', '/etc/hosts')).toBe(false)
    expect(resolveFile(rules, 'db02', '/etc/hosts')).toBe(true)
  })

  it('uses the longest (most specific) matching ancestor folder rule', () => {
    const rules = [
      { path: '/var', host: null, include: true },
      { path: '/var/log', host: null, include: false },
    ]
    expect(resolveFile(rules, 'web01', '/var/log/access.log')).toBe(false)
    expect(resolveFile(rules, 'web01', '/var/lib/data.db')).toBe(true)
  })
})
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `docker run --rm -v "$(pwd)/web":/app -w /app node:20-alpine npx vitest run src/utils/restoreRules.spec.js`
(or `cd web && npx vitest run src/utils/restoreRules.spec.js` if Node 20 is available locally)
Expected: FAIL — `Cannot find module './restoreRules'` or similar.

- [ ] **Step 3: Implement `resolveFile`**

Create `web/src/utils/restoreRules.js`:

```js
// A rule captures one explicit restore-selection decision: { path, host,
// include }. host === null means a folder-level rule, applying across
// every source host -- folder rows in the catalog UI are already
// host-agnostic (ListDirectoryChildren's existence check ignores the
// clients/host filter). host set to a string means a file-level rule,
// scoped to that one (host, path) pair -- matches how file rows are
// already grouped client-side (groupEntriesByFile).
//
// A path's selection state is *resolved* from the rule list rather than
// stored directly, using longest-matching-prefix semantics (like
// .gitignore): the most specific rule covering a path wins. This keeps
// the rule list small regardless of how many files a folder contains --
// selecting a folder is one rule, not one per descendant file. See
// docs/superpowers/specs/2026-08-09-restore-cart-design.md.
import { pathCrumbs } from './pathSplit'

// ancestorsOrSelf returns path's ancestor chain root-first, path itself
// last -- reuses pathCrumbs (already handles Unix/Windows/UNC shapes)
// rather than re-deriving path structure here.
function ancestorsOrSelf(path) {
  return pathCrumbs(path).map((c) => c.path)
}

// longestMatchingFolderRule finds the most specific host-agnostic folder
// rule covering path (checking path itself before its ancestors), and
// returns its `include` value, or undefined if none match.
function longestMatchingFolderRule(rules, path) {
  const chain = ancestorsOrSelf(path)
  for (let i = chain.length - 1; i >= 0; i--) {
    const rule = rules.find((r) => r.host === null && r.path === chain[i])
    if (rule) return rule.include
  }
  return undefined
}

// resolveFile returns whether (host, path) is currently selected: an
// exact host-specific rule wins outright; otherwise the longest matching
// host-agnostic ancestor folder rule applies. No match = unselected.
export function resolveFile(rules, host, path) {
  const exact = rules.find((r) => r.host === host && r.path === path)
  if (exact) return exact.include
  return longestMatchingFolderRule(rules, path) === true
}
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd web && npx vitest run src/utils/restoreRules.spec.js`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/restoreRules.js web/src/utils/restoreRules.spec.js
git commit -m "feat: add resolveFile to the restore-cart rule engine"
```

- [ ] **Step 6: Write failing tests for `resolveFolderState`**

Append to `web/src/utils/restoreRules.spec.js`:

```js
import { resolveFolderState } from './restoreRules'
```

(add `resolveFolderState` to the existing `import { resolveFile } from './restoreRules'` line instead of a second import statement)

```js
describe('resolveFolderState', () => {
  it('is unchecked when nothing covers the path and nothing sits under it', () => {
    expect(resolveFolderState([], '/etc')).toBe('unchecked')
  })

  it('is checked when a rule fully covers the path with nothing underneath', () => {
    const rules = [{ path: '/etc', host: null, include: true }]
    expect(resolveFolderState(rules, '/etc')).toBe('checked')
  })

  it('is checked when covered by an ancestor rule, with nothing underneath', () => {
    const rules = [{ path: '/', host: null, include: true }]
    expect(resolveFolderState(rules, '/etc')).toBe('checked')
  })

  it('is indeterminate when a nested exception exists under a covering rule', () => {
    const rules = [
      { path: '/var', host: null, include: true },
      { path: '/var/log', host: null, include: false },
    ]
    expect(resolveFolderState(rules, '/var')).toBe('indeterminate')
  })

  it('is indeterminate when a file below is individually selected without covering the folder', () => {
    const rules = [{ path: '/var/log/access.log', host: 'web01', include: true }]
    expect(resolveFolderState(rules, '/var/log')).toBe('indeterminate')
    expect(resolveFolderState(rules, '/var')).toBe('indeterminate')
  })

  it('is indeterminate even when its own exact rule excludes it, if something nested re-includes', () => {
    const rules = [
      { path: '/var', host: null, include: true },
      { path: '/var/log', host: null, include: false },
      { path: '/var/log/nginx', host: null, include: true },
    ]
    expect(resolveFolderState(rules, '/var/log')).toBe('indeterminate')
  })

  it('is unaffected by sibling rules', () => {
    const rules = [{ path: '/home', host: null, include: true }]
    expect(resolveFolderState(rules, '/etc')).toBe('unchecked')
  })
})
```

- [ ] **Step 7: Run the tests, confirm the new ones fail**

Run: `cd web && npx vitest run src/utils/restoreRules.spec.js`
Expected: FAIL — `resolveFolderState is not a function` (or not exported).

- [ ] **Step 8: Implement `resolveFolderState`**

Append to `web/src/utils/restoreRules.js` (after `longestMatchingFolderRule`, before `resolveFile` or after it — order doesn't matter, keep related functions grouped):

```js
// isStrictDescendantPath is true when ancestorPath is a proper ancestor
// of candidatePath (not equal to it).
function isStrictDescendantPath(candidatePath, ancestorPath) {
  if (candidatePath === ancestorPath) return false
  return ancestorsOrSelf(candidatePath).includes(ancestorPath)
}

// hasRuleUnder is true if any rule (folder or file, any host) sits
// strictly under path -- used to detect a folder's indeterminate state.
function hasRuleUnder(rules, path) {
  return rules.some((r) => isStrictDescendantPath(r.path, path))
}

// resolveFolderState returns the tri-state checkbox value for a folder
// row: 'checked' if a rule fully covers it and nothing overrides that
// underneath; 'unchecked' if nothing covers it and nothing sits under
// it; 'indeterminate' otherwise (mixed).
export function resolveFolderState(rules, path) {
  if (hasRuleUnder(rules, path)) return 'indeterminate'
  return longestMatchingFolderRule(rules, path) === true ? 'checked' : 'unchecked'
}
```

- [ ] **Step 9: Run the tests, confirm they pass**

Run: `cd web && npx vitest run src/utils/restoreRules.spec.js`
Expected: PASS (13 tests).

- [ ] **Step 10: Commit**

```bash
git add web/src/utils/restoreRules.js web/src/utils/restoreRules.spec.js
git commit -m "feat: add resolveFolderState to the restore-cart rule engine"
```

- [ ] **Step 11: Write failing tests for `toggleFolder`**

Append to `web/src/utils/restoreRules.spec.js` (add `toggleFolder` to the top import):

```js
describe('toggleFolder', () => {
  it('adds a wildcard rule when unchecked with no existing rules', () => {
    const result = toggleFolder([], '/etc')
    expect(result).toEqual([{ path: '/etc', host: null, include: true }])
  })

  it('removes the exact rule when checked via its own rule', () => {
    const rules = [{ path: '/etc', host: null, include: true }]
    expect(toggleFolder(rules, '/etc')).toEqual([])
  })

  it('adds an exception when checked via an inherited ancestor rule', () => {
    const rules = [{ path: '/', host: null, include: true }]
    const result = toggleFolder(rules, '/etc')
    expect(result).toEqual([
      { path: '/', host: null, include: true },
      { path: '/etc', host: null, include: false },
    ])
  })

  it('prunes nested exceptions when re-checking a folder, without a redundant rule', () => {
    const rules = [
      { path: '/var', host: null, include: true },
      { path: '/var/log', host: null, include: false },
      { path: '/var/log/nginx', host: null, include: true },
    ]
    // /var/log is indeterminate; checking it should clear everything
    // under it and, since /var already covers it, add nothing new.
    expect(toggleFolder(rules, '/var/log')).toEqual([{ path: '/var', host: null, include: true }])
  })

  it('prunes nested rules and adds a fresh wildcard when checking an uncovered indeterminate folder', () => {
    const rules = [{ path: '/var/log/access.log', host: 'web01', include: true }]
    expect(toggleFolder(rules, '/var/log')).toEqual([{ path: '/var/log', host: null, include: true }])
  })

  it('prunes a host-specific file exception nested under a newly re-checked folder', () => {
    const rules = [
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ]
    expect(toggleFolder(rules, '/etc')).toEqual([{ path: '/etc', host: null, include: true }])
  })
})
```

- [ ] **Step 12: Run the tests, confirm the new ones fail**

Run: `cd web && npx vitest run src/utils/restoreRules.spec.js`
Expected: FAIL — `toggleFolder is not a function`.

- [ ] **Step 13: Implement `toggleFolder`**

Append to `web/src/utils/restoreRules.js`:

```js
// toggleFolder returns a new rule list with path's selection flipped.
// Checked -> unchecked mirrors the exact-rule-removal trick below (a
// state of 'checked' guarantees nothing sits underneath, so no pruning
// is needed there). Unchecked/indeterminate -> checked first prunes
// every rule at-or-under path -- clearing any exceptions or partial
// selections underneath -- then adds a fresh wildcard only if the
// remaining rules don't already cover path via an ancestor (avoiding a
// redundant rule).
export function toggleFolder(rules, path) {
  const state = resolveFolderState(rules, path)
  if (state === 'checked') {
    const exact = rules.find((r) => r.host === null && r.path === path)
    if (exact) return rules.filter((r) => r !== exact)
    return [...rules, { path, host: null, include: false }]
  }
  const pruned = rules.filter((r) => r.path !== path && !isStrictDescendantPath(r.path, path))
  if (longestMatchingFolderRule(pruned, path) === true) return pruned
  return [...pruned, { path, host: null, include: true }]
}
```

- [ ] **Step 14: Run the tests, confirm they pass**

Run: `cd web && npx vitest run src/utils/restoreRules.spec.js`
Expected: PASS (19 tests).

- [ ] **Step 15: Commit**

```bash
git add web/src/utils/restoreRules.js web/src/utils/restoreRules.spec.js
git commit -m "feat: add toggleFolder to the restore-cart rule engine"
```

- [ ] **Step 16: Write failing tests for `toggleFile`**

Append to `web/src/utils/restoreRules.spec.js` (add `toggleFile` to the top import):

```js
describe('toggleFile', () => {
  it('adds an include rule when unchecked with no existing rules', () => {
    expect(toggleFile([], 'web01', '/etc/hosts')).toEqual([{ path: '/etc/hosts', host: 'web01', include: true }])
  })

  it('removes the exact rule when checked via its own rule', () => {
    const rules = [{ path: '/etc/hosts', host: 'web01', include: true }]
    expect(toggleFile(rules, 'web01', '/etc/hosts')).toEqual([])
  })

  it('adds a host-specific exception when checked via an inherited ancestor folder rule', () => {
    const rules = [{ path: '/etc', host: null, include: true }]
    const result = toggleFile(rules, 'web01', '/etc/hosts')
    expect(result).toEqual([
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ])
  })

  it('removes a host-specific exception to re-check a file, reverting to the ancestor rule', () => {
    const rules = [
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ]
    expect(toggleFile(rules, 'web01', '/etc/hosts')).toEqual([{ path: '/etc', host: null, include: true }])
  })

  it('does not affect other hosts sharing the same path', () => {
    const rules = [{ path: '/etc', host: null, include: true }]
    const result = toggleFile(rules, 'web01', '/etc/hosts')
    expect(resolveFile(result, 'db02', '/etc/hosts')).toBe(true)
  })
})
```

- [ ] **Step 17: Run the tests, confirm the new ones fail**

Run: `cd web && npx vitest run src/utils/restoreRules.spec.js`
Expected: FAIL — `toggleFile is not a function`.

- [ ] **Step 18: Implement `toggleFile`**

Append to `web/src/utils/restoreRules.js`:

```js
// toggleFile returns a new rule list with (host, path)'s selection
// flipped. If an exact rule already exists at (host, path), it is
// removed: by the pruning invariant maintained throughout this module,
// a stored rule only ever exists because it overrides its closest
// ancestor, so removing it always flips the resolved state back.
// Otherwise a fresh rule is added with the opposite of the current
// resolved state.
export function toggleFile(rules, host, path) {
  const exact = rules.find((r) => r.host === host && r.path === path)
  if (exact) return rules.filter((r) => r !== exact)
  const checked = resolveFile(rules, host, path)
  return [...rules, { path, host, include: !checked }]
}
```

- [ ] **Step 19: Run the full test file, confirm everything passes**

Run: `cd web && npx vitest run src/utils/restoreRules.spec.js`
Expected: PASS (24 tests).

- [ ] **Step 20: Commit**

```bash
git add web/src/utils/restoreRules.js web/src/utils/restoreRules.spec.js
git commit -m "feat: add toggleFile to the restore-cart rule engine"
```

---

## Task 2: Restore cart Pinia store

**Files:**
- Create: `web/src/stores/restoreCart.js`
- Test: `web/src/stores/restoreCart.spec.js`

**Interfaces:**
- Consumes: `toggleFile`, `toggleFolder` from `web/src/utils/restoreRules.js` (Task 1).
- Produces (used by Tasks 5, 6, 7): `useRestoreCartStore()` — Pinia store `'restoreCart'` with:
  - `state.rules: Array<{ path, host, include }>`
  - `getters.hasSelections: boolean`
  - `getters.entries: Array<{ path, host, include }>` (rules where `include === true`)
  - `actions.toggleFile(host, path)`
  - `actions.toggleFolder(path)`

Deliberately does **not** expose `fileState`/`folderState` as store actions — Task 7 calls `resolveFile`/`resolveFolderState` directly from `utils/restoreRules.js` against `restoreCart.rules`, the same pattern `CatalogView.vue` already uses for `groupEntriesByFile` against `catalog.entries`. This matters for testability: `@pinia/testing`'s `createTestingPinia({ stubActions: true })` (already used by every store-backed component test in this codebase) replaces actions with no-op spies, which would break checkbox rendering if reads went through an action.

- [ ] **Step 1: Write failing tests**

Create `web/src/stores/restoreCart.spec.js`:

```js
import { describe, it, expect, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useRestoreCartStore } from './restoreCart'

describe('restoreCart store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('starts with no rules', () => {
    const cart = useRestoreCartStore()
    expect(cart.rules).toEqual([])
    expect(cart.hasSelections).toBe(false)
    expect(cart.entries).toEqual([])
  })

  it('toggleFile adds a rule and updates hasSelections/entries', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toEqual([{ path: '/etc/hosts', host: 'web01', include: true }])
    expect(cart.hasSelections).toBe(true)
    expect(cart.entries).toEqual([{ path: '/etc/hosts', host: 'web01', include: true }])
  })

  it('toggleFile twice returns to no rules', () => {
    const cart = useRestoreCartStore()
    cart.toggleFile('web01', '/etc/hosts')
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toEqual([])
    expect(cart.hasSelections).toBe(false)
  })

  it('toggleFolder adds a wildcard rule', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/var')
    expect(cart.rules).toEqual([{ path: '/var', host: null, include: true }])
    expect(cart.hasSelections).toBe(true)
  })

  it('entries excludes exception (include: false) rules', () => {
    const cart = useRestoreCartStore()
    cart.toggleFolder('/etc')
    cart.toggleFile('web01', '/etc/hosts')
    expect(cart.rules).toHaveLength(2)
    expect(cart.entries).toEqual([{ path: '/etc', host: null, include: true }])
  })
})
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd web && npx vitest run src/stores/restoreCart.spec.js`
Expected: FAIL — `Cannot find module './restoreCart'`.

- [ ] **Step 3: Implement the store**

Create `web/src/stores/restoreCart.js`:

```js
import { defineStore } from 'pinia'
import { toggleFile as toggleFileRule, toggleFolder as toggleFolderRule } from '../utils/restoreRules'

export const useRestoreCartStore = defineStore('restoreCart', {
  state: () => ({
    rules: [],
  }),
  getters: {
    hasSelections: (state) => state.rules.length > 0,
    entries: (state) => state.rules.filter((r) => r.include),
  },
  actions: {
    toggleFile(host, path) {
      this.rules = toggleFileRule(this.rules, host, path)
    },
    toggleFolder(path) {
      this.rules = toggleFolderRule(this.rules, path)
    },
  },
})
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd web && npx vitest run src/stores/restoreCart.spec.js`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/restoreCart.js web/src/stores/restoreCart.spec.js
git commit -m "feat: add restoreCart Pinia store"
```

---

## Task 3: `TriStateCheckbox` UI component

**Files:**
- Create: `web/src/components/ui/TriStateCheckbox.vue`
- Test: `web/src/components/ui/TriStateCheckbox.spec.js`

**Interfaces:**
- Produces (used by Task 7): `<TriStateCheckbox :checked="Boolean" :indeterminate="Boolean" @toggle="handler" />`. Renders a single `<input type="checkbox">`. Sets the DOM `indeterminate` property (not expressible as a static HTML attribute) whenever the `indeterminate` prop is true. Stops the click event from propagating, so placing it inside a clickable table row doesn't also trigger the row's own click handler. Emits `toggle` (no payload) on `change`.

- [ ] **Step 1: Write failing tests**

Create `web/src/components/ui/TriStateCheckbox.spec.js`:

```js
import { describe, it, expect, vi } from 'vitest'
import { mount, defineComponent } from '@vue/test-utils'
import TriStateCheckbox from './TriStateCheckbox.vue'

describe('TriStateCheckbox', () => {
  it('reflects the checked prop', () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: true, indeterminate: false } })
    expect(wrapper.find('input').element.checked).toBe(true)
  })

  it('reflects unchecked when checked is false', () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: false, indeterminate: false } })
    expect(wrapper.find('input').element.checked).toBe(false)
  })

  it('sets the DOM indeterminate property when indeterminate is true', () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: false, indeterminate: true } })
    expect(wrapper.find('input').element.indeterminate).toBe(true)
  })

  it('clears the DOM indeterminate property when indeterminate is false', () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: false, indeterminate: false } })
    expect(wrapper.find('input').element.indeterminate).toBe(false)
  })

  it('emits toggle on change', async () => {
    const wrapper = mount(TriStateCheckbox, { props: { checked: false, indeterminate: false } })
    await wrapper.find('input').setValue(true)
    expect(wrapper.emitted('toggle')).toHaveLength(1)
  })

  it('stops the click event from bubbling to an ancestor handler', async () => {
    const onClick = vi.fn()
    const wrapper = mount(
      defineComponent({
        components: { TriStateCheckbox },
        template: `<div @click="onClick"><TriStateCheckbox :checked="false" :indeterminate="false" /></div>`,
        setup() {
          return { onClick }
        },
      })
    )
    await wrapper.find('input').trigger('click')
    expect(onClick).not.toHaveBeenCalled()
  })
})
```

Note: `mount` and `defineComponent` are both named exports of `@vue/test-utils` and `vue` respectively in most setups, but this codebase's `defineComponent` should come from `vue`, not `@vue/test-utils` — fix the import in this step to:

```js
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd web && npx vitest run src/components/ui/TriStateCheckbox.spec.js`
Expected: FAIL — `Failed to resolve import "./TriStateCheckbox.vue"`.

- [ ] **Step 3: Implement the component**

Create `web/src/components/ui/TriStateCheckbox.vue`:

```vue
<script setup>
import { ref, watchEffect } from 'vue'

const props = defineProps({
  checked: { type: Boolean, default: false },
  indeterminate: { type: Boolean, default: false },
})
const emit = defineEmits(['toggle'])

const input = ref(null)
watchEffect(() => {
  if (input.value) input.value.indeterminate = props.indeterminate
})
</script>

<template>
  <input ref="input" type="checkbox" :checked="checked" @click.stop @change="emit('toggle')" />
</template>
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd web && npx vitest run src/components/ui/TriStateCheckbox.spec.js`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/TriStateCheckbox.vue web/src/components/ui/TriStateCheckbox.spec.js
git commit -m "feat: add TriStateCheckbox UI component"
```

---

## Task 4: Restore icon

**Files:**
- Create: `web/src/components/icons/IconRestore.vue`
- Modify: `web/src/components/icons/icons.spec.js`

**Interfaces:**
- Produces (used by Task 5): `IconRestore.vue`, a template-only SFC matching the existing icon components' shape (`viewBox="0 0 24 24"`, `stroke="currentColor"`, forwards a passed `class` attribute).

- [ ] **Step 1: Create the icon**

Create `web/src/components/icons/IconRestore.vue` (matches the existing icons' plain-template shape, e.g. `IconCatalog.vue` — no `<script>` block):

```vue
<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.5"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path d="M3 12a9 9 0 1 0 3-6.7" />
    <path d="M3 4v5h5" />
  </svg>
</template>
```

- [ ] **Step 2: Add it to the icon test suite**

In `web/src/components/icons/icons.spec.js`, add the import and register it in the `icons` map:

```js
import IconRestore from './IconRestore.vue'
```

```js
const icons = { IconClients, IconCatalog, IconPolicies, IconStorage, IconJobs, IconRestore }
```

- [ ] **Step 3: Run the icon tests, confirm they pass**

Run: `cd web && npx vitest run src/components/icons/icons.spec.js`
Expected: PASS (6 icons, up from 5).

- [ ] **Step 4: Commit**

```bash
git add web/src/components/icons/IconRestore.vue web/src/components/icons/icons.spec.js
git commit -m "feat: add restore nav icon"
```

---

## Task 5: Router + Sidebar — Restore nav item and highlight

**Files:**
- Modify: `web/src/router.js`
- Modify: `web/src/router.spec.js`
- Modify: `web/src/components/Sidebar.vue`
- Modify: `web/src/components/Sidebar.spec.js`

**Interfaces:**
- Consumes: `useRestoreCartStore().hasSelections` (Task 2), `IconRestore` (Task 4), and (once Task 6 lands) `RestoreView.vue` as the route's lazy component. The route is added in this task pointing at a component path that doesn't exist until Task 6 — see Step 1's note.
- Produces: route named `'restore'` at path `/restore`; a sixth `Sidebar` nav item.

- [ ] **Step 1: Add the `/restore` route**

In `web/src/router.js`, add a new route. Insert it right after `catalog` (matches the browse-then-restore workflow order used in the sidebar in Step 3 below):

```js
    { path: '/catalog', name: 'catalog', component: () => import('./views/CatalogView.vue') },
    { path: '/restore', name: 'restore', component: () => import('./views/RestoreView.vue') },
```

Note: `RestoreView.vue` doesn't exist yet (Task 6 creates it) — this is fine because the route component is a lazy `import()`, only resolved when the route is actually navigated to or (in `router.spec.js`) when a test explicitly resolves it. Task 6 must land before `router.spec.js`'s "lazily resolves each route" test (see next step) can pass; run this task's Step 2 anyway to see that specific expected failure, then treat that one test as pending until Task 6 — the rest of Step 2's assertions are still valid signal.

- [ ] **Step 2: Update the router test and confirm the expected failure**

In `web/src/router.spec.js`, add `'restore'` to `EXPECTED_NAMES` right after `'catalog'`:

```js
const EXPECTED_NAMES = [
  'home',
  'clients',
  'client-new',
  'client-detail',
  'catalog',
  'restore',
  'policies',
  'policy-detail',
  'storage',
  'storage-detail',
  'jobs',
  'job-detail',
]
```

Run: `cd web && npx vitest run src/router.spec.js`
Expected: the route-names test PASSES; the "lazily resolves each route to its view component" test FAILS on the `restore` route specifically (`RestoreView.vue` doesn't exist yet). This is expected — proceed to Sidebar changes, and re-run this file after Task 6 to confirm full green.

- [ ] **Step 3: Add the Restore nav item and highlight binding to `Sidebar.vue`**

In `web/src/components/Sidebar.vue`, add the store and icon imports, insert the nav item, and bind the highlight class:

```vue
<script setup>
import IconClients from './icons/IconClients.vue'
import IconCatalog from './icons/IconCatalog.vue'
import IconRestore from './icons/IconRestore.vue'
import IconPolicies from './icons/IconPolicies.vue'
import IconStorage from './icons/IconStorage.vue'
import IconJobs from './icons/IconJobs.vue'
import { useRestoreCartStore } from '../stores/restoreCart'

const restoreCart = useRestoreCartStore()

const NAV_ITEMS = [
  { name: 'clients', label: 'Clients', icon: IconClients },
  { name: 'catalog', label: 'Catalog', icon: IconCatalog },
  { name: 'restore', label: 'Restore', icon: IconRestore },
  { name: 'policies', label: 'Policies', icon: IconPolicies },
  { name: 'storage', label: 'Storage', icon: IconStorage },
  { name: 'jobs', label: 'Jobs', icon: IconJobs },
]
</script>
```

Update the `<a>` element's `:class` binding to add the highlight (only when not the active route, so it never fights the active-route styling):

```vue
        <a
          :href="href"
          data-test="nav-link"
          class="flex items-center gap-2.5 pr-2.5 py-1.5 rounded text-sm"
          :class="[
            isActive
              ? 'bg-slate-800 text-white border-l-4 border-blue-500 pl-2'
              : 'text-slate-300 hover:bg-slate-800 hover:text-white pl-3',
            !isActive && item.name === 'restore' && restoreCart.hasSelections ? 'text-blue-400' : '',
          ]"
          @click="navigate"
        >
```

- [ ] **Step 4: Update `Sidebar.spec.js` for the new nav item and order**

Replace the full file `web/src/components/Sidebar.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { createTestingPinia } from '@pinia/testing'
import Sidebar from './Sidebar.vue'

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/clients', name: 'clients', component: { template: '<div />' } },
      { path: '/catalog', name: 'catalog', component: { template: '<div />' } },
      { path: '/restore', name: 'restore', component: { template: '<div />' } },
      { path: '/policies', name: 'policies', component: { template: '<div />' } },
      { path: '/storage', name: 'storage', component: { template: '<div />' } },
      { path: '/jobs', name: 'jobs', component: { template: '<div />' } },
    ],
  })
}

function mountSidebar({ router, hasSelections = false } = {}) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: { restoreCart: { rules: hasSelections ? [{ path: '/etc', host: null, include: true }] : [] } },
  })
  const plugins = [pinia]
  const stubs = { RouterLink: RouterLinkStub }
  if (router) {
    plugins.push(router)
    stubs.RouterLink = false
  }
  return mount(Sidebar, { global: { plugins, stubs } })
}

describe('Sidebar', () => {
  it('links to each top-level named route', () => {
    const wrapper = mountSidebar()
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links.map((l) => l.props('to'))).toEqual([
      { name: 'clients' },
      { name: 'catalog' },
      { name: 'restore' },
      { name: 'policies' },
      { name: 'storage' },
      { name: 'jobs' },
    ])
  })

  it('renders a brand header and an icon before each nav label', () => {
    const wrapper = mountSidebar()
    expect(wrapper.text()).toContain('Miniprotector')
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links).toHaveLength(6)
    links.forEach((link) => {
      expect(link.find('svg').exists()).toBe(true)
    })
  })

  it("marks the current route's nav link active and leaves the others inactive", async () => {
    const router = makeRouter()
    router.push({ name: 'policies' })
    await router.isReady()

    const wrapper = mountSidebar({ router })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links).toHaveLength(6)

    const activeLink = links[3]
    expect(activeLink.text()).toContain('Policies')
    expect(activeLink.classes()).toEqual(
      expect.arrayContaining(['bg-slate-800', 'text-white', 'border-l-4', 'border-blue-500', 'pl-2'])
    )
    expect(activeLink.classes()).not.toContain('pl-3')

    const inactiveLinks = [links[0], links[1], links[2], links[4], links[5]]
    inactiveLinks.forEach((link) => {
      expect(link.classes()).toEqual(expect.arrayContaining(['text-slate-300', 'pl-3']))
      expect(link.classes()).not.toContain('bg-slate-800')
      expect(link.classes()).not.toContain('border-l-4')
      expect(link.classes()).not.toContain('pl-2')
    })
  })

  it('does not highlight the Restore link when the cart is empty', () => {
    const wrapper = mountSidebar({ hasSelections: false })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links[2].text()).toContain('Restore')
    expect(links[2].classes()).not.toContain('text-blue-400')
  })

  it('highlights the Restore link when the cart has selections', () => {
    const wrapper = mountSidebar({ hasSelections: true })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links[2].text()).toContain('Restore')
    expect(links[2].classes()).toContain('text-blue-400')
  })

  it('does not highlight Restore when it is the active route, even with selections', async () => {
    const router = makeRouter()
    router.push({ name: 'restore' })
    await router.isReady()

    const wrapper = mountSidebar({ router, hasSelections: true })
    const links = wrapper.findAll('[data-test="nav-link"]')
    expect(links[2].classes()).toContain('bg-slate-800')
    expect(links[2].classes()).not.toContain('text-blue-400')
  })
})
```

- [ ] **Step 5: Run the Sidebar tests, confirm they pass**

Run: `cd web && npx vitest run src/components/Sidebar.spec.js`
Expected: PASS (7 tests).

- [ ] **Step 6: Commit**

```bash
git add web/src/router.js web/src/router.spec.js web/src/components/Sidebar.vue web/src/components/Sidebar.spec.js
git commit -m "feat: add Restore nav item, route, and cart-highlight styling"
```

---

## Task 6: Restore view placeholder

**Files:**
- Create: `web/src/views/RestoreView.vue`
- Test: `web/src/views/RestoreView.spec.js`

**Interfaces:**
- Consumes: `useRestoreCartStore().entries` (Task 2), `PageHeader`/`StatusMessage` (existing `components/ui/`).
- Produces: `/restore` page content. No remove/clear controls, no grouping — a flat list, explicitly a placeholder per the design doc.

- [ ] **Step 1: Write failing tests**

Create `web/src/views/RestoreView.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import RestoreView from './RestoreView.vue'

function mountView(rules) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { restoreCart: { rules } } })
  return mount(RestoreView, { global: { plugins: [pinia] } })
}

describe('RestoreView', () => {
  it('shows the empty state when the cart has no selections', () => {
    const wrapper = mountView([])
    expect(wrapper.text()).toContain('No files selected for restore yet.')
  })

  it('lists a folder wildcard rule as path/*', () => {
    const wrapper = mountView([{ path: '/var', host: null, include: true }])
    expect(wrapper.text()).toContain('/var/*')
  })

  it('lists a file rule as path (host)', () => {
    const wrapper = mountView([{ path: '/etc/hosts', host: 'web01', include: true }])
    expect(wrapper.text()).toContain('/etc/hosts (web01)')
  })

  it('omits exception (include: false) rules from the list', () => {
    const wrapper = mountView([
      { path: '/etc', host: null, include: true },
      { path: '/etc/hosts', host: 'web01', include: false },
    ])
    expect(wrapper.text()).toContain('/etc/*')
    expect(wrapper.text()).not.toContain('/etc/hosts')
  })

  it('renders the page breadcrumb', () => {
    const wrapper = mountView([])
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Restore')
  })
})
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd web && npx vitest run src/views/RestoreView.spec.js`
Expected: FAIL — `Failed to resolve import "./RestoreView.vue"`.

- [ ] **Step 3: Implement the view**

Create `web/src/views/RestoreView.vue`:

```vue
<script setup>
import { useRestoreCartStore } from '../stores/restoreCart'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'

const restoreCart = useRestoreCartStore()

function label(entry) {
  return entry.host === null ? `${entry.path}/*` : `${entry.path} (${entry.host})`
}
</script>

<template>
  <div>
    <PageHeader title="Restore" :crumbs="[{ label: 'Restore' }]" />
    <StatusMessage :empty="restoreCart.entries.length === 0" empty-text="No files selected for restore yet.">
      <ul>
        <li v-for="entry in restoreCart.entries" :key="`${entry.host ?? ''}:${entry.path}`">
          {{ label(entry) }}
        </li>
      </ul>
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd web && npx vitest run src/views/RestoreView.spec.js`
Expected: PASS (5 tests).

- [ ] **Step 5: Re-run the router spec to confirm Task 5's deferred assertion now passes**

Run: `cd web && npx vitest run src/router.spec.js`
Expected: PASS (both tests, including "lazily resolves each route to its view component").

- [ ] **Step 6: Commit**

```bash
git add web/src/views/RestoreView.vue web/src/views/RestoreView.spec.js
git commit -m "feat: add placeholder Restore view"
```

---

## Task 7: Catalog checkbox column

**Files:**
- Modify: `web/src/views/CatalogView.vue`
- Modify: `web/src/views/CatalogView.spec.js`

**Interfaces:**
- Consumes: `resolveFile`, `resolveFolderState` (Task 1, called directly — not through the store, see Task 2's note), `useRestoreCartStore()` (Task 2), `TriStateCheckbox` (Task 3).
- Produces: a `select` column rendered for every row (folder and file) in the catalog table, wired to the restore cart.

- [ ] **Step 1: Add failing tests to `CatalogView.spec.js`**

In `web/src/views/CatalogView.spec.js`, add the new imports and extend `mountView`'s `initialState` with an empty `restoreCart`:

```js
import { useRestoreCartStore } from '../stores/restoreCart'
```

```js
function mountView(state, restoreCartState) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: {
      catalog: {
        currentPath: null,
        entries: [],
        loading: false,
        error: null,
        filters: { pattern: '', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
        clientFacets: [],
        clientFacetsError: null,
        jobFacets: [],
        jobFacetsError: null,
        directoryChildren: [],
        directoryChildrenLoading: false,
        directoryChildrenError: null,
        ...state,
      },
      restoreCart: { rules: [], ...restoreCartState },
    },
  })
  const wrapper = mount(CatalogView, {
    global: { plugins: [pinia], stubs: { DateRangePanel: true, FacetPanel: true } },
  })
  return { wrapper, catalog: useCatalogStore(), restoreCart: useRestoreCartStore() }
}
```

(this changes `mountView`'s signature to take an optional second argument — existing calls with only one argument keep working unchanged since the second parameter defaults via `{ rules: [], ...undefined }`... actually spreading `undefined` throws. Use `restoreCartState = {}` as the default parameter value instead: `function mountView(state, restoreCartState = {}) {`)

Add these tests at the end of the `describe('CatalogView', ...)` block, before the closing `})`:

```js
  it('renders a checkbox for a file row reflecting its restore-cart state', () => {
    const { wrapper } = mountView(
      { currentPath: '/var/lib/dbdata', entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db' })] },
      { rules: [{ path: '/var/lib/dbdata/data.db', host: 'database', include: true }] }
    )
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    expect(checkbox.element.checked).toBe(true)
  })

  it('renders an unchecked checkbox for a file row not in the restore cart', () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db' })],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    expect(checkbox.element.checked).toBe(false)
  })

  it('clicking a file checkbox calls restoreCart.toggleFile and does not navigate', async () => {
    const { wrapper, catalog, restoreCart } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db' })],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    await checkbox.trigger('click')
    expect(restoreCart.toggleFile).toHaveBeenCalledWith('database', '/var/lib/dbdata/data.db')
    expect(catalog.navigateTo).not.toHaveBeenCalled()
  })

  it('renders a checked checkbox for a folder row fully covered by a wildcard rule', () => {
    const { wrapper } = mountView(
      {
        currentPath: '/var',
        directoryChildren: [{ path: '/var/log', name: 'log', file_count: 3, last_seen: 1752400010, has_children: false }],
      },
      { rules: [{ path: '/var/log', host: null, include: true }] }
    )
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    expect(checkbox.element.checked).toBe(true)
    expect(checkbox.element.indeterminate).toBe(false)
  })

  it('renders an indeterminate checkbox for a folder row with a nested exception', () => {
    const { wrapper } = mountView(
      {
        currentPath: '/var',
        directoryChildren: [{ path: '/var/log', name: 'log', file_count: 3, last_seen: 1752400010, has_children: true }],
      },
      {
        rules: [
          { path: '/var/log', host: null, include: true },
          { path: '/var/log/access.log', host: 'web01', include: false },
        ],
      }
    )
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    expect(checkbox.element.indeterminate).toBe(true)
  })

  it('clicking a folder checkbox calls restoreCart.toggleFolder and does not navigate into it', async () => {
    const { wrapper, catalog, restoreCart } = mountView({
      currentPath: '/var',
      directoryChildren: [{ path: '/var/log', name: 'log', file_count: 3, last_seen: 1752400010, has_children: false }],
    })
    const checkbox = wrapper.find('tbody tr input[type="checkbox"]')
    await checkbox.trigger('click')
    expect(restoreCart.toggleFolder).toHaveBeenCalledWith('/var/log')
    expect(catalog.navigateTo).not.toHaveBeenCalled()
  })
```

- [ ] **Step 2: Run the tests, confirm the new ones fail**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: FAIL — no `input[type="checkbox"]` found in any row (no checkbox column exists yet).

- [ ] **Step 3: Wire the checkbox column into `CatalogView.vue`**

Add imports and the `restoreCart` instance near the top of the `<script setup>` block in `web/src/views/CatalogView.vue`:

```js
import { useRestoreCartStore } from '../stores/restoreCart'
import { resolveFile, resolveFolderState } from '../utils/restoreRules'
import TriStateCheckbox from '../components/ui/TriStateCheckbox.vue'
```

```js
const catalog = useCatalogStore()
const restoreCart = useRestoreCartStore()
```

Add two helper functions near `onRowClick` (after the `rows` computed, before `summaryLabel` — placement doesn't matter functionally, keep row-related helpers grouped):

```js
function checkboxProps(row) {
  if (row.isFolder) {
    const state = resolveFolderState(restoreCart.rules, row.path)
    return { checked: state === 'checked', indeterminate: state === 'indeterminate' }
  }
  return { checked: resolveFile(restoreCart.rules, row.sourceHost, row.path), indeterminate: false }
}

function toggleSelection(row) {
  if (row.isFolder) restoreCart.toggleFolder(row.path)
  else restoreCart.toggleFile(row.sourceHost, row.path)
}
```

Prepend a `select` column to `baseColumns`:

```js
const baseColumns = [
  { label: '', field: 'select', sortable: false },
  { label: 'Path', field: 'path', sortable: true },
  { label: 'Source Host', field: 'sourceHost', sortable: true },
  { label: 'Store Host', field: 'representative.store_host', sortable: true },
  { label: 'Size', field: 'representative.size', sortable: true, type: 'number' },
  { label: 'Mode', field: 'representative.mode', sortable: true },
  { label: 'Modified', field: 'representative.mod_time', sortable: true, type: 'number' },
  { label: 'Versions', field: 'versions', sortable: false },
]
```

In the template, add a `select`-column branch to the `table-row` slot, before the existing `row.isFolder` branch — change `v-if="row.isFolder"` to `v-else-if` on that branch:

```vue
      <DataTable :columns="columns" :rows="rows" :search-enabled="false" @row-click="onRowClick">
        <template #table-row="{ column, row }">
          <span v-if="column.field === 'select'">
            <TriStateCheckbox v-bind="checkboxProps(row)" @toggle="toggleSelection(row)" />
          </span>
          <template v-else-if="row.isFolder">
            <span v-if="column.field === 'path'" class="font-semibold">{{ row.name }}/</span>
            <span v-else-if="column.field === 'representative.mod_time'">{{ formatTimestamp(row.last_seen) || '—' }}</span>
            <span v-else-if="column.field === 'versions'">{{ row.file_count || '' }}</span>
            <span v-else></span>
          </template>
          <template v-else>
            <span v-if="column.field === 'path'">{{ browsing ? row.representative.short_filename : row.path }}</span>
            <span v-else-if="column.field === 'sourceHost'">{{ row.sourceHost }}</span>
            <span v-else-if="column.field === 'representative.store_host'">{{ row.representative.store_host }}</span>
            <span v-else-if="column.field === 'representative.size'">{{ formatBytes(row.representative.size) }}</span>
            <span v-else-if="column.field === 'representative.mode'">{{ row.representative.mode }}</span>
            <span v-else-if="column.field === 'representative.mod_time'">{{ formatTimestamp(row.representative.mod_time) || '—' }}</span>
            <span v-else-if="column.field === 'versions'">{{ row.versions.length > 1 ? row.versions.length : '' }}</span>
          </template>
        </template>
      </DataTable>
```

- [ ] **Step 4: Run the full `CatalogView.spec.js`, confirm everything passes**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: PASS, including all pre-existing tests (the `checkboxProps`/`toggleSelection` addition and the new leading column must not change any pre-existing row-click, sort, or grouping behavior — the click-stop on `TriStateCheckbox`, from Task 3, is what keeps the existing row-click test at `web/src/views/CatalogView.spec.js`'s `'navigates into a folder when its row is clicked'` still passing, since that test triggers `click` on the `<tr>` itself, not the checkbox).

- [ ] **Step 5: Run the full frontend test suite**

Run: `cd web && npx vitest run`
Expected: PASS, all files.

- [ ] **Step 6: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js
git commit -m "feat: add restore-selection checkboxes to the catalog table"
```

---

## Task 8: Documentation

Per this repo's `.claude/CLAUDE.md` feature-change rule: update the affected component doc, and add a `CHANGELOG.md` entry.

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update `docs/components/web.md`**

In the `## Pages` section, after the existing `/catalog` bullet's paragraph (ends "...opens a modal (click anywhere on that row) listing that file's other versions."), add a new paragraph describing the selection UI, and add a `/restore` bullet in the pages list.

Add to the end of the `/catalog` bullet's text (same bullet, new sentence):

```
  Each row (folder or file) now also carries a checkbox for staging it into the restore cart
  (`stores/restoreCart.js`): checking a file adds it by `(source_host, path)`; checking a folder adds
  one host-agnostic wildcard rule covering everything under it, rather than one entry per file, so
  a large folder selection stays a single rule. Selection state is *resolved* from this small rule
  list on demand (longest-matching-path wins, like `.gitignore`), which is also what lets a user
  drill into an already-selected folder and see its contents pre-checked, then uncheck individual
  items to carve out exceptions — unchecking shows as a partial/indeterminate checkbox on any
  ancestor folder row. The cart is in-memory only (no persistence yet) and UI-only: nothing is
  submitted for restore in this pass.
```

Add a new bullet immediately after the `/catalog` bullet (before `/policies`):

```
- `/restore` — placeholder list of everything currently staged in the restore cart (folder
  selections as `path/*`, file selections as `path (host)`); no actions yet, just a preview of what
  `/catalog`'s checkboxes have accumulated. The sidebar's Restore link highlights whenever the cart
  is non-empty.
```

Add a new "See Also" entry:

```
- [Design: restore cart](../superpowers/specs/2026-08-09-restore-cart-design.md)
```

- [ ] **Step 2: Add the `CHANGELOG.md` entry**

Insert a new dated section at the top of `CHANGELOG.md`, immediately after the `# Changelog` header and its intro line, above the existing `## 2026-08-09 — catalog write-path...` entry:

```markdown
## 2026-08-09 — catalog UI gains restore selection (file/folder checkboxes, restore cart)

The catalog view can now stage files and folders for restore. Each row gets a checkbox; checking a
file adds it to a new `restoreCart` Pinia store keyed by `(source_host, path)`, while checking a
folder adds a single host-agnostic wildcard rule instead of one entry per contained file, so
selecting a large folder stays cheap regardless of its size. Selection state is resolved from this
small rule list on demand using longest-matching-path semantics (like `.gitignore`), which lets a
user drill into an already-selected folder and see it pre-checked, then uncheck individual items to
carve out exceptions. The sidebar's new Restore link highlights whenever the cart is non-empty, and
a new placeholder `/restore` page lists what's staged. This is UI-only groundwork — no restore job
submission or backend changes yet.
```

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document the restore cart and catalog selection UI"
```

---

## Final check

- [ ] Run the entire frontend test suite once more end to end: `cd web && npx vitest run`. Expected: PASS, all files, no skipped/failing tests.
