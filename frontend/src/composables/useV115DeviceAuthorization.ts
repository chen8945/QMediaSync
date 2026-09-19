import { authorizationPublicMessages, fetchV115QRCodeStatus, openV115QRCode } from '@/api/accounts'
import { parseHttpError } from '@/http/errors'
import type { V115AuthStatus, V115QrCodePayload } from '@/types/v115Auth'
import type { AxiosInstance } from 'axios'
import { computed, onScopeDispose, shallowRef } from 'vue'

// 等待扫码阶段保持较快轮询，便于及时显示扫码结果。
export const V115_QR_STATUS_POLL_DELAY_MS = 1_000
// 已扫码后服务端要串行完成取令牌、查用户信息和写库，降低轮询频率可减少同期数据库写入竞争。
export const V115_QR_STATUS_SCANNED_POLL_DELAY_MS = 3_000

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
    loading.value = false
    if (pollTimer.value !== null) {
      window.clearTimeout(pollTimer.value)
      pollTimer.value = null
    }
  }

  const schedulePollStatus = (runId: number) => {
    if (!pollingActive.value || runId !== authorizationRunId.value || isPageHidden()) return
    const delay =
      status.value === 'scanned'
        ? V115_QR_STATUS_SCANNED_POLL_DELAY_MS
        : V115_QR_STATUS_POLL_DELAY_MS
    pollTimer.value = window.setTimeout(() => void pollStatus(runId), delay)
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
      const data = await fetchV115QRCodeStatus(http, {
        account_id: accountId.value,
        uid: qrCode.value.uid,
        ...(authorizationId.value ? { authorization_id: authorizationId.value } : {}),
      })
      if (runId !== authorizationRunId.value) return
      status.value = data.status
      tip.value = data.tip
      if (['confirmed', 'expired', 'failed'].includes(data.status)) {
        stopPolling()
        return
      }
      schedulePollStatus(runId)
    } catch (error) {
      if (runId !== authorizationRunId.value) return
      const failure = parseHttpError(error, {
        publicMessages: authorizationPublicMessages,
        fallbackMessage: '授权状态查询失败',
      })
      status.value = failure.shouldNotify ? 'failed' : 'idle'
      tip.value = failure.shouldNotify ? failure.message : ''
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
      const data = await openV115QRCode(http, nextAccountId, nextAuthorizationId)
      if (runId !== authorizationRunId.value) return
      qrCode.value = data
      tip.value = '等待扫码'
      pollingActive.value = true
      if (!isPageHidden()) {
        void pollStatus(runId)
      }
    } catch (error) {
      if (runId !== authorizationRunId.value) return
      const failure = parseHttpError(error, {
        publicMessages: authorizationPublicMessages,
        fallbackMessage: '获取二维码失败',
      })
      status.value = failure.shouldNotify ? 'failed' : 'idle'
      tip.value = failure.shouldNotify ? failure.message : ''
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
