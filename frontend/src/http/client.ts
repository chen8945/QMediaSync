import axios, { type AxiosInstance } from 'axios'
import { inject, type InjectionKey } from 'vue'

import { installAuthResponseInterceptor, type AuthInvalidationStore } from '@/http/authInterceptor'
import { getCSRFTokenFromCookie, shouldAttachCSRFToken } from '@/utils/csrf'

type HttpAuthStore = AuthInvalidationStore & {
  csrfToken: string | null
}

type ConfigureHttpClientOptions = {
  http?: AxiosInstance
  getAuthStore: () => HttpAuthStore
  onAuthenticationInvalidated: () => void | Promise<void>
}

// 业务请求使用独立实例，避免改写 Axios 全局默认配置。
// 超时改用 ETIMEDOUT，与浏览器中止请求的 ECONNABORTED 区分。
export const http = axios.create({ transitional: { clarifyTimeoutError: true } })

export const httpKey: InjectionKey<AxiosInstance> = Symbol('http')

// 获取已由应用入口提供的 HTTP 客户端。
export const useHttpClient = (): AxiosInstance => {
  const client = inject(httpKey)
  if (!client) {
    throw new Error('HTTP 客户端未初始化')
  }
  return client
}

// 配置默认 HTTP 客户端
export const configureHttpClient = (options: ConfigureHttpClientOptions) => {
  const client = options.http ?? http

  client.defaults.timeout = 10000
  client.defaults.withCredentials = true

  const requestInterceptorID = client.interceptors.request.use(
    (config) => {
      config.headers.delete('Authorization')

      const authStore = options.getAuthStore()
      if (shouldAttachCSRFToken(config.method)) {
        const csrfToken = authStore.csrfToken || getCSRFTokenFromCookie()
        if (csrfToken) {
          config.headers.set('X-CSRF-Token', csrfToken)
        }
      }
      return config
    },
    (error) => {
      return Promise.reject(error)
    },
  )

  const uninstallAuthResponseInterceptor = installAuthResponseInterceptor(client, options)

  return () => {
    client.interceptors.request.eject(requestInterceptorID)
    uninstallAuthResponseInterceptor()
  }
}
