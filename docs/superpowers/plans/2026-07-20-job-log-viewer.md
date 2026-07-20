# Job Log Line Parsing & Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `JobDetailView.vue`'s raw-JSON log line display with a parsed, level-colored,
expandable renderer, split into a reusable `LogLine.vue` component and a standalone `logLine.js`
parser.

**Architecture:** Pure client-side refactor of `web` (Vue 3 SPA). A new pure-function parser
(`web/src/utils/logLine.js`) turns one raw log line string into `{ ok, level, message, fields, raw }`.
A new leaf component (`web/src/components/LogLine.vue`) consumes that parser and renders one line;
`JobDetailView.vue` shrinks to fetch/loading/error/empty state plus a `v-for` over `LogLine`. No
backend, store, or route changes.

**Tech Stack:** Vue 3 (`<script setup>`), Pinia, Vitest + `@vue/test-utils`, Tailwind v4 utility
classes (no custom CSS).

## Global Constraints

- Vue 3 Composition API with `<script setup>` — matches every existing component/view in `web/src`.
- Styling is inline Tailwind utility classes only — no new CSS files, no CSS-in-JS.
- Every interactive element under test gets a `data-test="..."` attribute, matching
  `KeyValueEditor.vue`/`ClientDetailView.vue`'s existing convention.
- Tests are colocated as `<Name>.spec.js` next to the file they cover, using Vitest + `@vue/test-utils`.
- No new npm dependencies — the parser and renderer are plain JS/Vue, nothing added to `package.json`.
- Run all `web` commands via the project's documented Docker pattern (no local `node`/`npm` on PATH
  in this environment):
  `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- <path>`
  run from the repo root (`/home/alex/miniprotector`).

---

## Task 1: `logLine.js` parser

**Files:**
- Create: `web/src/utils/logLine.js`
- Test: `web/src/utils/logLine.spec.js`

**Interfaces:**
- Produces: `parseLogLine(raw: string) => { ok: boolean, level: string|null, message: string, fields: object, raw: string }`
  — consumed by Task 2's `LogLine.vue`.

- [ ] **Step 1: Write the failing test**

Create `web/src/utils/logLine.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { parseLogLine } from './logLine'

describe('parseLogLine', () => {
  it('splits a full slog line into level, message, and fields', () => {
    const raw = JSON.stringify({
      time: '2026-07-20T05:16:24Z',
      level: 'INFO',
      msg: 'policy execution completed',
      app: 'agent',
      pid: 1234,
      job_id: 'operating-refresh:1752400500',
      event: 'finish',
      status: 'success',
    })

    const result = parseLogLine(raw)

    expect(result.ok).toBe(true)
    expect(result.level).toBe('INFO')
    expect(result.message).toBe('policy execution completed')
    expect(result.fields).toEqual({
      time: '2026-07-20T05:16:24Z',
      app: 'agent',
      pid: 1234,
      job_id: 'operating-refresh:1752400500',
      event: 'finish',
      status: 'success',
    })
    expect(result.raw).toBe(raw)
  })

  it('defaults message to an empty string when msg is missing', () => {
    const raw = JSON.stringify({ level: 'DEBUG', job_id: 'x' })

    const result = parseLogLine(raw)

    expect(result.ok).toBe(true)
    expect(result.level).toBe('DEBUG')
    expect(result.message).toBe('')
    expect(result.fields).toEqual({ job_id: 'x' })
  })

  it('falls back to the raw text for a non-JSON line', () => {
    const raw = 'this is not json at all'

    const result = parseLogLine(raw)

    expect(result).toEqual({ ok: false, level: null, message: raw, fields: {}, raw })
  })

  it('falls back to the raw text for JSON that parses to a bare string', () => {
    const raw = JSON.stringify('just a string')

    const result = parseLogLine(raw)

    expect(result).toEqual({ ok: false, level: null, message: raw, fields: {}, raw })
  })

  it('falls back to the raw text for JSON that parses to a number', () => {
    const raw = '42'

    const result = parseLogLine(raw)

    expect(result).toEqual({ ok: false, level: null, message: raw, fields: {}, raw })
  })

  it('falls back to the raw text for JSON that parses to an array', () => {
    const raw = JSON.stringify([1, 2, 3])

    const result = parseLogLine(raw)

    expect(result).toEqual({ ok: false, level: null, message: raw, fields: {}, raw })
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

From `/home/alex/miniprotector`, run:
```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/utils/logLine.spec.js
```
Expected: FAIL — `Failed to resolve import "./logLine"` (the file doesn't exist yet).

- [ ] **Step 3: Write the minimal implementation**

Create `web/src/utils/logLine.js`:

```js
// Parses one raw log line -- JSON emitted by Go's slog JSONHandler
// (common/logging) -- into level/message/fields. Field-name-agnostic
// beyond level/msg, so it needs no update as binaries add new attrs.
// Anything that isn't a JSON object (a genuinely malformed line, or
// non-JSON output slipping through the same *.log glob) falls back to
// the raw text unchanged rather than throwing.
export function parseLogLine(raw) {
  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { ok: false, level: null, message: raw, fields: {}, raw }
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { ok: false, level: null, message: raw, fields: {}, raw }
  }

  const { level, msg, ...fields } = parsed
  return {
    ok: true,
    level: typeof level === 'string' ? level : null,
    message: typeof msg === 'string' ? msg : '',
    fields,
    raw,
  }
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/utils/logLine.spec.js
```
Expected: PASS — 6 tests.

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/logLine.js web/src/utils/logLine.spec.js
git commit -m "feat(web): add parseLogLine for structured job log rendering"
```

---

## Task 2: `LogLine.vue` component

**Files:**
- Create: `web/src/components/LogLine.vue`
- Test: `web/src/components/LogLine.spec.js`

**Interfaces:**
- Consumes: `parseLogLine` from `web/src/utils/logLine.js` (Task 1); `formatTimestamp(epochSeconds)`
  from `web/src/utils/format.js` (existing).
- Produces: `LogLine.vue`, a component with one prop `line: { timestamp: number, hostname: string,
  binary: string, line: string }` (the existing `GET /jobs/{job_id}/logs` line DTO) — consumed by
  Task 3's `JobDetailView.vue`.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/LogLine.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import LogLine from './LogLine.vue'

function mountLine(overrides = {}) {
  const line = {
    timestamp: 1752400000123456789,
    hostname: 'database',
    binary: 'brfs',
    line: JSON.stringify({ level: 'INFO', msg: 'started', job_id: 'backup:x:1' }),
    ...overrides,
  }
  return mount(LogLine, { props: { line } })
}

describe('LogLine', () => {
  it('renders the level, timestamp, binary@hostname, and message summary', () => {
    const wrapper = mountLine()

    expect(wrapper.find('[data-test="log-line-level"]').text()).toBe('INFO')
    expect(wrapper.find('[data-test="log-line-message"]').text()).toBe('started')
    expect(wrapper.text()).toContain('brfs@database')
  })

  it('colors the level badge by severity', () => {
    const info = mountLine({ line: JSON.stringify({ level: 'INFO', msg: 'x' }) })
    const error = mountLine({ line: JSON.stringify({ level: 'ERROR', msg: 'x' }) })

    expect(info.find('[data-test="log-line-level"]').classes()).toContain('bg-blue-100')
    expect(error.find('[data-test="log-line-level"]').classes()).toContain('bg-red-100')
  })

  it('keeps extra fields hidden until the row is clicked, then shows them', async () => {
    const wrapper = mountLine({
      line: JSON.stringify({ level: 'INFO', msg: 'started', job_id: 'backup:x:1', event: 'start' }),
    })

    expect(wrapper.find('[data-test="log-line-fields"]').exists()).toBe(false)

    await wrapper.find('[data-test="log-line-summary"]').trigger('click')

    const fields = wrapper.find('[data-test="log-line-fields"]')
    expect(fields.exists()).toBe(true)
    expect(fields.text()).toContain('job_id')
    expect(fields.text()).toContain('backup:x:1')
    expect(fields.text()).toContain('event')
    expect(fields.text()).toContain('start')
  })

  it('shows no expand affordance and does not toggle when there are no extra fields', async () => {
    const wrapper = mountLine({ line: JSON.stringify({ level: 'INFO', msg: 'started' }) })

    expect(wrapper.find('[data-test="log-line-caret"]').exists()).toBe(false)

    await wrapper.find('[data-test="log-line-summary"]').trigger('click')

    expect(wrapper.find('[data-test="log-line-fields"]').exists()).toBe(false)
  })

  it('falls back to the raw line text with a neutral badge when the line is not JSON', () => {
    const wrapper = mountLine({ line: 'not json at all' })

    expect(wrapper.find('[data-test="log-line-level"]').text()).toBe('—')
    expect(wrapper.find('[data-test="log-line-message"]').text()).toBe('not json at all')
    expect(wrapper.find('[data-test="log-line-caret"]').exists()).toBe(false)
  })

  it('stringifies a non-primitive field value instead of showing [object Object]', async () => {
    const wrapper = mountLine({
      line: JSON.stringify({ level: 'ERROR', msg: 'failed', error: { code: 'E1', retryable: true } }),
    })

    await wrapper.find('[data-test="log-line-summary"]').trigger('click')

    expect(wrapper.find('[data-test="log-line-fields"]').text()).toContain('{"code":"E1","retryable":true}')
  })
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/components/LogLine.spec.js
```
Expected: FAIL — `Failed to resolve import "./LogLine.vue"` (the file doesn't exist yet).

- [ ] **Step 3: Write the minimal implementation**

Create `web/src/components/LogLine.vue`:

```vue
<script setup>
import { computed, ref } from 'vue'
import { formatTimestamp } from '../utils/format'
import { parseLogLine } from '../utils/logLine'

const props = defineProps({
  line: { type: Object, required: true },
})

const expanded = ref(false)

const parsed = computed(() => parseLogLine(props.line.line))
const fieldEntries = computed(() => Object.entries(parsed.value.fields))

const LEVEL_CLASSES = {
  DEBUG: 'bg-gray-100 text-gray-600',
  INFO: 'bg-blue-100 text-blue-700',
  WARN: 'bg-amber-100 text-amber-700',
  ERROR: 'bg-red-100 text-red-700',
}

const levelClass = computed(() => LEVEL_CLASSES[parsed.value.level] || 'bg-gray-100 text-gray-400')
const levelLabel = computed(() => parsed.value.level || '—')

// GET /jobs/{job_id}/logs returns Loki's raw nanosecond timestamp (unlike
// GET /jobs's started_at/finished_at, already seconds) -- convert before
// formatTimestamp, which expects epoch seconds.
function formatLineTimestamp(nanos) {
  return formatTimestamp(Math.floor(nanos / 1e9))
}

function formatFieldValue(value) {
  return typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean'
    ? String(value)
    : JSON.stringify(value)
}

function toggle() {
  if (fieldEntries.value.length === 0) return
  expanded.value = !expanded.value
}
</script>

<template>
  <li class="border-b py-1" data-test="log-line">
    <div
      class="font-mono text-sm flex items-baseline gap-2"
      :class="{ 'cursor-pointer': fieldEntries.length > 0 }"
      data-test="log-line-summary"
      @click="toggle"
    >
      <span v-if="fieldEntries.length > 0" class="text-gray-400 select-none" data-test="log-line-caret">{{
        expanded ? '▾' : '▸'
      }}</span>
      <span :class="['inline-block rounded px-1.5 text-xs font-semibold', levelClass]" data-test="log-line-level">{{
        levelLabel
      }}</span>
      <span class="text-gray-500">{{ formatLineTimestamp(line.timestamp) }}</span>
      <span>{{ line.binary }}@{{ line.hostname }}:</span>
      <span data-test="log-line-message">{{ parsed.message }}</span>
    </div>
    <dl
      v-if="expanded"
      class="mt-1 ml-6 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs font-mono text-gray-600"
      data-test="log-line-fields"
    >
      <template v-for="[key, value] in fieldEntries" :key="key">
        <dt class="font-semibold">{{ key }}</dt>
        <dd>{{ formatFieldValue(value) }}</dd>
      </template>
    </dl>
  </li>
</template>
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/components/LogLine.spec.js
```
Expected: PASS — 6 tests.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/LogLine.vue web/src/components/LogLine.spec.js
git commit -m "feat(web): add LogLine component for parsed job log rendering"
```

---

## Task 3: Wire `LogLine.vue` into `JobDetailView.vue`

**Files:**
- Modify: `web/src/views/JobDetailView.vue` (full rewrite, currently 37 lines)
- Modify: `web/src/views/JobDetailView.spec.js:28-39` (one test updated)

**Interfaces:**
- Consumes: `LogLine.vue` (Task 2), props `{ line }`.

- [ ] **Step 1: Update the failing/changed test first**

In `web/src/views/JobDetailView.spec.js`, replace the test at lines 28-39:

```js
  it('renders each log line with its formatted timestamp, hostname, binary, and raw line', () => {
    const { wrapper } = mountView({
      logs: [
        { timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{"msg":"started"}' },
      ],
      logsLoading: false,
      logsError: null,
    })
    expect(wrapper.text()).toContain('database')
    expect(wrapper.text()).toContain('brfs')
    expect(wrapper.text()).toContain('{"msg":"started"}')
  })
```

with:

```js
  it('renders each log line via LogLine with timestamp, hostname, binary, and message', () => {
    const { wrapper } = mountView({
      logs: [
        { timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{"msg":"started"}' },
      ],
      logsLoading: false,
      logsError: null,
    })
    expect(wrapper.text()).toContain('database')
    expect(wrapper.text()).toContain('brfs')
    expect(wrapper.text()).toContain('started')
    expect(wrapper.text()).not.toContain('{"msg":"started"}')
  })
```

Note the added `not.toContain('{"msg":"started"}')`: `'started'` alone is already a substring of the
raw JSON `{"msg":"started"}`, so a bare `toContain('started')` would pass against *both* the old
raw-text rendering and the new parsed rendering — it wouldn't actually prove anything changed. The
`not.toContain` assertion is what's false today (the old view renders that raw string verbatim) and
true after Step 3's rewrite.

The other four tests in this file (`calls fetchLogs...`, `renders the job id...`, `shows an
empty-state message...`, `shows the store error message...`) are unchanged — they test
fetch-on-mount and loading/error/empty state, none of which this task touches.

- [ ] **Step 2: Run the test to verify it fails**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/views/JobDetailView.spec.js
```
Expected: FAIL — `not.toContain('{"msg":"started"}')` fails, because the still-unmodified
`JobDetailView.vue` renders that raw string verbatim.

- [ ] **Step 3: Rewrite the view**

Replace `web/src/views/JobDetailView.vue` entirely with:

```vue
<script setup>
import { onMounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useJobsStore } from '../stores/jobs'
import LogLine from '../components/LogLine.vue'

const route = useRoute()
const jobs = useJobsStore()
const jobId = computed(() => route.params.job_id)

onMounted(async () => {
  await jobs.fetchLogs(jobId.value)
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">{{ jobId }}</h1>
    <p v-if="jobs.logsLoading">Loading...</p>
    <p v-else-if="jobs.logsError" class="text-red-600">{{ jobs.logsError }}</p>
    <p v-else-if="jobs.logs.length === 0">No log lines found for this job in the last 24h.</p>
    <ul v-else>
      <LogLine v-for="(line, index) in jobs.logs" :key="index" :line="line" />
    </ul>
  </div>
</template>
```

This removes the `formatTimestamp`/`formatLineTimestamp` logic entirely from this file — it now
lives in `LogLine.vue` (Task 2), which is the only place that needs it.

- [ ] **Step 4: Run the test to verify it passes**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test -- src/views/JobDetailView.spec.js
```
Expected: PASS — 5 tests.

- [ ] **Step 5: Run the full web test suite to check for regressions**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm test
```
Expected: PASS — every test file, including `logLine.spec.js` and `LogLine.spec.js` from Tasks 1-2.

- [ ] **Step 6: Commit**

```bash
git add web/src/views/JobDetailView.vue web/src/views/JobDetailView.spec.js
git commit -m "refactor(web): render job logs through LogLine instead of raw text"
```

---

## Task 4: Documentation and changelog

**Files:**
- Modify: `docs/components/web.md:42-43`
- Modify: `CHANGELOG.md` (insert new entry at top)

**Interfaces:** None — documentation only.

- [ ] **Step 1: Update `docs/components/web.md`**

Replace lines 42-43:

```markdown
- `/jobs/:job_id` — one job's raw log lines from the last 24h, fetched once on page load (no
  live-tail/polling)
```

with:

```markdown
- `/jobs/:job_id` — one job's log lines from the last 24h, fetched once on page load (no
  live-tail/polling); each line is parsed from its underlying JSON via `LogLine.vue` into a
  level-colored `[LEVEL] time binary@hostname: message` summary, with the remaining fields
  (`job_id`, `event`, `status`, etc.) collapsed behind a click — a line that isn't valid JSON
  falls back to plain text
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Insert at the top of `CHANGELOG.md`, immediately after the `# Changelog` header and its intro
paragraph, above the existing `## 2026-07-19 — web: add client enrollment/...` entry:

```markdown
## 2026-07-20 — web: parse and render job log lines

`/jobs/:job_id` now parses each line's underlying slog JSON instead of showing it raw: a
level-colored `[LEVEL] time binary@hostname: message` summary per line, with the remaining fields
(`job_id`, `event`, `status`, `duration`, `error`, etc.) collapsed behind a click. Lines that aren't
valid JSON still render as plain text, unchanged from before. New `LogLine.vue` component and
`utils/logLine.js` parser take over rendering that was previously inlined in `JobDetailView.vue`.

```

- [ ] **Step 3: Commit**

```bash
git add docs/components/web.md CHANGELOG.md
git commit -m "docs: document job log line parsing/rendering"
```
