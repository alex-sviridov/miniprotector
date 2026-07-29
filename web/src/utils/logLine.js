// Parses one raw log line -- JSON emitted by Go's slog JSONHandler
// (common/logging) -- into level/message/fields. Field-name-agnostic
// beyond level/msg, so it needs no update as binaries add new attrs.
// Anything that isn't a JSON object (a genuinely malformed line, or
// non-JSON output slipping through the same *.log glob) falls back to
// the raw text unchanged rather than throwing.
export function parseLogLine(raw) {
  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { ok: false, level: null, message: raw, fields: {}, raw }
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { ok: false, level: null, message: raw, fields: {}, raw }
  }

  // Lines shipped by Vector before it was switched to `encoding.codec: text`
  // (see vector.go) wrap the app's own log line one level deeper inside a
  // `message` string instead of carrying level/msg at the top -- unwrap it
  // so those still-in-flight lines (within the 24h retention window) render
  // sensibly instead of a blank message and no severity.
  if (typeof parsed.msg !== 'string' && typeof parsed.message === 'string') {
    try {
      const inner = JSON.parse(parsed.message)
      if (inner !== null && typeof inner === 'object' && !Array.isArray(inner)) {
        parsed = inner
      }
    } catch {
      // parsed.message isn't JSON -- fall through, use the outer object as-is
    }
  }

  const { level, msg, ...fields } = parsed
  return {
    ok: true,
    level: typeof level === 'string' ? level : null,
    message: typeof msg === 'string' ? msg : '',
    fields,
    raw,
  }
}
