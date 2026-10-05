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

// formatDurationNs renders a duration given in nanoseconds -- how Go's slog
// JSON handler writes a time.Duration attribute -- as e.g. "250 ms", "1.5 s",
// "2 min 5 s", "1 h 5 min".
export function formatDurationNs(ns) {
  if (ns === null || ns === undefined) return '—'
  const ms = Math.round(ns / 1e6)
  if (ms < 1000) return `${ms} ms`
  const totalSeconds = ns / 1e9
  if (totalSeconds < 10) return `${Math.round(totalSeconds * 10) / 10} s`
  const seconds = Math.round(totalSeconds)
  if (seconds < 60) return `${seconds} s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} min ${seconds % 60} s`
  return `${Math.floor(minutes / 60)} h ${minutes % 60} min`
}
