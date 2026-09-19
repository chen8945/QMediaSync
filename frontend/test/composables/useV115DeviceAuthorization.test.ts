import { V115_QR_STATUS_TIMEOUT_MS } from '@/api/accounts'
import { SERVER_URL } from '@/const'
import { effectScope } from 'vue'
import { flushPromises } from '@vue/test-utils'
import axios, { AxiosError, CanceledError, type AxiosInstance } from 'axios'
import { markAuthInvalidationHandled } from '@/http/errors'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createDeferred } from '../support/deferred'
import {
  V115_QR_STATUS_POLL_DELAY_MS,
  V115_QR_STATUS_SCANNED_POLL_DELAY_MS,
  useV115DeviceAuthorization,
} from '@/composables/useV115DeviceAuthorization'

const createClient = (post: AxiosInstance['post']) => {
  const http = axios.create()
  http.post = post
  return http
}

const qrResponse = (uid = 'qr-uid') => ({
  data: {
    code: 200,
    data: { uid, time: 1, sign: 'private-sign', qrcode: '115://auth', expires: 300 },
  },
})

const handledUnauthorized = () => {
  const error = { response: { status: 401, data: { code: 500, message: 'private-token' } } }
  markAuthInvalidationHandled(error)
  return error
}

describe('useV115DeviceAuthorization', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('uses a long timeout for 115 QR status polling', async () => {
    const setTimeoutMock = vi.fn(() => 1)
    const clearTimeoutMock = vi.fn()
    vi.stubGlobal('window', {
      setInterval: vi.fn(() => 1),
      clearInterval: vi.fn(),
      setTimeout: setTimeoutMock,
      clearTimeout: clearTimeoutMock,
    })

    const post = vi
      .fn()
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: {
            uid: 'qr-uid',
            time: 1,
            sign: 'sign',
            qrcode: '115://auth-content',
            expires: 300,
          },
        },
      })
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: {
            status: 'waiting',
            tip: '等待扫码',
          },
        },
      })

    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))

    expect(authorization).toBeDefined()
    await authorization!.startAuthorization(12)

    expect(post).toHaveBeenNthCalledWith(
      2,
      `${SERVER_URL}/auth/115-qrcode-status`,
      {
        account_id: 12,
        uid: 'qr-uid',
      },
      {
        timeout: V115_QR_STATUS_TIMEOUT_MS,
      },
    )
    expect(V115_QR_STATUS_TIMEOUT_MS).toBeGreaterThan(60_000)

    scope.stop()
  })

  it('passes the replacement authorization session through QR requests', async () => {
    const setTimeoutMock = vi.fn(() => 1)
    vi.stubGlobal('window', {
      setTimeout: setTimeoutMock,
      clearTimeout: vi.fn(),
    })

    const post = vi
      .fn()
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: {
            uid: 'qr-uid',
            time: 1,
            sign: 'sign',
            qrcode: '115://auth-content',
            expires: 300,
          },
        },
      })
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: { status: 'waiting', tip: '等待扫码' },
        },
      })

    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))

    await authorization!.startAuthorization(12, 'change-session')

    expect(post).toHaveBeenNthCalledWith(1, `${SERVER_URL}/auth/115-qrcode-open`, {
      account_id: 12,
      authorization_id: 'change-session',
    })
    expect(post).toHaveBeenNthCalledWith(
      2,
      `${SERVER_URL}/auth/115-qrcode-status`,
      {
        account_id: 12,
        uid: 'qr-uid',
        authorization_id: 'change-session',
      },
      { timeout: V115_QR_STATUS_TIMEOUT_MS },
    )

    scope.stop()
  })

  it('pauses QR status polling while hidden and resumes when visible', async () => {
    let hidden = true
    let visibilityHandler: (() => void) | undefined
    vi.stubGlobal('window', {
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    })
    vi.stubGlobal('document', {
      get hidden() {
        return hidden
      },
      addEventListener: vi.fn((_event, listener) => {
        visibilityHandler = listener as () => void
      }),
      removeEventListener: vi.fn(),
    })

    const post = vi
      .fn()
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: {
            uid: 'qr-uid',
            time: 1,
            sign: 'sign',
            qrcode: '115://auth-content',
            expires: 300,
          },
        },
      })
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: { status: 'waiting', tip: '等待扫码' },
        },
      })

    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))

    await authorization!.startAuthorization(12)
    expect(post).toHaveBeenCalledTimes(1)

    hidden = false
    visibilityHandler?.()
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(2))

    scope.stop()
  })

  it('uses a safe fallback for ordinary QR open exceptions', async () => {
    vi.stubGlobal('window', {
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    })

    const post = vi.fn().mockRejectedValue(new Error('request timeout'))
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))

    await authorization!.startAuthorization(12)

    expect(authorization!.status.value).toBe('failed')
    expect(authorization!.tip.value).toBe('获取二维码失败')
    scope.stop()
  })

  it('does not restart polling when the QR request resolves after cancellation', async () => {
    vi.stubGlobal('window', {
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    })

    let resolveOpen: ((value: unknown) => void) | undefined
    const post = vi.fn().mockReturnValueOnce(
      new Promise((resolve) => {
        resolveOpen = resolve
      }),
    )
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))

    const startPromise = authorization!.startAuthorization(12, 'change-session')
    authorization!.stopPolling()
    resolveOpen?.({
      data: {
        code: 200,
        data: { uid: 'late-qr', time: 1, sign: 'sign', qrcode: '115://auth', expires: 300 },
      },
    })
    await startPromise

    expect(post).toHaveBeenCalledTimes(1)
    expect(authorization!.isPolling.value).toBe(false)
    scope.stop()
  })

  it('stops polling when the QR status expires', async () => {
    vi.stubGlobal('window', {
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    })

    const post = vi
      .fn()
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: { uid: 'qr-uid', time: 1, sign: 'sign', qrcode: '115://auth', expires: 300 },
        },
      })
      .mockResolvedValueOnce({
        data: { code: 200, data: { status: 'expired', tip: '二维码已过期' } },
      })
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))

    await authorization!.startAuthorization(12)
    await vi.waitFor(() => expect(authorization!.status.value).toBe('expired'))

    expect(authorization!.tip.value).toBe('二维码已过期')
    scope.stop()
  })

  it('slows QR status polling down after the code has been scanned', async () => {
    const setTimeoutMock = vi.fn<(handler: () => void, delay?: number) => number>(() => 1)
    vi.stubGlobal('window', {
      setTimeout: setTimeoutMock,
      clearTimeout: vi.fn(),
    })

    const post = vi
      .fn()
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: { uid: 'qr-uid', time: 1, sign: 'sign', qrcode: '115://auth', expires: 300 },
        },
      })
      .mockResolvedValueOnce({
        data: { code: 200, data: { status: 'waiting', tip: '等待扫码' } },
      })
      .mockResolvedValueOnce({
        data: { code: 200, data: { status: 'scanned', tip: '已扫码，请在 115 客户端确认' } },
      })

    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))

    await authorization!.startAuthorization(12)
    await vi.waitFor(() => expect(authorization!.status.value).toBe('waiting'))
    expect(setTimeoutMock).toHaveBeenLastCalledWith(
      expect.any(Function),
      V115_QR_STATUS_POLL_DELAY_MS,
    )

    // 手动触发下一次轮询，模拟计时器到期后进入已扫码阶段。
    const scheduledPoll = setTimeoutMock.mock.calls.at(-1)?.[0]
    scheduledPoll?.()
    await vi.waitFor(() => expect(authorization!.status.value).toBe('scanned'))

    expect(setTimeoutMock).toHaveBeenLastCalledWith(
      expect.any(Function),
      V115_QR_STATUS_SCANNED_POLL_DELAY_MS,
    )
    expect(V115_QR_STATUS_SCANNED_POLL_DELAY_MS).toBeGreaterThan(V115_QR_STATUS_POLL_DELAY_MS)
    scope.stop()
  })

  it.each([
    [
      'HTTP 200 业务失败',
      {
        response: { status: 200, data: { code: 500, message: '获取二维码失败：授权服务暂不可用' } },
      },
      '获取二维码失败',
    ],
    [
      '来源校验失败',
      { response: { status: 403, data: { code: 500, error_code: 'REQUEST_ORIGIN_INVALID' } } },
      '访问地址校验失败',
    ],
    ['无响应', new AxiosError('private-token', 'ERR_NETWORK'), '无法获取服务器响应'],
    [
      '真实写入超时',
      new AxiosError('private-token', 'ETIMEDOUT', {
        method: 'post',
        headers: new axios.AxiosHeaders(),
      }),
      '操作结果尚未确认',
    ],
  ])('安全显示二维码创建错误：%s', async (_name, error, expected) => {
    const post = vi.fn().mockRejectedValue(error)
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))!
    await authorization.startAuthorization(12)
    expect(authorization.status.value).toBe('failed')
    expect(authorization.tip.value).toContain(expected)
    expect(authorization.tip.value).not.toContain('private-token')
    expect(authorization.isPolling.value).toBe(false)
    expect(authorization.loading.value).toBe(false)
    scope.stop()
  })

  it.each(['open', 'status'])('取消与已处理 401 在 %s 阶段保持静默', async (stage) => {
    for (const failure of [new CanceledError('private-token'), handledUnauthorized()]) {
      const post = vi.fn()
      if (stage === 'status') post.mockResolvedValueOnce(qrResponse())
      post.mockRejectedValueOnce(failure)
      const scope = effectScope()
      const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))!
      await authorization.startAuthorization(12)
      await flushPromises()
      expect(authorization.status.value).toBe('idle')
      expect(authorization.tip.value).toBe('')
      expect(authorization.isPolling.value).toBe(false)
      scope.stop()
    }
  })

  it('轮询的业务失败只公开许可消息并停止后续请求', async () => {
    const setTimeout = vi.fn()
    vi.stubGlobal('window', { setTimeout, clearTimeout: vi.fn() })
    const post = vi
      .fn()
      .mockResolvedValueOnce(qrResponse())
      .mockResolvedValueOnce({
        data: {
          code: 500,
          message: '保存 115 授权失败：授权会话不存在、已过期或已取消',
          data: null,
        },
      })
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))!
    await authorization.startAuthorization(12)
    await flushPromises()
    expect(authorization.status.value).toBe('failed')
    expect(authorization.tip.value).toBe('授权会话不存在、已过期或已取消')
    expect(authorization.isPolling.value).toBe(false)
    expect(setTimeout).not.toHaveBeenCalled()
    scope.stop()
  })

  it('重置等待中的二维码请求后立即清除 loading，迟到结果不恢复二维码', async () => {
    const pending = createDeferred<ReturnType<typeof qrResponse>>()
    const post = vi.fn().mockReturnValue(pending.promise)
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))!
    const start = authorization.startAuthorization(12)
    expect(authorization.loading.value).toBe(true)
    authorization.resetAuthorization()
    expect(authorization.loading.value).toBe(false)
    pending.resolve(qrResponse())
    await start
    expect(authorization.qrCode.value).toBeNull()
    expect(authorization.status.value).toBe('idle')
    expect(post).toHaveBeenCalledTimes(1)
    scope.stop()
  })

  it('旧二维码创建完成不能清除新请求的 loading 或覆盖新状态', async () => {
    const oldRequest = createDeferred<ReturnType<typeof qrResponse>>()
    const newRequest = createDeferred<ReturnType<typeof qrResponse>>()
    vi.stubGlobal('document', {
      hidden: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })
    const post = vi
      .fn()
      .mockReturnValueOnce(oldRequest.promise)
      .mockReturnValueOnce(newRequest.promise)
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))!
    const oldStart = authorization.startAuthorization(12, 'old-session')
    const newStart = authorization.startAuthorization(13, 'new-session')
    oldRequest.resolve(qrResponse('old-uid'))
    await oldStart
    expect(authorization.qrCode.value).toBeNull()
    expect(authorization.loading.value).toBe(true)
    newRequest.resolve(qrResponse('new-uid'))
    await newStart
    expect(authorization.qrCode.value?.uid).toBe('new-uid')
    expect(authorization.loading.value).toBe(false)
    scope.stop()
  })

  it.each(['success', 'failure'])('旧轮询 %s 不能覆盖新二维码或重新创建计时器', async (result) => {
    const setTimeout = vi.fn(() => 1)
    vi.stubGlobal('window', { setTimeout, clearTimeout: vi.fn() })
    const oldPoll = createDeferred<unknown>()
    const post = vi
      .fn()
      .mockResolvedValueOnce(qrResponse('old-uid'))
      .mockReturnValueOnce(oldPoll.promise)
      .mockResolvedValueOnce(qrResponse('new-uid'))
      .mockResolvedValueOnce({
        data: { code: 200, data: { status: 'scanned', tip: '已扫码，请确认' } },
      })
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))!
    await authorization.startAuthorization(12, 'old-session')
    await authorization.startAuthorization(13, 'new-session')
    await flushPromises()
    oldPoll.resolve(
      result === 'success'
        ? { data: { code: 200, data: { status: 'confirmed', tip: '授权成功' } } }
        : { data: { code: 500, message: '授权会话不存在或已过期', data: null } },
    )
    await flushPromises()
    expect(authorization.qrCode.value?.uid).toBe('new-uid')
    expect(authorization.status.value).toBe('scanned')
    expect(authorization.tip.value).toBe('已扫码，请确认')
    expect(authorization.isPolling.value).toBe(true)
    expect(setTimeout).toHaveBeenCalledTimes(1)
    scope.stop()
  })

  it('可见性重复变化不会并发轮询，卸载后迟到结果不再创建计时器', async () => {
    const pending = createDeferred<unknown>()
    let visibilityHandler: (() => void) | undefined
    let hidden = false
    const setTimeout = vi.fn()
    const removeEventListener = vi.fn()
    vi.stubGlobal('window', { setTimeout, clearTimeout: vi.fn() })
    vi.stubGlobal('document', {
      get hidden() {
        return hidden
      },
      addEventListener: vi.fn((_event, listener) => {
        visibilityHandler = listener
      }),
      removeEventListener,
    })
    const post = vi.fn().mockResolvedValueOnce(qrResponse()).mockReturnValueOnce(pending.promise)
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization(createClient(post)))!
    await authorization.startAuthorization(12)
    hidden = true
    visibilityHandler?.()
    hidden = false
    visibilityHandler?.()
    visibilityHandler?.()
    expect(post).toHaveBeenCalledTimes(2)
    scope.stop()
    pending.resolve({ data: { code: 200, data: { status: 'scanned', tip: '已扫码' } } })
    await flushPromises()
    expect(authorization.isPolling.value).toBe(false)
    expect(authorization.status.value).toBe('waiting')
    expect(setTimeout).not.toHaveBeenCalled()
    expect(removeEventListener).toHaveBeenCalledWith('visibilitychange', visibilityHandler)
  })
})
