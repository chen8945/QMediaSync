import { onActivated, onBeforeUnmount, onDeactivated, shallowRef } from 'vue'
import { registerRealtimeSource } from '@/composables/realtimeSources'

// 全局业务事件类型。
export type RealtimeEventType =
  | 'scraper_task_start'
  | 'scraper_task_complete'
  | 'scraper_item_complete'
  | 'strm_sync_task_start'
  | 'strm_sync_task_queued'
  | 'strm_sync_task_complete'
  | 'sync_task_created'
  | 'sync_task_updated'
  | 'sync_task_deleted'
  | 'upload_queue_status_changed'
  | 'download_queue_status_changed'
  | 'upload_queue_changed'
  | 'download_queue_changed'

export interface RealtimeEvent {
  event_type: RealtimeEventType
  timestamp: string
  data: Record<string, unknown>
}

type EventCallback = (data: Record<string, unknown>) => void
type ReconnectCallback = () => void
type Listener = {
  callback: EventCallback
  onReconnect?: ReconnectCallback
}

export type RealtimeConnectionState =
  'idle' | 'connecting' | 'connected' | 'reconnecting' | 'disconnected'

const listeners = new Map<RealtimeEventType, Set<Listener>>()
const sourceHandlers = new Map<RealtimeEventType, (event: MessageEvent<string>) => void>()
let source: EventSource | null = null
let unregisterSource: (() => void) | null = null
let opened = false
let pendingReconnect = false

export const realtimeSupported = shallowRef(typeof EventSource !== 'undefined')
export const realtimeConnectionState = shallowRef<RealtimeConnectionState>('idle')
export const realtimeActive = shallowRef(false)

function attachSourceHandlers(currentSource: EventSource) {
  sourceHandlers.forEach((handler, eventType) => currentSource.addEventListener(eventType, handler))
}

function closeSource() {
  const currentSource = source
  source = null
  unregisterSource?.()
  unregisterSource = null
  opened = false
  pendingReconnect = false
  realtimeActive.value = false
  realtimeConnectionState.value = 'idle'
  currentSource?.close()
}

function runReconnectCallbacks() {
  listeners.forEach((eventListeners) => {
    eventListeners.forEach((listener) => listener.onReconnect?.())
  })
}

function onVisibilityChange() {
  if (!pendingReconnect || document.hidden) return
  pendingReconnect = false
  runReconnectCallbacks()
}

function ensureSource() {
  if (!realtimeSupported.value || source || listeners.size === 0) return

  realtimeConnectionState.value = 'connecting'
  try {
    const currentSource = new EventSource('/api/events/stream')
    source = currentSource
    unregisterSource = registerRealtimeSource(closeSource)
    attachSourceHandlers(currentSource)
    currentSource.onopen = () => {
      if (source !== currentSource) return
      realtimeConnectionState.value = 'connected'
      if (opened) {
        if (typeof document !== 'undefined' && document.hidden) {
          pendingReconnect = true
        } else {
          runReconnectCallbacks()
        }
      }
      opened = true
    }
    currentSource.onerror = () => {
      if (source !== currentSource) return
      if (currentSource.readyState !== EventSource.CLOSED) {
        realtimeConnectionState.value = 'reconnecting'
        return
      }
      // CLOSED 表示浏览器不会再自动重连；释放连接并保留监听，下一次订阅会重新建立连接。
      source = null
      unregisterSource?.()
      unregisterSource = null
      currentSource.close()
      realtimeConnectionState.value = 'disconnected'
    }
  } catch {
    realtimeConnectionState.value = 'reconnecting'
  }
}

function eventHandler(eventType: RealtimeEventType) {
  const handler = (event: MessageEvent<string>) => {
    try {
      const realtimeEvent = JSON.parse(event.data) as RealtimeEvent
      listeners.get(eventType)?.forEach((listener) => listener.callback(realtimeEvent.data))
    } catch {
      // 忽略无法解析的实时事件。
    }
  }
  sourceHandlers.set(eventType, handler)
  source?.addEventListener(eventType, handler)
}

function removeEventHandlerIfUnused(eventType: RealtimeEventType) {
  if (listeners.has(eventType)) return
  const handler = sourceHandlers.get(eventType)
  if (handler) source?.removeEventListener(eventType, handler)
  sourceHandlers.delete(eventType)
}

/** 注册全局实时事件监听，并在原生重连后执行 HTTP snapshot 收敛。 */
export function on(
  eventType: RealtimeEventType,
  callback: EventCallback,
  onReconnect?: ReconnectCallback,
): () => void {
  const eventListeners = listeners.get(eventType) ?? new Set<Listener>()
  const listener = { callback, onReconnect }
  eventListeners.add(listener)
  listeners.set(eventType, eventListeners)
  if (!sourceHandlers.has(eventType)) eventHandler(eventType)

  realtimeActive.value = true
  ensureSource()

  return () => {
    eventListeners.delete(listener)
    if (eventListeners.size === 0) {
      listeners.delete(eventType)
      removeEventHandlerIfUnused(eventType)
    }
    if (listeners.size === 0) closeSource()
  }
}

/** 注销同一事件类型下的回调。 */
export function off(eventType: RealtimeEventType, callback: EventCallback) {
  const eventListeners = listeners.get(eventType)
  if (!eventListeners) return
  eventListeners.forEach((listener) => {
    if (listener.callback === callback) eventListeners.delete(listener)
  })
  if (eventListeners.size === 0) {
    listeners.delete(eventType)
    removeEventHandlerIfUnused(eventType)
  }
  if (listeners.size === 0) closeSource()
}

/** Vue composable：自动管理全局实时事件监听的生命周期。 */
export function useRealtimeEvent(
  eventType: RealtimeEventType,
  callback: EventCallback,
  onReconnect?: ReconnectCallback,
) {
  let unsubscribe: (() => void) | null = null
  let stopped = false

  const subscribe = () => {
    if (stopped || unsubscribe) return
    unsubscribe = on(eventType, callback, onReconnect)
  }
  const unsubscribeCurrent = () => {
    unsubscribe?.()
    unsubscribe = null
  }
  const stop = () => {
    stopped = true
    unsubscribeCurrent()
  }

  subscribe()
  onActivated(subscribe)
  onDeactivated(unsubscribeCurrent)
  onBeforeUnmount(stop)

  return { unsubscribe: stop }
}

if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', onVisibilityChange)
}
