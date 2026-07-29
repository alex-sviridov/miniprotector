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

  it('unwraps a Vector envelope line whose message field holds the real slog JSON', () => {
    const inner = {
      time: '2026-07-27T20:19:32.745Z',
      level: 'INFO',
      msg: 'New backup stream connected',
      app: 'bwfs',
      pid: 54,
      job_id: 'backup:hosts:etc-hosts:aa0c9314:1785183572',
    }
    const raw = JSON.stringify({
      binary: 'bwfs',
      event: null,
      file: '/var/log/miniprotector/bwfs.log',
      host: 'f3e818dd00c5',
      job_id: 'backup:hosts:etc-hosts:aa0c9314:1785183572',
      message: JSON.stringify(inner),
      source_type: 'file',
      status: null,
    })

    const result = parseLogLine(raw)

    expect(result.ok).toBe(true)
    expect(result.level).toBe('INFO')
    expect(result.message).toBe('New backup stream connected')
    expect(result.fields).toEqual({
      time: '2026-07-27T20:19:32.745Z',
      app: 'bwfs',
      pid: 54,
      job_id: 'backup:hosts:etc-hosts:aa0c9314:1785183572',
    })
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
