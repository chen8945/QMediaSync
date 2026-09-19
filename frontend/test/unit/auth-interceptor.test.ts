import axios, {
  AxiosError,
  type AxiosAdapter,
  type AxiosResponse,
  type InternalAxiosRequestConfig,
} from 'axios'
import { describe, expect, it, vi } from 'vitest'
import { parseHttpError } from '@/http/errors'

import {
  installAuthResponseInterceptor,
  type AuthInvalidationStore,
} from '../../src/http/authInterceptor'

type AuthStoreStub = AuthInvalidationStore & {
  clearAuth: ReturnType<typeof vi.fn<() => void>>
}

const unauthorizedResponse = (config: InternalAxiosRequestConfig) => ({
  config,
  data: { code: 401 },
  headers: {},
  status: 401,
  statusText: 'Unauthorized',
})

const unauthorizedAdapter: AxiosAdapter = async (config) => {
  if (config.url === '/body-code-401') {
    return {
      ...unauthorizedResponse(config),
      status: 200,
      statusText: 'OK',
    }
  }

  return Promise.reject(
    new AxiosError(
      'Request failed with status code 401',
      'ERR_BAD_REQUEST',
      config,
      undefined,
      unauthorizedResponse(config),
    ),
  )
}

const createAuthenticatedStore = (): AuthStoreStub => {
  const store: AuthStoreStub = {
    isAuthenticated: true,
    isLoggingOut: false,
    sessionVersion: 1,
    clearAuth: vi.fn<() => void>(),
  }
  store.clearAuth.mockImplementation(() => {
    store.isAuthenticated = false
    store.sessionVersion += 1
  })
  return store
}

describe('auth response interceptor', () => {
  it('并发 HTTP 401 只清理、提示和跳转一次', async () => {
    const http = axios.create({ adapter: unauthorizedAdapter })
    const store = createAuthenticatedStore()
    const notify = vi.fn()
    const replace = vi.fn().mockResolvedValue(undefined)
    const uninstall = installAuthResponseInterceptor(http, {
      getAuthStore: () => store,
      onAuthenticationInvalidated: async () => {
        notify('登录已失效，请重新登录')
        await replace('/login')
      },
    })

    const results = await Promise.allSettled([
      http.get('/protected-a'),
      http.get('/protected-b'),
      http.get('/protected-c'),
    ])

    expect(store.clearAuth).toHaveBeenCalledTimes(1)
    expect(notify).toHaveBeenCalledWith('登录已失效，请重新登录')
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith('/login')
    for (const result of results) {
      expect(result.status).toBe('rejected')
      if (result.status === 'rejected') {
        expect(parseHttpError(result.reason)).toMatchObject({
          kind: 'unauthorized',
          handled: true,
          shouldNotify: false,
          response: { data: { code: 401 } },
        })
      }
    }
    uninstall()
  })

  it.each([true, false])(
    '重新登录后旧 401 不影响新会话（旧批次已失效：%s）',
    async (invalidateFirst) => {
      let release: (() => void) | undefined
      const http = axios.create({
        adapter: (config) =>
          config.url === '/late'
            ? new Promise<AxiosResponse>((resolve) => {
                release = () => resolve(unauthorizedResponse(config))
              })
            : unauthorizedAdapter(config),
      })
      const store = createAuthenticatedStore()
      const invalidate = vi.fn()
      const uninstall = installAuthResponseInterceptor(http, {
        getAuthStore: () => store,
        onAuthenticationInvalidated: invalidate,
      })
      const late = http.get('/late').catch((error: unknown) => error)
      // 同一轮另一请求完成时，迟到请求已经发出并记录旧会话。
      if (invalidateFirst) {
        await expect(http.get('/first')).rejects.toBeInstanceOf(AxiosError)
      } else {
        await vi.waitFor(() => expect(release).toBeTypeOf('function'))
      }
      store.isAuthenticated = true
      store.sessionVersion += 1
      release!()

      expect(parseHttpError(await late)).toMatchObject({
        handled: true,
        shouldNotify: false,
        diagnostics: { status: 401 },
        response: { data: { code: 401 } },
      })
      expect(store.isAuthenticated).toBe(true)
      expect(store.clearAuth).toHaveBeenCalledTimes(invalidateFirst ? 1 : 0)
      expect(invalidate).toHaveBeenCalledTimes(invalidateFirst ? 1 : 0)

      await expect(http.get('/new-session')).rejects.toBeInstanceOf(AxiosError)
      expect(store.isAuthenticated).toBe(false)
      expect(invalidate).toHaveBeenCalledTimes(invalidateFirst ? 2 : 1)
      uninstall()
    },
  )

  it('跳转失败仍保留原始 HTTP 错误与已处理标记', async () => {
    const http = axios.create({ adapter: unauthorizedAdapter })
    const store = createAuthenticatedStore()
    const uninstall = installAuthResponseInterceptor(http, {
      getAuthStore: () => store,
      onAuthenticationInvalidated: vi.fn().mockRejectedValue(new Error('navigation failed')),
    })

    const error = await http.get('/protected').catch((error: unknown) => error)
    expect(error).toBeInstanceOf(AxiosError)
    expect(parseHttpError(error)).toMatchObject({
      kind: 'unauthorized',
      handled: true,
      shouldNotify: false,
      diagnostics: { status: 401 },
    })
    uninstall()
  })

  it('不把历史业务数值当作 HTTP 认证状态', async () => {
    const http = axios.create({ adapter: unauthorizedAdapter })
    const store = createAuthenticatedStore()
    const invalidate = vi.fn()
    const uninstall = installAuthResponseInterceptor(http, {
      getAuthStore: () => store,
      onAuthenticationInvalidated: invalidate,
    })
    await expect(http.get('/body-code-401')).resolves.toMatchObject({ status: 200 })
    expect(store.clearAuth).not.toHaveBeenCalled()
    expect(invalidate).not.toHaveBeenCalled()
    uninstall()
  })

  it('跳过认证流程请求和匿名状态不触发认证失效处理', async () => {
    const http = axios.create({ adapter: unauthorizedAdapter })
    const store = createAuthenticatedStore()
    const notify = vi.fn()
    const replace = vi.fn().mockResolvedValue(undefined)
    const uninstall = installAuthResponseInterceptor(http, {
      getAuthStore: () => store,
      onAuthenticationInvalidated: async () => {
        notify('登录已失效，请重新登录')
        await replace('/login')
      },
    })

    await Promise.allSettled([
      http.get('/session', { skipAuthInvalidation: true }),
      http.post('/login', {}, { skipAuthInvalidation: true }),
    ])
    expect(store.isAuthenticated).toBe(true)
    store.isAuthenticated = false
    await expect(http.get('/anonymous-request')).rejects.toBeInstanceOf(AxiosError)

    expect(store.clearAuth).not.toHaveBeenCalled()
    expect(notify).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    uninstall()
  })

  it('认证失效处理不会请求服务端登出', async () => {
    const requestedURLs: string[] = []
    const http = axios.create({
      adapter: async (config) => {
        requestedURLs.push(config.url || '')
        return Promise.reject(
          new AxiosError(
            'Request failed with status code 401',
            'ERR_BAD_REQUEST',
            config,
            undefined,
            unauthorizedResponse(config),
          ),
        )
      },
    })
    const store = createAuthenticatedStore()
    const uninstall = installAuthResponseInterceptor(http, {
      getAuthStore: () => store,
      onAuthenticationInvalidated: vi.fn(),
    })

    await expect(http.get('/protected')).rejects.toThrow('Request failed with status code 401')

    expect(requestedURLs).toEqual(['/protected'])
    uninstall()
  })
})
