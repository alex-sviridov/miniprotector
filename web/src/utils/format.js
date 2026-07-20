export function formatTimestamp(epochSeconds) {
  return epochSeconds ? new Date(epochSeconds * 1000).toLocaleString() : null
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

export function formatBytes(bytes) {
  if (bytes === null || bytes === undefined) return '—'
  if (bytes === 0) return '0 B'
  let exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), BYTE_UNITS.length - 1)
  let value = bytes / 1024 ** exponent
  if (exponent > 0 && Number(value.toFixed(1)) >= 1024 && exponent < BYTE_UNITS.length - 1) {
    exponent += 1
    value = bytes / 1024 ** exponent
  }
  return `${exponent === 0 ? value : value.toFixed(1)} ${BYTE_UNITS[exponent]}`
}
