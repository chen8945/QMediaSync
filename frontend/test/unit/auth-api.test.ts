import axios, { AxiosError } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import { createInitialAdmin, fetchSession, fetchSetupStatus, login, logout } from '@/api/auth'
import { HttpResponseError } from '@/http/errors'

describe('认证 API', () => {
  it('使用传入客户端、原有载荷和会话配置，不修改匿名查询语义', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      data: {
        code: 200,
        message: '',
        data: config.url.endsWith('/setup/status') ? { required: false } : { authenticated: false },
      },
      status: 200,
      statusText: 'OK',
      headers: {},
    }))
    const http = axios.create({ adapter })
    const credentials = {
      username: 'admin',
      password: 'secret',
      totp_code: '123456',
      rememberMe: true,
    }
    const initialAdmin = { setup_token: 'setup-secret', username: 'admin', password: 'secret' }
    expect(await fetchSetupStatus(http)).toEqual({ required: false })
    await createInitialAdmin(http, initialAdmin)
    await login(http, credentials)
    expect(await fetchSession(http)).toEqual({ authenticated: false })
    await logout(http)

    const configs = adapter.mock.calls.map(([config]) => config)
    expect(configs.map((config) => [config.method, config.url])).toEqual([
      ['get', '/api/setup/status'],
      ['post', '/api/setup/admin'],
      ['post', '/api/login'],
      ['get', '/api/session'],
      ['post', '/api/logout'],
    ])
    expect(JSON.parse(configs[1].data)).toEqual(initialAdmin)
    expect(JSON.parse(configs[2].data)).toEqual(credentials)
    expect(configs[2].skipAuthInvalidation).toBe(true)
    expect(configs[2].headers.get('Content-Type')).toBe('application/json')
    for (const config of configs.slice(3)) {
      expect(config).toMatchObject({ skipAuthInvalidation: true, withCredentials: true })
    }
  })

  it('所有请求都检查业务码，失败不会被返回成成功', async () => {
    const http = axios.create({
      adapter: async (config) => ({
        config,
        data: { code: 500, message: 'internal secret', data: null },
        status: 200,
        statusText: 'OK',
        headers: {},
      }),
    })
    for (const request of [
      () => fetchSetupStatus(http),
      () => createInitialAdmin(http, { setup_token: '', username: '', password: '' }),
      () => login(http, { username: '', password: '', totp_code: '', rememberMe: false }),
      () => fetchSession(http),
      () => logout(http),
    ]) {
      await expect(request()).rejects.toBeInstanceOf(HttpResponseError)
    }
  })

  it('写入超时不重试且保留原始错误', async () => {
    const failure = new AxiosError('timeout', 'ETIMEDOUT')
    const adapter = vi.fn().mockRejectedValue(failure)
    const http = axios.create({ adapter })
    await expect(
      createInitialAdmin(http, { setup_token: '', username: '', password: '' }),
    ).rejects.toBe(failure)
    expect(adapter).toHaveBeenCalledTimes(1)
  })
})
