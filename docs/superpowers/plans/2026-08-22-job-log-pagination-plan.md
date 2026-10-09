# Job Log Pagination & Bounded Retention Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `web`'s job log viewer from growing memory/DOM without bound, by adding cursor-based pagination to `GET /api/v1/jobs/{job_id}/logs` and a follow-gated bounded-retention policy on the frontend.

**Architecture:** `api-server`'s `GET /jobs/{job_id}/logs` gains `limit`/`ending_before`/`has_more` (Stripe-style bidirectional cursor, matching `rest-v1.md`'s existing convention). The frontend `jobs` Pinia store switches its per-line merge from an `O(n log n)` push+resort to an `O(1)`-common-case sorted insert, caps the resident `logs` array at a fixed ceiling that's only enforced while the view is "following" the live tail, and gains a `loadOlder` action for paging history backward on demand. `JobDetailView.vue` keys its list by log identity (not array index — required once the array is mutated by eviction/prepend), and a new `useAutoFollow` composable owns bottom-of-viewport detection so the store knows when it's safe to evict.

**Tech Stack:** Go (`api-server`, stdlib `net/http` + `testify`), Vue 3 `<script setup>` + Pinia (`jobs` store), Vitest + `@vue/test-utils`.

## Global Constraints

- Page size / default+max `limit` for `GET /jobs/{job_id}/logs`: **500** (`JOB_LOGS_PAGE_SIZE` on the frontend).
- Frontend live-follow resident-line cap: **2000** (`JOB_LOGS_LIVE_CAP`).
- No DOM virtualization library — the live-follow cap already bounds rendered node count (spec's explicit Non-Goal).
- No cap on how much history a user can page back through via repeated "Load older" clicks — bounded by deliberate user action, not automatic growth (spec's explicit Non-Goal).
- `JobsListView.vue` / `GET /jobs` / `GET /jobs/stream` are out of scope — unchanged.
- Per `.claude/CLAUDE.md`'s feature-change rule: `docs/api/rest-v1.md` and `docs/components/web.md` must be updated as part of this work (Task 6), and a `CHANGELOG.md` entry added before merge (Task 6).
- Full spec: `docs/superpowers/specs/2026-08-22-job-log-pagination-design.md`.

---

### Task 1: Backend — paginate `GET /api/v1/jobs/{job_id}/logs`

**Files:**
- Modify: `src/cmd/api-server/jobs.go` (constants block near line 15-21; `handleGetJobLogs`, lines 338-404)
- Test: `src/cmd/api-server/jobs_test.go` (extend `fakeLokiClient`, lines 16-26; add tests after line 527)

**Interfaces:**
- Consumes: existing `lokiQuerier.QueryRange(ctx, query, start, end time.Time, limit int) ([]lokiStream, error)` — unchanged signature.
- Produces: `GET /api/v1/jobs/{job_id}/logs` now accepts `limit` (int, default/max 500) and `ending_before` (unix-nanosecond int, exclusive cursor) query params, and its JSON response gains `"has_more": bool` alongside the existing `"data"` array. Frontend tasks (2 onward) depend on this exact param/response shape.

- [ ] **Step 1: Extend `fakeLokiClient` to capture the last call's arguments**

In `src/cmd/api-server/jobs_test.go`, replace the `fakeLokiClient` type and its `QueryRange` method (lines 16-26) with:

```go
type fakeLokiClient struct {
	byQuery map[string][]lokiStream
	err     error

	calls     int
	lastStart time.Time
	lastEnd   time.Time
	lastLimit int
}

func (f *fakeLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	f.calls++
	f.lastStart = start
	f.lastEnd = end
	f.lastLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.byQuery[query], nil
}
```

This is purely additive (new fields, same behavior for existing lookups) — every existing test in this file keeps passing unchanged.

- [ ] **Step 2: Write the failing pagination tests**

Append to `src/cmd/api-server/jobs_test.go`:

```go
func TestHandleGetJobLogs_DefaultLimitPassedToLoki(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="operating-refresh:1752400500"`: {},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/operating-refresh:1752400500/logs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 500, fake.lastLimit)
}

func TestHandleGetJobLogs_LimitOutsideRangeReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	for _, raw := range []string{"0", "501", "not-a-number"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/operating-refresh:1752400500/logs?limit="+raw, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "limit=%s should be rejected", raw)
	}
}

func TestHandleGetJobLogs_HasMoreTrueWhenPageIsFull(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="operating-refresh:1752400500"`: {
			{Stream: map[string]string{"hostname": "webserver", "binary": "agent"}, Values: []lokiValue{
				{Timestamp: 100, Line: "a"},
				{Timestamp: 200, Line: "b"},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/operating-refresh:1752400500/logs?limit=2", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["has_more"])
}

func TestHandleGetJobLogs_HasMoreFalseWhenPageIsPartial(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="operating-refresh:1752400500"`: {
			{Stream: map[string]string{"hostname": "webserver", "binary": "agent"}, Values: []lokiValue{
				{Timestamp: 100, Line: "a"},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/operating-refresh:1752400500/logs?limit=5", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, false, body["has_more"])
}

func TestHandleGetJobLogs_EndingBeforeNarrowsEndExclusive(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="operating-refresh:1752400500"`: {},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/operating-refresh:1752400500/logs?ending_before=1752400500000000000", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int64(1752400499999999999), fake.lastEnd.UnixNano())
}

func TestHandleGetJobLogs_EndingBeforeInvalidReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/operating-refresh:1752400500/logs?ending_before=not-a-number", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleGetJobLogs_EndingBeforeAtWindowFloorReturnsEmptyWithoutQueryingLoki(t *testing.T) {
	fake := &fakeLokiClient{}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	since := time.Now().Add(-1 * time.Hour).Unix()
	endingBefore := time.Now().Add(-2 * time.Hour).UnixNano() // before the since floor
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/jobs/operating-refresh:1752400500/logs?since=%d&ending_before=%d", since, endingBefore), nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []any{}, body["data"])
	assert.Equal(t, false, body["has_more"])
	assert.Equal(t, 0, fake.calls, "must not query Loki with an inverted/empty range")
}
```

`TestHandleGetJobLogs_EndingBeforeAtWindowFloorReturnsEmptyWithoutQueryingLoki` needs `"fmt"` — already imported in this file's package (`jobs.go` imports it; test file is same package `main`, but each `.go` file needs its own imports). Add `"fmt"` to `jobs_test.go`'s import block (currently: `context`, `encoding/json`, `net/http`, `net/http/httptest`, `strconv`, `testing`, `time`, plus the two `testify` packages).

- [ ] **Step 3: Run the new tests and confirm they fail**

```bash
cd src && go test ./cmd/api-server/... -run TestHandleGetJobLogs -v
```

Expected: `TestHandleGetJobLogs_DefaultLimitPassedToLoki`, `_LimitOutsideRangeReturns400`, `_HasMoreTrueWhenPageIsFull`, `_HasMoreFalseWhenPageIsPartial`, `_EndingBeforeNarrowsEndExclusive`, `_EndingBeforeInvalidReturns400`, and `_EndingBeforeAtWindowFloorReturnsEmptyWithoutQueryingLoki` all FAIL (`has_more` key missing / wrong status codes / `fake.lastLimit` is the old hardcoded value).

- [ ] **Step 4: Implement pagination in `handleGetJobLogs`**

In `src/cmd/api-server/jobs.go`, add two constants to the existing block (lines 15-21):

```go
const (
	defaultJobsWindow  = 24 * time.Hour
	maxJobsWindow      = 168 * time.Hour
	defaultJobsLimit   = 100
	maxJobsLimit       = 500
	jobsQueryLineLimit = 5000

	defaultJobLogsLimit = 500
	maxJobLogsLimit     = 500
)
```

Replace `handleGetJobLogs` (lines 338-404) with:

```go
func (s *server) handleGetJobLogs(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job_id")
	if !jobIDPattern.MatchString(jobID) {
		writeJSONError(w, http.StatusBadRequest, "job_id contains invalid characters")
		return
	}

	q := r.URL.Query()
	until := time.Now()
	since := until.Add(-defaultJobsWindow)
	if raw := q.Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "since must be a unix-second integer")
			return
		}
		since = time.Unix(parsed, 0)
	}

	limit := defaultJobLogsLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxJobLogsLimit {
			writeJSONError(w, http.StatusBadRequest, "limit must be an integer between 1 and 500")
			return
		}
		limit = parsed
	}

	if raw := q.Get("ending_before"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "ending_before must be a unix-nanosecond integer")
			return
		}
		// ending_before is the timestamp of the oldest line already loaded on
		// the client -- exclusive, so the next page stops strictly before it
		// instead of re-returning that same line.
		until = time.Unix(0, parsed-1)
	}

	if !until.After(since) {
		// Paged back past the window floor -- not an error, just nothing
		// left to return.
		writeJSON(w, http.StatusOK, map[string]any{"data": []logLineDTO{}, "has_more": false})
		return
	}

	sourceHost := q.Get("source_host")
	if sourceHost != "" && !jobHostnamePattern.MatchString(sourceHost) {
		writeJSONError(w, http.StatusBadRequest, "source_host contains invalid characters")
		return
	}
	storeHost := q.Get("store_host")
	if storeHost != "" && !jobHostnamePattern.MatchString(storeHost) {
		writeJSONError(w, http.StatusBadRequest, "store_host contains invalid characters")
		return
	}
	// Unlike binariesForKind's default selector, this includes rwfs: this
	// endpoint returns every raw log line for a job_id verbatim (no
	// start/finish pairing), so rwfs's lines are useful signal here even
	// though handleListJobs excludes rwfs to avoid pairing noise (rwfs never
	// emits event=start/event=finish).
	labelSelector := `{binary=~"agent|brfs|bwfs|rwfs"}`
	switch {
	case sourceHost != "" && storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname=~"%s|%s"}`, sourceHost, storeHost)
	case sourceHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname="%s"}`, sourceHost)
	case storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname="%s"}`, storeHost)
	}

	query := fmt.Sprintf(`%s | job_id="%s"`, labelSelector, jobID)
	streams, err := s.loki.QueryRange(r.Context(), query, since, until, limit)
	if err != nil {
		s.logger.Error("handleGetJobLogs: query failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}

	lines := []logLineDTO{}
	for _, stream := range streams {
		for _, v := range stream.Values {
			lines = append(lines, logLineDTO{
				Timestamp: v.Timestamp,
				Hostname:  stream.Stream["hostname"],
				Binary:    stream.Stream["binary"],
				Line:      v.Line,
			})
		}
	}
	sort.Slice(lines, func(i, k int) bool { return lines[i].Timestamp < lines[k].Timestamp })

	writeJSON(w, http.StatusOK, map[string]any{"data": lines, "has_more": len(lines) >= limit})
}
```

- [ ] **Step 5: Run all `api-server` tests and confirm they pass**

```bash
cd src && go test ./cmd/api-server/... -v
```

Expected: every test PASSES, including the pre-existing `TestHandleGetJobLogs_*` tests (they don't assert on `has_more`'s absence, so the new field doesn't break them) and the new ones from Step 2.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go
git commit -m "feat(api-server): paginate GET /jobs/{job_id}/logs with limit/ending_before"
```

---

### Task 2: Frontend — move `logKey` into `utils/logLine.js`

**Files:**
- Modify: `web/src/utils/logLine.js`
- Modify: `web/src/utils/logLine.spec.js`
- Modify: `web/src/stores/jobs.js` (remove local `logKey`, lines 10-12; import instead)

**Interfaces:**
- Produces: `logKey(line: { timestamp, hostname, binary }): string` exported from `web/src/utils/logLine.js`. Task 3 (store) and Task 5 (view) both import this instead of duplicating the format string.

- [ ] **Step 1: Write the failing test**

In `web/src/utils/logLine.spec.js`, add (alongside the existing `parseLogLine` import/describe block):

```js
import { parseLogLine, logKey } from './logLine'
```

```js
describe('logKey', () => {
  it('joins timestamp, hostname, and binary into one identity string', () => {
    expect(logKey({ timestamp: 100, hostname: 'h', binary: 'brfs' })).toBe('100|h|brfs')
  })

  it('differs when only the binary differs, so a rwfs and brfs line at the same timestamp/host never collide', () => {
    const a = logKey({ timestamp: 100, hostname: 'h', binary: 'brfs' })
    const b = logKey({ timestamp: 100, hostname: 'h', binary: 'rwfs' })
    expect(a).not.toBe(b)
  })
})
```

- [ ] **Step 2: Run it and confirm it fails**

```bash
cd web && npx vitest run src/utils/logLine.spec.js
```

Expected: FAIL — `logKey` is not exported from `./logLine`.

- [ ] **Step 3: Add `logKey` to `logLine.js`**

In `web/src/utils/logLine.js`, add (e.g. above `parseLogLine`):

```js
// logKey identifies one log line for dedup/eviction purposes -- shared by
// stores/jobs.js (dedup on merge, eviction bookkeeping) and
// views/JobDetailView.vue (the list's Vue :key), so both use exactly the
// same identity instead of two copies of the same format string drifting
// apart.
export function logKey(line) {
  return `${line.timestamp}|${line.hostname}|${line.binary}`
}
```

- [ ] **Step 4: Update `jobs.js` to import instead of defining its own copy**

In `web/src/stores/jobs.js`, change the import line (line 5) and remove the local function (lines 10-12):

```js
import { parseLogLine, logKey } from '../utils/logLine'
```

Delete:

```js
function logKey(line) {
  return `${line.timestamp}|${line.hostname}|${line.binary}`
}
```

- [ ] **Step 5: Run the full frontend test suite and confirm everything still passes**

```bash
cd web && npm run test
```

Expected: PASS — this step is a pure relocation, no behavior change yet.

- [ ] **Step 6: Commit**

```bash
git add web/src/utils/logLine.js web/src/utils/logLine.spec.js web/src/stores/jobs.js
git commit -m "refactor(web): move logKey into utils/logLine, shared by store and view"
```

---

### Task 3: Frontend store — cheap merge, follow-gated cap, `loadOlder`

**Files:**
- Modify: `web/src/stores/jobs.js`
- Modify: `web/src/stores/jobs.spec.js`

**Interfaces:**
- Consumes: `logKey` from Task 2 (`web/src/utils/logLine.js`).
- Produces (used by Task 5's view):
  - State: `jobs.hasOlderLogs: boolean`, `jobs.isFollowing: boolean`.
  - Actions: `jobs.loadOlder(jobId: string): Promise<void>`, `jobs.setFollowing(value: boolean): void`.
  - Unchanged from the caller's perspective: `fetchLogs`, `connectLogsStream`, `disconnectLogsStream`, `logs`, `logsLoading`, `logsError`, `logsStatus`.

- [ ] **Step 1: Update the two tests whose expected URL changes**

`fetchLogs`/`connectLogsStream` now always request a bounded page, so their expected `apiFetch` URL gains `?limit=500`. In `web/src/stores/jobs.spec.js`:

Replace:
```js
    expect(apiFetch).toHaveBeenCalledWith('/jobs/backup%3Anightly%3A1752400000/logs')
```
with:
```js
    expect(apiFetch).toHaveBeenCalledWith('/jobs/backup%3Anightly%3A1752400000/logs?limit=500')
```

Replace:
```js
      expect(apiFetch).toHaveBeenCalledWith('/jobs/restore%3Ax%3A1/logs')
```
with:
```js
      expect(apiFetch).toHaveBeenCalledWith('/jobs/restore%3Ax%3A1/logs?limit=500')
```

- [ ] **Step 2: Write the new failing tests**

Add to `web/src/stores/jobs.spec.js`, inside `describe('connectLogsStream', ...)` (reuses that block's `liveStreamHandlers` setup) or as new top-level `describe` blocks — add these as new top-level blocks after `describe('connectJobsStream', ...)`:

```js
describe('log merge cost and ordering', () => {
  let liveStreamHandlers

  beforeEach(() => {
    createLiveStream.mockReset()
    createLiveStream.mockImplementation((path, handlers) => {
      liveStreamHandlers = handlers
      return { close: vi.fn() }
    })
  })

  it('inserts an out-of-order line at its correct sorted position', async () => {
    apiFetch.mockResolvedValue({
      data: [
        { timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' },
        { timestamp: 300, hostname: 'h', binary: 'brfs', line: '{}' },
      ],
    })
    const jobs = useJobsStore()
    await jobs.connectLogsStream('restore:x:1')

    liveStreamHandlers.onMessage({ timestamp: 200, hostname: 'h', binary: 'brfs', line: '{"msg":"mid"}' })

    expect(jobs.logs.map((l) => l.timestamp)).toEqual([100, 200, 300])
  })
})

describe('live-follow cap', () => {
  let liveStreamHandlers

  beforeEach(() => {
    createLiveStream.mockReset()
    createLiveStream.mockImplementation((path, handlers) => {
      liveStreamHandlers = handlers
      return { close: vi.fn() }
    })
  })

  it('drops the oldest line once the 2000-line cap is exceeded while following', async () => {
    apiFetch.mockResolvedValue({ data: [] })
    const jobs = useJobsStore()
    await jobs.connectLogsStream('restore:x:1')

    for (let i = 0; i < 2001; i++) {
      liveStreamHandlers.onMessage({ timestamp: i, hostname: 'h', binary: 'brfs', line: '{}' })
    }

    expect(jobs.logs).toHaveLength(2000)
    expect(jobs.logs[0].timestamp).toBe(1)
    expect(jobs.logs.at(-1).timestamp).toBe(2000)
  })

  it('does not evict while not following, then catches up once following resumes', async () => {
    apiFetch.mockResolvedValue({ data: [] })
    const jobs = useJobsStore()
    await jobs.connectLogsStream('restore:x:1')
    jobs.setFollowing(false)

    for (let i = 0; i < 2001; i++) {
      liveStreamHandlers.onMessage({ timestamp: i, hostname: 'h', binary: 'brfs', line: '{}' })
    }
    expect(jobs.logs).toHaveLength(2001)

    jobs.setFollowing(true)

    expect(jobs.logs).toHaveLength(2000)
    expect(jobs.logs[0].timestamp).toBe(1)
  })

  it('evicts a dropped line from the dedup set too, so a re-delivered old line is re-added rather than silently deduped away', async () => {
    apiFetch.mockResolvedValue({ data: [] })
    const jobs = useJobsStore()
    await jobs.connectLogsStream('restore:x:1')

    for (let i = 0; i < 2001; i++) {
      liveStreamHandlers.onMessage({ timestamp: i, hostname: 'h', binary: 'brfs', line: '{}' })
    }
    expect(jobs.logs.some((l) => l.timestamp === 0)).toBe(false) // evicted

    // Pause eviction before re-delivering the evicted line, so this
    // assertion isolates "was the dedup key freed up" from "did the cap
    // immediately re-evict it for being the new oldest line" (both true
    // is the correct steady-state behavior, but would make this specific
    // regression -- _logsSeen never freeing the key -- unobservable).
    jobs.setFollowing(false)
    liveStreamHandlers.onMessage({ timestamp: 0, hostname: 'h', binary: 'brfs', line: '{}' })

    expect(jobs.logs[0].timestamp).toBe(0)
  })
})

describe('loadOlder', () => {
  it('prepends an older page and updates hasOlderLogs from has_more', async () => {
    apiFetch.mockResolvedValueOnce({
      data: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }],
      has_more: true,
    })
    const jobs = useJobsStore()
    await jobs.fetchLogs('restore:x:1')
    expect(jobs.hasOlderLogs).toBe(true)

    apiFetch.mockResolvedValueOnce({
      data: [{ timestamp: 50, hostname: 'h', binary: 'brfs', line: '{}' }],
      has_more: false,
    })
    await jobs.loadOlder('restore:x:1')

    expect(apiFetch).toHaveBeenLastCalledWith('/jobs/restore%3Ax%3A1/logs?limit=500&ending_before=100')
    expect(jobs.logs.map((l) => l.timestamp)).toEqual([50, 100])
    expect(jobs.hasOlderLogs).toBe(false)
  })

  it('does nothing when hasOlderLogs is false', async () => {
    apiFetch.mockResolvedValue({ data: [], has_more: false })
    const jobs = useJobsStore()
    await jobs.fetchLogs('restore:x:1')
    apiFetch.mockClear()

    await jobs.loadOlder('restore:x:1')

    expect(apiFetch).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 3: Run the store tests and confirm the new/updated ones fail**

```bash
cd web && npx vitest run src/stores/jobs.spec.js
```

Expected: the two URL-updated tests FAIL (old code doesn't send `?limit=500`); the new merge/cap/`loadOlder` tests FAIL (`setFollowing`/`loadOlder`/`hasOlderLogs` don't exist yet).

- [ ] **Step 4: Implement the store changes**

Replace the whole of `web/src/stores/jobs.js` with:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'
import { createLiveStream } from '../utils/wsClient'
import { parseLogLine, logKey } from '../utils/logLine'

const OVERLAP_MARGIN_SEC = 2
const RECONCILE_INTERVAL_MS = 60000
const JOB_LOGS_PAGE_SIZE = 500
const JOB_LOGS_LIVE_CAP = 2000

function isFinishLine(line) {
  return parseLogLine(line.line).fields.event === 'finish'
}

// Incoming live lines are almost always newer than everything already held
// (Loki delivers them in emission order), so appending is the common case
// and stays O(1). A line that arrives out of order (rare -- distinct
// hosts' clocks can skew slightly) gets a binary-search insert instead of
// re-sorting the whole array, which is what made the old push+sort
// approach O(n log n) per incoming line over a job's whole lifetime.
function insertSorted(lines, line) {
  const last = lines[lines.length - 1]
  if (!last || line.timestamp >= last.timestamp) {
    lines.push(line)
    return
  }
  let lo = 0
  let hi = lines.length
  while (lo < hi) {
    const mid = (lo + hi) >>> 1
    if (lines[mid].timestamp <= line.timestamp) lo = mid + 1
    else hi = mid
  }
  lines.splice(lo, 0, line)
}

export const useJobsStore = defineStore('jobs', {
  state: () => ({
    list: [],
    loading: false,
    error: null,
    logs: [],
    logsLoading: false,
    logsError: null,
    logsStatus: 'connecting',
    hasOlderLogs: false,
    isFollowing: true,
    _logsStream: null,
    _logsSeen: new Set(),
    _logsReconcileTimer: null,
    listStatus: 'connecting',
    _listStream: null,
    _listReconcileTimer: null,
  }),
  actions: {
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/jobs')
          this.list = body.data
        },
        { rethrow: false }
      )
    },

    async fetchLogs(jobId) {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs?limit=${JOB_LOGS_PAGE_SIZE}`)
          this.logs = body.data ?? []
          this._logsSeen = new Set(this.logs.map(logKey))
          this.hasOlderLogs = body.has_more ?? false
          this.isFollowing = true
          // A job that already finished before this page loaded is the
          // common case, not an edge case -- its finish line arrives here,
          // in history, not as a fresh onMessage over the live stream
          // below, so _mergeLogLine's own isFinishLine check (which only
          // runs for lines not already in _logsSeen) would never see it.
          if (this.logs.some(isFinishLine)) {
            this.logsStatus = 'finished'
          }
        },
        { rethrow: false, loadingKey: 'logsLoading', errorKey: 'logsError' }
      )
    },

    // Pages one older page in, using the oldest resident line's timestamp
    // as the backend's exclusive `ending_before` cursor. A no-op when
    // there's nothing older to fetch (hasOlderLogs false) or nothing
    // resident yet to derive a cursor from.
    async loadOlder(jobId) {
      if (!this.hasOlderLogs || this.logs.length === 0) return
      const oldest = this.logs[0]
      const body = await apiFetch(
        `/jobs/${encodeURIComponent(jobId)}/logs?limit=${JOB_LOGS_PAGE_SIZE}&ending_before=${oldest.timestamp}`
      )
      const older = (body.data ?? []).filter((line) => !this._logsSeen.has(logKey(line)))
      older.forEach((line) => this._logsSeen.add(logKey(line)))
      this.logs.unshift(...older)
      this.hasOlderLogs = body.has_more ?? false
    },

    // Drives the live-follow eviction gate (see _trimLiveCap): the view's
    // useAutoFollow composable calls this as the user scrolls away from /
    // back to the bottom. Returning to following immediately reclaims
    // whatever grew past the cap while not following, instead of waiting
    // for the next live line.
    setFollowing(value) {
      this.isFollowing = value
      if (value) this._trimLiveCap()
    },

    _trimLiveCap() {
      while (this.logs.length > JOB_LOGS_LIVE_CAP) {
        const dropped = this.logs.shift()
        this._logsSeen.delete(logKey(dropped))
      }
    },

    _mergeLogLine(line) {
      const key = logKey(line)
      if (this._logsSeen.has(key)) return
      this._logsSeen.add(key)
      insertSorted(this.logs, line)
      if (this.isFollowing) this._trimLiveCap()
      if (isFinishLine(line)) {
        this.logsStatus = 'finished'
        this.disconnectLogsStream()
      }
    },

    async connectLogsStream(jobId) {
      await this.fetchLogs(jobId)
      const startSec = Math.floor(Date.now() / 1000) - OVERLAP_MARGIN_SEC
      this._logsStream = createLiveStream(`/jobs/${encodeURIComponent(jobId)}/logs/stream?start=${startSec}`, {
        onMessage: (line) => this._mergeLogLine(line),
        onStatus: (status) => {
          if (this.logsStatus !== 'finished') this.logsStatus = status
        },
        onFallback: (intervalMs) => {
          if (this._logsReconcileTimer) clearInterval(this._logsReconcileTimer)
          this._logsReconcileTimer = setInterval(() => this._reconcileLogs(jobId), intervalMs)
        },
      })
      this._logsReconcileTimer = setInterval(() => this._reconcileLogs(jobId), RECONCILE_INTERVAL_MS)
    },

    async _reconcileLogs(jobId) {
      const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs?limit=${JOB_LOGS_PAGE_SIZE}`)
      ;(body.data ?? []).forEach((line) => this._mergeLogLine(line))
    },

    disconnectLogsStream() {
      if (this._logsStream) {
        this._logsStream.close()
        this._logsStream = null
      }
      if (this._logsReconcileTimer) {
        clearInterval(this._logsReconcileTimer)
        this._logsReconcileTimer = null
      }
    },

    _mergeJobsSnapshot(jobs) {
      this.list = jobs
    },

    _mergeJobUpsert(job) {
      const idx = this.list.findIndex((j) => j.job_id === job.job_id)
      if (idx === -1) this.list.push(job)
      else this.list[idx] = job
    },

    connectJobsStream() {
      this._listStream = createLiveStream('/jobs/stream', {
        onMessage: (msg) => {
          if (msg.type === 'snapshot') this._mergeJobsSnapshot(msg.jobs ?? [])
          else if (msg.type === 'upsert' && msg.job) this._mergeJobUpsert(msg.job)
        },
        onStatus: (status) => {
          this.listStatus = status
        },
        onFallback: (intervalMs) => {
          if (this._listReconcileTimer) clearInterval(this._listReconcileTimer)
          this._listReconcileTimer = setInterval(() => this.fetchAll(), intervalMs)
        },
      })
      this._listReconcileTimer = setInterval(() => this.fetchAll(), RECONCILE_INTERVAL_MS)
    },

    disconnectJobsStream() {
      if (this._listStream) {
        this._listStream.close()
        this._listStream = null
      }
      if (this._listReconcileTimer) {
        clearInterval(this._listReconcileTimer)
        this._listReconcileTimer = null
      }
    },
  },
})
```

(Note: `logKey` is no longer defined locally — it's imported from Task 2's `utils/logLine.js`.)

- [ ] **Step 5: Run the store tests and confirm they pass**

```bash
cd web && npx vitest run src/stores/jobs.spec.js
```

Expected: PASS, all tests including the ones from Task 2's untouched suite.

- [ ] **Step 6: Run the full frontend suite**

```bash
cd web && npm run test
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add web/src/stores/jobs.js web/src/stores/jobs.spec.js
git commit -m "perf(web): O(1) log merge, follow-gated eviction cap, loadOlder pagination"
```

---

### Task 4: Frontend — `useAutoFollow` composable

**Files:**
- Create: `web/src/composables/useAutoFollow.js`
- Create: `web/src/composables/useAutoFollow.spec.js`

**Interfaces:**
- Produces: `useAutoFollow(itemCount: Ref<number> | (() => number)): { sentinel: Ref<HTMLElement|null>, isFollowing: Ref<boolean>, newLineCount: Ref<number>, scrollToBottom: () => void }`. Task 5 (view) binds `sentinel` via a template `ref`, watches `isFollowing` to call the store's `setFollowing`, and renders `newLineCount`/`scrollToBottom` for the "jump to latest" affordance.

- [ ] **Step 1: Write the failing test**

Create `web/src/composables/useAutoFollow.spec.js`:

```js
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { defineComponent, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { useAutoFollow } from './useAutoFollow'

let observedCallback

class MockIntersectionObserver {
  constructor(callback) {
    observedCallback = callback
  }
  observe() {}
  disconnect() {}
}

beforeEach(() => {
  observedCallback = null
  vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
})

function mountHarness(initialCount) {
  const TestComponent = defineComponent({
    props: { count: { type: Number, required: true } },
    setup(props) {
      return { ...useAutoFollow(() => props.count) }
    },
    template: '<div><div ref="sentinel" /></div>',
  })
  return mount(TestComponent, { props: { count: initialCount } })
}

describe('useAutoFollow', () => {
  it('starts following and flips to not-following when the sentinel leaves view', async () => {
    const wrapper = mountHarness(0)
    await nextTick()
    expect(wrapper.vm.isFollowing).toBe(true)

    observedCallback([{ isIntersecting: false }])
    await nextTick()

    expect(wrapper.vm.isFollowing).toBe(false)
  })

  it('counts new lines that arrive while not following, and clears the count on returning to the bottom', async () => {
    const wrapper = mountHarness(5)
    await nextTick()
    observedCallback([{ isIntersecting: false }])
    await nextTick()

    await wrapper.setProps({ count: 8 })
    expect(wrapper.vm.newLineCount).toBe(3)

    observedCallback([{ isIntersecting: true }])
    await nextTick()
    expect(wrapper.vm.newLineCount).toBe(0)
  })

  it('scrollToBottom scrolls the sentinel element into view', async () => {
    const wrapper = mountHarness(0)
    await nextTick()
    const scrollIntoView = vi.fn()
    wrapper.vm.sentinel.scrollIntoView = scrollIntoView

    wrapper.vm.scrollToBottom()

    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'end' })
  })
})
```

- [ ] **Step 2: Run it and confirm it fails**

```bash
cd web && npx vitest run src/composables/useAutoFollow.spec.js
```

Expected: FAIL — the module doesn't exist yet.

- [ ] **Step 3: Implement `useAutoFollow`**

Create `web/src/composables/useAutoFollow.js`:

```js
import { ref, watch, onBeforeUnmount } from 'vue'

// Tracks whether the viewport is scrolled down to a bottom sentinel element
// via IntersectionObserver, rather than polling scroll events. Consumers
// (JobDetailView.vue) use `isFollowing` both to gate the jobs store's
// live-log eviction cap (only trim resident lines while the tail is
// actually being watched) and to decide whether to auto-scroll new lines
// into view or surface a "N new lines" affordance instead of yanking a
// deliberately-scrolled-up reader back down.
export function useAutoFollow(itemCount) {
  const sentinel = ref(null)
  const isFollowing = ref(true)
  const newLineCount = ref(0)
  let observer = null

  function attach(el) {
    observer?.disconnect()
    observer = null
    if (!el) return
    observer = new IntersectionObserver(
      ([entry]) => {
        isFollowing.value = entry.isIntersecting
        if (entry.isIntersecting) newLineCount.value = 0
      },
      { threshold: 1.0 }
    )
    observer.observe(el)
  }

  watch(sentinel, attach)

  watch(itemCount, (next, prev) => {
    if (!isFollowing.value && next > prev) {
      newLineCount.value += next - prev
    }
  })

  function scrollToBottom() {
    sentinel.value?.scrollIntoView({ block: 'end' })
  }

  onBeforeUnmount(() => observer?.disconnect())

  return { sentinel, isFollowing, newLineCount, scrollToBottom }
}
```

- [ ] **Step 4: Run the test and confirm it passes**

```bash
cd web && npx vitest run src/composables/useAutoFollow.spec.js
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/composables/useAutoFollow.js web/src/composables/useAutoFollow.spec.js
git commit -m "feat(web): add useAutoFollow composable for bottom-of-log detection"
```

---

### Task 5: Frontend view — wire pagination/eviction into `JobDetailView.vue`

**Files:**
- Modify: `web/src/views/JobDetailView.vue`
- Modify: `web/src/views/JobDetailView.spec.js`

**Interfaces:**
- Consumes: `jobs.hasOlderLogs`, `jobs.loadOlder(jobId)`, `jobs.setFollowing(value)` (Task 3); `useAutoFollow` (Task 4); `logKey` (Task 2).

- [ ] **Step 1: Write the failing tests**

Add to `web/src/views/JobDetailView.spec.js`. First, replace the top import lines with:

```js
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { nextTick } from 'vue'
import { mount, RouterLinkStub } from '@vue/test-utils'
```

(`beforeEach` and `nextTick` are new; the `@vue/test-utils` line is unchanged from today, shown here only so the three import lines are easy to place in order at the top of the file.)

Then add these tests inside the `describe('JobDetailView', ...)` block:

```js
  it('shows a "Load older" button when hasOlderLogs is true, and calls loadOlder on click', async () => {
    const { wrapper, jobs } = mountView({ logs: [], logsLoading: false, logsError: null, hasOlderLogs: true })
    const button = wrapper.find('[data-test="load-older"]')
    expect(button.exists()).toBe(true)

    await button.trigger('click')

    expect(jobs.loadOlder).toHaveBeenCalledWith('backup:nightly:1752400000')
  })

  it('hides the "Load older" button when hasOlderLogs is false', () => {
    const { wrapper } = mountView({ logs: [], logsLoading: false, logsError: null, hasOlderLogs: false })
    expect(wrapper.find('[data-test="load-older"]').exists()).toBe(false)
  })

  it("keeps each LogLine's expanded state attached to its own line, not its array position, when older lines are prepended", async () => {
    const { wrapper, jobs } = mountView({
      logs: [
        { timestamp: 200, hostname: 'h', binary: 'brfs', line: JSON.stringify({ level: 'INFO', msg: 'second', extra: 'x' }) },
      ],
      logsLoading: false,
      logsError: null,
    })

    await wrapper.find('[data-test="log-line-summary"]').trigger('click')

    jobs.logs.unshift({
      timestamp: 100,
      hostname: 'h',
      binary: 'brfs',
      line: JSON.stringify({ level: 'INFO', msg: 'first', extra: 'y' }),
    })
    await nextTick()

    const items = wrapper.findAll('li')
    expect(items[0].text()).toContain('first')
    expect(items[0].find('[data-test="log-line-fields"]').exists()).toBe(false)
    expect(items[1].text()).toContain('second')
    expect(items[1].find('[data-test="log-line-fields"]').exists()).toBe(true)
  })

  describe('follow / jump-to-latest wiring', () => {
    let observedCallback

    beforeEach(() => {
      class MockIntersectionObserver {
        constructor(cb) {
          observedCallback = cb
        }
        observe() {}
        disconnect() {}
      }
      vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
    })

    it('calls setFollowing on the store when the sentinel leaves view, and shows a jump-to-latest button with a new-line count', async () => {
      const { wrapper, jobs } = mountView({
        logs: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }],
        logsLoading: false,
        logsError: null,
      })
      await nextTick()

      observedCallback([{ isIntersecting: false }])
      await nextTick()
      expect(jobs.setFollowing).toHaveBeenCalledWith(false)

      jobs.logs.push({ timestamp: 200, hostname: 'h', binary: 'brfs', line: '{}' })
      await nextTick()

      const jumpButton = wrapper.find('[data-test="jump-to-latest"]')
      expect(jumpButton.exists()).toBe(true)
      expect(jumpButton.text()).toContain('1 new line')
    })
  })
```

- [ ] **Step 2: Run the view tests and confirm the new ones fail**

```bash
cd web && npx vitest run src/views/JobDetailView.spec.js
```

Expected: FAIL — no `[data-test="load-older"]`/`[data-test="jump-to-latest"]` elements exist yet, and the index-key test fails because the current `:key="index"` implementation lets the expanded state stay on index 0 (now "first") instead of following "second".

- [ ] **Step 3: Implement the view changes**

Replace `web/src/views/JobDetailView.vue` with:

```vue
<script setup>
import { onMounted, onUnmounted, computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useJobsStore } from '../stores/jobs'
import { useAutoFollow } from '../composables/useAutoFollow'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import ConnectionStatus from '../components/ui/ConnectionStatus.vue'
import BaseButton from '../components/ui/BaseButton.vue'
import LogLine from '../components/LogLine.vue'
import { logKey } from '../utils/logLine'

const route = useRoute()
const jobs = useJobsStore()
const jobId = computed(() => route.params.job_id)

const logsCount = computed(() => jobs.logs.length)
const { sentinel, isFollowing, newLineCount, scrollToBottom } = useAutoFollow(logsCount)

watch(isFollowing, (value) => jobs.setFollowing(value), { immediate: true })

onMounted(async () => {
  await jobs.connectLogsStream(jobId.value)
})

onUnmounted(() => {
  jobs.disconnectLogsStream()
})

function loadOlder() {
  jobs.loadOlder(jobId.value)
}
</script>

<template>
  <div>
    <PageHeader :title="jobId" :crumbs="[{ label: 'Jobs', to: { name: 'jobs' } }, { label: jobId }]">
      <template #actions>
        <ConnectionStatus :status="jobs.logsStatus" />
      </template>
    </PageHeader>
    <StatusMessage
      :loading="jobs.logsLoading"
      :error="jobs.logsError"
      :empty="jobs.logs.length === 0"
      empty-text="No log lines found for this job in the last 24h."
    >
      <BaseButton v-if="jobs.hasOlderLogs" data-test="load-older" class="mb-2" @click="loadOlder">
        Load older lines
      </BaseButton>
      <ul>
        <LogLine v-for="line in jobs.logs" :key="logKey(line)" :line="line" />
      </ul>
      <div ref="sentinel" data-test="scroll-sentinel"></div>
    </StatusMessage>
    <button
      v-if="!isFollowing && newLineCount > 0"
      type="button"
      data-test="jump-to-latest"
      class="fixed bottom-6 right-6 rounded-full bg-blue-600 text-white px-4 py-2 text-sm shadow-lg hover:bg-blue-700"
      @click="scrollToBottom"
    >
      {{ newLineCount }} new line{{ newLineCount === 1 ? '' : 's' }} — jump to latest
    </button>
  </div>
</template>
```

- [ ] **Step 4: Run the view tests and confirm they pass**

```bash
cd web && npx vitest run src/views/JobDetailView.spec.js
```

Expected: PASS, including the pre-existing tests (heading, breadcrumb, empty state, error state, `connectLogsStream` call) — none of their assertions touch the new elements.

- [ ] **Step 5: Run the full frontend suite**

```bash
cd web && npm run test
```

Expected: PASS.

- [ ] **Step 6: Manual smoke check**

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run dev
```

Against a running demo lab (`make demo-up` from the repo root in another terminal), open `/jobs/:job_id` for a real job and confirm: log lines still render correctly, no console errors, and (if `hasOlderLogs` is true for a job with more than 500 lines of history) the "Load older lines" button appears and successfully prepends older lines on click.

- [ ] **Step 7: Commit**

```bash
git add web/src/views/JobDetailView.vue web/src/views/JobDetailView.spec.js
git commit -m "feat(web): wire log pagination, eviction-safe keys, and jump-to-latest into JobDetailView"
```

---

### Task 6: Documentation & changelog

**Files:**
- Modify: `docs/api/rest-v1.md` (`GET /api/v1/jobs/{job_id}/logs` section, lines 490-508)
- Modify: `docs/components/web.md` (`/jobs/:job_id` bullet, lines ~117-136, and the "See Also" list, lines ~187-198)
- Modify: `CHANGELOG.md` (new entry at the top)

**Interfaces:** None — documentation only, no code interfaces.

- [ ] **Step 1: Update `docs/api/rest-v1.md`**

Replace the `## GET /api/v1/jobs/{job_id}/logs` section (from `## GET /api/v1/jobs/{job_id}/logs` through the line `A client polling with an advancing since cursor gets a near-real-time tail.`) with:

```markdown
## `GET /api/v1/jobs/{job_id}/logs`

| Param | Type | Description |
|-------|------|--------------|
| `since` | unix seconds | Only lines after this timestamp. Default: 24h before now |
| `limit` | int | Page size. Default and max: 500. `400` if outside `[1, 500]` |
| `ending_before` | unix nanoseconds | Opaque cursor — the timestamp of the oldest line already loaded. Returns the `limit` lines immediately before it (exclusive). Omit for the most recent page |
| `source_host` / `store_host` | string | Optional — narrows the query to the hosts involved, if already known from a prior `/jobs` response. Each must match `^[a-zA-Z0-9.-]+$` — `400` on invalid characters |

`job_id` must match `^[a-zA-Z0-9:._-]+$` — `400` otherwise.

Without `ending_before`, returns the most recent `limit` lines in the `since`-to-now window. With
`ending_before`, returns the `limit` lines immediately before that cursor, still floored at `since` —
paging can't run past the window. An `ending_before` at or before the window floor returns an empty
page (`has_more: false`), not an error.

```json
{
  "data": [
    {"timestamp": 1752400000123456789, "hostname": "database", "binary": "brfs", "line": "{...raw json log line...}"}
  ],
  "has_more": true
}
```

A client polling with an advancing `since` cursor gets a near-real-time tail; a client paging
backward with `ending_before` gets progressively older history. See
[Design: Job Log Pagination & Bounded Retention](../superpowers/specs/2026-08-22-job-log-pagination-design.md).
```

- [ ] **Step 2: Update `docs/components/web.md`**

In the `/jobs/:job_id` bullet, immediately after the paragraph ending "...a stalled page is never left looking up to date. See [Design: Live Job & Log Updates](../superpowers/specs/2026-08-17-live-job-updates-design.md).", add a new paragraph:

```markdown

  `/jobs/:job_id`'s log view now caps itself at 2000 resident lines while the user is following the
  live tail (auto-scrolled to the bottom); scrolling away from the bottom pauses that cap so
  history being read isn't evicted out from under the reader, and returning to the bottom resumes
  it. A "Load older lines" button (visible whenever the backend reports more history exists) pages
  further history in via `GET /jobs/{job_id}/logs`'s new `ending_before` cursor; a "N new lines —
  jump to latest" button appears instead of auto-scrolling once the user has scrolled away from the
  bottom. See
  [Design: Job Log Pagination & Bounded Retention](../superpowers/specs/2026-08-22-job-log-pagination-design.md).
```

In the "See Also" list at the bottom of the file, add a line after the `Live Job & Log Updates` entry:

```markdown
- [Design: Job Log Pagination & Bounded Retention](../superpowers/specs/2026-08-22-job-log-pagination-design.md)
```

- [ ] **Step 3: Add the `CHANGELOG.md` entry**

At the top of `CHANGELOG.md`, immediately after the `All notable changes...` line and before the existing `## 2026-08-22 — Fix live job-log tail connection leak` entry, insert:

```markdown
## 2026-08-22 — Bound job log viewer memory and DOM growth

`web`'s job log viewer (`/jobs/:job_id`) had no cap on retained log lines and re-sorted its entire
in-memory log array on every single incoming WebSocket line — for a long-running or verbose job,
both memory and per-line merge cost grew without bound. `GET /api/v1/jobs/{job_id}/logs` now
supports `limit`/`ending_before` cursor pagination (closing a related silent-truncation gap: the
endpoint previously returned up to a fixed 5000-line Loki cap with no indication when more existed).
The frontend caps resident log lines at 2000 while following the live tail, evicting from the oldest
end only while the user is actually watching the bottom of the log — history a user has scrolled
back to view is never evicted out from under them — and a new "Load older lines" affordance pages
further history in on demand. See
[Design: Job Log Pagination & Bounded Retention](docs/superpowers/specs/2026-08-22-job-log-pagination-design.md).

```

- [ ] **Step 4: Commit**

```bash
git add docs/api/rest-v1.md docs/components/web.md CHANGELOG.md
git commit -m "docs: document job log pagination and update changelog"
```

---

## Self-Review Notes

- **Spec coverage:** Backend pagination (Task 1) ✓, cheap merge + follow-gated cap + `_logsSeen` lockstep eviction (Task 3) ✓, `loadOlder` (Task 3) ✓, initial-fetch/reconcile switching to latest-page (Task 3, `fetchLogs`/`_reconcileLogs`) ✓, `logKey`-based keying fix (Task 5, with a regression test proving the index-key bug) ✓, `useAutoFollow` composable (Task 4) ✓, no virtualization library added (no task introduces one) ✓, documentation impact (Task 6) ✓.
- **Placeholder scan:** none found — every step has real code/commands.
- **Type consistency:** `logKey(line)` signature matches across Task 2 (definition), Task 3 (`jobs.js` import/use), and Task 5 (`JobDetailView.vue` import/use). `jobs.loadOlder(jobId)`, `jobs.setFollowing(value)`, `jobs.hasOlderLogs` are defined in Task 3 and consumed with matching names/arity in Task 5. `useAutoFollow(itemCount)` return shape (`sentinel`, `isFollowing`, `newLineCount`, `scrollToBottom`) matches between Task 4's definition and Task 5's destructuring.
