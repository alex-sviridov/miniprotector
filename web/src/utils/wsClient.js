// web/src/utils/wsClient.js
import { apiFetch } from '../api/client'

const MAX_RECONNECT_ATTEMPTS = 5
const BASE_BACKOFF_MS = 500
const MAX_BACKOFF_MS = 8000
export const FALLBACK_POLL_MS = 10000

// backoff mirrors src/cmd/agent/reconcile.go's jittered backoff() idiom --
// exponential, capped, half-jittered -- reimplemented here since this is a
// separate language/runtime, not shared code.
function backoff(attempt) {
  const base = Math.min(BASE_BACKOFF_MS * 2 ** attempt, MAX_BACKOFF_MS)
  return base / 2 + Math.random() * (base / 2)
}

// openTicketedSocket mints a fresh single-use ticket (POST /ws-tickets --
// required on every call, since a ticket authenticates exactly one
// connection attempt, see src/cmd/api-server/ws_tickets.go) and opens the
// WebSocket with it as a query param -- a WS handshake can't carry an
// Authorization header the way every other apiFetch call does.
export async function openTicketedSocket(path) {
  const { ticket } = await apiFetch('/ws-tickets', { method: 'POST' })
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const separator = path.includes('?') ? '&' : '?'
  const url = `${proto}//${window.location.host}/api/v1${path}${separator}ticket=${encodeURIComponent(ticket)}`
  return new WebSocket(url)
}

// createLiveStream manages one logical live connection: opens a ticketed
// socket, calls onMessage for each parsed JSON frame, and reconnects with
// jittered backoff on an unexpected close -- up to MAX_RECONNECT_ATTEMPTS,
// after which it calls onFallback(FALLBACK_POLL_MS) once and stops
// retrying, so the caller can switch to REST polling instead of retrying
// forever silently (see jobs.js). onStatus reports 'live' | 'reconnecting'
// | 'polling', so the page can never look current while actually stalled.
export function createLiveStream(path, { onMessage, onStatus, onFallback }) {
  let attempt = 0
  let closedByCaller = false
  let socket = null

  async function connect() {
    if (closedByCaller) return
    try {
      socket = await openTicketedSocket(path)
    } catch {
      scheduleReconnect()
      return
    }
    socket.onopen = () => {
      attempt = 0
      onStatus('live')
    }
    socket.onmessage = (event) => {
      try {
        onMessage(JSON.parse(event.data))
      } catch {
        // a malformed frame is dropped, not fatal to the stream
      }
    }
    socket.onclose = () => {
      if (closedByCaller) return
      scheduleReconnect()
    }
    socket.onerror = () => socket.close()
  }

  function scheduleReconnect() {
    attempt += 1
    if (attempt > MAX_RECONNECT_ATTEMPTS) {
      onStatus('polling')
      onFallback(FALLBACK_POLL_MS)
      return
    }
    onStatus('reconnecting')
    setTimeout(connect, backoff(attempt))
  }

  connect()

  return {
    close() {
      closedByCaller = true
      if (socket) socket.close()
    },
  }
}
