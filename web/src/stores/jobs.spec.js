import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useJobsStore } from './jobs'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
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

    expect(apiFetch).toHaveBeenCalledWith('/jobs/backup%3Anightly%3A1752400000/logs')
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
})
