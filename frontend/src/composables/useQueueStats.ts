import { onMounted, onUnmounted, ref } from 'vue'
import { fetchQueueStats, type QueueStats } from '@/api/dashboard'
import { useHttpClient } from '@/http/client'
import { parseHttpError } from '@/http/errors'

export type { QueueStats } from '@/api/dashboard'

export function useQueueStats(pollingInterval = 3000) {
  const http = useHttpClient()
  const queueStats = ref<QueueStats | null>(null)
  const queueStatsLoading = ref(false)
  const hasLoaded = ref(false)
  const maxPollingInterval = 30000
  let queueStatsTimer: ReturnType<typeof setTimeout> | null = null
  let inFlight = false
  let isPollingActive = false
  let currentPollingInterval = pollingInterval

  const loadQueueStats = async () => {
    if (inFlight) {
      return
    }
    inFlight = true

    try {
      queueStatsLoading.value = !hasLoaded.value
      queueStats.value = await fetchQueueStats(http)
      hasLoaded.value = true
      currentPollingInterval = pollingInterval
    } catch (error) {
      const parsed = parseHttpError(error, { fallbackMessage: '加载 115 接口请求统计失败' })
      if (parsed.shouldNotify) console.error('加载 115 接口请求统计错误：', parsed.diagnostics)
      if (!hasLoaded.value) {
        queueStats.value = null
      }
      currentPollingInterval = Math.min(currentPollingInterval * 2, maxPollingInterval)
    } finally {
      inFlight = false
      queueStatsLoading.value = false
    }
  }

  const scheduleNextPolling = () => {
    if (!isPollingActive || queueStatsTimer || document.hidden) {
      return
    }
    queueStatsTimer = setTimeout(async () => {
      queueStatsTimer = null
      await loadQueueStats()
      scheduleNextPolling()
    }, currentPollingInterval)
  }

  const startPolling = () => {
    stopPolling()
    scheduleNextPolling()
  }

  const stopPolling = () => {
    if (queueStatsTimer) {
      clearTimeout(queueStatsTimer)
      queueStatsTimer = null
    }
  }

  const handleVisibilityChange = () => {
    if (!isPollingActive) {
      return
    }
    if (document.hidden) {
      stopPolling()
      return
    }
    void loadQueueStats()
    scheduleNextPolling()
  }

  onMounted(() => {
    isPollingActive = true
    void loadQueueStats()
    document.addEventListener('visibilitychange', handleVisibilityChange)
    startPolling()
  })

  onUnmounted(() => {
    isPollingActive = false
    stopPolling()
    document.removeEventListener('visibilitychange', handleVisibilityChange)
  })

  return {
    queueStats,
    queueStatsLoading,
    loadQueueStats,
    startPolling,
    stopPolling,
  }
}
