import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { ElMessage } from 'element-plus'
import type { AxiosInstance } from 'axios'
import type { BackupTaskType, BackupProgress } from '@/typing'
import { fetchBackupStatus, getBackupTaskStatus, backupPublicMessages } from '@/api/backup'
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
  let pollingHttp: AxiosInstance | null = null
  const errorRetryCount = ref(0)

  const MAX_RETRY_COUNT = 3

  const isRunning = computed(() => progress.value?.running === true)

  const startProgressPolling = (
    type: 'backup' | 'restore',
    id: number | undefined,
    http: AxiosInstance,
  ) => {
    void id
    pollingHttp = http
    pollingGeneration.value += 1
    const generation = pollingGeneration.value
    taskType.value = type
    showProgressDialog.value = true
    errorRetryCount.value = 0
    stopProgressPolling(false)
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
      const statusData = await fetchBackupStatus(http)
      if (generation !== pollingGeneration.value || !pageVisible.value || document.hidden) return
      const status = getBackupTaskStatus(statusData)
      const ratio = statusData.total > 0 ? (statusData.count / statusData.total) * 100 : 0
      progress.value = {
        running: status === 'running',
        status,
        progress: Number.isFinite(ratio) ? Math.max(0, Math.min(100, Math.floor(ratio))) : 0,
        elapsed_seconds: statusData.elapsed,
        estimated_seconds: 0,
        current_step: status === 'running' ? statusData.desc : taskResultMessage(status),
        processed_tables: statusData.count,
        total_tables: statusData.total,
      }
      errorRetryCount.value = 0
      if (status !== 'running') {
        stopProgressPolling()
        handleTaskComplete(status)
      }
    } catch (error) {
      if (generation !== pollingGeneration.value || !pageVisible.value || document.hidden) return
      const parsed = parseHttpError(error, {
        fallbackMessage: '查询备份或恢复进度失败',
        publicMessages: backupPublicMessages,
      })
      if (!parsed.shouldNotify) return
      console.error('轮询进度失败', parsed.diagnostics)
      errorRetryCount.value++

      if (errorRetryCount.value >= MAX_RETRY_COUNT) {
        stopProgressPolling()
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

  const taskResultMessage = (status: string) => {
    const action = taskType.value === 'restore' ? '恢复' : '备份'
    if (status === 'completed') return `${action}任务完成！`
    if (status === 'failed')
      return taskType.value === 'restore'
        ? '恢复任务失败，部分数据可能已恢复，请查看服务日志并核验数据。'
        : '备份任务失败，请查看服务日志。'
    return `${action}任务结果尚未确认，请查看服务日志并核验结果。`
  }

  const handleTaskComplete = (status: string) => {
    switch (status) {
      case 'completed':
        ElMessage.success(taskResultMessage(status))
        break
      case 'failed':
        ElMessage.error(taskResultMessage(status))
        break
      default:
        ElMessage.warning(taskResultMessage(status))
        break
    }

    const completedGeneration = pollingGeneration.value
    setTimeout(() => {
      if (completedGeneration !== pollingGeneration.value) return
      showProgressDialog.value = false
      resetState()
    }, 1500)
  }

  const stopProgressPolling = (invalidate = true) => {
    if (invalidate) pollingGeneration.value += 1
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
    } else if (taskType.value && (!progress.value || isRunning.value) && pollingHttp) {
      startProgressPolling(taskType.value, undefined, pollingHttp)
    }
  }

  const resetState = () => {
    progress.value = null
    taskType.value = null
    pollingGeneration.value += 1
    errorRetryCount.value = 0
  }

  if (typeof document !== 'undefined')
    document.addEventListener('visibilitychange', handleVisibilityChange)

  return {
    progress,
    taskType,
    showProgressDialog,
    errorRetryCount,
    isRunning,
    startProgressPolling,
    stopProgressPolling,
    resetState,
  }
})
