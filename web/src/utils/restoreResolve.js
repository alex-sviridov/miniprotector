// Turns a set of raw /catalog entries into what the restore cart's rules
// actually resolve as selected -- reuses resolveFile (restoreRules.js)
// rather than a substring/prefix check of our own, so an unrelated file
// like /var/lib/dbdata2/x is never swept in by a rule for /var/lib/dbdata:
// resolveFile walks real path segments, a substring check would not.
import { resolveFile } from './restoreRules'

export function filterResolved(rules, entries) {
  return entries.filter((entry) => resolveFile(rules, entry.source_host, entry.path))
}

export function dedupeById(entries) {
  const seen = new Set()
  const result = []
  for (const entry of entries) {
    if (seen.has(entry.id)) continue
    seen.add(entry.id)
    result.push(entry)
  }
  return result
}

// groupByStore groups resolved entries by the physical bwfs node they're
// stored on (store_host) -- this is what makes "one restore policy per
// store" possible: a single source host's files can in principle live on
// more than one store over time, and grouping at the file level (rather
// than trying to pick one store per source host) handles that for free.
export function groupByStore(entries) {
  const byStore = new Map()
  for (const entry of entries) {
    if (!byStore.has(entry.store_host)) byStore.set(entry.store_host, [])
    byStore.get(entry.store_host).push({ sourceHost: entry.source_host, path: entry.path })
  }
  return Array.from(byStore.entries())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([storeHost, files]) => ({
      storeHost,
      files: [...files].sort(
        (a, b) => a.sourceHost.localeCompare(b.sourceHost) || a.path.localeCompare(b.path)
      ),
    }))
}
