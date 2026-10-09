// retentionRule.js: pure helpers for the retention policy form and list --
// validation mirroring policy-server's RetentionPolicy.Validate
// (src/cmd/policy-server/retention_policy.go), payload/form conversion, and
// display formatting. Kept free of Vue so it is trivially unit-testable.
import { validateGlobPattern } from './globPattern'

export const SECONDS_PER_DAY = 86400

// normalizePath trims, collapses repeated slashes and strips a trailing
// slash (except for the root itself), so what the operator types matches the
// clean absolute path policy-server requires.
export function normalizePath(raw) {
  let p = (raw || '').trim().replace(/\/{2,}/g, '/')
  if (p.length > 1 && p.endsWith('/')) p = p.slice(0, -1)
  return p
}

// validateRetentionForm returns {field: message} for every invalid field, or
// {} when the form is valid. The same rules policy-server enforces: a name,
// an absolute path without "..", basename-only include globs (a "/" has no
// sound meaning for a rule whose prefix can sit above or below a job's
// root), and a whole number of days >= 1 unless keeping forever.
export function validateRetentionForm(form) {
  const errors = {}
  if (!form.name.trim()) errors.name = 'Name is required.'

  const path = normalizePath(form.path)
  if (!path) {
    errors.path = 'Path is required.'
  } else if (!path.startsWith('/')) {
    errors.path = 'Path must be absolute (start with /).'
  } else if (path.split('/').includes('..')) {
    errors.path = "Path must not contain '..'."
  }

  for (const raw of form.include) {
    const pattern = raw.trim()
    if (!pattern) {
      errors.include = 'Include patterns must not be empty.'
      break
    }
    if (pattern.includes('/')) {
      errors.include = `Include pattern "${pattern}" must not contain "/": patterns match file names only.`
      break
    }
    const syntax = validateGlobPattern(pattern)
    if (!syntax.valid) {
      errors.include = `Include pattern "${pattern}" is invalid: ${syntax.error}`
      break
    }
  }

  if (!form.keepForever) {
    const days = String(form.keepDays).trim()
    if (!/^\d+$/.test(days) || Number(days) < 1) {
      errors.keepDays = 'Keep must be a whole number of days, at least 1.'
    }
  }
  return errors
}

export function toPayload(form) {
  return {
    name: form.name.trim(),
    client_filters: {
      hostnames: form.client_filters.hostnames.map((h) => h.trim()).filter(Boolean),
      labels: Object.fromEntries(
        form.client_filters.labels
          .map((l) => [l.key.trim(), l.value.trim()])
          .filter(([key]) => key)
      ),
    },
    retention: {
      backup_type: form.backup_type,
      path: normalizePath(form.path),
      include: form.include.map((i) => i.trim()).filter(Boolean),
      keep_seconds: form.keepForever ? 0 : Number(String(form.keepDays).trim()) * SECONDS_PER_DAY,
    },
  }
}

// toFormShape builds the editable form model from a policy (or defaults for
// a new rule). A keep that isn't a whole number of days -- possible only if
// the rule was created through the API directly -- is rounded UP to whole
// days, so saving from the UI can never silently shorten a retention.
export function toFormShape(policy) {
  if (!policy) {
    return {
      name: '',
      client_filters: { hostnames: [], labels: [] },
      backup_type: 'filesystem',
      path: '',
      include: [],
      keepForever: false,
      keepDays: '30',
    }
  }
  const rule = policy.retention || {}
  const seconds = rule.keep_seconds || 0
  return {
    name: policy.name,
    client_filters: {
      hostnames: [...(policy.client_filters?.hostnames || [])],
      labels: Object.entries(policy.client_filters?.labels || {}).map(([key, value]) => ({ key, value })),
    },
    backup_type: rule.backup_type || 'filesystem',
    path: rule.path || '',
    include: [...(rule.include || [])],
    keepForever: seconds === 0,
    keepDays: seconds === 0 ? '30' : String(Math.ceil(seconds / SECONDS_PER_DAY)),
  }
}

function plural(n, unit) {
  return `${n} ${unit}${n === 1 ? '' : 's'}`
}

export function formatKeep(seconds) {
  if (!seconds) return 'Forever'
  if (seconds % SECONDS_PER_DAY === 0) return plural(seconds / SECONDS_PER_DAY, 'day')
  if (seconds % 3600 === 0) return plural(seconds / 3600, 'hour')
  return plural(seconds, 'second')
}

export function summarizeTarget(clientFilters) {
  const hostnames = clientFilters?.hostnames || []
  const labels = Object.entries(clientFilters?.labels || {}).map(([k, v]) => `${k}=${v}`)
  const parts = []
  if (hostnames.length) parts.push(hostnames.join(', '))
  if (labels.length) parts.push(labels.join(', '))
  return parts.length ? parts.join(' · ') : 'All clients'
}
