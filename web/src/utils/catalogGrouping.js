export function groupEntriesByFile(entries) {
  const groups = new Map()

  for (const entry of entries) {
    const key = `${entry.source_host} ${entry.path}`
    if (!groups.has(key)) {
      groups.set(key, { sourceHost: entry.source_host, path: entry.path, versions: [] })
    }
    groups.get(key).versions.push(entry)
  }

  return Array.from(groups.values()).map((group) => {
    const versions = [...group.versions].sort((a, b) => b.store_created_at - a.store_created_at)
    return { sourceHost: group.sourceHost, path: group.path, versions, representative: versions[0] }
  })
}
