import { onMounted, onUnmounted, ref } from 'vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { ElMessage } from 'element-plus'

import * as updateAPI from '@/api/update'
import { parseHttpError } from '@/http/errors'
import type { UpdateInfo, UpdateProgress, UpdateChannel, UpdateStatus } from '@/api/update'
export type { UpdateInfo, UpdateProgress, UpdateChannel, UpdateStatus } from '@/api/update'

export function isUpdateTerminalStatus(
  status: string,
): status is Extract<UpdateStatus, 'completed' | 'failed' | 'cancelled'> {
  return status === 'completed' || status === 'failed' || status === 'cancelled'
}

export function isUpdateRunningStatus(
  status: string,
): status is Extract<UpdateStatus, 'downloading' | 'install'> {
  return status === 'downloading' || status === 'install'
}

export function useUpdate() {
  const http = useHttpClient()

  const updateList = ref<UpdateInfo[]>([])
  const updateLoading = ref(false)
  const isUpdating = ref(false)
  const updatingVersion = ref<string>('')
  const updateProgress = ref<UpdateProgress>({
    progress: 0,
    total_size: 0,
    downloaded: 0,
    status: '',
  })
  const showUpdateCompleteDialog = ref(false)
  const countdown = ref(30)
  const updateChannel = ref<UpdateChannel>('github')

  let progressTimer: ReturnType<typeof setInterval> | null = null
  let progressInFlight = false
  let pollingGeneration = 0
  let pageActive = true
  let countdownTimer: ReturnType<typeof setInterval> | null = null

  const loadUpdateList = async (force = false) => {
    try {
      updateLoading.value = true
      const updates = await updateAPI.fetchUpdateList(http, updateChannel.value, force)
      if (!pageActive) return
      updateList.value = updates.map((item) => ({ ...item, url: item.url || '' }))
    } catch (error) {
      if (!pageActive) return
      const parsed = parseUpdateError(error, '加载最新版本列表失败', 'get', '/update/last')
      if (parsed.shouldNotify) ElMessage.error(parsed.message)
      updateList.value = []
    } finally {
      updateLoading.value = false
    }
  }

  const parseUpdateError = (
    error: unknown,
    fallbackMessage: string,
    method: string,
    path: string,
  ) => {
    const parsed = parseHttpError(error, {
      fallbackMessage,
      publicMessages: updateAPI.updatePublicMessages,
      request: { method, url: `${SERVER_URL}${path}` },
    })
    if (parsed.shouldNotify && !updateAPI.isUpdateNotStarted(parsed)) {
      console.error(fallbackMessage, parsed.diagnostics)
    }
    return parsed
  }

  const checkUpdateStatusOnLoad = async () => {
    const generation = pollingGeneration
    if (!pageActive) return
    try {
      const progressData = await updateAPI.fetchUpdateProgress(http)
      if (!pageActive || generation !== pollingGeneration) return
      if (progressData && isUpdateRunningStatus(progressData.status)) {
        isUpdating.value = true
        updatingVersion.value = progressData.version || ''
        updateProgress.value = {
          progress: progressData.progress || 0,
          total_size: progressData.total_size || 0,
          downloaded: progressData.downloaded || 0,
          status: progressData.status,
        }
        startProgressPolling()
      }
    } catch (error) {
      if (!pageActive || generation !== pollingGeneration) return
      const parsed = parseUpdateError(error, '检查更新状态失败', 'get', '/update/progress')
      if (parsed.shouldNotify && !updateAPI.isUpdateNotStarted(parsed))
        ElMessage.error(parsed.message)
    }
  }

  const resetUpdateState = () => {
    if (countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
    showUpdateCompleteDialog.value = false
    isUpdating.value = false
    updatingVersion.value = ''
    updateProgress.value = {
      progress: 0,
      total_size: 0,
      downloaded: 0,
      status: '',
    }
  }

  const updateToVersion = async (version: string) => {
    isUpdating.value = true
    updatingVersion.value = version
    updateProgress.value = {
      progress: 0,
      total_size: 0,
      downloaded: 0,
      status: 'downloading',
    }

    const generation = ++pollingGeneration
    try {
      await updateAPI.startUpdate(http, version, updateChannel.value)
      if (!pageActive || generation !== pollingGeneration) return
      startProgressPolling()
    } catch (error) {
      if (!pageActive || generation !== pollingGeneration) return
      const parsed = parseUpdateError(error, '触发版本更新失败', 'post', '/update/to-version')
      resetUpdateState()
      if (parsed.shouldNotify) ElMessage.error(parsed.message)
    }
  }

  const scheduleProgressPolling = (generation: number) => {
    if (!pageActive || document.hidden || generation !== pollingGeneration || progressTimer) return
    progressTimer = setInterval(() => {
      if (!pageActive || document.hidden || generation !== pollingGeneration) return
      void checkUpdateProgress(generation)
    }, 1000)
  }

  const startProgressPolling = () => {
    pollingGeneration += 1
    const generation = pollingGeneration
    stopProgressPolling(false)
    if (!pageActive || document.hidden) return
    void checkUpdateProgress(generation)
    scheduleProgressPolling(generation)
  }

  const stopProgressPolling = (invalidate = true) => {
    if (invalidate) pollingGeneration += 1
    if (progressTimer) {
      clearInterval(progressTimer)
      progressTimer = null
    }
  }

  const showUpdateCompleteNotification = () => {
    showUpdateCompleteDialog.value = true
    countdown.value = 30

    if (countdownTimer) {
      clearInterval(countdownTimer)
    }

    countdownTimer = setInterval(() => {
      countdown.value--
      if (countdown.value <= 0) {
        if (countdownTimer) {
          clearInterval(countdownTimer)
          countdownTimer = null
        }
        window.location.reload()
      }
    }, 1000)
  }

  const manuallyRefresh = () => {
    if (countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
    window.location.reload()
  }

  const checkUpdateProgress = async (generation = pollingGeneration) => {
    if (!pageActive || document.hidden || generation !== pollingGeneration || progressInFlight)
      return
    progressInFlight = true
    try {
      const progressData = await updateAPI.fetchUpdateProgress(http)
      if (!pageActive || document.hidden || generation !== pollingGeneration) return
      if (!progressData) return

      if (progressData.progress !== undefined) updateProgress.value.progress = progressData.progress
      if (progressData.total_size !== undefined)
        updateProgress.value.total_size = progressData.total_size
      if (progressData.downloaded !== undefined)
        updateProgress.value.downloaded = progressData.downloaded

      const status = progressData.status
      if (status !== undefined) {
        updateProgress.value.status = status
        if (status === 'completed') {
          stopProgressPolling()
          updateProgress.value.progress = 100
          showUpdateCompleteNotification()
          return
        }
        if (status === 'failed') {
          stopProgressPolling()
          resetUpdateState()
          ElMessage.error({
            message: '更新失败，请稍后重试或手动下载最新版本',
            duration: 5000,
          })
          scheduleUpdateListRefresh(1000)
          return
        }
        if (status === 'cancelled') {
          stopProgressPolling()
          resetUpdateState()
          ElMessage.info('更新已取消')
          scheduleUpdateListRefresh(1000)
        }
      }
    } catch (error) {
      if (!pageActive || document.hidden || generation !== pollingGeneration) return
      const parsed = parseUpdateError(error, '查询更新进度失败', 'get', '/update/progress')
      if (!parsed.shouldNotify) return
      if (parsed.kind === 'application') {
        stopProgressPolling()
        if (!updateAPI.isUpdateNotStarted(parsed)) ElMessage.error(parsed.message)
        scheduleUpdateListRefresh(2000, true)
      }
    } finally {
      progressInFlight = false
      if (
        pageActive &&
        !document.hidden &&
        generation === pollingGeneration &&
        isUpdating.value &&
        !progressTimer
      ) {
        scheduleProgressPolling(generation)
      }
    }
  }

  const scheduleUpdateListRefresh = (delay: number, reset = false) => {
    const generation = pollingGeneration
    setTimeout(() => {
      if (!pageActive || generation !== pollingGeneration) return
      if (reset) resetUpdateState()
      void loadUpdateList()
    }, delay)
  }

  const handleVisibilityChange = () => {
    if (!pageActive) return
    if (document.hidden) {
      stopProgressPolling(false)
    } else if (isUpdating.value) {
      pollingGeneration += 1
      const generation = pollingGeneration
      void checkUpdateProgress(generation)
      scheduleProgressPolling(generation)
    }
  }

  const cancelUpdate = async () => {
    const generation = pollingGeneration
    try {
      await updateAPI.cancelUpdate(http)
      if (!pageActive || generation !== pollingGeneration) return
      stopProgressPolling()
      resetUpdateState()
      ElMessage.success('已取消更新')
      scheduleUpdateListRefresh(1000)
    } catch (error) {
      if (!pageActive || generation !== pollingGeneration) return
      const parsed = parseUpdateError(error, '取消更新失败，请稍后重试', 'post', '/update/cancel')
      if (parsed.shouldNotify) ElMessage.error(parsed.message)
    }
  }

  const handleDownloadClick = (update: UpdateInfo) => {
    if (!update.url) {
      ElMessage.error('下载链接不存在，请稍后重试')
      return false
    }
    window.open(update.url, '_blank')
    return true
  }

  const cleanup = () => {
    pageActive = false
    pollingGeneration += 1
    stopProgressPolling(false)
    if (countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
  }

  onMounted(() => {
    document.addEventListener('visibilitychange', handleVisibilityChange)
    loadUpdateList().then(() => {
      checkUpdateStatusOnLoad()
    })
  })

  onUnmounted(() => {
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    cleanup()
  })

  return {
    updateList,
    updateLoading,
    isUpdating,
    updatingVersion,
    updateProgress,
    showUpdateCompleteDialog,
    countdown,
    updateChannel,
    loadUpdateList,
    updateToVersion,
    cancelUpdate,
    handleDownloadClick,
    manuallyRefresh,
  }
}
