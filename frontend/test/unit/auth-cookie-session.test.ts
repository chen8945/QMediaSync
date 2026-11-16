// @vitest-environment happy-dom
import { createPinia, setActivePinia } from 'pinia'
import axios, { AxiosError, type AxiosResponse } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useAuthStore } from '../../src/stores/auth'
import { installAuthResponseInterceptor } from '@/http/authInterceptor'
import { parseHttpError } from '@/http/errors'

describe('cookie-only auth store', () => {
  afterEach(() => vi.useRealTimers())
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    setActivePinia(createPinia())
  })

  it('登录状态不写入 Web Storage token', () => {
    const store = useAuthStore()

    store.login({
      user: { id: '1', username: 'admin', role: 'admin' },
      csrfToken: 'csrf-token',
      session: { session_id: 'sid', expires_at: 1 },
    })

    expect(store.isAuthenticated).toBe(true)
    expect(store.csrfToken).toBe('csrf-token')
    expect(localStorage.getItem('auth_token')).toBeNull()
    expect(sessionStorage.getItem('auth_token')).toBeNull()
  })

  it.each(['success', 'failure'] as const)(
    '服务端退出%s仍清理本地会话，只保留已确认的操作反馈',
    async (outcome) => {
      vi.useFakeTimers()
      const store = useAuthStore()
      store.login({ user: { id: '1', username: 'admin' }, csrfToken: 'csrf-token' })
      const adapter = vi.fn(async (config) => {
        if (outcome === 'failure') throw new AxiosError('canceled', 'ERR_CANCELED', config)
        return {
          config,
          headers: {},
          statusText: 'OK',
          status: 200,
          data: { status: 200, data: null, message: '服务端退出结果', warnings: ['退出附加提醒'] },
        }
      })
      const result = await store.logoutWithServer(axios.create({ adapter }))
      if (outcome === 'success')
        expect(result).toEqual({ message: '服务端退出结果', warnings: ['退出附加提醒'] })
      else expect(result).toBeUndefined()
      expect(store.isAuthenticated).toBe(false)
      expect(store.csrfToken).toBeNull()
      expect(adapter).toHaveBeenCalledExactlyOnceWith(
        expect.objectContaining({
          url: '/api/logout',
          withCredentials: true,
          skipAuthInvalidation: true,
        }),
      )
    },
  )

  it('通过 /session 恢复服务端会话', async () => {
    const store = useAuthStore()
    const http = {
      get: vi.fn().mockResolvedValue({
        status: 200,
        data: {
          status: 200,
          data: {
            authenticated: true,
            user: { id: '1', username: 'admin', role: 'admin' },
            csrf_token: 'csrf-token',
            session: { session_id: 'sid', expires_at: 1 },
          },
        },
      }),
    }

    await store.bootstrapAuth(http as never)

    expect(http.get).toHaveBeenCalledWith(expect.stringContaining('/session'), {
      skipAuthInvalidation: true,
      withCredentials: true,
    })
    expect(store.authStatus).toBe('authenticated')
    expect(store.csrfToken).toBe('csrf-token')
  })

  it('重新建立会话后，旧请求返回 HTTP 401 不清理新用户和 CSRF', async () => {
    const store = useAuthStore()
    store.login({ user: { id: '1', username: 'old-admin' }, csrfToken: 'old-csrf' })
    let rejectOldRequest: (() => void) | undefined
    const http = axios.create({
      adapter: (config) =>
        new Promise<AxiosResponse>((_resolve, reject) => {
          rejectOldRequest = () =>
            reject(
              new AxiosError('Unauthorized', 'ERR_BAD_REQUEST', config, undefined, {
                config,
                status: 401,
                statusText: 'Unauthorized',
                headers: {},
                data: {
                  status: 401,
                  data: null,
                  error_code: 'SESSION_INVALID',
                  message: '测试会话失效',
                  instance: '/request-errors/abcdefghijklmnopqrstuvwx',
                },
              }),
            )
        }),
    })
    const notify = vi.fn()
    const uninstall = installAuthResponseInterceptor(http, {
      getAuthStore: () => store,
      onAuthenticationInvalidated: notify,
    })
    const oldRequest = http.get('/protected').catch((error: unknown) => error)
    await vi.waitFor(() => expect(rejectOldRequest).toBeTypeOf('function'))
    store.clearAuth()
    store.login({ user: { id: '2', username: 'new-admin' }, csrfToken: 'new-csrf' })
    rejectOldRequest!()

    expect(parseHttpError(await oldRequest)).toMatchObject({
      kind: 'unauthorized',
      shouldNotify: false,
    })
    expect(store.isAuthenticated).toBe(true)
    expect(store.user?.username).toBe('new-admin')
    expect(store.csrfToken).toBe('new-csrf')
    expect(notify).not.toHaveBeenCalled()
    uninstall()
  })
})
