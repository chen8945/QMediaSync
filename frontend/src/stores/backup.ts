import { defineStore } from 'pinia'
import { ref, computed, shallowRef, onScopeDispose } from 'vue'
import { ElMessage } from 'element-plus'
import type { AxiosInstance } from 'axios'
import type { BackupTaskType, BackupProgress } from '@/typing'
import {
  fetchBackupStatus,
  getBackupTaskStatus,
  backupPublicMessages,
  restartAfterRestore,
  type BackupStatusResponse,
} from '@/api/backup'
import { parseHttpError } from '@/http/errors'

export const useBackupStore = defineStore('backup', () => {
  const progress = ref<BackupProgress | null>(null)
  const taskType = ref<BackupTaskType>(null)
  const showProgressDialog = ref(false)
  const pollingTimer = ref<number | null>(null)
  const delayedPollingTimer = ref<number | null>(null)
  const pollingGeneration = ref(0)
  const pollInFlight = ref(false)
  const pageVisible = ref(true)
  const isPolling = shallowRef(false)
  const restartRequired = shallowRef(false)
  const restartSupported = shallowRef(false)
  const restartPhase = shallowRef<'idle' | 'requesting' | 'waiting' | 'unknown' | 'ready'>('idle')
  const restartError = shallowRef('')
  let restartDeadline = 0
  const progressQueryError = shallowRef('')
  let pollingHttp: AxiosInstance | null = null
  // 恢复后浏览器会话会失效，凭证只留在当前页面内存中。
  let restoreReceipt: string | undefined
  const errorRetryCount = ref(0)

  const MAX_RETRY_COUNT = 3

  const isRunning = computed(() => progress.value?.running === true)
  const canRestart = computed(
    () =>
      restartRequired.value &&
      restartSupported.value &&
      restartPhase.value === 'idle' &&
      !!restoreReceipt &&
      (progress.value?.status === 'completed' || progress.value?.status === 'failed'),
  )

  const startProgressPolling = (
    type: 'backup' | 'restore',
    id: number | undefined,
    http: AxiosInstance,
    receipt?: string,
  ) => {
    void id
    resetState()
    pollingHttp = http
    restoreReceipt = type === 'restore' ? receipt : undefined
    taskType.value = type
    showProgressDialog.value = true
    errorRetryCount.value = 0
    progressQueryError.value = ''
    resumeProgressPolling()
  }

  const resumeProgressPolling = () => {
    if (!pollingHttp) return
    const http = pollingHttp
    pollingGeneration.value += 1
    const generation = pollingGeneration.value
    stopProgressPolling(false)
    isPolling.value = true
    const schedule = () => {
      if (!pageVisible.value || generation !== pollingGeneration.value || pollingTimer.value) return
      pollingTimer.value = window.setInterval(() => {
        if (pageVisible.value && generation === pollingGeneration.value)
          void pollProgress(http, generation)
      }, 2000)
    }
    if (pageVisible.value && !document.hidden) {
      delayedPollingTimer.value = window.setTimeout(() => {
        delayedPollingTimer.value = null
        if (generation !== pollingGeneration.value || !pageVisible.value || document.hidden) return
        void pollProgress(http, generation)
        schedule()
      }, 3000)
    }
  }

  const pollProgress = async (http: AxiosInstance, generation = pollingGeneration.value) => {
    if (
      !pageVisible.value ||
      document.hidden ||
      generation !== pollingGeneration.value ||
      pollInFlight.value
    )
      return
    pollInFlight.value = true
    try {
      if (restartPhase.value === 'waiting' && Date.now() >= restartDeadline) {
        stopProgressPolling()
        restartPhase.value = 'unknown'
        restartError.value = '尚未确认服务重新上线，请检查服务状态，或重新检查；不要重复提交恢复。'
        return
      }
      const statusData = await fetchBackupStatus(http, restoreReceipt)
      if (generation !== pollingGeneration.value || !pageVisible.value || document.hidden) return
      if (restartPhase.value === 'waiting') {
        if (statusData.restart_requested !== true) {
          stopProgressPolling()
          restartPhase.value = 'idle'
          restartError.value = '重启尚未执行，请重试；若持续失败，请手动重启服务。'
        } else {
          restartError.value = ''
        }
        return
      }
      const status = getBackupTaskStatus(statusData)
      restartRequired.value = taskType.value === 'restore' && statusData.restart_required === true
      restartSupported.value = statusData.restart_supported === true
      const ratio = statusData.total > 0 ? (statusData.count / statusData.total) * 100 : 0
      progress.value = {
        running: status === 'running',
        status,
        progress: Number.isFinite(ratio) ? Math.max(0, Math.min(100, Math.floor(ratio))) : 0,
        elapsed_seconds: statusData.elapsed,
        estimated_seconds: 0,
        current_step:
          status === 'running' ? statusData.desc : taskResultMessage(status, statusData),
        processed_tables: statusData.count,
        total_tables: statusData.total,
      }
      errorRetryCount.value = 0
      progressQueryError.value = ''
      if (status !== 'running') {
        stopProgressPolling()
        handleTaskComplete(status)
        if (restartRequired.value && statusData.restart_requested === true) checkRestartStatus()
      }
    } catch (error) {
      if (generation !== pollingGeneration.value || !pageVisible.value || document.hidden) return
      const parsed = parseHttpError(error, {
        fallbackMessage: '查询备份或恢复进度失败',
        publicMessages: backupPublicMessages,
      })
      if (restartPhase.value === 'waiting') {
        // 只有应用明确拒绝旧进程回执，才确认新进程已上线；普通 401 可能来自代理。
        if (
          parsed.diagnostics.status === 401 &&
          parsed.diagnostics.errorCode === 'RESTORE_RECEIPT_INVALID'
        ) {
          stopProgressPolling()
          restartPhase.value = 'ready'
          restartError.value = ''
        }
        return
      }
      if (!parsed.shouldNotify) return
      console.error('轮询进度失败', parsed.diagnostics)
      errorRetryCount.value++

      if (errorRetryCount.value >= MAX_RETRY_COUNT) {
        stopProgressPolling()
        if (taskType.value === 'restore') {
          progressQueryError.value = '暂时无法查询恢复结果，请重新查询；不要重复提交恢复。'
          progress.value = {
            ...progress.value,
            running: false,
            status: 'unknown',
            current_step: taskResultMessage('unknown'),
          }
          ElMessage.warning(progressQueryError.value)
          return
        }
        ElMessage.error(`${parsed.message}。页面即将刷新…`)
        const stoppedGeneration = pollingGeneration.value
        setTimeout(() => {
          if (
            stoppedGeneration !== pollingGeneration.value ||
            !pageVisible.value ||
            document.hidden
          )
            return
          location.reload()
        }, 2000)
      }
    } finally {
      pollInFlight.value = false
    }
  }

  const taskResultMessage = (status: string, data?: BackupStatusResponse) => {
    const action = taskType.value === 'restore' ? '恢复' : '备份'
    if (status === 'failed') {
      // 旧版本可能返回原始错误；只有约定的安全错误码才允许展示服务端原因。
      const safeReason = [
        'BACKUP_ARCHIVE_INVALID',
        'BACKUP_ARCHIVE_UNSUPPORTED',
        'BACKUP_ARCHIVE_LIMIT',
        'RESTORE_KEY_MISMATCH',
        'BACKUP_FAILED',
        'RESTORE_FAILED',
      ].includes(data?.error_code ?? '')
        ? data?.error_msg?.trim()
        : ''
      if (taskType.value === 'backup' && safeReason) return safeReason
      if (taskType.value === 'restore' && (safeReason || data?.restore_outcome)) {
        const outcomes = {
          not_started: '本次恢复未修改数据库。',
          rolled_back: '数据库改动已确认回滚，原有数据保留。',
          committed: '数据库恢复已提交，后续处理未完成。',
          uncertain: '数据库提交或回滚结果不确定，请核验数据库，不要重复提交恢复。',
        }
        const outcome =
          data?.restore_outcome && Object.hasOwn(outcomes, data.restore_outcome)
            ? outcomes[data.restore_outcome]
            : undefined
        return [
          safeReason || '恢复任务失败，请查看服务日志。',
          outcome || '数据库结果尚未确认，请查看服务日志并核验。',
          restartRequired.value ? '服务已暂停，请重启服务后重新登录。' : '',
        ]
          .filter(Boolean)
          .join(' ')
      }
    }
    if (status === 'completed') {
      if (taskType.value === 'restore' && data?.restore_outcome === 'committed')
        return restartRequired.value
          ? '数据库恢复已提交。请重启应用服务，服务上线后重新登录。'
          : '数据库恢复已提交。'
      return restartRequired.value
        ? '数据库已恢复。请重启应用服务，服务上线后重新登录。'
        : `${action}任务完成！`
    }
    if (status === 'failed')
      return taskType.value === 'restore'
        ? restartRequired.value
          ? '恢复任务失败，服务已暂停。请查看服务日志，重启服务后核验数据库。'
          : '恢复任务失败，请查看服务日志并核验结果。'
        : '备份任务失败，请查看服务日志。'
    return `${action}任务结果尚未确认，请查看服务日志并核验结果。`
  }

  const checkRestartStatus = () => {
    if (
      !pollingHttp ||
      !restoreReceipt ||
      !restartRequired.value ||
      restartPhase.value === 'requesting'
    )
      return
    restartPhase.value = 'waiting'
    restartError.value = ''
    restartDeadline = Date.now() + 60000
    resumeProgressPolling()
  }

  const requestRestart = async () => {
    if (!canRestart.value || !pollingHttp || !restoreReceipt) return
    stopProgressPolling()
    const generation = pollingGeneration.value
    restartPhase.value = 'requesting'
    restartError.value = ''
    let responseLost = false
    try {
      const result = await restartAfterRestore(pollingHttp, restoreReceipt)
      if (generation !== pollingGeneration.value) return
      if (result.restart_requested !== true) throw new Error('重启请求未确认')
    } catch (error) {
      if (generation !== pollingGeneration.value) return
      const parsed = parseHttpError(error, { fallbackMessage: '重启请求未确认，请检查服务状态' })
      const explicitlyRejected = [
        'RESTORE_RESTART_NOT_READY',
        'RESTORE_RESTART_UNSUPPORTED',
        'RESTORE_RESTART_PREPARATION_FAILED',
      ].includes(parsed.diagnostics.errorCode ?? '')
      if (parsed.response && (parsed.response.status < 500 || explicitlyRejected)) {
        restartPhase.value = 'idle'
        restartError.value = parsed.message
        return
      }
      // 写入响应丢失或代理返回未知 5xx 时只查询状态，不能推断重启未执行。
      responseLost = true
    }
    if (generation !== pollingGeneration.value) return
    restartPhase.value = 'waiting'
    checkRestartStatus()
    if (responseLost) restartError.value = '重启请求结果尚未确认，正在检查服务状态；不会重复提交。'
  }

  const handleTaskComplete = (status: string) => {
    const message = progress.value?.current_step || taskResultMessage(status)
    switch (status) {
      case 'completed':
        ElMessage.success(message)
        break
      case 'failed':
        ElMessage.error(message)
        break
      default:
        ElMessage.warning(message)
        break
    }

    // 恢复结果需要用户确认，不能自动关闭后丢失重启说明。
    if (taskType.value === 'restore') return

    const completedGeneration = pollingGeneration.value
    setTimeout(() => {
      if (completedGeneration !== pollingGeneration.value) return
      showProgressDialog.value = false
      resetState()
    }, 1500)
  }

  const stopProgressPolling = (invalidate = true) => {
    if (invalidate) {
      pollingGeneration.value += 1
      isPolling.value = false
    }
    if (delayedPollingTimer.value) {
      clearTimeout(delayedPollingTimer.value)
      delayedPollingTimer.value = null
    }
    if (pollingTimer.value) {
      clearInterval(pollingTimer.value)
      pollingTimer.value = null
    }
  }

  const handleVisibilityChange = () => {
    pageVisible.value = !document.hidden
    if (!pageVisible.value) {
      stopProgressPolling(false)
    } else if (isPolling.value) {
      resumeProgressPolling()
    }
  }

  const resetState = () => {
    stopProgressPolling()
    progress.value = null
    taskType.value = null
    pollingGeneration.value += 1
    errorRetryCount.value = 0
    restartRequired.value = false
    restartSupported.value = false
    restartPhase.value = 'idle'
    restartError.value = ''
    restartDeadline = 0
    progressQueryError.value = ''
    restoreReceipt = undefined
  }

  const retryProgressPolling = () => {
    if (!taskType.value || !pollingHttp || isPolling.value) return
    errorRetryCount.value = 0
    resumeProgressPolling()
  }

  const closeProgressDialog = () => {
    if (isRunning.value || restartRequired.value || progress.value?.status === 'unknown') return
    stopProgressPolling()
    showProgressDialog.value = false
    resetState()
  }

  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', handleVisibilityChange)
    onScopeDispose(() => {
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      stopProgressPolling()
    })
  }

  return {
    progress,
    taskType,
    showProgressDialog,
    errorRetryCount,
    restartRequired,
    restartSupported,
    restartPhase,
    restartError,
    canRestart,
    progressQueryError,
    isRunning,
    isPolling,
    startProgressPolling,
    stopProgressPolling,
    resetState,
    retryProgressPolling,
    requestRestart,
    checkRestartStatus,
    closeProgressDialog,
  }
})
