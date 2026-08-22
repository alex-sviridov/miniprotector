import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useJobsStore } from './jobs'
import { apiFetch } from '../api/client'
import { createLiveStream } from '../utils/wsClient'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

vi.mock('../utils/wsClient', () => ({
  createLiveStream: vi.fn(),
}))

describe('jobs store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('fetchAll populates the list from the API', async () => {
    apiFetch.mockResolvedValue({ data: [{ job_id: 'backup:x:1' }], truncated: false })
    const jobs = useJobsStore()

    await jobs.fetchAll()

    expect(apiFetch).toHaveBeenCalledWith('/jobs')
    expect(jobs.list).toEqual([{ job_id: 'backup:x:1' }])
    expect(jobs.loading).toBe(false)
    expect(jobs.error).toBeNull()
  })

  it('fetchAll records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const jobs = useJobsStore()

    await jobs.fetchAll()

    expect(jobs.error).toBe('boom')
    expect(jobs.list).toEqual([])
  })

  it('fetchLogs populates logs from the API', async () => {
    apiFetch.mockResolvedValue({
      data: [{ timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{}' }],
    })
    const jobs = useJobsStore()

    await jobs.fetchLogs('backup:nightly:1752400000')

    expect(apiFetch).toHaveBeenCalledWith('/jobs/backup%3Anightly%3A1752400000/logs?limit=500')
    expect(jobs.logs).toEqual([
      { timestamp: 1752400000123456789, hostname: 'database', binary: 'brfs', line: '{}' },
    ])
    expect(jobs.logsLoading).toBe(false)
    expect(jobs.logsError).toBeNull()
  })

  it('fetchLogs treats a null data field as an empty list', async () => {
    apiFetch.mockResolvedValue({ data: null })
    const jobs = useJobsStore()

    await jobs.fetchLogs('backup:nightly:1752400000')

    expect(jobs.logs).toEqual([])
    expect(jobs.logsError).toBeNull()
  })

  it('fetchLogs records an error message on failure', async () => {
    apiFetch.mockRejectedValue(new Error('boom'))
    const jobs = useJobsStore()

    await jobs.fetchLogs('backup:nightly:1752400000')

    expect(jobs.logsError).toBe('boom')
    expect(jobs.logs).toEqual([])
  })

  describe('connectLogsStream', () => {
    let liveStreamHandlers

    beforeEach(() => {
      createLiveStream.mockReset()
      createLiveStream.mockImplementation((path, handlers) => {
        liveStreamHandlers = handlers
        return { close: vi.fn() }
      })
    })

    it('fetches history first, then opens a live stream from a cursor near "now"', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const jobs = useJobsStore()

      await jobs.connectLogsStream('restore:x:1')

      expect(apiFetch).toHaveBeenCalledWith('/jobs/restore%3Ax%3A1/logs?limit=500')
      expect(createLiveStream).toHaveBeenCalledWith(
        expect.stringMatching(/^\/jobs\/restore%3Ax%3A1\/logs\/stream\?start=\d+$/),
        expect.objectContaining({ onMessage: expect.any(Function), onStatus: expect.any(Function), onFallback: expect.any(Function) })
      )
    })

    it('merges a new line delivered over the stream, deduping by timestamp/hostname/binary', async () => {
      apiFetch.mockResolvedValue({
        data: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }],
      })
      const jobs = useJobsStore()
      await jobs.connectLogsStream('restore:x:1')

      liveStreamHandlers.onMessage({ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }) // duplicate of history
      liveStreamHandlers.onMessage({ timestamp: 200, hostname: 'h', binary: 'brfs', line: '{"msg":"new"}' })

      expect(jobs.logs).toHaveLength(2)
      expect(jobs.logs.map((l) => l.timestamp)).toEqual([100, 200])
    })

    it('flips status to "finished" and closes the stream on an event=finish line', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const jobs = useJobsStore()
      await jobs.connectLogsStream('restore:x:1')

      liveStreamHandlers.onMessage({ timestamp: 100, hostname: 'h', binary: 'agent', line: '{"event":"finish","status":"success"}' })

      expect(jobs.logsStatus).toBe('finished')
    })

    it('flips status to "finished" from history alone, when the finish line was already fetched before the stream opened', async () => {
      // A job that already finished before its detail page loaded is the
      // common case, not an edge case (see jobs.js's fetchLogs comment) --
      // the finish line arrives in this initial history fetch, not as a
      // fresh onMessage over the live stream, so logsStatus must be derived
      // from history too, not only from _mergeLogLine's onMessage path.
      apiFetch.mockResolvedValue({
        data: [{ timestamp: 100, hostname: 'h', binary: 'agent', line: '{"event":"finish","status":"success"}' }],
      })
      const jobs = useJobsStore()

      await jobs.connectLogsStream('restore:x:1')

      expect(jobs.logsStatus).toBe('finished')
    })

    it('onStatus updates logsStatus, except once finished it stays finished', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const jobs = useJobsStore()
      await jobs.connectLogsStream('restore:x:1')

      liveStreamHandlers.onStatus('live')
      expect(jobs.logsStatus).toBe('live')

      jobs.logsStatus = 'finished'
      liveStreamHandlers.onStatus('reconnecting')
      expect(jobs.logsStatus).toBe('finished')
    })

    it('disconnectLogsStream closes the stream and clears reconciliation timers', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const jobs = useJobsStore()
      await jobs.connectLogsStream('restore:x:1')
      const closeSpy = createLiveStream.mock.results[0].value.close

      jobs.disconnectLogsStream()

      expect(closeSpy).toHaveBeenCalled()
    })
  })

  describe('connectJobsStream', () => {
    let liveStreamHandlers

    beforeEach(() => {
      createLiveStream.mockReset()
      createLiveStream.mockImplementation((path, handlers) => {
        liveStreamHandlers = handlers
        return { close: vi.fn() }
      })
    })

    it('opens a stream at /jobs/stream', () => {
      const jobs = useJobsStore()
      jobs.connectJobsStream()
      expect(createLiveStream).toHaveBeenCalledWith(
        '/jobs/stream',
        expect.objectContaining({ onMessage: expect.any(Function), onStatus: expect.any(Function), onFallback: expect.any(Function) })
      )
    })

    it('a "snapshot" message replaces the whole list', () => {
      const jobs = useJobsStore()
      jobs.connectJobsStream()
      liveStreamHandlers.onMessage({ type: 'snapshot', jobs: [{ job_id: 'a' }, { job_id: 'b' }] })
      expect(jobs.list).toEqual([{ job_id: 'a' }, { job_id: 'b' }])
    })

    it('an "upsert" message updates an existing job in place', () => {
      const jobs = useJobsStore()
      jobs.list = [{ job_id: 'a', state: 'in_progress' }]
      jobs.connectJobsStream()
      liveStreamHandlers.onMessage({ type: 'upsert', job: { job_id: 'a', state: 'success' } })
      expect(jobs.list).toEqual([{ job_id: 'a', state: 'success' }])
    })

    it('an "upsert" message for an unseen job_id appends it', () => {
      const jobs = useJobsStore()
      jobs.list = [{ job_id: 'a' }]
      jobs.connectJobsStream()
      liveStreamHandlers.onMessage({ type: 'upsert', job: { job_id: 'b' } })
      expect(jobs.list).toEqual([{ job_id: 'a' }, { job_id: 'b' }])
    })

    it('disconnectJobsStream closes the stream and clears the reconciliation timer', () => {
      const jobs = useJobsStore()
      jobs.connectJobsStream()
      const closeSpy = createLiveStream.mock.results[0].value.close

      jobs.disconnectJobsStream()

      expect(closeSpy).toHaveBeenCalled()
    })
  })

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
})
