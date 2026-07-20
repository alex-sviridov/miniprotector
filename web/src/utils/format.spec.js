import { describe, it, expect } from 'vitest'
import { formatBytes } from './format'

describe('formatBytes', () => {
  it('renders 0 bytes as "0 B"', () => {
    expect(formatBytes(0)).toBe('0 B')
  })

  it('renders null as an em dash', () => {
    expect(formatBytes(null)).toBe('—')
  })

  it('renders undefined as an em dash', () => {
    expect(formatBytes(undefined)).toBe('—')
  })

  it('renders sub-1024 byte counts with no unit conversion', () => {
    expect(formatBytes(512)).toBe('512 B')
  })

  it('renders exactly 1024 bytes as 1.0 KB', () => {
    expect(formatBytes(1024)).toBe('1.0 KB')
  })

  it('renders a fractional KB value to one decimal place', () => {
    expect(formatBytes(8192)).toBe('8.0 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
  })

  it('renders exactly 1024*1024 bytes as 1.0 MB', () => {
    expect(formatBytes(1024 * 1024)).toBe('1.0 MB')
  })

  it('renders large multi-unit values in GB', () => {
    expect(formatBytes(3 * 1024 ** 3)).toBe('3.0 GB')
  })

  it('caps at TB for enormous values instead of introducing a new unit', () => {
    expect(formatBytes(5 * 1024 ** 5)).toBe('5120.0 TB')
  })
})
