export function formatTimestamp(epochSeconds) {
  return epochSeconds ? new Date(epochSeconds * 1000).toLocaleString() : null
}
