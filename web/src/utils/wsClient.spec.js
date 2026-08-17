import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({ apiFetch: vi.fn() }))

class FakeWebSocket {
  static instances = []
  constructor(url) {
    this.url = url
    this.sent = []
    this.closed = false
    FakeWebSocket.instances.push(this)
  }
  send(data) { this.sent.push(data) }
  close() {
    this.closed = true
    this.onclose && this.onclose({})
  }
  triggerOpen() { this.onopen && this.onopen({}) }
  triggerMessage(data) { this.onmessage && this.onmessage({ data: JSON.stringify(data) }) }
  triggerClose() { this.onclose && this.onclose({}) }
}

describe('wsClient', () => {
  let wsClient

  beforeEach(async () => {
    vi.resetModules()
    apiFetch.mockReset()
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket)
    wsClient = await import('./wsClient')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('openTicketedSocket fetches a ticket and includes it in the WS URL', async () => {
    apiFetch.mockResolvedValue({ ticket: 'abc123' })

    const socket = await wsClient.openTicketedSocket('/jobs/stream')

    expect(apiFetch).toHaveBeenCalledWith('/ws-tickets', { method: 'POST' })
    expect(socket.url).toContain('/api/v1/jobs/stream')
    expect(socket.url).toContain('ticket=abc123')
  })

  it('createLiveStream reports "live" once the socket opens and forwards messages', async () => {
    apiFetch.mockResolvedValue({ ticket: 'abc123' })
    const onMessage = vi.fn()
    const onStatus = vi.fn()

    wsClient.createLiveStream('/jobs/stream', { onMessage, onStatus, onFallback: vi.fn() })
    await Promise.resolve()
    await Promise.resolve()

    const socket = FakeWebSocket.instances[0]
    socket.triggerOpen()
    expect(onStatus).toHaveBeenCalledWith('live')

    socket.triggerMessage({ type: 'snapshot', jobs: [] })
    expect(onMessage).toHaveBeenCalledWith({ type: 'snapshot', jobs: [] })
  })

  it('reconnects with backoff on an unexpected close, reporting "reconnecting"', async () => {
    vi.useFakeTimers()
    apiFetch.mockResolvedValue({ ticket: 'abc123' })
    const onStatus = vi.fn()

    wsClient.createLiveStream('/jobs/stream', { onMessage: vi.fn(), onStatus, onFallback: vi.fn() })
    await vi.advanceTimersByTimeAsync(0)

    const first = FakeWebSocket.instances[0]
    first.triggerClose()
    expect(onStatus).toHaveBeenCalledWith('reconnecting')

    await vi.advanceTimersByTimeAsync(10000)
    expect(FakeWebSocket.instances.length).toBeGreaterThan(1)
  })

  it('falls back to polling after repeated reconnect failures', async () => {
    vi.useFakeTimers()
    apiFetch.mockResolvedValue({ ticket: 'abc123' })
    const onStatus = vi.fn()
    const onFallback = vi.fn()

    wsClient.createLiveStream('/jobs/stream', { onMessage: vi.fn(), onStatus, onFallback })

    for (let i = 0; i < 6; i++) {
      await vi.advanceTimersByTimeAsync(0)
      const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
      socket.triggerClose()
      await vi.advanceTimersByTimeAsync(10000)
    }

    expect(onStatus).toHaveBeenCalledWith('polling')
    expect(onFallback).toHaveBeenCalled()
  })

  it('close() prevents further reconnect attempts', async () => {
    vi.useFakeTimers()
    apiFetch.mockResolvedValue({ ticket: 'abc123' })

    const stream = wsClient.createLiveStream('/jobs/stream', { onMessage: vi.fn(), onStatus: vi.fn(), onFallback: vi.fn() })
    await vi.advanceTimersByTimeAsync(0)
    const countBeforeClose = FakeWebSocket.instances.length

    stream.close()
    await vi.advanceTimersByTimeAsync(20000)

    expect(FakeWebSocket.instances.length).toBe(countBeforeClose)
  })

  it('close() called synchronously before the in-flight connect resolves closes the socket without going live', async () => {
    apiFetch.mockResolvedValue({ ticket: 'abc123' })
    const onMessage = vi.fn()
    const onStatus = vi.fn()

    const stream = wsClient.createLiveStream('/jobs/stream', { onMessage, onStatus, onFallback: vi.fn() })
    // no await here -- close() runs while the ticket fetch / WebSocket
    // constructor is still in flight, before `socket` is ever assigned
    stream.close()

    // let the pending openTicketedSocket() promise chain settle
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()

    expect(FakeWebSocket.instances.length).toBe(1)
    const socket = FakeWebSocket.instances[0]
    expect(socket.closed).toBe(true)
    expect(onStatus).not.toHaveBeenCalledWith('live')
    expect(onMessage).not.toHaveBeenCalled()

    // no handlers should have been attached to the stale socket at all
    socket.triggerOpen()
    socket.triggerMessage({ type: 'snapshot', jobs: [] })
    expect(onStatus).not.toHaveBeenCalledWith('live')
    expect(onMessage).not.toHaveBeenCalled()
  })
})
