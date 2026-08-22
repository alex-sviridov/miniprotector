import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'
import { createLiveStream } from '../utils/wsClient'
import { parseLogLine } from '../utils/logLine'

const OVERLAP_MARGIN_SEC = 2
const RECONCILE_INTERVAL_MS = 60000

function logKey(line) {
  return `${line.timestamp}|${line.hostname}|${line.binary}`
}

function isFinishLine(line) {
  return parseLogLine(line.line).fields.event === 'finish'
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
          const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs`)
          this.logs = body.data ?? []
          this._logsSeen = new Set(this.logs.map(logKey))
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

    _mergeLogLine(line) {
      const key = logKey(line)
      if (this._logsSeen.has(key)) return
      this._logsSeen.add(key)
      this.logs.push(line)
      this.logs.sort((a, b) => a.timestamp - b.timestamp)
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
      const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs`)
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
