// splitPath mirrors src/cmd/catalog/pathsplit.go's splitPath: derive
// (parentPath, name) for a directory path, choosing separator style from
// the path's own shape (leading "/" vs a drive-letter or UNC prefix)
// rather than assuming Unix, since a catalog mixes entries from both
// platforms. Used client-side only to render breadcrumb segments for a
// known currentPath -- never to re-derive directory structure, which
// always comes from the backend's catalog_directories table.
export function splitPath(path) {
  if (!path) return { parentPath: '', name: '' }
  let sep = '/'
  if (isWindowsStyle(path)) {
    sep = path.includes('\\') ? '\\' : '/'
  }
  const idx = path.lastIndexOf(sep)
  if (idx < 0) return { parentPath: '', name: path }
  let parentPath = path.slice(0, idx)
  const name = path.slice(idx + 1)
  if (name === '') return splitPath(parentPath) // tolerate a trailing separator, strip and retry
  if (parentPath === '') parentPath = sep
  else if (isDriveRoot(parentPath)) parentPath += sep
  return { parentPath, name }
}

function isWindowsStyle(path) {
  if (path.startsWith('\\\\')) return true
  return isDriveRoot(path.slice(0, 2))
}

function isDriveRoot(s) {
  return s.length === 2 && s[1] === ':' && /^[a-zA-Z]$/.test(s[0])
}

// pathCrumbs returns [{path, name}] from the true root down to path
// itself, root first -- the reverse walk of
// cmd/catalog/server.go's decodeDirectoryAncestors.
export function pathCrumbs(path) {
  const crumbs = []
  let current = path
  while (current) {
    const { parentPath, name } = splitPath(current)
    crumbs.unshift({ path: current, name: parentPath === '' ? current : name })
    current = parentPath
  }
  return crumbs
}
