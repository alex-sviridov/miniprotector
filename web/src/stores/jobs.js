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
    logsOlderLoading: false,
    logsOlderError: null,
    isFollowing: true,
    // Monotonic count of lines merged via the live/tail path only. The
    // view's useAutoFollow watches this rather than logs.length so that
    // paging older history in (loadOlder unshifts up to a page of lines)
    // is never mistaken for new tail activity.
    tailSeq: 0,
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
          // Singleton store: without this reset a previous job's tail
          // activity would carry over and the view would open showing a
          // spurious "N new lines" affordance.
          this.tailSeq = 0
          this.logsOlderError = null
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
    // there's nothing older to fetch (hasOlderLogs false), nothing
    // resident yet to derive a cursor from, or a page is already in
    // flight (a double-click would otherwise issue two requests from the
    // same cursor and race their prepends and hasOlderLogs writes).
    //
    // Failures land in logsOlderLoading/logsOlderError, deliberately
    // separate from logsLoading/logsError: those drive StatusMessage over
    // the whole log list, so reusing them would replace logs the user can
    // already read with a spinner or an error screen. A "load older"
    // failure belongs inline next to its button instead.
    //
    // Deliberately does NOT touch tailSeq -- prepended history is not new
    // tail activity, and counting it would make the view offer to scroll
    // the user away from the history they just asked for.
    async loadOlder(jobId) {
      if (!this.hasOlderLogs || this.logs.length === 0 || this.logsOlderLoading) return
      const oldest = this.logs[0]
      await withRequest(
        this,
        async () => {
          const body = await apiFetch(
            `/jobs/${encodeURIComponent(jobId)}/logs?limit=${JOB_LOGS_PAGE_SIZE}&ending_before=${oldest.timestamp}`
          )
          const older = (body.data ?? []).filter((line) => !this._logsSeen.has(logKey(line)))
          older.forEach((line) => this._logsSeen.add(logKey(line)))
          this.logs.unshift(...older)
          this.hasOlderLogs = body.has_more ?? false
        },
        { rethrow: false, loadingKey: 'logsOlderLoading', errorKey: 'logsOlderError' }
      )
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
      this.tailSeq++
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
