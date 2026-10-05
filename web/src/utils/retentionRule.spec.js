import { describe, it, expect } from 'vitest'
import {
  SECONDS_PER_DAY,
  normalizePath,
  validateRetentionForm,
  toPayload,
  toFormShape,
  formatKeep,
  summarizeTarget,
} from './retentionRule'

function validForm(overrides = {}) {
  return {
    name: 'keep-logs',
    client_filters: { hostnames: ['web-*'], labels: [{ key: 'env', value: 'prod' }] },
    backup_type: 'filesystem',
    path: '/var/log',
    include: ['*.log'],
    keepForever: false,
    keepDays: '30',
    ...overrides,
  }
}

describe('normalizePath', () => {
  it('trims, collapses repeated slashes and strips a trailing slash', () => {
    expect(normalizePath('  /var//log/ ')).toBe('/var/log')
    expect(normalizePath('/')).toBe('/')
    expect(normalizePath('///')).toBe('/')
    expect(normalizePath('/a/b')).toBe('/a/b')
  })
})

describe('validateRetentionForm', () => {
  it('accepts a valid form', () => {
    expect(validateRetentionForm(validForm())).toEqual({})
  })

  it('requires a name', () => {
    expect(validateRetentionForm(validForm({ name: '   ' })).name).toBeTruthy()
  })

  it('requires an absolute path without ..', () => {
    expect(validateRetentionForm(validForm({ path: '' })).path).toBeTruthy()
    expect(validateRetentionForm(validForm({ path: 'var/log' })).path).toMatch(/absolute/i)
    expect(validateRetentionForm(validForm({ path: '/var/../etc' })).path).toMatch(/\.\./)
    expect(validateRetentionForm(validForm({ path: '/var/log/' }))).toEqual({})
  })

  it('rejects include globs that are empty, contain a slash, or are malformed', () => {
    expect(validateRetentionForm(validForm({ include: [''] })).include).toBeTruthy()
    expect(validateRetentionForm(validForm({ include: ['sub/*.log'] })).include).toMatch(/\//)
    expect(validateRetentionForm(validForm({ include: ['[abc'] })).include).toBeTruthy()
    expect(validateRetentionForm(validForm({ include: [] }))).toEqual({})
  })

  it('requires a whole number of days of at least 1 unless keeping forever', () => {
    for (const bad of ['', '0', '-3', '1.5', 'abc']) {
      expect(validateRetentionForm(validForm({ keepDays: bad })).keepDays, bad).toBeTruthy()
    }
    expect(validateRetentionForm(validForm({ keepDays: '1' }))).toEqual({})
    expect(validateRetentionForm(validForm({ keepForever: true, keepDays: '' }))).toEqual({})
  })
})

describe('toPayload', () => {
  it('builds the API body with trimmed values, normalized path and days converted to seconds', () => {
    expect(
      toPayload(
        validForm({
          name: ' keep-logs ',
          path: '/var//log/',
          include: [' *.log ', ''],
          client_filters: { hostnames: [' web-* ', ''], labels: [{ key: ' env ', value: ' prod ' }, { key: '', value: 'x' }] },
        })
      )
    ).toEqual({
      name: 'keep-logs',
      client_filters: { hostnames: ['web-*'], labels: { env: 'prod' } },
      retention: { backup_type: 'filesystem', path: '/var/log', include: ['*.log'], keep_seconds: 30 * SECONDS_PER_DAY },
    })
  })

  it('sends keep_seconds 0 when keeping forever', () => {
    expect(toPayload(validForm({ keepForever: true })).retention.keep_seconds).toBe(0)
  })
})

describe('toFormShape', () => {
  it('returns sensible defaults for a new rule', () => {
    expect(toFormShape(null)).toEqual({
      name: '',
      client_filters: { hostnames: [], labels: [] },
      backup_type: 'filesystem',
      path: '',
      include: [],
      keepForever: false,
      keepDays: '30',
    })
  })

  it('round-trips an existing policy', () => {
    const policy = {
      name: 'keep-logs',
      client_filters: { hostnames: ['web-*'], labels: { env: 'prod' } },
      retention: { backup_type: 'filesystem', path: '/var/log', include: ['*.log'], keep_seconds: 7 * SECONDS_PER_DAY, priority: 2 },
    }
    const form = toFormShape(policy)
    expect(form.keepForever).toBe(false)
    expect(form.keepDays).toBe('7')
    expect(form.client_filters.labels).toEqual([{ key: 'env', value: 'prod' }])
    expect(toPayload(form).retention).toEqual({ backup_type: 'filesystem', path: '/var/log', include: ['*.log'], keep_seconds: 7 * SECONDS_PER_DAY })
  })

  it('maps keep_seconds 0 to keep forever', () => {
    const form = toFormShape({ name: 'n', retention: { backup_type: 'filesystem', path: '/', keep_seconds: 0 } })
    expect(form.keepForever).toBe(true)
  })

  it('rounds a non-whole-day keep up to whole days so saving never shortens it', () => {
    const form = toFormShape({ name: 'n', retention: { backup_type: 'filesystem', path: '/', keep_seconds: SECONDS_PER_DAY + 1 } })
    expect(form.keepDays).toBe('2')
  })
})

describe('formatKeep', () => {
  it('formats forever, days, and sub-day values', () => {
    expect(formatKeep(0)).toBe('Forever')
    expect(formatKeep(SECONDS_PER_DAY)).toBe('1 day')
    expect(formatKeep(7 * SECONDS_PER_DAY)).toBe('7 days')
    expect(formatKeep(3600)).toBe('1 hour')
    expect(formatKeep(2 * 3600)).toBe('2 hours')
    expect(formatKeep(90)).toBe('90 seconds')
  })
})

describe('summarizeTarget', () => {
  it('summarizes hostnames and labels, or all clients when unrestricted', () => {
    expect(summarizeTarget({ hostnames: ['web-*', 'db-1'], labels: { env: 'prod' } })).toBe('web-*, db-1 · env=prod')
    expect(summarizeTarget({ hostnames: [], labels: {} })).toBe('All clients')
    expect(summarizeTarget(undefined)).toBe('All clients')
    expect(summarizeTarget({ hostnames: [], labels: { env: 'prod' } })).toBe('env=prod')
  })
})
