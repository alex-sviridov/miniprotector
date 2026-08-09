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
