import { SERVER_URL } from '@/const'
import type { V115AuthStatus, V115QrCodePayload, V115QrCodeStatusPayload } from '@/types/v115Auth'
import type { AxiosInstance } from 'axios'
import { computed, onScopeDispose, shallowRef } from 'vue'

export const V115_QR_STATUS_TIMEOUT_MS = 70_000
const V115_QR_STATUS_POLL_DELAY_MS = 1_000

export function useV115DeviceAuthorization(http: AxiosInstance) {
  const qrCode = shallowRef<V115QrCodePayload | null>(null)
  const status = shallowRef<V115AuthStatus>('idle')
  const tip = shallowRef('')
  const loading = shallowRef(false)
  const pollTimer = shallowRef<number | null>(null)
  const pollingActive = shallowRef(false)
  const accountId = shallowRef<number | null>(null)
  const authorizationId = shallowRef<string | null>(null)
  const authorizationRunId = shallowRef(0)
  let pollingRequestRunId: number | null = null

  const isPolling = computed(() => pollingActive.value)

  const isPageHidden = () => typeof document !== 'undefined' && document.hidden

  const stopPolling = () => {
    authorizationRunId.value += 1
    pollingActive.value = false
    if (pollTimer.value !== null) {
      window.clearTimeout(pollTimer.value)
      pollTimer.value = null
    }
  }

  const schedulePollStatus = (runId: number) => {
    if (!pollingActive.value || runId !== authorizationRunId.value || isPageHidden()) return
    pollTimer.value = window.setTimeout(() => void pollStatus(runId), V115_QR_STATUS_POLL_DELAY_MS)
  }

  const pollStatus = async (runId: number) => {
    if (
      !accountId.value ||
      !qrCode.value ||
      runId !== authorizationRunId.value ||
      isPageHidden() ||
      pollingRequestRunId === runId
    ) {
      return
    }
    pollTimer.value = null
    pollingRequestRunId = runId

    try {
      const response = await http.post(
        `${SERVER_URL}/auth/115-qrcode-status`,
        {
          account_id: accountId.value,
          uid: qrCode.value.uid,
          ...(authorizationId.value ? { authorization_id: authorizationId.value } : {}),
        },
        {
          timeout: V115_QR_STATUS_TIMEOUT_MS,
        },
      )
      if (runId !== authorizationRunId.value) return

      const data = response.data?.data as V115QrCodeStatusPayload | undefined
      if (response.data?.code !== 200 || !data) {
        status.value = 'failed'
        tip.value = response.data?.message || '授权状态查询失败'
        stopPolling()
        return
      }
      status.value = data.status
      tip.value = data.tip
      if (['confirmed', 'expired', 'failed'].includes(data.status)) {
        stopPolling()
        return
      }
      schedulePollStatus(runId)
    } catch (error) {
      if (runId !== authorizationRunId.value) return
      status.value = 'failed'
      tip.value = error instanceof Error ? error.message : '授权状态查询失败'
      stopPolling()
    } finally {
      if (pollingRequestRunId === runId) {
        pollingRequestRunId = null
      }
    }
  }

  const handleVisibilityChange = () => {
    if (!pollingActive.value) return
    if (isPageHidden()) {
      if (pollTimer.value !== null) {
        window.clearTimeout(pollTimer.value)
        pollTimer.value = null
      }
      return
    }
    void pollStatus(authorizationRunId.value)
  }

  const startAuthorization = async (nextAccountId: number, nextAuthorizationId?: string) => {
    stopPolling()
    const runId = authorizationRunId.value + 1
    authorizationRunId.value = runId
    loading.value = true
    accountId.value = nextAccountId
    authorizationId.value = nextAuthorizationId || null
    status.value = 'waiting'
    tip.value = '正在获取二维码…'
    qrCode.value = null

    try {
      const response = await http.post(`${SERVER_URL}/auth/115-qrcode-open`, {
        account_id: nextAccountId,
        ...(authorizationId.value ? { authorization_id: authorizationId.value } : {}),
      })
      if (runId !== authorizationRunId.value) return
      if (response.data?.code !== 200 || !response.data.data) {
        status.value = 'failed'
        tip.value = response.data?.message || '获取二维码失败'
        return
      }
      qrCode.value = response.data.data as V115QrCodePayload
      tip.value = '等待扫码'
      pollingActive.value = true
      if (!isPageHidden()) {
        void pollStatus(runId)
      }
    } catch (error) {
      if (runId !== authorizationRunId.value) return
      status.value = 'failed'
      tip.value = error instanceof Error ? error.message : '获取二维码失败'
    } finally {
      if (runId === authorizationRunId.value) loading.value = false
    }
  }

  const resetAuthorization = () => {
    stopPolling()
    qrCode.value = null
    status.value = 'idle'
    tip.value = ''
    accountId.value = null
    authorizationId.value = null
  }

  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', handleVisibilityChange)
  }

  onScopeDispose(() => {
    stopPolling()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  })

  return {
    qrCode,
    status,
    tip,
    loading,
    isPolling,
    startAuthorization,
    stopPolling,
    resetAuthorization,
  }
}
