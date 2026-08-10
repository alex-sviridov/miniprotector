import { describe, it, expect } from 'vitest'
import { validateGlobPattern, findParentChildConflict } from './globPattern'

describe('validateGlobPattern', () => {
  it('accepts an empty pattern', () => {
    expect(validateGlobPattern('')).toEqual({ valid: true })
  })

  it('accepts a plain literal', () => {
    expect(validateGlobPattern('database.sql')).toEqual({ valid: true })
  })

  it('accepts a leading-star pattern', () => {
    expect(validateGlobPattern('*.sql')).toEqual({ valid: true })
  })

  it('accepts a bare trailing star', () => {
    expect(validateGlobPattern('*')).toEqual({ valid: true })
  })

  it('accepts a single-char wildcard', () => {
    expect(validateGlobPattern('file?.txt')).toEqual({ valid: true })
  })

  it('accepts a character class range', () => {
    expect(validateGlobPattern('[a-z]*.log')).toEqual({ valid: true })
  })

  it('accepts a negated character class', () => {
    expect(validateGlobPattern('[^a-z]*.log')).toEqual({ valid: true })
  })

  it('accepts an escaped literal star', () => {
    expect(validateGlobPattern('file\\*.txt')).toEqual({ valid: true })
  })

  it('accepts an escaped closing bracket inside a class', () => {
    expect(validateGlobPattern('[\\]]')).toEqual({ valid: true })
  })

  it('rejects an unterminated character class', () => {
    expect(validateGlobPattern('[abc').valid).toBe(false)
  })

  it('rejects an empty character class', () => {
    expect(validateGlobPattern('[]').valid).toBe(false)
  })

  it('rejects a dangling range dash before the closing bracket', () => {
    expect(validateGlobPattern('[a-]').valid).toBe(false)
  })

  it('rejects a trailing lone backslash', () => {
    expect(validateGlobPattern('file\\').valid).toBe(false)
  })

  it('rejects an unterminated class after a star', () => {
    expect(validateGlobPattern('*[').valid).toBe(false)
  })

  it('includes an error message when invalid', () => {
    const result = validateGlobPattern('[abc')
    expect(result.valid).toBe(false)
    expect(typeof result.error).toBe('string')
    expect(result.error.length).toBeGreaterThan(0)
  })
})

describe('findParentChildConflict', () => {
  it('returns undefined when nothing conflicts', () => {
    expect(findParentChildConflict(['*.sql', '*.log'], '*.dump')).toBeUndefined()
  })

  it('flags a new pattern that is a child of an existing one', () => {
    expect(findParentChildConflict(['/var/log'], '/var/log/app')).toBe('/var/log')
  })

  it('flags a new pattern that is a parent of an existing one', () => {
    expect(findParentChildConflict(['/var/log/app'], '/var/log')).toBe('/var/log/app')
  })

  it('flags an exact duplicate', () => {
    expect(findParentChildConflict(['/var/log'], '/var/log')).toBe('/var/log')
  })

  it('does not flag a pattern that merely shares a text prefix', () => {
    expect(findParentChildConflict(['/var/log'], '/var/logs')).toBeUndefined()
  })

  it('does not flag an unrelated sibling directory', () => {
    expect(findParentChildConflict(['/var/log'], '/var/lib')).toBeUndefined()
  })
})
