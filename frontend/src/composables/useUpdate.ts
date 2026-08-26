import { onMounted, onUnmounted, ref } from 'vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { ElMessage } from 'element-plus'

export interface UpdateInfo {
  version: string
  published_at?: number
  date: string
  note: string
  url: string
  latest?: boolean
  current?: boolean
}

export interface UpdateProgress {
  progress: number
  total_size: number
  downloaded: number
  status: UpdateStatus
  version?: string
  error_message?: string
}

export type UpdateChannel = 'github' | 'gitee'
export type UpdateStatus = 'downloading' | 'install' | 'completed' | 'failed' | 'cancelled' | ''

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
      const params: string[] = []
      params.push(`channel=${updateChannel.value}`)
      if (force) {
        params.push('force=1')
      }
      const url = `${SERVER_URL}/update/last?${params.join('&')}`
      const response = await http.get(url)
      if (response && response.data && response.data.data) {
        updateList.value = response.data.data.map((item: UpdateInfo) => {
          return {
            ...item,
            url: item.url || '',
          }
        })
      } else {
        updateList.value = []
      }
    } catch (error) {
      console.error('加载最新版本列表错误：', error)
      updateList.value = []
    } finally {
      updateLoading.value = false
    }
  }

  const checkUpdateStatusOnLoad = async () => {
    try {
      const response = await http.get(`${SERVER_URL}/update/progress`)

      if (response && response.data && response.data.code === 200) {
        const progressData = response.data.data
        if (progressData && isUpdateRunningStatus(progressData.status)) {
          isUpdating.value = true
          updatingVersion.value = progressData.version || ''

          updateProgress.value = {
            progress: progressData.progress || 0,
            total_size: progressData.total_size || 0,
            downloaded: progressData.downloaded || 0,
            status: progressData.status || 'downloading',
          }

          startProgressPolling()
        }
      }
    } catch (error) {
      console.error('检查更新状态错误：', error)
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

    try {
      const response = await http.post(`${SERVER_URL}/update/to-version`, {
        version: version,
        channel: updateChannel.value,
      })

      if (response && response.data && response.data.code === 200) {
        startProgressPolling()
      } else {
        resetUpdateState()
        ElMessage.error(response?.data.message || '触发版本更新失败')
      }
    } catch (error) {
      console.error('触发版本更新错误：', error)
      resetUpdateState()
      ElMessage.error('触发版本更新失败')
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
      const response = await http.get(`${SERVER_URL}/update/progress`)
      if (!pageActive || document.hidden || generation !== pollingGeneration) return

      if (response && response.data) {
        if (response.data.code !== 200) {
          stopProgressPolling()

          setTimeout(() => {
            resetUpdateState()
            loadUpdateList()
          }, 2000)
          return
        }

        const progressData = response.data.data
        if (!progressData) {
          return
        }

        if (progressData.progress !== undefined) {
          updateProgress.value.progress = progressData.progress
        }
        if (progressData.total_size !== undefined) {
          updateProgress.value.total_size = progressData.total_size
        }
        if (progressData.downloaded !== undefined) {
          updateProgress.value.downloaded = progressData.downloaded
        }

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
              message: progressData.error_message || '更新失败，请稍后重试或手动下载最新版本',
              duration: 5000,
            })

            setTimeout(() => {
              loadUpdateList()
            }, 1000)

            return
          }

          if (status === 'cancelled') {
            stopProgressPolling()
            resetUpdateState()
            ElMessage.info('更新已取消')

            setTimeout(() => {
              loadUpdateList()
            }, 1000)

            return
          }
        }
      }
    } catch (error) {
      console.error('查询更新进度错误：', error)
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
    try {
      await http.post(`${SERVER_URL}/update/cancel`)

      stopProgressPolling()
      resetUpdateState()

      ElMessage.success('已取消更新')

      setTimeout(() => {
        loadUpdateList()
      }, 1000)
    } catch (error) {
      console.error('取消更新错误：', error)
      ElMessage.error('取消更新失败，请稍后重试')
    }
  }

  const handleDownloadClick = (update: UpdateInfo) => {
    if (!update.url) {
      console.error('下载链接不存在：', update)
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
