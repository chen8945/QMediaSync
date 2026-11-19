import { registerRealtimeSource } from '@/composables/realtimeSources'
import { fetchSyncTask, fetchSyncTaskResult } from '@/api/syncTasks'
import { parseHttpError } from '@/http/errors'
import { isSyncTaskStreamTerminal, isSyncTaskTerminal } from '@/utils/syncTaskStatusUtils'
import type {
  SyncTask,
  SyncTaskEventPayload,
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
}

type SyncTaskConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting'

export function useSyncTaskStream(
  syncId: MaybeRefOrGetter<number | string>,
  options: UseSyncTaskStreamOptions = {},
) {
  const { immediate = true } = options
  const task = shallowRef<SyncTask | null>(null)
  const loading = shallowRef(false)
  const connected = shallowRef(false)
  const connectionState = shallowRef<SyncTaskConnectionState>('idle')
  const terminal = shallowRef(false)
  const unsupported = shallowRef(false)
  const errorMessage = shallowRef('')
  const logPath = shallowRef('')
  const source = shallowRef<EventSource | null>(null)
  let unregisterSource: (() => void) | null = null
  let pollTimer: ReturnType<typeof setInterval> | null = null
  let revision = 0
  let sequence = 0
  let taskController: AbortController | null = null
  let resultController: AbortController | null = null
  const resultErrorMessage = shallowRef('')
  const resultLoading = shallowRef(false)
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
    taskController?.abort()
    resultController?.abort()
    closeSource()
    clearPolling()
  }

  const applySnapshot = (snapshot: SyncTaskSnapshot) => {
    task.value = snapshot.task
    logPath.value = snapshot.log_path
    loading.value = false
    terminal.value = isSyncTaskStreamTerminal(snapshot.task)
  }

  const applyTaskPatch = (payload: SyncTaskEventPayload) => {
    if (payload.deleted) {
      terminal.value = true
      errorMessage.value = '同步记录已删除'
      return
    }
    if (!task.value) return

    const next = { ...task.value }
    // 生成结果一旦终结，后续后台事件只更新自身状态。
    if (!isSyncTaskTerminal(next.status)) {
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
      if (payload.net_file_duration_ms !== undefined)
        next.net_file_duration_ms = payload.net_file_duration_ms
      if (payload.local_file_duration_ms !== undefined)
        next.local_file_duration_ms = payload.local_file_duration_ms
      if (payload.generation_duration_ms !== undefined)
        next.generation_duration_ms = payload.generation_duration_ms
      if (typeof payload.fail_reason === 'string') next.fail_reason = payload.fail_reason
      if (payload.scan_result !== undefined) next.scan_result = payload.scan_result
    }
    if (typeof payload.updated_at === 'number') next.updated_at = payload.updated_at
    if (payload.ledger_status !== undefined) next.ledger_status = payload.ledger_status
    if (payload.ledger_finished_at !== undefined)
      next.ledger_finished_at = payload.ledger_finished_at
    if (payload.ledger_error !== undefined) next.ledger_error = payload.ledger_error
    if (payload.total_duration_ms !== undefined) next.total_duration_ms = payload.total_duration_ms
    task.value = next
  }

  const handleMessage = (message: SyncTaskStreamMessage) => {
    if (message.type === 'snapshot') {
      revision++
      sequence = message.sequence || 0
      applySnapshot(message.data as SyncTaskSnapshot)
      void loadResult()
      if (terminal.value) closeSource()
      return
    }
    if (message.type === 'task_patch') {
      if (message.sequence && message.sequence <= sequence) return
      sequence = message.sequence || sequence
      revision++
      applyTaskPatch(message.data as SyncTaskEventPayload)
      return
    }
    if (message.type === 'complete') {
      revision++
      applyTaskPatch(message.data as SyncTaskEventPayload)
      terminal.value = true
      closeSource()
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
    const requestRevision = revision
    const controller = new AbortController()
    taskController = controller
    fallbackRequest = request
    const isCurrent = () =>
      generation === fallbackGeneration &&
      revision === requestRevision &&
      Number(toValue(syncId)) === currentID
    try {
      const nextTask = await fetchSyncTask(currentID, controller.signal)
      if (!isCurrent()) return
      task.value = { ...nextTask, scan_result: nextTask.scan_result || task.value?.scan_result }
      logPath.value = nextTask.log_path || logPath.value
      errorMessage.value = ''
      terminal.value = isSyncTaskStreamTerminal(nextTask)
      loading.value = false
      if (terminal.value) clearPolling()
    } catch (error) {
      if (
        controller.signal.aborted ||
        generation !== fallbackGeneration ||
        Number(toValue(syncId)) !== currentID
      )
        return
      const failure = parseHttpError(error, { fallbackMessage: '加载同步任务失败' })
      const stop = [401, 403, 404].includes(failure.response?.status || 0)
      if (!isCurrent() && !stop) return
      if (failure.shouldNotify) errorMessage.value = failure.message
      loading.value = false
      if (stop) {
        // 首次降级读取后也不能由 then 重新创建 timer。
        fallbackGeneration++
        clearPolling()
        closeSource()
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
        task.value &&
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

    sequence = 0
    connectionState.value = 'connecting'
    void loadFallbackTask(currentID, fallbackGeneration)
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
        fallbackGeneration++
        taskController?.abort()
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
    ;(['snapshot', 'task_patch', 'complete', 'resync_required', 'error'] as const).forEach(listen)
  }

  const disconnect = () => {
    closeRealtime()
  }

  const loadResult = async () => {
    const currentID = task.value?.id
    if (!currentID || !isSyncTaskTerminal(task.value!.status) || task.value?.scan_result) return
    if (resultController && !resultController.signal.aborted) return
    resultController?.abort()
    const controller = new AbortController()
    resultController = controller
    resultLoading.value = true
    resultErrorMessage.value = ''
    try {
      const result = await fetchSyncTaskResult(currentID, controller.signal)
      if (!controller.signal.aborted && task.value?.id === currentID && !task.value.scan_result) {
        task.value = { ...task.value, scan_result: result }
      }
    } catch (error) {
      if (controller.signal.aborted) return
      const failure = parseHttpError(error, { fallbackMessage: '加载处理结果失败' })
      if (failure.shouldNotify) resultErrorMessage.value = failure.message
    } finally {
      if (resultController === controller) {
        resultController = null
        resultLoading.value = false
      }
    }
  }

  watch([() => task.value?.id, () => task.value?.status], () => void loadResult())

  watch(
    toRef(syncId),
    () => {
      closeRealtime()
      task.value = null
      resultErrorMessage.value = ''
      terminal.value = false
      logPath.value = ''
      if (immediate) connect()
    },
    { immediate },
  )

  onBeforeUnmount(disconnect)

  return {
    task: readonly(task),
    loading: readonly(loading),
    connected: readonly(connected),
    connectionState: readonly(connectionState),
    terminal: readonly(terminal),
    unsupported: readonly(unsupported),
    errorMessage: readonly(errorMessage),
    isRunning,
    logPath: readonly(logPath),
    resultErrorMessage: readonly(resultErrorMessage),
    resultLoading: readonly(resultLoading),
    reloadResult: loadResult,
    reconnect: connect,
    disconnect,
  }
}
