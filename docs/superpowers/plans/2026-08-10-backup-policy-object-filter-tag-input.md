# Backup Policy Object Filter Tag Input Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the comma-separated include/exclude text fields on the backup policy form's object filters with a chip/tag input that validates each pattern's glob syntax and flags same-list parent/child path overlaps before Save is allowed.

**Architecture:** A pure JS validation module (`globPattern.js`) is ported by hand from Go's `path.Match` syntax grammar (the same grammar `backup_policy.go`'s `Validate()` checks server-side) plus a plain-string prefix conflict checker; a new reusable `TagInput.vue` component owns chip entry/removal and calls into that module on each commit; `BackupPolicyFormModal.vue` swaps its two free-text inputs per filter row for two `TagInput` instances and its form model's `include`/`exclude` fields from CSV strings to the `string[]` arrays the backend already expects.

**Tech Stack:** Vue 3 (`<script setup>`), Vitest + `@vue/test-utils`, Pinia (unaffected by this change, no store involved).

## Global Constraints

- No new npm dependency — `web/package.json` has no tag-input library today; this follows the existing hand-rolled `Base*`/`RepeatableFieldList` convention.
- `TagInput` takes a plain `items: Array` prop and mutates it in place (push/splice), matching `RepeatableFieldList.vue`'s existing convention — not Vue's `defineModel`/`v-model` macro, which no sibling component in this codebase uses.
- Client-side validation is a faster feedback loop only; `BackupPolicy.Validate()` in `src/cmd/policy-server/backup_policy.go` (Go, `path.Match`-based) remains the sole source of truth and is not touched by this plan.
- Per `.claude/CLAUDE.md`'s feature-change doc rule: `docs/components/web.md` and `CHANGELOG.md` must be updated before this is considered done (Task 4).

---

### Task 1: `globPattern.js` — glob syntax validation and parent/child conflict detection

**Files:**
- Create: `web/src/utils/globPattern.js`
- Test: `web/src/utils/globPattern.spec.js`

**Interfaces:**
- Produces: `validateGlobPattern(pattern: string) -> { valid: boolean, error?: string }`
- Produces: `findParentChildConflict(patterns: string[], pattern: string) -> string | undefined` (the first conflicting entry from `patterns`, or `undefined`)

- [ ] **Step 1: Write the failing test file**

Create `web/src/utils/globPattern.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { validateGlobPattern, findParentChildConflict } from './globPattern'

describe('validateGlobPattern', () => {
  it('accepts an empty pattern', () => {
    expect(validateGlobPattern('')).toEqual({ valid: true })
  })

  it('accepts a plain literal', () => {
    expect(validateGlobPattern('database.sql')).toEqual({ valid: true })
  })

  it('accepts a leading-star pattern', () => {
    expect(validateGlobPattern('*.sql')).toEqual({ valid: true })
  })

  it('accepts a bare trailing star', () => {
    expect(validateGlobPattern('*')).toEqual({ valid: true })
  })

  it('accepts a single-char wildcard', () => {
    expect(validateGlobPattern('file?.txt')).toEqual({ valid: true })
  })

  it('accepts a character class range', () => {
    expect(validateGlobPattern('[a-z]*.log')).toEqual({ valid: true })
  })

  it('accepts a negated character class', () => {
    expect(validateGlobPattern('[^a-z]*.log')).toEqual({ valid: true })
  })

  it('accepts an escaped literal star', () => {
    expect(validateGlobPattern('file\\*.txt')).toEqual({ valid: true })
  })

  it('accepts an escaped closing bracket inside a class', () => {
    expect(validateGlobPattern('[\\]]')).toEqual({ valid: true })
  })

  it('rejects an unterminated character class', () => {
    expect(validateGlobPattern('[abc').valid).toBe(false)
  })

  it('rejects an empty character class', () => {
    expect(validateGlobPattern('[]').valid).toBe(false)
  })

  it('rejects a dangling range dash before the closing bracket', () => {
    expect(validateGlobPattern('[a-]').valid).toBe(false)
  })

  it('rejects a trailing lone backslash', () => {
    expect(validateGlobPattern('file\\').valid).toBe(false)
  })

  it('rejects an unterminated class after a star', () => {
    expect(validateGlobPattern('*[').valid).toBe(false)
  })

  it('includes an error message when invalid', () => {
    const result = validateGlobPattern('[abc')
    expect(result.valid).toBe(false)
    expect(typeof result.error).toBe('string')
    expect(result.error.length).toBeGreaterThan(0)
  })
})

describe('findParentChildConflict', () => {
  it('returns undefined when nothing conflicts', () => {
    expect(findParentChildConflict(['*.sql', '*.log'], '*.dump')).toBeUndefined()
  })

  it('flags a new pattern that is a child of an existing one', () => {
    expect(findParentChildConflict(['/var/log'], '/var/log/app')).toBe('/var/log')
  })

  it('flags a new pattern that is a parent of an existing one', () => {
    expect(findParentChildConflict(['/var/log/app'], '/var/log')).toBe('/var/log/app')
  })

  it('flags an exact duplicate', () => {
    expect(findParentChildConflict(['/var/log'], '/var/log')).toBe('/var/log')
  })

  it('does not flag a pattern that merely shares a text prefix', () => {
    expect(findParentChildConflict(['/var/log'], '/var/logs')).toBeUndefined()
  })

  it('does not flag an unrelated sibling directory', () => {
    expect(findParentChildConflict(['/var/log'], '/var/lib')).toBeUndefined()
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/utils/globPattern.spec.js`
Expected: FAIL — `Failed to resolve import "./globPattern"` (the module doesn't exist yet).

- [ ] **Step 3: Write the implementation**

Create `web/src/utils/globPattern.js`:

```js
// globPattern.js: a hand-ported syntax-only check mirroring Go's
// path.Match grammar (the "path" package, not "path/filepath" -- see
// src/cmd/policy-server/backup_policy.go's Validate(), which calls
// path.Match(pattern, "") for exactly this same syntax-only check). Kept in
// lockstep with path.Match's grammar rather than approximated with a looser
// regex, so a pattern this module accepts never bounces off the server and
// vice versa.
//
// pattern := { term }
// term    := '*' | '?' | '[' ['^'] { range } ']' | c | '\\' c
// range   := c | '\\' c | (c|'\\'c) '-' (c|'\\'c)

// scanChunk splits pattern into its leading run of '*' (star) and the
// following star-free chunk, treating '[...]' as opaque (a '*' inside
// brackets doesn't split) and skipping the character right after a
// backslash so an escaped '*' isn't treated as a split point either.
function scanChunk(pattern) {
  let star = false
  while (pattern.length > 0 && pattern[0] === '*') {
    pattern = pattern.slice(1)
    star = true
  }
  let inrange = false
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i]
    if (c === '\\') {
      if (i + 1 < pattern.length) i++
    } else if (c === '[') {
      inrange = true
    } else if (c === ']') {
      inrange = false
    } else if (c === '*' && !inrange) {
      return { star, chunk: pattern.slice(0, i), rest: pattern.slice(i) }
    }
  }
  return { star, chunk: pattern, rest: '' }
}

// getEsc validates and consumes one possibly-escaped character from the
// front of chunk, for use inside a '[...]' character class: a bare '-' or
// ']' (unescaped), an empty chunk, or a trailing lone '\\' is an error.
function getEsc(chunk) {
  if (chunk.length === 0 || chunk[0] === '-' || chunk[0] === ']') {
    return { error: true }
  }
  if (chunk[0] === '\\') {
    chunk = chunk.slice(1)
    if (chunk.length === 0) return { error: true }
  }
  return { error: false, rest: chunk.slice(1) }
}

// checkChunkSyntax validates one star-free chunk's syntax. This mirrors
// calling Go's matchChunk(chunk, "") -- since the name being matched is
// always empty, only matchChunk's syntax-error paths are reachable: a
// malformed '[...]' character class, or a trailing lone '\\'.
function checkChunkSyntax(chunk) {
  while (chunk.length > 0) {
    const c = chunk[0]
    if (c === '[') {
      chunk = chunk.slice(1)
      if (chunk[0] === '^') chunk = chunk.slice(1)
      let nrange = 0
      for (;;) {
        if (chunk.length > 0 && chunk[0] === ']' && nrange > 0) {
          chunk = chunk.slice(1)
          break
        }
        const lo = getEsc(chunk)
        if (lo.error) return false
        chunk = lo.rest
        if (chunk[0] === '-') {
          const hi = getEsc(chunk.slice(1))
          if (hi.error) return false
          chunk = hi.rest
        }
        nrange++
      }
    } else if (c === '?') {
      chunk = chunk.slice(1)
    } else if (c === '\\') {
      chunk = chunk.slice(1)
      if (chunk.length === 0) return false
      chunk = chunk.slice(1)
    } else {
      chunk = chunk.slice(1)
    }
  }
  return true
}

// validateGlobPattern checks pattern's syntax only (never matches against a
// real path) -- the JS-side equivalent of Go's path.Match(pattern, "").
export function validateGlobPattern(pattern) {
  let p = pattern
  while (p.length > 0) {
    const { star, chunk, rest } = scanChunk(p)
    if (star && chunk === '') {
      return { valid: true } // trailing '*' with nothing after it
    }
    if (!checkChunkSyntax(chunk)) {
      return { valid: false, error: `invalid pattern: syntax error near "${chunk}"` }
    }
    p = rest
  }
  return { valid: true }
}

// findParentChildConflict returns the first entry in patterns that is a
// plain-string parent, child, or duplicate of pattern -- normalized with a
// trailing '/' on both sides so "/var/log" doesn't false-positive against
// "/var/logs" (shares a text prefix but is a different directory).
// Comparison is deliberately plain string prefix, not glob-aware.
export function findParentChildConflict(patterns, pattern) {
  const normalize = (p) => (p.endsWith('/') ? p : p + '/')
  const target = normalize(pattern)
  return patterns.find((existing) => {
    const other = normalize(existing)
    return target.startsWith(other) || other.startsWith(target)
  })
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/utils/globPattern.spec.js`
Expected: PASS, all cases green.

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/globPattern.js web/src/utils/globPattern.spec.js
git commit -m "feat: add glob pattern syntax and overlap validation for object filters"
```

---

### Task 2: `TagInput.vue` — reusable chip/tag input component

**Files:**
- Create: `web/src/components/ui/TagInput.vue`
- Test: `web/src/components/ui/TagInput.spec.js`

**Interfaces:**
- Consumes: `validateGlobPattern`, `findParentChildConflict` from `web/src/utils/globPattern.js` (Task 1)
- Produces: `TagInput` component. Props: `items: { type: Array, required: true }` (mutated in place — same convention as `RepeatableFieldList`), `testPrefix: { type: String, required: true }`, `placeholder: { type: String, default: '' }`. Exposes `isValid(): boolean` via `defineExpose` for the parent form to query before submit. Renders chips with `data-test="${testPrefix}-chip"` (invalid ones additionally get class `border-red-500` and a `title` attribute with the error message), a remove button per chip at `data-test="${testPrefix}-chip-remove"`, and a text box at `data-test="${testPrefix}-input"`.

- [ ] **Step 1: Write the failing test file**

Create `web/src/components/ui/TagInput.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import TagInput from './TagInput.vue'

function mountTagInput(items = []) {
  const wrapper = mount(TagInput, { props: { items, testPrefix: 'pattern' } })
  return { wrapper, items }
}

async function type(wrapper, text) {
  await wrapper.find('[data-test="pattern-input"]').setValue(text)
}

async function pressKey(wrapper, key) {
  await wrapper.find('[data-test="pattern-input"]').trigger('keydown', { key })
}

describe('TagInput', () => {
  it('renders existing items as chips on mount', () => {
    const { wrapper } = mountTagInput(['*.sql', '*.log'])
    const texts = wrapper.findAll('[data-test="pattern-chip"]').map((n) => n.text())
    expect(texts[0]).toContain('*.sql')
    expect(texts[1]).toContain('*.log')
  })

  it('adds a chip and clears the input on Enter', async () => {
    const { wrapper, items } = mountTagInput([])
    await type(wrapper, '*.sql')
    await pressKey(wrapper, 'Enter')
    expect(wrapper.findAll('[data-test="pattern-chip"]')).toHaveLength(1)
    expect(wrapper.find('[data-test="pattern-input"]').element.value).toBe('')
    expect(items).toEqual(['*.sql'])
  })

  it('adds a chip on comma', async () => {
    const { wrapper, items } = mountTagInput([])
    await type(wrapper, '*.sql')
    await pressKey(wrapper, ',')
    expect(items).toEqual(['*.sql'])
  })

  it('commits leftover text on blur', async () => {
    const { wrapper, items } = mountTagInput([])
    await type(wrapper, '*.sql')
    await wrapper.find('[data-test="pattern-input"]').trigger('blur')
    expect(items).toEqual(['*.sql'])
  })

  it('ignores empty/whitespace-only commits', async () => {
    const { wrapper, items } = mountTagInput([])
    await type(wrapper, '   ')
    await pressKey(wrapper, 'Enter')
    expect(wrapper.findAll('[data-test="pattern-chip"]')).toHaveLength(0)
    expect(items).toEqual([])
  })

  it('removes the last chip on backspace when the input is empty', async () => {
    const { wrapper, items } = mountTagInput(['*.sql', '*.log'])
    await pressKey(wrapper, 'Backspace')
    expect(items).toEqual(['*.sql'])
  })

  it('does not remove a chip on backspace when the input has text', async () => {
    const { wrapper, items } = mountTagInput(['*.sql'])
    await type(wrapper, 'x')
    await pressKey(wrapper, 'Backspace')
    expect(items).toEqual(['*.sql'])
  })

  it('removes a specific chip via its remove button', async () => {
    const { wrapper, items } = mountTagInput(['*.sql', '*.log'])
    await wrapper.findAll('[data-test="pattern-chip-remove"]')[0].trigger('click')
    expect(items).toEqual(['*.log'])
  })

  it('flags a syntactically invalid pattern and reports invalid', async () => {
    const { wrapper } = mountTagInput([])
    await type(wrapper, '[abc')
    await pressKey(wrapper, 'Enter')
    expect(wrapper.find('[data-test="pattern-chip"]').classes()).toContain('border-red-500')
    expect(wrapper.vm.isValid()).toBe(false)
  })

  it('flags a pattern that overlaps an existing one in the list', async () => {
    const { wrapper } = mountTagInput(['/var/log'])
    await type(wrapper, '/var/log/app')
    await pressKey(wrapper, 'Enter')
    const chips = wrapper.findAll('[data-test="pattern-chip"]')
    expect(chips[1].classes()).toContain('border-red-500')
    expect(wrapper.vm.isValid()).toBe(false)
  })

  it('does not flag unrelated patterns that merely share a text prefix', async () => {
    const { wrapper } = mountTagInput(['/var/log'])
    await type(wrapper, '/var/logs')
    await pressKey(wrapper, 'Enter')
    const chips = wrapper.findAll('[data-test="pattern-chip"]')
    expect(chips[1].classes()).not.toContain('border-red-500')
    expect(wrapper.vm.isValid()).toBe(true)
  })

  it('reports valid when all chips are valid and non-conflicting', async () => {
    const { wrapper } = mountTagInput(['*.sql'])
    await type(wrapper, '*.log')
    await pressKey(wrapper, 'Enter')
    expect(wrapper.vm.isValid()).toBe(true)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/components/ui/TagInput.spec.js`
Expected: FAIL — `Failed to resolve import "./TagInput.vue"`.

- [ ] **Step 3: Write the implementation**

Create `web/src/components/ui/TagInput.vue`:

```vue
<!-- web/src/components/ui/TagInput.vue -->
<script setup>
import { reactive, ref } from 'vue'
import { validateGlobPattern, findParentChildConflict } from '../../utils/globPattern'

const props = defineProps({
  items: { type: Array, required: true },
  testPrefix: { type: String, required: true },
  placeholder: { type: String, default: '' },
})

let nextId = 0

function errorFor(value, existingValues) {
  const syntax = validateGlobPattern(value)
  if (!syntax.valid) return syntax.error
  const conflict = findParentChildConflict(existingValues, value)
  if (conflict) return `overlaps with "${conflict}"`
  return undefined
}

const chips = reactive(
  props.items.map((value) => ({ id: nextId++, value, error: validateGlobPattern(value).valid ? undefined : validateGlobPattern(value).error }))
)
const text = ref('')

function syncItems() {
  props.items.splice(0, props.items.length, ...chips.map((c) => c.value))
}

function commit(rawText) {
  const value = rawText.trim()
  if (!value) return
  const error = errorFor(
    value,
    chips.map((c) => c.value)
  )
  chips.push({ id: nextId++, value, error })
  syncItems()
}

function removeChip(id) {
  const index = chips.findIndex((c) => c.id === id)
  if (index !== -1) chips.splice(index, 1)
  syncItems()
}

function removeLast() {
  if (chips.length === 0) return
  chips.pop()
  syncItems()
}

function onKeydown(event) {
  if (event.key === 'Enter' || event.key === ',') {
    event.preventDefault()
    commit(text.value)
    text.value = ''
  } else if (event.key === 'Backspace' && text.value === '') {
    removeLast()
  }
}

function onBlur() {
  if (text.value.trim()) {
    commit(text.value)
    text.value = ''
  }
}

function isValid() {
  return chips.every((c) => !c.error)
}

defineExpose({ isValid })
</script>

<template>
  <div>
    <div class="flex flex-wrap gap-1 mb-1">
      <span
        v-for="chip in chips"
        :key="chip.id"
        :data-test="`${testPrefix}-chip`"
        :title="chip.error || ''"
        class="inline-flex items-center gap-1 border rounded px-2 py-0.5 text-sm"
        :class="chip.error ? 'border-red-500 text-red-600' : 'border-gray-300'"
      >
        {{ chip.value }}
        <button
          type="button"
          :data-test="`${testPrefix}-chip-remove`"
          class="leading-none"
          @click="removeChip(chip.id)"
        >
          ×
        </button>
      </span>
    </div>
    <input
      :data-test="`${testPrefix}-input`"
      :placeholder="placeholder"
      v-model="text"
      class="border rounded px-2 py-1 text-sm"
      @keydown="onKeydown"
      @blur="onBlur"
    />
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/components/ui/TagInput.spec.js`
Expected: PASS, all cases green.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/TagInput.vue web/src/components/ui/TagInput.spec.js
git commit -m "feat: add TagInput chip component with glob validation and overlap detection"
```

---

### Task 3: Integrate `TagInput` into `BackupPolicyFormModal.vue`

**Files:**
- Modify: `web/src/components/backup_policies/BackupPolicyFormModal.vue`
- Modify: `web/src/components/backup_policies/BackupPolicyFormModal.spec.js`

**Interfaces:**
- Consumes: `TagInput` (Task 2), props `items`/`testPrefix`/`placeholder` only — **not** its exposed `isValid()`. Submit-time validation is re-derived directly from `form.object_filters` using `validateGlobPattern`/`findParentChildConflict` (Task 1) instead of collecting child component refs. (`TagInput` instances live inside `RepeatableFieldList`'s `#row` scoped slot; a `ref="name"` on an element inside scoped-slot content compiled in a *different* SFC than the one declaring the `v-for` does not get Vue's automatic ref-array batching — each row would silently overwrite the same ref, leaving only the last row checked. Re-deriving validity from data sidesteps this entirely rather than working around it.)
- Produces: `object_filters` entries emitted by `save`/`run-now` keep the exact shape `{ path, include: string[], exclude: string[] }` already expected by the backend (`docs/superpowers/specs/2026-08-03-backup-policy-storage-link-design.md` / `backup_policy.go`) — unchanged from before this task, only how the arrays are populated in the UI changes.

- [ ] **Step 1: Update the failing/changing assertions in the spec file**

In `web/src/components/backup_policies/BackupPolicyFormModal.spec.js`, replace the object-filter-related assertion in `'pre-fills fields from the policy prop in edit mode'`:

```js
    expect(wrapper.find('[data-test="filter-path-input"]').element.value).toBe('/var/lib/dbdata')
    expect(wrapper.find('[data-test="filter-include-input"]').element.value).toBe('*.sql')
    expect(wrapper.find('[data-test="window-input"]').element.value).toBe('0 2 * * *')
```

with:

```js
    expect(wrapper.find('[data-test="filter-path-input"]').element.value).toBe('/var/lib/dbdata')
    expect(wrapper.find('[data-test="filter-include-chip"]').text()).toContain('*.sql')
    expect(wrapper.find('[data-test="window-input"]').element.value).toBe('0 2 * * *')
```

Then replace the whole `'adds an object filter and splits comma-separated include/exclude into arrays'` test with:

```js
  it('adds object filter patterns as chips and sends them as arrays', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('[data-test="filter-add"]').trigger('click')
    await wrapper.find('[data-test="filter-path-input"]').setValue('/var/lib/dbdata')

    const includeInput = wrapper.find('[data-test="filter-include-input"]')
    await includeInput.setValue('*.sql')
    await includeInput.trigger('keydown', { key: 'Enter' })
    await includeInput.setValue('*.dump')
    await includeInput.trigger('keydown', { key: ',' })

    const excludeInput = wrapper.find('[data-test="filter-exclude-input"]')
    await excludeInput.setValue('*.tmp')
    await excludeInput.trigger('keydown', { key: 'Enter' })

    await wrapper.find('form').trigger('submit')

    expect(wrapper.emitted('save')[0][0]).toEqual(
      expect.objectContaining({
        object_filters: [{ path: '/var/lib/dbdata', include: ['*.sql', '*.dump'], exclude: ['*.tmp'] }],
      })
    )
  })

  it('blocks submit when an object filter pattern is syntactically invalid', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('[data-test="filter-add"]').trigger('click')
    await wrapper.find('[data-test="filter-path-input"]').setValue('/var/lib/dbdata')

    const includeInput = wrapper.find('[data-test="filter-include-input"]')
    await includeInput.setValue('[abc')
    await includeInput.trigger('keydown', { key: 'Enter' })

    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
  })

  it('blocks submit when an object filter pattern overlaps another in the same list', async () => {
    const { wrapper } = mountModal({ policy: null })
    await wrapper.find('input[name="name"]').setValue('x')
    await wrapper.find('[data-test="backup-policy-storage-select"]').setValue('sp1')
    await wrapper.find('[data-test="filter-add"]').trigger('click')
    await wrapper.find('[data-test="filter-path-input"]').setValue('/var/lib/dbdata')

    const includeInput = wrapper.find('[data-test="filter-include-input"]')
    await includeInput.setValue('/var/log')
    await includeInput.trigger('keydown', { key: 'Enter' })
    await includeInput.setValue('/var/log/app')
    await includeInput.trigger('keydown', { key: 'Enter' })

    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('save')).toBeUndefined()
  })
```

- [ ] **Step 2: Run the spec to verify it fails**

Run: `cd web && npx vitest run src/components/backup_policies/BackupPolicyFormModal.spec.js`
Expected: FAIL — the pre-fill test fails (no `filter-include-chip` element exists yet, still the old text input), and the new chip-entry tests fail (`TagInput`'s `data-test="filter-include-input"` doesn't exist yet either, since the component still renders plain text inputs).

- [ ] **Step 3: Update `BackupPolicyFormModal.vue`**

In `web/src/components/backup_policies/BackupPolicyFormModal.vue`, add the imports (near the other component imports, after the `BaseSelect` import):

```js
import TagInput from '../ui/TagInput.vue'
import { validateGlobPattern, findParentChildConflict } from '../../utils/globPattern'
```

Replace `toFormShape`'s `object_filters` mapping:

```js
    object_filters: (policy.object_filters || []).map((f) => ({
      path: f.path,
      includeText: (f.include || []).join(', '),
      excludeText: (f.exclude || []).join(', '),
    })),
```

with:

```js
    object_filters: (policy.object_filters || []).map((f) => ({
      path: f.path,
      include: [...(f.include || [])],
      exclude: [...(f.exclude || [])],
    })),
```

Remove the now-unused `splitCsv` function entirely:

```js
function splitCsv(text) {
  return text.split(',').map((s) => s.trim()).filter(Boolean)
}
```

Replace `buildPayload`'s `object_filters` mapping:

```js
    object_filters: form.object_filters
      .filter((f) => f.path.trim())
      .map((f) => ({
        path: f.path.trim(),
        include: splitCsv(f.includeText || ''),
        exclude: splitCsv(f.excludeText || ''),
      })),
```

with:

```js
    object_filters: form.object_filters
      .filter((f) => f.path.trim())
      .map((f) => ({
        path: f.path.trim(),
        include: f.include,
        exclude: f.exclude,
      })),
```

Add a helper above `validate()` that re-derives pattern validity directly from `form.object_filters`'
data, rather than collecting `TagInput` refs — a `ref="name"` on an element inside a scoped slot
(`RepeatableFieldList`'s `#row` slot, below) is not itself inside a `v-for` in *this* SFC's own
template, so Vue's compile-time "batch same-named refs into an array when under `v-for`" heuristic
doesn't fire here; every row would silently overwrite the same single ref, leaving only the last row
checked. Re-running the same validation used for the live chip feedback avoids that pitfall entirely:

```js
function hasInvalidPatterns(patterns) {
  return patterns.some((pattern, index) => {
    if (!validateGlobPattern(pattern).valid) return true
    const others = patterns.filter((_, i) => i !== index)
    return findParentChildConflict(others, pattern) !== undefined
  })
}
```

Extend `validate()` — insert a check between the name check and the final `reportValidity()` call:

```js
function validate() {
  errors.message = ''
  if (!form.name.trim()) {
    errors.message = 'Name is required.'
    return false
  }
  const hasInvalidFilter = form.object_filters.some(
    (f) => hasInvalidPatterns(f.include) || hasInvalidPatterns(f.exclude)
  )
  if (hasInvalidFilter) {
    errors.message = 'One or more filter patterns are invalid.'
    return false
  }
  return formEl.value.reportValidity()
}
```

In the template, update the Object Filters `RepeatableFieldList`'s `new-item` factory:

```html
        <div>
          <label class="block font-medium mb-1">Object Filters</label>
          <RepeatableFieldList
            :items="form.object_filters"
            :new-item="() => ({ path: '', include: [], exclude: [] })"
            add-label="Add Object Filter"
            remove-label="Remove Filter"
            row-class="border rounded p-2 mb-2 space-y-1"
            test-prefix="filter"
          >
```

Replace the two pattern `<input>`s inside that row's `#row` slot:

```html
              <input
                data-test="filter-include-input"
                v-model="form.object_filters[index].includeText"
                placeholder="include patterns, comma-separated"
                class="w-full border rounded px-2 py-1"
              />
              <input
                data-test="filter-exclude-input"
                v-model="form.object_filters[index].excludeText"
                placeholder="exclude patterns, comma-separated"
                class="w-full border rounded px-2 py-1"
              />
```

with:

```html
              <TagInput
                :items="form.object_filters[index].include"
                test-prefix="filter-include"
                placeholder="include pattern, Enter to add"
              />
              <TagInput
                :items="form.object_filters[index].exclude"
                test-prefix="filter-exclude"
                placeholder="exclude pattern, Enter to add"
              />
```

- [ ] **Step 4: Run the spec to verify it passes**

Run: `cd web && npx vitest run src/components/backup_policies/BackupPolicyFormModal.spec.js`
Expected: PASS, all cases green (including every pre-existing test in the file — none of their assertions target `includeText`/`excludeText`/`splitCsv` directly, so this is a pure behind-the-scenes shape change for everything except the two tests updated in Step 1).

- [ ] **Step 5: Run the full web test suite to check for regressions**

Run: `cd web && npm test`
Expected: PASS — no other spec file references `includeText`, `excludeText`, or `splitCsv`.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/backup_policies/BackupPolicyFormModal.vue web/src/components/backup_policies/BackupPolicyFormModal.spec.js
git commit -m "feat: enter backup policy object filter patterns as validated chips"
```

---

### Task 4: Documentation

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

**Interfaces:** None — documentation only.

- [ ] **Step 1: Update `docs/components/web.md`**

In the `/policies` bullet (currently starting `- `/policies` — every policy...`), find:

```
(fields: name, RPO, backup window, client filters, object filters, destination (a required select over `/storage`'s storage policies, replacing free-text host:port entry))
```

Replace with:

```
(fields: name, RPO, backup window, client filters, object filters (each filter's include/exclude glob patterns entered as individual chips via a reusable `TagInput` component (`components/ui/TagInput.vue`) — each pattern is validated client-side for glob syntax and checked against the rest of its own list for parent/child path overlap, e.g. `/var/log` and `/var/log/app` in the same list, before Save is allowed), destination (a required select over `/storage`'s storage policies, replacing free-text host:port entry))
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Insert immediately after line 3 (`All notable changes to this project are documented here, most recent first.`) and its following blank line, before the existing `## 2026-08-09` entries:

```markdown
## 2026-08-10 — backup policy object filter patterns entered as chips

The backup policy form's object filter include/exclude fields are no longer a single
comma-separated text box each. A new reusable `TagInput` component (`web/src/components/ui/TagInput.vue`)
lets an operator add and remove one pattern at a time as a chip, with two checks run client-side on
each pattern as it's added: glob syntax (a hand-ported mirror of the same `path.Match` grammar
`policy-server` already enforces server-side) and same-list parent/child path overlap (e.g. `/var/log`
and `/var/log/app` in the same `include` list) — both catch mistakes before Save rather than after a
round trip to the server.
```

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document backup policy object filter chip input"
```
