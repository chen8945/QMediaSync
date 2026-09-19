import type { AxiosInstance, AxiosResponse, InternalAxiosRequestConfig } from 'axios'
import { HttpResponseError, markAuthInvalidationHandled, parseHttpError } from '@/http/errors'

export type AuthInvalidationStore = {
  isAuthenticated: boolean
  isLoggingOut: boolean
  // clearAuth 与建立会话都会递增版本，版本变化即表示旧批次已失效。
  sessionVersion: number
  clearAuth: () => void
}

type AuthResponseInterceptorOptions = {
  getAuthStore: () => AuthInvalidationStore
  onAuthenticationInvalidated: () => void | Promise<void>
}

type AuthRequestWave = {
  sessionVersion: number
  promise: Promise<void> | null
}

export const installAuthResponseInterceptor = (
  http: AxiosInstance,
  options: AuthResponseInterceptorOptions,
) => {
  let currentWave: AuthRequestWave | undefined
  const requestWaves = new WeakMap<InternalAxiosRequestConfig, AuthRequestWave>()

  const requestInterceptorID = http.interceptors.request.use((config) => {
    const authStore = options.getAuthStore()
    if (!config.skipAuthInvalidation && authStore.isAuthenticated && !authStore.isLoggingOut) {
      if (currentWave?.sessionVersion !== authStore.sessionVersion) {
        currentWave = { sessionVersion: authStore.sessionVersion, promise: null }
      }
      requestWaves.set(config, currentWave)
    }
    return config
  })

  const handleAuthenticationFailure = async (
    error: object,
    config?: InternalAxiosRequestConfig,
  ) => {
    const authStore = options.getAuthStore()
    if (config?.skipAuthInvalidation) return false
    const wave = config && requestWaves.get(config)
    if (!wave) return false

    if (
      !authStore.isLoggingOut &&
      authStore.isAuthenticated &&
      wave.sessionVersion === authStore.sessionVersion
    ) {
      authStore.clearAuth()
      wave.promise = Promise.resolve().then(options.onAuthenticationInvalidated)
    }

    // 同批迟到的失败仍归属于旧会话，不清理之后重新建立的会话。
    markAuthInvalidationHandled(error)
    // 导航失败不能替换原始 HTTP 错误，否则页面无法读取状态和处理标记。
    await wave.promise?.catch(() => undefined)
    return true
  }

  const interceptorID = http.interceptors.response.use(
    async (response: AxiosResponse) => {
      if (parseHttpError({ response }).kind !== 'unauthorized') return response

      const error = new HttpResponseError(response)
      if (await handleAuthenticationFailure(error, response.config)) {
        return Promise.reject(error)
      }

      return response
    },
    async (error) => {
      if (parseHttpError(error).kind === 'unauthorized') {
        await handleAuthenticationFailure(error, error.config)
      }
      return Promise.reject(error)
    },
  )

  return () => {
    http.interceptors.request.eject(requestInterceptorID)
    http.interceptors.response.eject(interceptorID)
  }
}
