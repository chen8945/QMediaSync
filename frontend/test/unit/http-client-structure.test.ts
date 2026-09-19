import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import axios, { AxiosHeaders, type AxiosAdapter, type InternalAxiosRequestConfig } from 'axios'
import { createApp } from 'vue'
import { describe, expect, it } from 'vitest'

import { configureHttpClient, http, httpKey, useHttpClient } from '../../src/http/client'

const mainPath = resolve(dirname(fileURLToPath(import.meta.url)), '../../src/main.ts')

const successAdapter: AxiosAdapter = async (config) => ({
  config,
  data: null,
  headers: {},
  status: 200,
  statusText: 'OK',
})

describe('HTTP 客户端组织', () => {
  it('使用隔离的 Axios 实例，而不是全局 Axios 静态对象', () => {
    expect(http).not.toBe(axios)
  })

  it('通过类型化注入键提供 HTTP 客户端', () => {
    const client = axios.create()
    const app = createApp({})
    app.provide(httpKey, client)

    expect(app.runWithContext(useHttpClient)).toBe(client)
  })

  it('应用入口通过 HTTP 客户端模块配置 Axios', () => {
    const source = readFileSync(mainPath, 'utf8')

    expect(source).toContain("import { configureHttpClient, http, httpKey } from '@/http/client'")
    expect(source).not.toContain("import axios from 'axios'")
    expect(source).toContain('configureHttpClient({')
    expect(source).toContain('app.provide(httpKey, http)')
    expect(source).not.toContain("app.provide('$http', http)")
  })

  it('配置请求拦截器以移除旧认证头并附加 CSRF Token，且不为 GET 设置 Content-Type', async () => {
    const client = axios.create({ adapter: successAdapter })
    const authStore = {
      csrfToken: 'csrf-token',
      isAuthenticated: true,
      isLoggingOut: false,
      sessionVersion: 1,
      clearAuth: () => undefined,
    }
    let requestConfig: InternalAxiosRequestConfig | undefined
    client.defaults.adapter = async (config) => {
      requestConfig = config
      return successAdapter(config)
    }

    const uninstall = configureHttpClient({
      http: client,
      getAuthStore: () => authStore,
      onAuthenticationInvalidated: () => undefined,
    })

    await client.post('/protected', undefined, { headers: { Authorization: 'Bearer stale-token' } })

    const headers = AxiosHeaders.from(requestConfig?.headers)
    expect(client.defaults.timeout).toBe(10000)
    expect(client.defaults.withCredentials).toBe(true)
    expect(headers.get('Authorization')).toBeUndefined()
    expect(headers.get('X-CSRF-Token')).toBe('csrf-token')

    await client.get('/public')

    const getHeaders = AxiosHeaders.from(requestConfig?.headers)
    expect(getHeaders.get('Content-Type')).toBeUndefined()
    uninstall()
  })
})
