import { registerRealtimeSource } from '@/composables/realtimeSources'
import { fetchSyncTask } from '@/api/syncTasks'
import { parseHttpError } from '@/http/errors'
import type {
  SyncTask,
  SyncTaskEventPayload,
  SyncTaskLogEntry,
  SyncTaskSnapshot,
  SyncTaskStreamMessage,
} from '@/types/syncTaskStream'
import {
  computed,
  onBeforeUnmount,
  readonly,
  shallowRef,
  toRef,
  toValue,
  watch,
  type MaybeRefOrGetter,
} from 'vue'

interface UseSyncTaskStreamOptions {
  immediate?: boolean
  maxLogs?: number
}

type SyncTaskConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting'

export function useSyncTaskStream(
  syncId: MaybeRefOrGetter<number | string>,
  options: UseSyncTaskStreamOptions = {},
) {
  const { immediate = true, maxLogs = 2000 } = options
  const task = shallowRef<SyncTask | null>(null)
  const logs = shallowRef<SyncTaskLogEntry[]>([])
  const loading = shallowRef(false)
  const connected = shallowRef(false)
  const connectionState = shallowRef<SyncTaskConnectionState>('idle')
  const terminal = shallowRef(false)
  const unsupported = shallowRef(false)
  const errorMessage = shallowRef('')
  const logCursor = shallowRef(0)
  const logPath = shallowRef('')
  const source = shallowRef<EventSource | null>(null)
  let unregisterSource: (() => void) | null = null
  let pollTimer: ReturnType<typeof setInterval> | null = null
  let fallbackGeneration = 0
  let fallbackRequest: { generation: number } | null = null

  const isRunning = computed(() => task.value?.status === 0 || task.value?.status === 1)

  const clearPolling = () => {
    if (!pollTimer) return
    clearInterval(pollTimer)
    pollTimer = null
  }

  const closeSource = (currentSource = source.value) => {
    if (!currentSource || source.value !== currentSource) return
    source.value = null
    unregisterSource?.()
    unregisterSource = null
    currentSource.close()
    connected.value = false
    connectionState.value = 'idle'
  }

  const closeRealtime = () => {
    fallbackGeneration++
    closeSource()
    clearPolling()
  }

  const appendLog = (entry: SyncTaskLogEntry) => {
    logs.value = [withLogID(entry), ...logs.value].slice(0, maxLogs)
  }

  const normalizeSnapshotLogs = (entries: SyncTaskLogEntry[]) =>
    entries.map(withLogID).reverse().slice(0, maxLogs)

  const withLogID = (entry: SyncTaskLogEntry): SyncTaskLogEntry => ({
    ...entry,
    id: entry.id || `${entry.cursor || Date.now()}-${Math.random().toString(36).slice(2, 9)}`,
  })

  const applySnapshot = (snapshot: SyncTaskSnapshot) => {
    task.value = snapshot.task
    logs.value = normalizeSnapshotLogs(snapshot.logs)
    logCursor.value = snapshot.log_cursor
    logPath.value = snapshot.log_path
    loading.value = false
    terminal.value = snapshot.task.status === 2 || snapshot.task.status === 3
  }

  const applyTaskPatch = (payload: SyncTaskEventPayload) => {
    if (payload.deleted) {
      terminal.value = true
      errorMessage.value = '同步记录已删除'
      return
    }
    if (!task.value) return

    const next = { ...task.value }
    if (typeof payload.status === 'number') next.status = payload.status as SyncTask['status']
    if (typeof payload.sub_status === 'number')
      next.sub_status = payload.sub_status as SyncTask['sub_status']
    if (typeof payload.total === 'number') next.total = payload.total
    if (typeof payload.new_strm === 'number') next.new_strm = payload.new_strm
    if (typeof payload.new_meta === 'number') next.new_meta = payload.new_meta
    if (typeof payload.new_upload === 'number') next.new_upload = payload.new_upload
    if (typeof payload.finish_at === 'number') next.finish_at = payload.finish_at
    if (typeof payload.net_file_start_at === 'number')
      next.net_file_start_at = payload.net_file_start_at
    if (typeof payload.net_file_finish_at === 'number')
      next.net_file_finish_at = payload.net_file_finish_at
    if (typeof payload.local_file_start_at === 'number')
      next.local_file_start_at = payload.local_file_start_at
    if (typeof payload.local_file_finish_at === 'number')
      next.local_file_finish_at = payload.local_file_finish_at
    if (typeof payload.updated_at === 'number') next.updated_at = payload.updated_at
    if (typeof payload.fail_reason === 'string') next.fail_reason = payload.fail_reason
    task.value = next
  }

  const handleMessage = (message: SyncTaskStreamMessage) => {
    if (message.type === 'snapshot') {
      applySnapshot(message.data as SyncTaskSnapshot)
      if (terminal.value) closeSource()
      return
    }
    if (message.type === 'task_patch') {
      applyTaskPatch(message.data as SyncTaskEventPayload)
      return
    }
    if (message.type === 'complete') {
      applyTaskPatch(message.data as SyncTaskEventPayload)
      terminal.value = true
      closeSource()
      return
    }
    if (message.type === 'log_append') {
      const data = message.data as { entry?: SyncTaskLogEntry; cursor?: number }
      if (data.entry) appendLog(data.entry)
      if (typeof data.cursor === 'number') logCursor.value = data.cursor
      return
    }
    if (message.type === 'resync_required') {
      closeSource()
      connect()
      return
    }
    if (message.type === 'error') {
      errorMessage.value = '同步任务实时流返回错误'
    }
  }

  const loadFallbackTask = async (currentID: number, generation: number) => {
    if (fallbackRequest?.generation === generation) return
    const request = { generation }
    fallbackRequest = request
    const isCurrent = () =>
      generation === fallbackGeneration && !source.value && Number(toValue(syncId)) === currentID
    try {
      const nextTask = await fetchSyncTask(currentID)
      if (!isCurrent()) return
      task.value = nextTask
      errorMessage.value = ''
      terminal.value = nextTask.status === 2 || nextTask.status === 3
      loading.value = false
      if (terminal.value) clearPolling()
    } catch (error) {
      if (!isCurrent()) return
      const failure = parseHttpError(error, { fallbackMessage: '加载同步任务失败' })
      if (failure.shouldNotify) errorMessage.value = failure.message
      loading.value = false
      if (
        failure.response?.status === 401 ||
        failure.response?.status === 403 ||
        failure.response?.status === 404
      ) {
        // 首次降级读取后也不能由 then 重新创建 timer。
        fallbackGeneration++
        clearPolling()
      }
    } finally {
      if (fallbackRequest === request) fallbackRequest = null
    }
  }

  const startFallbackPolling = (currentID: number) => {
    clearPolling()
    const generation = fallbackGeneration
    void loadFallbackTask(currentID, generation).then(() => {
      if (
        generation === fallbackGeneration &&
        !terminal.value &&
        isRunning.value &&
        Number(toValue(syncId)) === currentID
      ) {
        pollTimer = setInterval(() => void loadFallbackTask(currentID, generation), 5000)
      }
    })
  }

  const connect = () => {
    const currentID = Number(toValue(syncId))
    if (!currentID || terminal.value) return

    closeRealtime()
    loading.value = !task.value
    errorMessage.value = ''
    unsupported.value = typeof EventSource === 'undefined'
    if (unsupported.value) {
      connectionState.value = 'idle'
      startFallbackPolling(currentID)
      return
    }

    connectionState.value = 'connecting'
    const currentSource = new EventSource(`/api/sync/tasks/${currentID}/stream`)
    source.value = currentSource
    unregisterSource = registerRealtimeSource(() => closeSource(currentSource))
    currentSource.onopen = () => {
      if (source.value === currentSource) {
        connected.value = true
        connectionState.value = 'connected'
      }
    }
    currentSource.onerror = (event) => {
      if ('data' in event) return
      if (source.value !== currentSource) return
      // CLOSED 表示浏览器不会再自动重连，改用与不支持 SSE 时相同的降级轮询。
      if (currentSource.readyState === EventSource.CLOSED) {
        closeSource(currentSource)
        startFallbackPolling(currentID)
        return
      }
      connected.value = false
      connectionState.value = 'reconnecting'
    }
    const listen = (eventType: SyncTaskStreamMessage['type']) => {
      currentSource.addEventListener(eventType, (event) => {
        if (source.value !== currentSource) return
        try {
          handleMessage(JSON.parse((event as MessageEvent<string>).data) as SyncTaskStreamMessage)
        } catch {
          errorMessage.value = '同步任务实时数据解析失败'
        }
      })
    }
    ;(
      ['snapshot', 'task_patch', 'log_append', 'complete', 'resync_required', 'error'] as const
    ).forEach(listen)
  }

  const disconnect = () => {
    closeRealtime()
  }

  const clearLogs = () => {
    logs.value = []
  }

  watch(
    toRef(syncId),
    () => {
      closeRealtime()
      task.value = null
      logs.value = []
      terminal.value = false
      logCursor.value = 0
      logPath.value = ''
      if (immediate) connect()
    },
    { immediate },
  )

  onBeforeUnmount(disconnect)

  return {
    task: readonly(task),
    logs: readonly(logs),
    loading: readonly(loading),
    connected: readonly(connected),
    connectionState: readonly(connectionState),
    terminal: readonly(terminal),
    unsupported: readonly(unsupported),
    errorMessage: readonly(errorMessage),
    isRunning,
    logCursor: readonly(logCursor),
    logPath: readonly(logPath),
    clearLogs,
    reconnect: connect,
    disconnect,
  }
}
