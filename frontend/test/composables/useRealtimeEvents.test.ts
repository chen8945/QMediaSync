// @vitest-environment happy-dom

import { afterEach, describe, expect, it, vi } from 'vitest'

type EventListener = (event: MessageEvent<string>) => void

class MockEventSource {
  static instances: MockEventSource[] = []
  static readonly CLOSED = 2
  readyState = 0
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  private readonly listeners = new Map<string, Set<EventListener>>()
  closed = false

  constructor(public readonly url: string) {
    MockEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: EventListener) {
    const listeners = this.listeners.get(type) ?? new Set<EventListener>()
    listeners.add(listener)
    this.listeners.set(type, listeners)
  }

  removeEventListener(type: string, listener: EventListener) {
    this.listeners.get(type)?.delete(listener)
  }

  close() {
    this.closed = true
  }
}

describe('global realtime EventSource', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.resetModules()
    MockEventSource.instances = []
  })

  it('reconnects with HTTP convergence only after the first open and closes after final unsubscribe', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const realtime = await import('@/composables/useRealtimeEvents')
    const onReconnect = vi.fn()

    const unsubscribe = realtime.on('upload_queue_changed', vi.fn(), onReconnect)
    const source = MockEventSource.instances[0]

    expect(source.url).toBe('/api/events/stream')
    expect(realtime.realtimeActive.value).toBe(true)
    expect(realtime.realtimeConnectionState.value).toBe('connecting')
    source.onopen?.()
    expect(realtime.realtimeConnectionState.value).toBe('connected')
    expect(onReconnect).not.toHaveBeenCalled()

    source.onerror?.()
    expect(realtime.realtimeConnectionState.value).toBe('reconnecting')
    source.onopen?.()
    expect(realtime.realtimeConnectionState.value).toBe('connected')
    expect(onReconnect).toHaveBeenCalledTimes(1)

    unsubscribe()
    expect(source.closed).toBe(true)
    expect(realtime.realtimeActive.value).toBe(false)
  })

  it('浏览器放弃重连（CLOSED）时进入已断开状态并释放连接，下一次订阅重建', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const realtime = await import('@/composables/useRealtimeEvents')
    realtime.on('upload_queue_changed', vi.fn())
    const source = MockEventSource.instances[0]

    source.readyState = MockEventSource.CLOSED
    source.onerror?.()
    expect(source.closed).toBe(true)
    expect(realtime.realtimeConnectionState.value).toBe('disconnected')

    realtime.on('download_queue_changed', vi.fn())
    expect(MockEventSource.instances).toHaveLength(2)
    expect(realtime.realtimeConnectionState.value).toBe('connecting')
  })

  it('keeps listener lifecycle without creating a source when EventSource is unsupported', async () => {
    vi.stubGlobal('EventSource', undefined)
    const realtime = await import('@/composables/useRealtimeEvents')
    const unsubscribe = realtime.on('upload_queue_changed', vi.fn())

    expect(realtime.realtimeSupported.value).toBe(false)
    expect(realtime.realtimeActive.value).toBe(true)
    expect(MockEventSource.instances).toHaveLength(0)

    unsubscribe()
    expect(realtime.realtimeActive.value).toBe(false)
  })
})
