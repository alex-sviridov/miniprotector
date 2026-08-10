// globPattern.js: a hand-ported syntax-only check mirroring Go's
// path.Match grammar (the "path" package, not "path/filepath" -- see
// src/cmd/policy-server/backup_policy.go's Validate(), which calls
// path.Match(pattern, "") for exactly this same syntax-only check). Kept in
// lockstep with path.Match's grammar rather than approximated with a looser
// regex, so a pattern this module accepts never bounces off the server and
// vice versa.
//
// pattern := { term }
// term    := '*' | '?' | '[' ['^'] { range } ']' | c | '\\' c
// range   := c | '\\' c | (c|'\\'c) '-' (c|'\\'c)

// scanChunk splits pattern into its leading run of '*' (star) and the
// following star-free chunk, treating '[...]' as opaque (a '*' inside
// brackets doesn't split) and skipping the character right after a
// backslash so an escaped '*' isn't treated as a split point either.
function scanChunk(pattern) {
  let star = false
  while (pattern.length > 0 && pattern[0] === '*') {
    pattern = pattern.slice(1)
    star = true
  }
  let inrange = false
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i]
    if (c === '\\') {
      if (i + 1 < pattern.length) i++
    } else if (c === '[') {
      inrange = true
    } else if (c === ']') {
      inrange = false
    } else if (c === '*' && !inrange) {
      return { star, chunk: pattern.slice(0, i), rest: pattern.slice(i) }
    }
  }
  return { star, chunk: pattern, rest: '' }
}

// getEsc validates and consumes one possibly-escaped character from the
// front of chunk, for use inside a '[...]' character class: a bare '-' or
// ']' (unescaped), an empty chunk, or a trailing lone '\\' is an error.
function getEsc(chunk) {
  if (chunk.length === 0 || chunk[0] === '-' || chunk[0] === ']') {
    return { error: true }
  }
  if (chunk[0] === '\\') {
    chunk = chunk.slice(1)
    if (chunk.length === 0) return { error: true }
  }
  return { error: false, rest: chunk.slice(1) }
}

// checkChunkSyntax validates one star-free chunk's syntax. This mirrors
// calling Go's matchChunk(chunk, "") -- since the name being matched is
// always empty, only matchChunk's syntax-error paths are reachable: a
// malformed '[...]' character class, or a trailing lone '\\'.
function checkChunkSyntax(chunk) {
  while (chunk.length > 0) {
    const c = chunk[0]
    if (c === '[') {
      chunk = chunk.slice(1)
      if (chunk[0] === '^') chunk = chunk.slice(1)
      let nrange = 0
      for (;;) {
        if (chunk.length > 0 && chunk[0] === ']' && nrange > 0) {
          chunk = chunk.slice(1)
          break
        }
        const lo = getEsc(chunk)
        if (lo.error) return false
        chunk = lo.rest
        if (chunk[0] === '-') {
          const hi = getEsc(chunk.slice(1))
          if (hi.error) return false
          chunk = hi.rest
        }
        nrange++
      }
    } else if (c === '?') {
      chunk = chunk.slice(1)
    } else if (c === '\\') {
      chunk = chunk.slice(1)
      if (chunk.length === 0) return false
      chunk = chunk.slice(1)
    } else {
      chunk = chunk.slice(1)
    }
  }
  return true
}

// validateGlobPattern checks pattern's syntax only (never matches against a
// real path) -- the JS-side equivalent of Go's path.Match(pattern, "").
export function validateGlobPattern(pattern) {
  let p = pattern
  while (p.length > 0) {
    const { star, chunk, rest } = scanChunk(p)
    if (star && chunk === '') {
      return { valid: true } // trailing '*' with nothing after it
    }
    if (!checkChunkSyntax(chunk)) {
      return { valid: false, error: `invalid pattern: syntax error near "${chunk}"` }
    }
    p = rest
  }
  return { valid: true }
}

// findParentChildConflict returns the first entry in patterns that is a
// plain-string parent, child, or duplicate of pattern -- normalized with a
// trailing '/' on both sides so "/var/log" doesn't false-positive against
// "/var/logs" (shares a text prefix but is a different directory).
// Comparison is deliberately plain string prefix, not glob-aware.
export function findParentChildConflict(patterns, pattern) {
  const normalize = (p) => (p.endsWith('/') ? p : p + '/')
  const target = normalize(pattern)
  return patterns.find((existing) => {
    const other = normalize(existing)
    return target.startsWith(other) || other.startsWith(target)
  })
}
