# Web Nav Shell & Visual Consistency Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `web`'s sidebar, breadcrumb orientation, and table/button styling a deliberate, consistent visual treatment without introducing a new color palette, a new dependency, or any backend change.

**Architecture:** Same Vue 3 + Pinia + vue-router + Tailwind 4 SPA. Adds one new component directory (`components/icons/`), one new component (`Badge.vue`), two new optional props (`BaseButton.to`, `PageHeader.crumbs`), a scoped style override on `DataTable.vue`, and a favicon — then wires all of it through the existing views.

**Tech Stack:** Vue 3.5 (`<script setup>`), Pinia, vue-router 4, Tailwind 4, `vue-good-table-next`, Vitest + `@vue/test-utils` + `@pinia/testing`.

## Global Constraints

- No new npm dependency — icons are hand-authored inline SVG, not a package.
- Accent color stays `blue-600` (Tailwind's `bg-blue-600`/`text-blue-600`/`border-blue-500`/`border-blue-600` scale) — no new color palette; only existing Tailwind slate/gray/emerald/red neutrals are introduced.
- No dark mode, no `HomeView` dashboard, no mobile/responsive sidebar collapse, no toasts/skeletons — all explicitly out of scope (see spec).
- No backend/API change — same REST endpoints, same request/response shapes.
- Per this repo's `.claude/CLAUDE.md`: any feature change updates `docs/components/web.md`; `CHANGELOG.md` gets one dated entry before merge. No `docs/protocols/` or `docs/ARCHITECTURE.md` change — no protocol or topology change.
- Run tests from the `web/` directory: `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run test` — or, if Node 20+ is available locally, `cd web && npm run test`. Every task's test-running steps below use the local form (`npm run test`); substitute the Docker form if Node isn't installed locally.

---

### Task 1: Local icon set

**Files:**
- Create: `web/src/components/icons/IconClients.vue`
- Create: `web/src/components/icons/IconCatalog.vue`
- Create: `web/src/components/icons/IconPolicies.vue`
- Create: `web/src/components/icons/IconStorage.vue`
- Create: `web/src/components/icons/IconJobs.vue`
- Test: `web/src/components/icons/icons.spec.js`

**Interfaces:**
- Consumes: nothing.
- Produces: five Vue SFCs, each a single root `<svg viewBox="0 0 24 24" ...>` with no props and no `<script>` block. Vue's automatic attribute fallthrough means a `class` passed by a parent (e.g. `class="w-4 h-4"`) lands on the root `<svg>` with no extra wiring needed. Task 5 (`Sidebar.vue`) imports and renders these.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/icons/icons.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import IconClients from './IconClients.vue'
import IconCatalog from './IconCatalog.vue'
import IconPolicies from './IconPolicies.vue'
import IconStorage from './IconStorage.vue'
import IconJobs from './IconJobs.vue'

const icons = { IconClients, IconCatalog, IconPolicies, IconStorage, IconJobs }

describe('icons', () => {
  for (const [name, component] of Object.entries(icons)) {
    it(`${name} renders a single 24x24 svg and forwards a passed class`, () => {
      const wrapper = mount(component, { attrs: { class: 'w-4 h-4' } })
      const svg = wrapper.find('svg')
      expect(svg.exists()).toBe(true)
      expect(svg.attributes('viewBox')).toBe('0 0 24 24')
      expect(svg.classes()).toContain('w-4')
      expect(svg.classes()).toContain('h-4')
    })
  }
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm run test -- icons.spec.js`
Expected: FAIL — `Failed to resolve import "./IconClients.vue"` (files don't exist yet).

- [ ] **Step 3: Write the five icon components**

Create `web/src/components/icons/IconClients.vue` (a small server-rack glyph):

```vue
<template>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
    <rect x="3" y="4" width="18" height="6" rx="1.5" />
    <rect x="3" y="14" width="18" height="6" rx="1.5" />
    <circle cx="7" cy="7" r="0.8" fill="currentColor" stroke="none" />
    <circle cx="7" cy="17" r="0.8" fill="currentColor" stroke="none" />
  </svg>
</template>
```

Create `web/src/components/icons/IconCatalog.vue` (a folder glyph):

```vue
<template>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
    <path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4l2 2h9A1.5 1.5 0 0 1 21 8.5v9A1.5 1.5 0 0 1 19.5 19h-15A1.5 1.5 0 0 1 3 17.5v-11Z" />
  </svg>
</template>
```

Create `web/src/components/icons/IconPolicies.vue` (a clipboard glyph):

```vue
<template>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
    <rect x="5" y="4" width="14" height="17" rx="1.5" />
    <path d="M9 3.5h6a1 1 0 0 1 1 1V6H8V4.5a1 1 0 0 1 1-1Z" />
    <path d="M8.5 11h7M8.5 14.5h7M8.5 18h4" />
  </svg>
</template>
```

Create `web/src/components/icons/IconStorage.vue` (a database-cylinder glyph):

```vue
<template>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
    <ellipse cx="12" cy="6" rx="7" ry="2.5" />
    <path d="M5 6v12c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5V6" />
    <path d="M5 12c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5" />
  </svg>
</template>
```

Create `web/src/components/icons/IconJobs.vue` (a lightning-bolt glyph):

```vue
<template>
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
    <path d="M13 3 5 13.5h5.5L11 21l8-11h-5.5L13 3Z" />
  </svg>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm run test -- icons.spec.js`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/icons/
git commit -m "feat(web): add a hand-authored inline-SVG icon set"
```

---

### Task 2: `Badge` component

**Files:**
- Create: `web/src/components/ui/Badge.vue`
- Test: `web/src/components/ui/Badge.spec.js`

**Interfaces:**
- Consumes: nothing.
- Produces: `Badge.vue` — prop `variant: 'ok' | 'bad' | 'neutral'` (default `'neutral'`), default slot for the label. Renders a `<span>` styled as a pill. Task 12 (`ClientsListView`, `JobsListView`) consumes this.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/ui/Badge.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import Badge from './Badge.vue'

describe('Badge', () => {
  it('renders slot content', () => {
    const wrapper = mount(Badge, { slots: { default: 'Yes' } })
    expect(wrapper.text()).toBe('Yes')
  })

  it('defaults to the neutral variant', () => {
    const wrapper = mount(Badge)
    expect(wrapper.classes()).toContain('bg-gray-100')
  })

  it('applies ok variant classes', () => {
    const wrapper = mount(Badge, { props: { variant: 'ok' } })
    expect(wrapper.classes()).toContain('bg-emerald-50')
  })

  it('applies bad variant classes', () => {
    const wrapper = mount(Badge, { props: { variant: 'bad' } })
    expect(wrapper.classes()).toContain('bg-red-50')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm run test -- Badge.spec.js`
Expected: FAIL — `Failed to resolve import "./Badge.vue"`.

- [ ] **Step 3: Write the component**

Create `web/src/components/ui/Badge.vue`:

```vue
<!-- web/src/components/ui/Badge.vue -->
<script setup>
defineProps({
  variant: { type: String, default: 'neutral' },
})

const VARIANT_CLASSES = {
  ok: 'bg-emerald-50 text-emerald-600',
  bad: 'bg-red-50 text-red-600',
  neutral: 'bg-gray-100 text-gray-600',
}
</script>

<template>
  <span class="inline-block rounded-full px-2 py-0.5 text-xs font-semibold" :class="VARIANT_CLASSES[variant]">
    <slot />
  </span>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm run test -- Badge.spec.js`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/Badge.vue web/src/components/ui/Badge.spec.js
git commit -m "feat(web): add Badge component for boolean/state table columns"
```

---

### Task 3: `BaseButton` gains a `to` prop

**Files:**
- Modify: `web/src/components/ui/BaseButton.vue`
- Test: `web/src/components/ui/BaseButton.spec.js`

**Interfaces:**
- Consumes: nothing new.
- Produces: `BaseButton.vue` now accepts an optional prop `to: [String, Object]` (default `null`). When set, the component renders `<router-link :to="to">` (same `VARIANT_CLASSES` styling, plus `inline-block`) instead of `<button>`; `type`/`disabled` are ignored in that branch. Task 6 (`ClientsListView`) consumes this.

- [ ] **Step 1: Write the failing test**

Add to `web/src/components/ui/BaseButton.spec.js` (append inside the existing `describe('BaseButton', ...)` block, after the last `it`; add the `RouterLinkStub` import to the existing `@vue/test-utils` import line):

```js
import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import BaseButton from './BaseButton.vue'
```

```js
  it('renders as a router-link when the to prop is set, keeping the variant classes', () => {
    const wrapper = mount(BaseButton, {
      props: { to: { name: 'client-new' }, variant: 'primary' },
      slots: { default: 'New Client' },
      global: { stubs: { RouterLink: RouterLinkStub } },
    })
    const link = wrapper.findComponent(RouterLinkStub)
    expect(link.exists()).toBe(true)
    expect(link.props('to')).toEqual({ name: 'client-new' })
    expect(wrapper.classes()).toContain('bg-blue-600')
    expect(wrapper.find('button').exists()).toBe(false)
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm run test -- BaseButton.spec.js`
Expected: FAIL — `link.exists()` is `false` (no `to` prop or router-link branch exists yet).

- [ ] **Step 3: Implement the `to` prop**

Replace the full contents of `web/src/components/ui/BaseButton.vue`:

```vue
<script setup>
defineProps({
  variant: { type: String, default: 'secondary' },
  type: { type: String, default: 'button' },
  to: { type: [String, Object], default: null },
})

const VARIANT_CLASSES = {
  primary: 'bg-blue-600 text-white hover:bg-blue-700',
  secondary: 'border border-gray-300 hover:bg-gray-50',
  danger: 'border border-red-300 text-red-600 hover:bg-red-50',
}
</script>

<template>
  <router-link
    v-if="to"
    :to="to"
    class="inline-block rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed"
    :class="VARIANT_CLASSES[variant]"
  >
    <slot />
  </router-link>
  <button
    v-else
    :type="type"
    class="rounded px-3 py-1 disabled:opacity-50 disabled:cursor-not-allowed"
    :class="VARIANT_CLASSES[variant]"
  >
    <slot />
  </button>
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm run test -- BaseButton.spec.js`
Expected: PASS (6 tests — the 5 existing plus the new one)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/BaseButton.vue web/src/components/ui/BaseButton.spec.js
git commit -m "feat(web): let BaseButton render as a router-link via a to prop"
```

---

### Task 4: `PageHeader` gains a `crumbs` prop

**Files:**
- Modify: `web/src/components/ui/PageHeader.vue`
- Test: `web/src/components/ui/PageHeader.spec.js`

**Interfaces:**
- Consumes: nothing new.
- Produces: `PageHeader.vue` now accepts an optional prop `crumbs: Array<{ label: String, to?: Object }>` (default `null`). When non-empty, renders a `data-test="breadcrumb"` line above the `<h1>`, joining segment labels with `/`; a segment with `to` renders as `<router-link>`, the last segment always renders as plain text. Tasks 7–11 (every view) consume this.

- [ ] **Step 1: Write the failing test**

Replace the full contents of `web/src/components/ui/PageHeader.spec.js`:

```js
// web/src/components/ui/PageHeader.spec.js
import { describe, it, expect } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import PageHeader from './PageHeader.vue'

describe('PageHeader', () => {
  it('renders the title as an h1', () => {
    const wrapper = mount(PageHeader, { props: { title: 'Clients' } })
    expect(wrapper.find('h1').text()).toBe('Clients')
  })

  it('renders default slot content below the header row', () => {
    const wrapper = mount(PageHeader, {
      props: { title: 'Clients' },
      slots: { default: '<p>body</p>' },
    })
    expect(wrapper.find('p').text()).toBe('body')

    // Verify ordering: h1 should appear before p in the DOM
    const html = wrapper.html()
    const h1Index = html.indexOf('<h1')
    const pIndex = html.indexOf('<p')
    expect(h1Index).toBeGreaterThanOrEqual(0)
    expect(pIndex).toBeGreaterThanOrEqual(0)
    expect(h1Index).toBeLessThan(pIndex)
  })

  it('renders the actions slot when provided', () => {
    const wrapper = mount(PageHeader, {
      props: { title: 'Clients' },
      slots: { actions: '<button>New Client</button>' },
    })
    expect(wrapper.find('button').text()).toBe('New Client')
  })

  it('does not render an actions wrapper when no actions slot is given', () => {
    const wrapper = mount(PageHeader, { props: { title: 'Clients' } })
    expect(wrapper.find('[data-test="page-header-actions"]').exists()).toBe(false)
  })

  it('renders no breadcrumb when crumbs is omitted', () => {
    const wrapper = mount(PageHeader, { props: { title: 'Clients' } })
    expect(wrapper.find('[data-test="breadcrumb"]').exists()).toBe(false)
  })

  it('renders breadcrumb segments in order, linking all but the last', () => {
    const wrapper = mount(PageHeader, {
      props: {
        title: 'webserver',
        crumbs: [{ label: 'Clients', to: { name: 'clients' } }, { label: 'webserver' }],
      },
      global: { stubs: { RouterLink: RouterLinkStub } },
    })
    const crumb = wrapper.find('[data-test="breadcrumb"]')
    expect(crumb.text()).toBe('Clients / webserver')
    const link = crumb.findComponent(RouterLinkStub)
    expect(link.props('to')).toEqual({ name: 'clients' })
    expect(link.text()).toBe('Clients')
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm run test -- PageHeader.spec.js`
Expected: FAIL — the two new tests fail (`crumb.exists()` is falsy / breadcrumb never renders).

- [ ] **Step 3: Implement the `crumbs` prop**

Replace the full contents of `web/src/components/ui/PageHeader.vue`:

```vue
<!-- web/src/components/ui/PageHeader.vue -->
<script setup>
defineProps({
  title: { type: String, required: true },
  crumbs: { type: Array, default: null },
})
</script>

<template>
  <nav v-if="crumbs && crumbs.length" data-test="breadcrumb" class="flex gap-1 text-xs text-gray-400 mb-1">
    <template v-for="(crumb, index) in crumbs" :key="index">
      <router-link v-if="crumb.to" :to="crumb.to" class="hover:underline hover:text-gray-600">
        {{ crumb.label }}
      </router-link>
      <span v-else>{{ crumb.label }}</span>
      <span v-if="index < crumbs.length - 1">/</span>
    </template>
  </nav>
  <div class="flex items-center justify-between mb-4">
    <h1 class="text-xl font-semibold">{{ title }}</h1>
    <div v-if="$slots.actions" data-test="page-header-actions" class="flex gap-2">
      <slot name="actions" />
    </div>
  </div>
  <slot />
</template>
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm run test -- PageHeader.spec.js`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/PageHeader.vue web/src/components/ui/PageHeader.spec.js
git commit -m "feat(web): let PageHeader render an optional breadcrumb trail"
```

---

### Task 5: Sidebar branding, icons, and active-state restyle

**Files:**
- Modify: `web/src/components/Sidebar.vue`
- Test: `web/src/components/Sidebar.spec.js`

**Interfaces:**
- Consumes: `IconClients`, `IconCatalog`, `IconPolicies`, `IconStorage`, `IconJobs` from `web/src/components/icons/` (Task 1).
- Produces: restyled `Sidebar.vue`. No API change — still five `router-link`s to the same named routes in the same order, so nothing downstream depends on its internals.

- [ ] **Step 1: Write the failing test**

Add to `web/src/components/Sidebar.spec.js` (append inside the existing `describe('Sidebar', ...)` block, after the existing `it`):

```js
  it('renders a brand header and an icon before each nav label', () => {
    const wrapper = mount(Sidebar, { global: { stubs: { RouterLink: RouterLinkStub } } })
    expect(wrapper.text()).toContain('Miniprotector')
    const links = wrapper.findAllComponents(RouterLinkStub)
    expect(links).toHaveLength(5)
    links.forEach((link) => {
      expect(link.find('svg').exists()).toBe(true)
    })
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm run test -- Sidebar.spec.js`
Expected: FAIL — `wrapper.text()` doesn't contain `'Miniprotector'` yet.

- [ ] **Step 3: Restyle the sidebar**

Replace the full contents of `web/src/components/Sidebar.vue`:

```vue
<script setup>
import IconClients from './icons/IconClients.vue'
import IconCatalog from './icons/IconCatalog.vue'
import IconPolicies from './icons/IconPolicies.vue'
import IconStorage from './icons/IconStorage.vue'
import IconJobs from './icons/IconJobs.vue'

const NAV_ITEMS = [
  { name: 'clients', label: 'Clients', icon: IconClients },
  { name: 'catalog', label: 'Catalog', icon: IconCatalog },
  { name: 'policies', label: 'Policies', icon: IconPolicies },
  { name: 'storage', label: 'Storage', icon: IconStorage },
  { name: 'jobs', label: 'Jobs', icon: IconJobs },
]
</script>

<template>
  <nav class="w-48 h-screen bg-slate-900 text-slate-300 p-3 flex flex-col">
    <div class="flex items-center gap-2 px-2 pb-4 mb-3 border-b border-slate-800">
      <div class="w-6 h-6 rounded bg-blue-600 text-white text-xs font-bold flex items-center justify-center">
        M
      </div>
      <span class="text-slate-50 font-semibold text-sm">Miniprotector</span>
    </div>
    <div class="space-y-1">
      <router-link
        v-for="item in NAV_ITEMS"
        :key="item.name"
        :to="{ name: item.name }"
        class="flex items-center gap-2.5 pl-3 pr-2.5 py-1.5 rounded text-sm text-slate-300 hover:bg-slate-800 hover:text-white"
        active-class="bg-slate-800 text-white border-l-4 border-blue-500 pl-2"
      >
        <component :is="item.icon" class="w-4 h-4 shrink-0" />
        {{ item.label }}
      </router-link>
    </div>
  </nav>
</template>
```

Note: the inactive state deliberately carries no `border-l-*` class at all (only the active state does, via `active-class`), so the active and inactive states never define conflicting values for the same CSS property — avoiding a Tailwind utility-ordering conflict where two classes on the same element both set `border-color`. `pl-3` (inactive) vs `pl-2` (active, to compensate for the added 4px border) keeps the icon roughly aligned between states.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm run test -- Sidebar.spec.js`
Expected: PASS (2 tests — the pre-existing route-link test plus the new one)

- [ ] **Step 5: Commit**

```bash
git add web/src/components/Sidebar.vue web/src/components/Sidebar.spec.js
git commit -m "feat(web): restyle Sidebar with branding, icons, and an accent active state"
```

---

### Task 6: Fix `ClientsListView`'s hardcoded "New Client" button

**Files:**
- Modify: `web/src/views/ClientsListView.vue`

**Interfaces:**
- Consumes: `BaseButton`'s `to` prop (Task 3).
- Produces: no change to `ClientsListView`'s public behavior — same route, same link text.

- [ ] **Step 1: Confirm the existing test still describes the desired behavior**

`web/src/views/ClientsListView.spec.js` already has (no edit needed):

```js
  it('links to the enroll form', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    const link = wrapper.findAllComponents(RouterLinkStub).find((l) => l.text() === 'New Client')
    expect(link.props('to')).toEqual({ name: 'client-new' })
  })
```

This test currently passes against the old hand-rolled `<router-link>`. It must keep passing unchanged after the refactor, since `BaseButton`'s `to` branch (Task 3) also renders a `<router-link>` under the hood — from the test's point of view, nothing observable changes.

- [ ] **Step 2: Run the existing test to confirm current behavior**

Run: `cd web && npm run test -- ClientsListView.spec.js`
Expected: PASS (all existing tests, including "links to the enroll form")

- [ ] **Step 3: Replace the hardcoded button**

In `web/src/views/ClientsListView.vue`, add the import:

```js
import BaseButton from '../components/ui/BaseButton.vue'
```

Replace:

```html
        <router-link :to="{ name: 'client-new' }" class="bg-blue-600 text-white rounded px-3 py-1">
          New Client
        </router-link>
```

with:

```html
        <BaseButton :to="{ name: 'client-new' }" variant="primary">
          New Client
        </BaseButton>
```

- [ ] **Step 4: Run the test to verify nothing broke**

Run: `cd web && npm run test -- ClientsListView.spec.js`
Expected: PASS (all existing tests, unchanged)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/ClientsListView.vue
git commit -m "fix(web): route ClientsListView's New Client action through BaseButton"
```

---

### Task 7: Breadcrumbs on the Clients pages

**Files:**
- Modify: `web/src/views/ClientsListView.vue`
- Modify: `web/src/views/ClientFormView.vue`
- Modify: `web/src/views/ClientDetailView.vue`
- Test: `web/src/views/ClientsListView.spec.js`
- Test: `web/src/views/ClientFormView.spec.js`

**Interfaces:**
- Consumes: `PageHeader`'s `crumbs` prop (Task 4).
- Produces: no change to any view's public behavior beyond the added breadcrumb line.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/views/ClientsListView.spec.js` (append inside `describe('ClientsListView', ...)`):

```js
  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Clients')
  })
```

Add the `RouterLinkStub` import and a `stubs` entry to `web/src/views/ClientFormView.spec.js`'s `mountView` helper, then a new test. The full updated top of the file:

```js
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import ClientFormView from './ClientFormView.vue'
import { useClientsStore } from '../stores/clients'

const push = vi.fn()

vi.mock('vue-router', () => ({
  useRouter: () => ({ push }),
}))

function mountView(state) {
  const pinia = createTestingPinia({ stubActions: true, initialState: { clients: state } })
  const wrapper = mount(ClientFormView, {
    global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
  })
  return { wrapper, clients: useClientsStore() }
}
```

Then append this test inside `describe('ClientFormView', ...)`:

```js
  it('renders the page title and a breadcrumb back to the clients list', () => {
    const { wrapper } = mountView({ error: null })
    expect(wrapper.find('h1').text()).toBe('New Client')
    const crumb = wrapper.find('[data-test="breadcrumb"]')
    expect(crumb.text()).toBe('Clients / New Client')
    const link = crumb.findComponent(RouterLinkStub)
    expect(link.props('to')).toEqual({ name: 'clients' })
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm run test -- ClientsListView.spec.js ClientFormView.spec.js`
Expected: FAIL — both new tests fail (no breadcrumb rendered yet; `ClientFormView` doesn't even use `PageHeader` yet).

- [ ] **Step 3: Wire crumbs into all three views**

In `web/src/views/ClientsListView.vue`, change:

```html
    <PageHeader title="Clients">
```

to:

```html
    <PageHeader title="Clients" :crumbs="[{ label: 'Clients' }]">
```

In `web/src/views/ClientDetailView.vue`, change:

```html
    <PageHeader :title="hostname" />
```

to:

```html
    <PageHeader :title="hostname" :crumbs="[{ label: 'Clients', to: { name: 'clients' } }, { label: hostname }]" />
```

In `web/src/views/ClientFormView.vue`, add the import:

```js
import PageHeader from '../components/ui/PageHeader.vue'
```

and replace:

```html
    <h1 class="text-xl font-semibold mb-4">New Client</h1>
```

with:

```html
    <PageHeader title="New Client" :crumbs="[{ label: 'Clients', to: { name: 'clients' } }, { label: 'New Client' }]" />
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm run test -- ClientsListView.spec.js ClientFormView.spec.js ClientDetailView.spec.js`
Expected: PASS (all tests in all three files)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/ClientsListView.vue web/src/views/ClientFormView.vue web/src/views/ClientDetailView.vue \
        web/src/views/ClientsListView.spec.js web/src/views/ClientFormView.spec.js
git commit -m "feat(web): add breadcrumbs to the Clients list, new-client, and detail pages"
```

---

### Task 8: Breadcrumb and `PageHeader` adoption on the Catalog page

**Files:**
- Modify: `web/src/views/CatalogView.vue`
- Test: `web/src/views/CatalogView.spec.js`

**Interfaces:**
- Consumes: `PageHeader`'s `crumbs` prop (Task 4).
- Produces: no change to any store call, filter behavior, or the versions-modal flow — only the heading markup changes.

- [ ] **Step 1: Write the failing test**

Add to `web/src/views/CatalogView.spec.js` (append inside `describe('CatalogView', ...)`):

```js
  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({})
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Catalog')
  })
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm run test -- CatalogView.spec.js`
Expected: FAIL — `CatalogView` doesn't render `PageHeader` yet, so `[data-test="breadcrumb"]` doesn't exist.

- [ ] **Step 3: Adopt `PageHeader`**

In `web/src/views/CatalogView.vue`, add the import:

```js
import PageHeader from '../components/ui/PageHeader.vue'
```

Replace:

```html
    <h1 class="text-xl font-semibold mb-4">Catalog</h1>
```

with:

```html
    <PageHeader title="Catalog" :crumbs="[{ label: 'Catalog' }]" />
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npm run test -- CatalogView.spec.js`
Expected: PASS (all tests, including the new one)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js
git commit -m "feat(web): adopt PageHeader and add a breadcrumb on the Catalog page"
```

---

### Task 9: Breadcrumbs on the Policies pages

**Files:**
- Modify: `web/src/views/BackupPoliciesView.vue`
- Modify: `web/src/views/BackupPolicyView.vue`
- Test: `web/src/views/BackupPoliciesView.spec.js`
- Test: `web/src/views/BackupPolicyView.spec.js`

**Interfaces:**
- Consumes: `PageHeader`'s `crumbs` prop (Task 4).
- Produces: no change to any store call, modal flow, or tab behavior.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/views/BackupPoliciesView.spec.js` (append inside `describe('BackupPoliciesView', ...)`):

```js
  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Policies')
  })
```

Add to `web/src/views/BackupPolicyView.spec.js`: this file mocks `vue-router` for `useRoute`/`useRouter` but doesn't stub `RouterLink`, so the breadcrumb's link segment needs a stub. Change the top-of-file import line from:

```js
import { mount } from '@vue/test-utils'
```

to:

```js
import { mount, RouterLinkStub } from '@vue/test-utils'
```

Then append this test inside `describe('BackupPolicyView', ...)` (it builds its own wrapper directly, passing the stub, rather than going through the shared `mountView` helper — no other test in this file needs the stub):

```js
  it('renders a breadcrumb back to the policies list once the policy has loaded', () => {
    const pinia = createTestingPinia({
      stubActions: true,
      initialState: {
        policies: {
          byId: { p1: { id: 'p1', name: 'nightly-db-backup', object_filters: [], client_filters: {} } },
          loading: false,
          error: null,
        },
      },
    })
    const wrapper = mount(BackupPolicyView, {
      global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
    })
    const crumb = wrapper.find('[data-test="breadcrumb"]')
    expect(crumb.text()).toBe('Policies / nightly-db-backup')
    expect(crumb.findComponent(RouterLinkStub).props('to')).toEqual({ name: 'policies' })
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm run test -- BackupPoliciesView.spec.js BackupPolicyView.spec.js`
Expected: FAIL — both new tests fail (no breadcrumb rendered yet).

- [ ] **Step 3: Wire crumbs into both views**

In `web/src/views/BackupPoliciesView.vue`, change:

```html
    <PageHeader title="Policies">
```

to:

```html
    <PageHeader title="Policies" :crumbs="[{ label: 'Policies' }]">
```

In `web/src/views/BackupPolicyView.vue`, change:

```html
    <PageHeader :title="policy?.name || id">
```

to:

```html
    <PageHeader
      :title="policy?.name || id"
      :crumbs="[{ label: 'Policies', to: { name: 'policies' } }, { label: policy?.name || id }]"
    >
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm run test -- BackupPoliciesView.spec.js BackupPolicyView.spec.js`
Expected: PASS (all tests in both files)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/BackupPoliciesView.vue web/src/views/BackupPolicyView.vue \
        web/src/views/BackupPoliciesView.spec.js web/src/views/BackupPolicyView.spec.js
git commit -m "feat(web): add breadcrumbs to the Policies list and detail pages"
```

---

### Task 10: Breadcrumbs on the Storage pages

**Files:**
- Modify: `web/src/views/StorageView.vue`
- Modify: `web/src/views/StoragePolicyView.vue`
- Test: `web/src/views/StorageView.spec.js`
- Test: `web/src/views/StoragePolicyView.spec.js`

**Interfaces:**
- Consumes: `PageHeader`'s `crumbs` prop (Task 4).
- Produces: no change to any store call, modal flow, or tab behavior.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/views/StorageView.spec.js` (append inside `describe('StorageView', ...)`):

```js
  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Storage')
  })
```

Add to `web/src/views/StoragePolicyView.spec.js`: same reasoning as Task 9 — this file doesn't stub `RouterLink` in its shared `mountView`. Change the top-of-file import line from:

```js
import { mount } from '@vue/test-utils'
```

to:

```js
import { mount, RouterLinkStub } from '@vue/test-utils'
```

Then append this test inside `describe('StoragePolicyView', ...)`:

```js
  it('renders a breadcrumb back to the storage list once the policy has loaded', () => {
    const pinia = createTestingPinia({
      stubActions: true,
      initialState: {
        storagePolicies: {
          byId: { s1: { id: 's1', name: 'east-1-storage', config: '{}', client_filters: {} } },
          loading: false,
          error: null,
        },
      },
    })
    const wrapper = mount(StoragePolicyView, {
      global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
    })
    const crumb = wrapper.find('[data-test="breadcrumb"]')
    expect(crumb.text()).toBe('Storage / east-1-storage')
    expect(crumb.findComponent(RouterLinkStub).props('to')).toEqual({ name: 'storage' })
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm run test -- StorageView.spec.js StoragePolicyView.spec.js`
Expected: FAIL — both new tests fail (no breadcrumb rendered yet).

- [ ] **Step 3: Wire crumbs into both views**

In `web/src/views/StorageView.vue`, change:

```html
    <PageHeader title="Storage">
```

to:

```html
    <PageHeader title="Storage" :crumbs="[{ label: 'Storage' }]">
```

In `web/src/views/StoragePolicyView.vue`, change:

```html
    <PageHeader :title="policy?.name || id">
```

to:

```html
    <PageHeader
      :title="policy?.name || id"
      :crumbs="[{ label: 'Storage', to: { name: 'storage' } }, { label: policy?.name || id }]"
    >
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm run test -- StorageView.spec.js StoragePolicyView.spec.js`
Expected: PASS (all tests in both files)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/StorageView.vue web/src/views/StoragePolicyView.vue \
        web/src/views/StorageView.spec.js web/src/views/StoragePolicyView.spec.js
git commit -m "feat(web): add breadcrumbs to the Storage list and detail pages"
```

---

### Task 11: Breadcrumbs on the Jobs pages

**Files:**
- Modify: `web/src/views/JobsListView.vue`
- Modify: `web/src/views/JobDetailView.vue`
- Test: `web/src/views/JobsListView.spec.js`
- Test: `web/src/views/JobDetailView.spec.js`

**Interfaces:**
- Consumes: `PageHeader`'s `crumbs` prop (Task 4).
- Produces: no change to log fetching or rendering.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/views/JobsListView.spec.js` (append inside `describe('JobsListView', ...)`):

```js
  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({ list: [], loading: false, error: null })
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Jobs')
  })
```

Add to `web/src/views/JobDetailView.spec.js`. This file mocks `vue-router`'s `useRoute` only and doesn't stub `RouterLink`; same pattern as Tasks 9–10. Change the top-of-file import line from:

```js
import { mount } from '@vue/test-utils'
```

to:

```js
import { mount, RouterLinkStub } from '@vue/test-utils'
```

Then append this test inside `describe('JobDetailView', ...)`:

```js
  it('renders a breadcrumb back to the jobs list', () => {
    const pinia = createTestingPinia({
      stubActions: true,
      initialState: { jobs: { logs: [], logsLoading: false, logsError: null } },
    })
    const wrapper = mount(JobDetailView, {
      global: { plugins: [pinia], stubs: { RouterLink: RouterLinkStub } },
    })
    const crumb = wrapper.find('[data-test="breadcrumb"]')
    expect(crumb.text()).toBe('Jobs / backup:nightly:1752400000')
    expect(crumb.findComponent(RouterLinkStub).props('to')).toEqual({ name: 'jobs' })
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm run test -- JobsListView.spec.js JobDetailView.spec.js`
Expected: FAIL — both new tests fail (no breadcrumb rendered yet).

- [ ] **Step 3: Wire crumbs into both views**

In `web/src/views/JobsListView.vue`, change:

```html
    <PageHeader title="Jobs" />
```

to:

```html
    <PageHeader title="Jobs" :crumbs="[{ label: 'Jobs' }]" />
```

In `web/src/views/JobDetailView.vue`, change:

```html
    <PageHeader :title="jobId" />
```

to:

```html
    <PageHeader :title="jobId" :crumbs="[{ label: 'Jobs', to: { name: 'jobs' } }, { label: jobId }]" />
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm run test -- JobsListView.spec.js JobDetailView.spec.js`
Expected: PASS (all tests in both files)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/JobsListView.vue web/src/views/JobDetailView.vue \
        web/src/views/JobsListView.spec.js web/src/views/JobDetailView.spec.js
git commit -m "feat(web): add breadcrumbs to the Jobs list and detail pages"
```

---

### Task 12: Badge-ify the Revoked and State columns

**Files:**
- Modify: `web/src/views/ClientsListView.vue`
- Modify: `web/src/views/JobsListView.vue`
- Test: `web/src/views/ClientsListView.spec.js`
- Test: `web/src/views/JobsListView.spec.js`

**Interfaces:**
- Consumes: `Badge` (Task 2).
- Produces: no change to the underlying data or column definitions — only the Revoked/State cell markup changes from plain text to a `Badge`.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/views/ClientsListView.spec.js` (append inside `describe('ClientsListView', ...)`):

```js
  it('renders the Revoked column as a red badge when the client is revoked', () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'legacy', revoked: true, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    const revokedCell = wrapper.findAll('tbody td')[1]
    expect(revokedCell.find('span').classes()).toContain('bg-red-50')
  })

  it('renders the Revoked column as a green badge when the client is not revoked', () => {
    const { wrapper } = mountView({
      list: [{ hostname: 'active-host', revoked: false, last_seen_at: 0 }],
      loading: false,
      error: null,
    })
    const revokedCell = wrapper.findAll('tbody td')[1]
    expect(revokedCell.find('span').classes()).toContain('bg-emerald-50')
  })
```

Add to `web/src/views/JobsListView.spec.js` (append inside `describe('JobsListView', ...)`):

```js
  it('renders the State column as a green badge for a successful job', () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'backup:nightly:1752400000',
          kind: 'backup',
          source_host: 'database',
          store_host: 'bwfs-east',
          started_at: 1752400000,
          finished_at: 1752400010,
          state: 'success',
        },
      ],
      loading: false,
      error: null,
    })
    const stateCell = wrapper.findAll('tbody td')[6]
    expect(stateCell.find('span').classes()).toContain('bg-emerald-50')
  })

  it('renders the State column as a red badge for a failed job', () => {
    const { wrapper } = mountView({
      list: [
        {
          job_id: 'backup:nightly:1752400000',
          kind: 'backup',
          source_host: 'database',
          store_host: 'bwfs-east',
          started_at: 1752400000,
          finished_at: 1752400010,
          state: 'failure',
        },
      ],
      loading: false,
      error: null,
    })
    const stateCell = wrapper.findAll('tbody td')[6]
    expect(stateCell.find('span').classes()).toContain('bg-red-50')
  })
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm run test -- ClientsListView.spec.js JobsListView.spec.js`
Expected: FAIL — the four new tests fail (`revokedCell.find('span')`/`stateCell.find('span')` don't exist; the cells are still plain text).

- [ ] **Step 3: Wire in `Badge`**

In `web/src/views/ClientsListView.vue`, add the import:

```js
import Badge from '../components/ui/Badge.vue'
```

Replace the `#table-row` template:

```html
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'hostname'"
            :to="{ name: 'client-detail', params: { hostname: row.hostname } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.hostname }}
          </router-link>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
```

with:

```html
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'hostname'"
            :to="{ name: 'client-detail', params: { hostname: row.hostname } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.hostname }}
          </router-link>
          <Badge v-else-if="column.field === 'revoked'" :variant="row.revoked ? 'bad' : 'ok'">
            {{ formattedRow[column.field] }}
          </Badge>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
```

In `web/src/views/JobsListView.vue`, add the import:

```js
import Badge from '../components/ui/Badge.vue'
```

and a helper function alongside `columns`:

```js
function stateVariant(state) {
  if (state === 'success') return 'ok'
  if (state === 'failure') return 'bad'
  return 'neutral'
}
```

Replace the `#table-row` template:

```html
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'job_id'"
            :to="{ name: 'job-detail', params: { job_id: row.job_id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.job_id }}
          </router-link>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
```

with:

```html
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'job_id'"
            :to="{ name: 'job-detail', params: { job_id: row.job_id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.job_id }}
          </router-link>
          <Badge v-else-if="column.field === 'state'" :variant="stateVariant(row.state)">
            {{ row.state }}
          </Badge>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && npm run test -- ClientsListView.spec.js JobsListView.spec.js`
Expected: PASS (all tests in both files, including the four new ones)

- [ ] **Step 5: Commit**

```bash
git add web/src/views/ClientsListView.vue web/src/views/JobsListView.vue \
        web/src/views/ClientsListView.spec.js web/src/views/JobsListView.spec.js
git commit -m "feat(web): render Revoked and job State columns as colored badges"
```

---

### Task 13: Restyle `DataTable` to match the app

**Files:**
- Modify: `web/src/components/ui/DataTable.vue`

**Interfaces:**
- Consumes: nothing.
- Produces: no prop, event, or slot changes — purely a `<style scoped>` addition. Every existing usage (`ClientsListView`, `CatalogView`, `JobsListView`, `BackupPoliciesView`, `StorageView`) picks this up automatically with no per-view change.

This task is styling-only against a third-party component's rendered DOM (`vue-good-table-next`), which Vitest's jsdom environment does not reliably apply scoped CSS to (SFC `<style>` blocks are injected by Vite's dev/build pipeline, not evaluated by the component's render function) — so verification here is the existing test suite (must stay green, confirming no structural regression) plus a manual browser check, not a new automated assertion. The class names below are confirmed by reading `vue-good-table-next`'s source directly: `.vgt-wrap` and `.vgt-table` (`web/node_modules/vue-good-table-next/src/components/Table.vue`), `.vgt-input` and `.vgt-global-search` (`VgtGlobalSearch.vue`), `.vgt-wrap__footer` and `.footer__navigation__page-btn` (`pagination/VgtPagination.vue`).

- [ ] **Step 1: Run the existing DataTable tests to confirm the current baseline**

Run: `cd web && npm run test -- DataTable.spec.js`
Expected: PASS (all 5 existing tests)

- [ ] **Step 2: Add the style overrides**

Append a `<style scoped>` block to the end of `web/src/components/ui/DataTable.vue` (after the existing `</template>`):

```vue
<style scoped>
:deep(.vgt-wrap) {
  border: 1px solid #e2e8f0;
  border-radius: 0.5rem;
  overflow: hidden;
}
:deep(.vgt-table) {
  border: none;
}
:deep(.vgt-table thead th) {
  background-color: #f1f5f9;
  color: #475569;
  font-size: 0.6875rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.02em;
  border-color: #e2e8f0;
}
:deep(.vgt-table tbody td) {
  border-color: #f1f5f9;
  color: #334155;
}
:deep(.vgt-table tbody tr:hover td) {
  background-color: #f8fafc;
}
:deep(.vgt-global-search) {
  border-color: #e2e8f0;
  background-color: #ffffff;
}
:deep(.vgt-input) {
  border-color: #e2e8f0;
}
:deep(.vgt-input:focus) {
  outline: none;
  border-color: #2563eb;
}
:deep(.vgt-wrap__footer) {
  background-color: #ffffff;
  border-color: #e2e8f0;
  color: #475569;
}
:deep(.footer__navigation__page-btn) {
  color: #2563eb;
}
</style>
```

- [ ] **Step 3: Run the existing tests again to confirm no regression**

Run: `cd web && npm run test -- DataTable.spec.js`
Expected: PASS (all 5 existing tests, unchanged)

- [ ] **Step 4: Manual visual check**

Run: `cd web && npm run dev` (proxies `/api` to `http://localhost:8090` — run `api-server` locally, or `make control-plane-up`, alongside it; alternatively `make demo-up` from the repo root and visit `http://localhost:8091`)

Visit `/clients`, `/catalog`, `/jobs`, `/policies`, `/storage` and confirm: the table border/corners match the app's other panels, the header row is uppercase slate-on-light-slate, hovering a row lightens it, and the search box and pagination footer no longer look like an unstyled third-party widget.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/DataTable.vue
git commit -m "style(web): restyle DataTable to match the app's existing borders and colors"
```

---

### Task 14: Favicon

**Files:**
- Create: `web/public/favicon.svg`
- Modify: `web/index.html`

**Interfaces:**
- Consumes: nothing.
- Produces: a static asset Vite serves at `/favicon.svg` (Vite serves everything under `web/public/` from the site root with no config change needed, since `web/public/` doesn't exist yet and this task creates it).

- [ ] **Step 1: Create the favicon asset**

Create `web/public/favicon.svg`:

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
  <rect width="32" height="32" rx="7" fill="#2563eb"/>
  <text x="16" y="22" font-family="ui-sans-serif, system-ui, sans-serif" font-size="16" font-weight="700" fill="#ffffff" text-anchor="middle">M</text>
</svg>
```

- [ ] **Step 2: Reference it from `index.html`**

In `web/index.html`, add a `<link>` inside `<head>`, after the `<title>` line:

```html
    <title>Miniprotector</title>
    <link rel="icon" type="image/svg+xml" href="/favicon.svg" />
```

- [ ] **Step 3: Verify manually**

Run: `cd web && npm run dev`, open the app in a browser, and confirm the browser tab shows the blue "M" mark instead of the generic default icon.

- [ ] **Step 4: Commit**

```bash
git add web/public/favicon.svg web/index.html
git commit -m "feat(web): add a favicon matching the sidebar brand mark"
```

---

### Task 15: Documentation and changelog

**Files:**
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing (documents the work done in Tasks 1–14).
- Produces: nothing consumed by later tasks — this is the last task.

- [ ] **Step 1: Update `docs/components/web.md`**

In the `## Pages` section's bullet describing `/clients`, `/policies`, etc. (the top-level bulleted list), and in the general description, weave in a short mention of the changes. Specifically:

- In the file's second paragraph (the one starting "On first load, the app prompts..."), leave as-is.
- At the end of the `## Pages` section (after the last bullet, currently the `/jobs/:job_id` bullet), add a new paragraph:

```markdown
Every page's header now shows a breadcrumb trail (e.g. "Policies / nightly-db-backup") above the
title via `PageHeader`'s `crumbs` prop, and the sidebar (`Sidebar.vue`) carries a small brand mark
plus one icon per section (`components/icons/`, hand-authored inline SVG — no icon package
dependency). Boolean/state table columns (a client's Revoked column, a job's State column) render
as a colored pill via the new `Badge` component (`components/ui/Badge.vue`) instead of plain text.
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

At the top of `CHANGELOG.md`, immediately after the `All notable changes...` line, insert a new dated entry (use today's date):

```markdown
## 2026-08-04 — web: navigation shell and visual consistency polish

The sidebar now carries a small brand mark and one icon per section, with a clearer accent-bordered
active state, instead of plain text links on a flat background. Every list and detail page shows a
breadcrumb trail above its title (`PageHeader`'s new `crumbs` prop), closing the orientation gap on
pages reached directly rather than via the sidebar. Boolean/state table columns (a client's Revoked
column, a job's State column) render as a colored badge instead of plain text, and the shared
`DataTable` component now overrides `vue-good-table-next`'s default theme to match the rest of the
app's borders and colors. `BaseButton` gained an optional `to` prop so link-styled actions (like
Clients' "New Client") go through the same component as `<button>`-styled ones, closing the one
spot that still hardcoded its own Tailwind classes. Same routes, same data, same API — a visual and
navigational pass, not a new feature.

```

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document the web nav shell and visual consistency polish"
```

---

## Final verification

After Task 15's commit, run the full suite once more from a clean state to confirm nothing was missed:

```bash
cd web && npm run test
```

Expected: PASS, full suite (no failures, no skipped files).

Then do the full manual pass from the spec's Testing section: `make demo-up` from the repo root, and click through every page — Clients (list, new, detail), Catalog, Policies (list, detail, both tabs), Storage (list, detail, both tabs), Jobs (list, detail) — confirming breadcrumbs are correct and navigable, sidebar icons/active-state render, "New Client" matches other primary buttons, table styling is consistent across all five list views, and the favicon shows in the browser tab.
