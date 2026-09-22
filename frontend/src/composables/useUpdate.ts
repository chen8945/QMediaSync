import { onMounted, onUnmounted, ref } from 'vue'
import { useHttpClient } from '@/http/client'
import { ElMessage } from 'element-plus'

import * as updateAPI from '@/api/update'
import { parseHttpError } from '@/http/errors'
import { fetchSystemVersion } from '@/api/systemInfo'
import { notifyHttpError } from '@/utils/httpErrorNotification'
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
  // 业务操作只随新任务、终态或卸载失效；切换可见性仅更新轮询代次。
  let operationGeneration = 0
  let pendingStartGeneration: number | null = null
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
      notifyHttpError(error, '加载最新版本列表失败', {
        fallbackMessage: '加载最新版本列表失败',
        publicMessages: updateAPI.updatePublicMessages,
      })
      updateList.value = []
    } finally {
      updateLoading.value = false
    }
  }

  const checkUpdateStatusOnLoad = async () => {
    const generation = pollingGeneration
    if (!pageActive || pendingStartGeneration !== null) return
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
      const parsed = parseHttpError(error)
      if (parsed.shouldNotify && !updateAPI.isUpdateNotStarted(parsed))
        console.error('检查更新状态失败', parsed.diagnostics)
    }
  }

  const resetUpdateState = () => {
    operationGeneration += 1
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
    const generation = ++operationGeneration
    pendingStartGeneration = generation
    stopProgressPolling()
    isUpdating.value = true
    updatingVersion.value = version
    updateProgress.value = {
      progress: 0,
      total_size: 0,
      downloaded: 0,
      status: 'downloading',
    }

    try {
      await updateAPI.startUpdate(http, version, updateChannel.value)
      if (!pageActive || generation !== operationGeneration) return
      pendingStartGeneration = null
      startProgressPolling()
    } catch (error) {
      if (!pageActive || generation !== operationGeneration) return
      resetUpdateState()
      notifyHttpError(error, '触发版本更新失败', {
        fallbackMessage: '触发版本更新失败',
        publicMessages: updateAPI.updatePublicMessages,
      })
    } finally {
      if (pendingStartGeneration === generation) pendingStartGeneration = null
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
          operationGeneration += 1
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
      const parsed = parseHttpError(error, {
        fallbackMessage: '查询更新进度失败',
        publicMessages: updateAPI.updatePublicMessages,
      })
      if (!parsed.shouldNotify) return
      if (updateAPI.isUpdateNotStarted(parsed) && updatingVersion.value) {
        try {
          const installed = await fetchSystemVersion(http)
          if (!pageActive || document.hidden || generation !== pollingGeneration) return
          if (installed.version === updatingVersion.value) {
            operationGeneration += 1
            stopProgressPolling()
            updateProgress.value.status = 'completed'
            updateProgress.value.progress = 100
            showUpdateCompleteNotification()
            return
          }
          ElMessage.error('更新未生效，请查看服务日志或手动下载安装')
        } catch (versionError) {
          if (!pageActive || document.hidden || generation !== pollingGeneration) return
          const failure = parseHttpError(versionError)
          if (failure.shouldNotify) console.error('确认更新版本失败', failure.diagnostics)
          return
        }
      }
      if (!updateAPI.isUpdateNotStarted(parsed)) {
        console.error('查询更新进度失败', parsed.diagnostics)
      }
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
    } else if (isUpdating.value && pendingStartGeneration === null) {
      pollingGeneration += 1
      const generation = pollingGeneration
      void checkUpdateProgress(generation)
      scheduleProgressPolling(generation)
    }
  }

  const cancelUpdate = async () => {
    const generation = operationGeneration
    try {
      await updateAPI.cancelUpdate(http)
      if (!pageActive || generation !== operationGeneration) return
      stopProgressPolling()
      resetUpdateState()
      ElMessage.success('已取消更新')
      scheduleUpdateListRefresh(1000)
    } catch (error) {
      if (!pageActive || generation !== operationGeneration) return
      notifyHttpError(error, '取消更新失败，请稍后重试', {
        fallbackMessage: '取消更新失败，请稍后重试',
        publicMessages: updateAPI.updatePublicMessages,
      })
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
    operationGeneration += 1
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
