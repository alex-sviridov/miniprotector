// A rule captures one explicit restore-selection decision: { path, host,
// include }. host === null means a folder-level rule, applying across
// every source host -- folder rows in the catalog UI are already
// host-agnostic (ListDirectoryChildren's existence check ignores the
// clients/host filter). host set to a string means a file-level rule,
// scoped to that one (host, path) pair -- matches how file rows are
// already grouped client-side (groupEntriesByFile).
//
// A path's selection state is *resolved* from the rule list rather than
// stored directly, using longest-matching-prefix semantics (like
// .gitignore): the most specific rule covering a path wins. This keeps
// the rule list small regardless of how many files a folder contains --
// selecting a folder is one rule, not one per descendant file. See
// docs/superpowers/specs/2026-08-09-restore-cart-design.md.
import { pathCrumbs } from './pathSplit'

// entryKey identifies a cart entry/rule by its (host, path) identity -- the
// same key restoreCart's rule list, restoreSubmission's per-entry status
// map, and RestoreView's table rows all use, so a single source of truth
// replaces three independent copies of the same template string.
export function entryKey(entry) {
  return `${entry.host ?? ''}:${entry.path}`
}

// ancestorsOrSelf returns path's ancestor chain root-first, path itself
// last -- reuses pathCrumbs (already handles Unix/Windows/UNC shapes)
// rather than re-deriving path structure here.
function ancestorsOrSelf(path) {
  return pathCrumbs(path).map((c) => c.path)
}

// longestMatchingFolderRule finds the most specific host-agnostic folder
// rule covering path (checking path itself before its ancestors), and
// returns its `include` value, or undefined if none match.
function longestMatchingFolderRule(rules, path) {
  const chain = ancestorsOrSelf(path)
  for (let i = chain.length - 1; i >= 0; i--) {
    const rule = rules.find((r) => r.host === null && r.path === chain[i])
    if (rule) return rule.include
  }
  return undefined
}

// resolveFile returns whether (host, path) is currently selected: an
// exact host-specific rule wins outright; otherwise the longest matching
// host-agnostic ancestor folder rule applies. No match = unselected.
export function resolveFile(rules, host, path) {
  const exact = rules.find((r) => r.host === host && r.path === path)
  if (exact) return exact.include
  return longestMatchingFolderRule(rules, path) === true
}

// isStrictDescendantPath is true when ancestorPath is a proper ancestor
// of candidatePath (not equal to it).
function isStrictDescendantPath(candidatePath, ancestorPath) {
  if (candidatePath === ancestorPath) return false
  return ancestorsOrSelf(candidatePath).includes(ancestorPath)
}

// hasRuleUnder is true if any rule (folder or file, any host) sits
// strictly under path -- used to detect a folder's indeterminate state.
function hasRuleUnder(rules, path) {
  return rules.some((r) => isStrictDescendantPath(r.path, path))
}

// resolveFolderState returns the tri-state checkbox value for a folder
// row: 'checked' if a rule fully covers it and nothing overrides that
// underneath; 'unchecked' if nothing covers it and nothing sits under
// it; 'indeterminate' otherwise (mixed).
export function resolveFolderState(rules, path) {
  if (hasRuleUnder(rules, path)) return 'indeterminate'
  return longestMatchingFolderRule(rules, path) === true ? 'checked' : 'unchecked'
}

// toggleFolder returns a new rule list with path's selection flipped.
// Checked -> unchecked mirrors the exact-rule-removal trick below (a
// state of 'checked' guarantees nothing sits underneath, so no pruning
// is needed there). Unchecked/indeterminate -> checked first prunes
// every rule at-or-under path -- clearing any exceptions or partial
// selections underneath -- then adds a fresh wildcard only if the
// remaining rules don't already cover path via an ancestor (avoiding a
// redundant rule). A newly created rule defaults destPath to path (no
// rename) and merges in any caller-supplied extra display-only
// properties (see restoreCart.js).
export function toggleFolder(rules, path, extra = {}) {
  const state = resolveFolderState(rules, path)
  if (state === 'checked') {
    const exact = rules.find((r) => r.host === null && r.path === path)
    if (exact) return rules.filter((r) => r !== exact)
    return [...rules, { path, host: null, include: false, destPath: path, ...extra }]
  }
  const pruned = rules.filter((r) => r.path !== path && !isStrictDescendantPath(r.path, path))
  if (longestMatchingFolderRule(pruned, path) === true) return pruned
  return [...pruned, { path, host: null, include: true, destPath: path, ...extra }]
}

// toggleFile returns a new rule list with (host, path)'s selection
// flipped. If an exact rule already exists at (host, path), it is
// removed: by the pruning invariant maintained throughout this module,
// a stored rule only ever exists because it overrides its closest
// ancestor, so removing it always flips the resolved state back.
// Otherwise a fresh rule is added with the opposite of the current
// resolved state, destPath defaulting to path (no rename), merged with
// any caller-supplied extra display-only properties.
export function toggleFile(rules, host, path, extra = {}) {
  const exact = rules.find((r) => r.host === host && r.path === path)
  if (exact) return rules.filter((r) => r !== exact)
  const checked = resolveFile(rules, host, path)
  return [...rules, { path, host, include: !checked, destPath: path, ...extra }]
}

// ensureFileRule guarantees an exact include rule exists at (host, path),
// merging in any caller-supplied extra properties on a newly-created one.
// Unlike toggleFile, it never flips based on the *resolved* state: it's
// meant for a path that already resolves as selected -- possibly only via
// an inherited ancestor folder rule, with no rule of its own -- and
// something needs to attach directly to this exact (host, path), e.g.
// pinning a version window (restoreCart.setVersionWindow only mutates an
// *exact* matching rule and silently no-ops otherwise). If an exact rule
// already exists here (include or exclude), it's left untouched -- callers
// should only reach for this when resolveFile(rules, host, path) is
// already true, so an exact exclude rule shouldn't be possible, but this
// stays a pure "create if missing" op regardless.
export function ensureFileRule(rules, host, path, extra = {}) {
  if (rules.some((r) => r.host === host && r.path === path)) return rules
  return [...rules, { path, host, include: true, destPath: path, ...extra }]
}

// ensureFolderRule is ensureFileRule's folder-level counterpart: guarantees
// an exact host-agnostic include rule exists at path, without pruning or
// flipping based on resolved state. Safe to call whenever
// resolveFolderState(rules, path) === 'checked' (that state already
// guarantees nothing sits underneath, so -- unlike toggleFolder's
// checked->unchecked branch -- there's nothing to prune).
export function ensureFolderRule(rules, path, extra = {}) {
  if (rules.some((r) => r.host === null && r.path === path)) return rules
  return [...rules, { path, host: null, include: true, destPath: path, ...extra }]
}
