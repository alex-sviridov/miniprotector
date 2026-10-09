import { describe, it, expect } from 'vitest'
import { bootstrapCertState } from './bootstrapCert'

const NOW = 1_800_000_000
const day = 86400

describe('bootstrapCertState', () => {
  it('is unknown when no expiry has been reported', () => {
    expect(bootstrapCertState(0, NOW)).toEqual({ state: 'unknown', variant: 'neutral', label: '—' })
    expect(bootstrapCertState(undefined, NOW).state).toBe('unknown')
  })

  it('is ok with plenty of time left', () => {
    expect(bootstrapCertState(NOW + 89 * day + 100, NOW)).toEqual({ state: 'ok', variant: 'ok', label: '89d left' })
    expect(bootstrapCertState(NOW + 30 * day, NOW).state).toBe('ok')
  })

  it('warns under 30 days', () => {
    expect(bootstrapCertState(NOW + 29 * day + 100, NOW)).toEqual({ state: 'warning', variant: 'warn', label: '29d left' })
    expect(bootstrapCertState(NOW + 7 * day, NOW).state).toBe('warning')
  })

  it('is critical under 7 days, with a sub-day label', () => {
    expect(bootstrapCertState(NOW + 6 * day + 100, NOW)).toEqual({ state: 'critical', variant: 'bad', label: '6d left' })
    expect(bootstrapCertState(NOW + 3600, NOW)).toEqual({ state: 'critical', variant: 'bad', label: 'under 1d left' })
  })

  it('is expired once the time has passed', () => {
    expect(bootstrapCertState(NOW, NOW)).toEqual({ state: 'expired', variant: 'bad', label: 'Expired' })
    expect(bootstrapCertState(NOW - day, NOW).state).toBe('expired')
  })
})
