import { SERVER_URL } from '@/const'
import { effectScope } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  V115_QR_STATUS_TIMEOUT_MS,
  useV115DeviceAuthorization,
} from '@/composables/useV115DeviceAuthorization'

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
    const authorization = scope.run(() => useV115DeviceAuthorization({ post } as never))

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
    const authorization = scope.run(() => useV115DeviceAuthorization({ post } as never))

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
    const authorization = scope.run(() => useV115DeviceAuthorization({ post } as never))

    await authorization!.startAuthorization(12)
    expect(post).toHaveBeenCalledTimes(1)

    hidden = false
    visibilityHandler?.()
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(2))

    scope.stop()
  })

  it('turns QR open failures and request timeouts into failed state', async () => {
    vi.stubGlobal('window', {
      setTimeout: vi.fn(() => 1),
      clearTimeout: vi.fn(),
    })

    const post = vi.fn().mockRejectedValue(new Error('request timeout'))
    const scope = effectScope()
    const authorization = scope.run(() => useV115DeviceAuthorization({ post } as never))

    await authorization!.startAuthorization(12)

    expect(authorization!.status.value).toBe('failed')
    expect(authorization!.tip.value).toBe('request timeout')
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
    const authorization = scope.run(() => useV115DeviceAuthorization({ post } as never))

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
    const authorization = scope.run(() => useV115DeviceAuthorization({ post } as never))

    await authorization!.startAuthorization(12)
    await vi.waitFor(() => expect(authorization!.status.value).toBe('expired'))

    expect(authorization!.tip.value).toBe('二维码已过期')
    scope.stop()
  })
})
