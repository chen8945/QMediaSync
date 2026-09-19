// @vitest-environment happy-dom
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useAuthStore } from '../../src/stores/auth'

const createHttp = (response: unknown) => ({
  get: vi.fn().mockResolvedValue(response),
})

describe('auth bootstrap', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    setActivePinia(createPinia())
  })

  it('通过服务端会话恢复登录状态', async () => {
    const authStore = useAuthStore()
    const http = createHttp({
      data: {
        code: 200,
        data: {
          authenticated: true,
          user: { id: '1', username: 'admin', role: 'admin' },
          csrf_token: 'csrf-token',
          session: { session_id: 'sid', expires_at: 1 },
        },
      },
    })

    await authStore.bootstrapAuth(http as never)

    expect(http.get).toHaveBeenCalledTimes(1)
    expect(authStore.authStatus).toBe('authenticated')
    expect(authStore.csrfToken).toBe('csrf-token')
  })

  it('服务端返回匿名状态时静默进入匿名状态', async () => {
    const authStore = useAuthStore()
    const http = createHttp({ data: { code: 200, data: { authenticated: false } } })
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)

    const restored = await authStore.bootstrapAuth(http as never)

    expect(restored).toBe(false)
    expect(authStore.authStatus).toBe('anonymous')
    expect(authStore.isAuthenticated).toBe(false)
    expect(consoleError).not.toHaveBeenCalled()
    consoleError.mockRestore()
  })

  it('服务端会话查询不可用时返回不可用状态', async () => {
    const authStore = useAuthStore()
    const http = {
      get: vi.fn().mockRejectedValue(new Error('network unavailable')),
    }
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)

    const result = await authStore.refreshSession(http as never)

    expect(result).toMatchObject({
      state: 'unavailable',
      error: { kind: 'unknown', message: '登录会话验证失败，请稍后重试' },
    })
    expect(authStore.authStatus).toBe('anonymous')
    expect(consoleError).toHaveBeenCalledWith('恢复登录会话失败：', {})
    consoleError.mockRestore()
  })

  it('并发启动只请求一次服务端会话', async () => {
    const authStore = useAuthStore()
    const http = createHttp({ data: { code: 200, data: { authenticated: false } } })

    await Promise.all([
      authStore.bootstrapAuth(http as never),
      authStore.bootstrapAuth(http as never),
      authStore.bootstrapAuth(http as never),
    ])

    expect(http.get).toHaveBeenCalledTimes(1)
    expect(authStore.authStatus).toBe('anonymous')
  })
})
