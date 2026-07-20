# Design: Job Log Line Parsing & Rendering

**Date:** 2026-07-20
**Status:** Approved for planning

## Problem

[Design: Jobs pages for `web`](2026-07-19-jobs-frontend-design.md) shipped `JobDetailView.vue`
deliberately out of scope for log parsing: "each line's raw `line` string is rendered as-is in a
monospace block." In practice every shipped line is JSON from Go's `common/logging` (slog's
`JSONHandler`) — e.g.
`{"time":"2026-07-20T05:16:24Z","level":"INFO","msg":"policy execution completed","app":"agent","pid":1234,"job_id":"operating-refresh:1752400500","event":"finish","status":"success"}`
— so today's view makes an operator read a wall of undifferentiated raw JSON per line, with no
visual distinction between an `ERROR` and a routine `DEBUG` line, and no way to scan messages
without the field noise around them.

Separately, `JobDetailView.vue` currently owns fetch-on-mount, loading/error/empty states, *and*
per-line formatting inline — the file mixes view-level concerns with line-level rendering with no
separation, unlike the rest of `web`'s components (e.g. `KeyValueEditor.vue`,
`ClientDetailView.vue` + its editor sub-components).

This design supersedes that prior out-of-scope decision and restructures the view to match the
rest of the codebase's component conventions.

## Scope

**In scope:**
- `web/src/utils/logLine.js` — pure `parseLogLine(raw)` parser, field-name-agnostic beyond
  `level`/`msg`
- `web/src/components/LogLine.vue` — one log entry: collapsed summary line + expandable field
  details
- `JobDetailView.vue` restructured to delegate per-line rendering to `LogLine.vue`

**Out of scope:**
- Any change to `api-server`'s `/jobs/{job_id}/logs` response shape, or to the `jobs.js` store's
  fetch logic
- Live-tail/polling, level filtering, search — still a single fetch-on-mount, still every line
  from the response rendered
- Changes to `JobsListView.vue` (the jobs table) — this is `JobDetailView` only

## Architecture

No change to `web`'s overall architecture (static SPA, Pinia store, `api/client.js`). This is a
pure client-side refactor + one new leaf component, following the same "view owns fetch/loading/
error state, delegates per-row rendering to a focused component" split `ClientDetailView.vue`
already uses for `KeyValueEditor.vue`/`SanListEditor.vue`.

### Parser contract (`parseLogLine`)

```js
parseLogLine(raw: string) => {
  ok: boolean,       // true if raw parsed as a JSON object
  level: string|null,// slog level ("INFO"/"WARN"/"ERROR"/"DEBUG"), null if absent or parse failed
  message: string,   // slog msg field; raw itself when parsing failed
  fields: object,     // every other key from the parsed object (job_id, event, status, duration,
                       // error, app, pid, policy, ...) -- nothing hardcoded beyond level/msg, so
                       // it needs no update as binaries add new attrs
  raw: string,        // original line, always preserved for the "no lines match" / parse-failure case
}
```

`JSON.parse` failure, or successful parse into something that isn't a plain object (a bare string,
number, or array), both fall back to `{ ok: false, level: null, message: raw, fields: {}, raw }` —
one malformed or unexpectedly-shaped line degrades to plain text, it never throws or blanks the
row.

## Components

**`logLine.js`:** as specified above. No Vue dependency — plain function, unit-testable in
isolation.

**`LogLine.vue`:**
- Props: `{ line }` — the existing API DTO (`{ timestamp, hostname, binary, line }`) unchanged.
- Computes `parseLogLine(props.line.line)`.
- Collapsed (default) row: `[LEVEL] time binary@hostname: message`, matching `formatLineTimestamp`'s
  existing nanosecond-to-`formatTimestamp` conversion already in `JobDetailView.vue`. `LEVEL` is a
  small colored badge: `DEBUG` gray, `INFO` blue, `WARN` amber, `ERROR` red, `—` (neutral gray) when
  `level` is `null` (parse failure or absent field).
- Click anywhere on the row toggles an expanded details block listing `fields` as `key: value`
  pairs, one per line, monospace — omitted entirely (no toggle affordance shown) when `fields` is
  empty.
- Parse-failure lines render with the `—` badge and `message` (= the raw line) in the summary row;
  since `fields` is `{}` in this case, there's nothing to expand, matching today's fallback
  behavior for any non-JSON line.

**`JobDetailView.vue`:** unchanged fetch/loading/error/empty logic; the `v-for` over `jobs.logs`
now renders `<LogLine :line="line" />` per entry instead of inlining the format directly.

## Data Flow

Unchanged from the existing design — `onMounted` → `jobs.fetchLogs(jobId)` → `jobs.logs` populated
→ template renders. The only change is what renders each entry: `LogLine.vue` instead of an inline
`<span>`/text interpolation.

## Error Handling

No new error paths. Store-level loading/error/empty states are untouched. The parser's own "can't
parse this line" case is handled entirely within `LogLine.vue`'s fallback rendering (see above),
never surfaced as a store-level `logsError` — a single malformed line is a per-line rendering
concern, not a fetch failure.

## Testing

- **`logLine.spec.js`:** valid line with full field set (level/msg/job_id/event/status/duration/
  error/app/pid) → correct `level`/`message`/`fields` split; line with `msg` missing; non-JSON
  garbage string; valid JSON that isn't an object (a bare string, a number, an array) — all three
  malformed cases assert `ok: false` and `message === raw`.
- **`LogLine.spec.js`:** renders the `[LEVEL] time binary@hostname: message` summary; badge color
  class per level; fields hidden until the row is clicked, then visible; no expand affordance when
  `fields` is empty; parse-failure line renders the raw text with the neutral badge and no expand
  affordance.
- **`JobDetailView.spec.js`:** existing loading/error/empty-state assertions unchanged. The two
  assertions currently checking the raw JSON string appears verbatim (`{"msg":"started"}`) are
  updated to assert the new rendered format instead (message text present, raw JSON no longer shown
  in the collapsed state).

## Documentation

- `docs/components/web.md`: note `JobDetailView` now parses/renders structured log lines (superseding
  the "out of scope" note carried over from the jobs-frontend design).
- `CHANGELOG.md`: one dated entry before merge.

## See Also

- [Design: Jobs pages for `web`](2026-07-19-jobs-frontend-design.md) — the design this supersedes
  the "no parsing" scope note from
- [Design: `/jobs` REST Endpoint](2026-07-19-jobs-endpoint-design.md) — the API this view consumes
- [web component doc](../../components/web.md)
