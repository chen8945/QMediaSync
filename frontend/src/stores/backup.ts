import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { ElMessage } from 'element-plus'
import type { AxiosInstance } from 'axios'
import type { BackupOperationState, BackupOperationView, BackupTaskType } from '@/typing'
import { SERVER_URL } from '@/const'

/** 状态令牌只走请求头，绝不进入 URL、Cookie 或持久化浏览器存储。 */
const OPERATION_TOKEN_HEADER = 'X-Backup-Operation-Token'
const POLL_INTERVAL_MS = 2000
const API_SUCCESS_CODE = 200
const MAX_RETRY_COUNT = 3

const TERMINAL_STATES: BackupOperationState[] = ['completed', 'failed', 'cancelled']

export const useBackupStore = defineStore('backup', () => {
  const operation = ref<BackupOperationView | null>(null)
  const taskType = ref<BackupTaskType>(null)
  const showProgressDialog = ref(false)
  const errorRetryCount = ref(0)

  // 令牌只保存在操作页内存中，页面刷新后由备份列表的最近一次终态收敛。
  // 下划线前缀表示内部状态：setup store 必须返回全部 ref，否则 DevTools 与插件看不到它们，
  // 但外部不应直接读写，且任何持久化插件都不得纳入 _operationToken。
  const _operationId = ref('')
  const _operationToken = ref('')

  // 轮询句柄不是状态，保持为普通变量，避免无意义的响应式开销。
  let pollingTimer: number | null = null
  let pollingGeneration = 0
  let pollingInFlight = false
  let pollingHTTP: AxiosInstance | null = null
  let visibilityListenerRegistered = false

  const isRunning = computed(() =>
    operation.value !== null
      ? !TERMINAL_STATES.includes(operation.value.state)
      : _operationId.value !== '',
  )
  // coordinator 仅在进入 running 前启用维护屏障；rolling_back 期间仍保持维护。
  const isMaintenance = computed(
    () => operation.value?.state === 'running' || operation.value?.state === 'rolling_back',
  )
  const progressPercent = computed(() => {
    const progress = operation.value?.progress
    if (!progress || progress.total <= 0) return 0
    return Math.min(100, Math.round((progress.completed / progress.total) * 100))
  })

  /** 受理响应已保证 operation 存在，因此立即首查，随后每次完成后间隔 2 秒轮询。 */
  const startOperationPolling = (
    type: 'backup' | 'restore',
    accepted: { operation_id: string; token: string },
    http: AxiosInstance,
  ) => {
    taskType.value = type
    _operationId.value = accepted.operation_id
    _operationToken.value = accepted.token
    operation.value = null
    errorRetryCount.value = 0
    showProgressDialog.value = true
    stopProgressPolling()
    pollingHTTP = http
    startVisibilityListener()
    resumeProgressPolling()
  }

  const isDocumentHidden = () => typeof document !== 'undefined' && document.hidden

  const clearProgressPollingTimer = () => {
    if (pollingTimer !== null) {
      clearTimeout(pollingTimer)
      pollingTimer = null
    }
  }

  const pauseProgressPolling = () => {
    pollingGeneration++
    clearProgressPollingTimer()
  }

  const scheduleProgressPolling = (http: AxiosInstance, generation: number) => {
    if (generation !== pollingGeneration || isDocumentHidden() || pollingTimer !== null) return

    pollingTimer = window.setTimeout(() => {
      pollingTimer = null
      void pollProgress(http, generation)
    }, POLL_INTERVAL_MS)
  }

  const resumeProgressPolling = () => {
    if (
      pollingInFlight ||
      isDocumentHidden() ||
      !pollingHTTP ||
      !_operationId.value ||
      !_operationToken.value
    )
      return

    void pollProgress(pollingHTTP, pollingGeneration)
  }

  const onVisibilityChange = () => {
    if (isDocumentHidden()) {
      pauseProgressPolling()
      return
    }
    resumeProgressPolling()
  }

  const startVisibilityListener = () => {
    if (visibilityListenerRegistered || typeof document === 'undefined') return
    document.addEventListener('visibilitychange', onVisibilityChange)
    visibilityListenerRegistered = true
  }

  const stopVisibilityListener = () => {
    if (!visibilityListenerRegistered || typeof document === 'undefined') return
    document.removeEventListener('visibilitychange', onVisibilityChange)
    visibilityListenerRegistered = false
  }

  const pollProgress = async (http: AxiosInstance, generation: number) => {
    if (
      !http ||
      generation !== pollingGeneration ||
      pollingInFlight ||
      isDocumentHidden() ||
      !_operationId.value ||
      !_operationToken.value
    )
      return

    const operationID = _operationId.value
    const operationToken = _operationToken.value
    pollingInFlight = true

    try {
      const res = await http.get<{ code: number; message?: string; data: BackupOperationView }>(
        `${SERVER_URL}/backup/status`,
        {
          params: { operation_id: operationID },
          headers: { [OPERATION_TOKEN_HEADER]: operationToken },
        },
      )
      if (generation !== pollingGeneration) return
      if (res.data.code !== API_SUCCESS_CODE) {
        stopProgressPolling()
        ElMessage.error(res.data.message || '备份任务状态查询失败，请通过备份记录确认最终结果')
        showProgressDialog.value = false
        resetState()
        return
      }

      operation.value = res.data.data
      errorRetryCount.value = 0
      if (TERMINAL_STATES.includes(operation.value.state)) {
        stopProgressPolling()
        handleTaskComplete(operation.value)
      }
    } catch (error) {
      if (generation !== pollingGeneration) return
      console.error('轮询进度失败：', error)
      errorRetryCount.value++

      if (errorRetryCount.value >= MAX_RETRY_COUNT) {
        stopProgressPolling()
        ElMessage.error('网络连接失败，页面即将刷新…')
        setTimeout(() => {
          location.reload()
        }, 2000)
      }
    } finally {
      pollingInFlight = false
      if (generation === pollingGeneration) scheduleProgressPolling(http, generation)
      else resumeProgressPolling()
    }
  }

  /** 终态文案不得从运行标记推导，恢复失败必须区分自动回滚结果。 */
  const handleTaskComplete = (view: BackupOperationView) => {
    const isRestore = view.kind === 'restore'
    switch (view.state) {
      case 'completed':
        ElMessage.success(isRestore ? '恢复完成！' : '备份完成！')
        break
      case 'cancelled':
        ElMessage.info(isRestore ? '恢复已取消' : '备份已取消')
        break
      case 'failed':
        if (isRestore && view.rollback_state === 'succeeded') {
          ElMessage.error('恢复失败，已自动回滚')
        } else if (isRestore && view.rollback_state === 'failed') {
          ElMessage.error('恢复失败，自动回滚失败，请查看控制台日志')
        } else {
          ElMessage.error(isRestore ? '恢复失败' : '备份失败')
        }
        break
    }

    setTimeout(() => {
      showProgressDialog.value = false
      resetState()
    }, 1500)
  }

  const stopProgressPolling = () => {
    pollingGeneration++
    pollingHTTP = null
    clearProgressPollingTimer()
    stopVisibilityListener()
  }

  const resetState = () => {
    operation.value = null
    taskType.value = null
    _operationId.value = ''
    _operationToken.value = ''
    errorRetryCount.value = 0
  }

  return {
    operation,
    taskType,
    showProgressDialog,
    errorRetryCount,
    _operationId,
    _operationToken,
    isRunning,
    isMaintenance,
    progressPercent,
    startOperationPolling,
    stopProgressPolling,
    resetState,
  }
})
