// A bootstrap certificate that renews healthily always sits near its full
// lifetime (about 90 days), so a short time remaining means renewal has been
// failing for a while. Thresholds are in whole days.
export const WARN_DAYS = 30
export const BAD_DAYS = 7

const SECONDS_PER_DAY = 86400

// bootstrapCertState maps a bootstrap certificate's expiry (unix seconds, 0 or
// missing when issuer has not reported one yet) to a badge variant and label.
export function bootstrapCertState(notAfter, nowSeconds = Date.now() / 1000) {
  if (!notAfter) return { state: 'unknown', variant: 'neutral', label: '—' }
  const remaining = notAfter - nowSeconds
  if (remaining <= 0) return { state: 'expired', variant: 'bad', label: 'Expired' }
  const days = Math.floor(remaining / SECONDS_PER_DAY)
  const label = days >= 1 ? `${days}d left` : 'under 1d left'
  if (days < BAD_DAYS) return { state: 'critical', variant: 'bad', label }
  if (days < WARN_DAYS) return { state: 'warning', variant: 'warn', label }
  return { state: 'ok', variant: 'ok', label }
}
