import { computed, onBeforeUnmount, onMounted, shallowRef, watch, type Ref } from 'vue'
import type { AxiosInstance } from 'axios'
import {
  fetchMediaLibrarySyncStatus,
  type MediaLibrarySyncInfo,
  type MediaServerType,
} from '@/api/mediaLibrary'
import { createActiveRequestGate } from '@/composables/useActiveRequestGate'
import { parseHttpError } from '@/http/errors'

export function useMediaLibrarySyncStatus(
  http: AxiosInstance,
  serverType: MediaServerType,
  active: Readonly<Ref<boolean>>,
) {
  const status = shallowRef<MediaLibrarySyncInfo | null>(null)
  const error = shallowRef<unknown>(null)
  const starting = shallowRef(false)
  const running = computed(() => starting.value || Boolean(status.value?.is_running))
  let mounted = false
  let inFlight = false
  let pending = false
  let errorNotified = false
  let timer: ReturnType<typeof setTimeout> | undefined
  const visible = () => mounted && active.value && !document.hidden
  const gate = createActiveRequestGate(visible)

  const stop = () => {
    clearTimeout(timer)
    timer = undefined
  }
  const schedule = () => {
    stop()
    if (visible() && running.value) timer = setTimeout(() => void refresh(), 3000)
  }
  const refresh = async (syncStarted = false) => {
    if (!mounted) return
    if (syncStarted) {
      starting.value = true
      gate.invalidate()
    }
    if (!visible() || inFlight) {
      pending = true
      return
    }
    stop()
    const requestId = gate.next()
    inFlight = true
    pending = false
    try {
      const result = await fetchMediaLibrarySyncStatus(http, serverType)
      if (!gate.isCurrent(requestId)) return
      status.value = result
      starting.value = false
      errorNotified = false
      error.value = null
    } catch (cause) {
      if (gate.isCurrent(requestId) && !errorNotified && parseHttpError(cause).shouldNotify) {
        errorNotified = true
        error.value = cause
      }
    } finally {
      inFlight = false
      if (pending && visible()) void refresh()
      else schedule()
    }
  }
  const updateVisibility = () => {
    stop()
    gate.invalidate()
    if (visible()) void refresh()
  }
  watch(active, updateVisibility)
  onMounted(() => {
    mounted = true
    document.addEventListener('visibilitychange', updateVisibility)
    if (visible()) void refresh()
  })
  onBeforeUnmount(() => {
    mounted = false
    stop()
    gate.invalidate()
    document.removeEventListener('visibilitychange', updateVisibility)
  })
  return { status, error, running, refresh }
}
