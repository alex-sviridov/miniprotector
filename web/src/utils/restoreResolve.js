// Turns a set of raw /catalog entries into what the restore cart's rules
// actually resolve as selected -- reuses resolveFile (restoreRules.js)
// rather than a substring/prefix check of our own, so an unrelated file
// like /var/lib/dbdata2/x is never swept in by a rule for /var/lib/dbdata:
// resolveFile walks real path segments, a substring check would not.
import { resolveFile } from './restoreRules'
import { groupEntriesByFile } from './catalogGrouping'

export function filterResolved(rules, entries) {
  return entries.filter((entry) => resolveFile(rules, entry.source_host, entry.path))
}

// collapseToLatestVersion turns raw /catalog rows -- one row per file
// *version*, so a nightly-backed-up file is dozens of rows with distinct
// ids -- into one row per distinct file, keeping the newest version's row.
// Deduping on id alone would keep every version, which both repeats each
// path in the submitted policy and can split one path across two store
// groups when its versions landed on different stores.
export function collapseToLatestVersion(entries) {
  return groupEntriesByFile(entries).map((group) => group.representative)
}

// groupByStore groups resolved entries by the physical bwfs node they're
// stored on (store_host) -- this is what makes "one restore policy per
// store" possible: a single source host's files can in principle live on
// more than one store, and grouping per distinct file (each already
// collapsed to its latest version) puts each file in exactly one group.
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
