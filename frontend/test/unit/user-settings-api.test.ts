import axios, { type AxiosInstance, type InternalAxiosRequestConfig } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  changeUserCredentials,
  disableTwoFactor,
  enableTwoFactor,
  fetchCurrentUser,
  fetchLoginSessions,
  fetchTwoFactorStatus,
  revokeLoginSession,
  revokeOtherLoginSessions,
  setupTwoFactor,
  userSettingsPublicMessages,
} from '@/api/userSettings'
import { HttpResponseError, parseHttpError } from '@/http/errors'

const operations: [string, string, (http: AxiosInstance) => Promise<unknown>][] = [
  ['get', '/user/info', fetchCurrentUser],
  [
    'post',
    '/user/change',
    (http) => changeUserCredentials(http, { username: 'admin', new_password: 'Pass123' }),
  ],
  ['get', '/user/two-factor/status', fetchTwoFactorStatus],
  ['post', '/user/two-factor/setup', setupTwoFactor],
  ['post', '/user/two-factor/enable', (http) => enableTwoFactor(http, '123456')],
  [
    'post',
    '/user/two-factor/disable',
    (http) => disableTwoFactor(http, { password: 'Pass123', totp_code: '123456' }),
  ],
  ['get', '/user/sessions', fetchLoginSessions],
  ['delete', '/user/sessions/sid', (http) => revokeLoginSession(http, 'sid')],
  ['post', '/user/sessions/revoke-others', revokeOtherLoginSessions],
]

describe('用户设置 API', () => {
  it.each(operations)('%s %s 保留请求契约并拒绝业务失败', async (method, path, operation) => {
    const adapter = vi.fn(async (config: InternalAxiosRequestConfig) => ({
      data: { code: 500, message: 'private secret', data: null },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }))
    const http = axios.create({ adapter })
    await expect(operation(http)).rejects.toBeInstanceOf(HttpResponseError)
    expect(adapter).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ method, url: `/api${path}` }),
    )
  })

  it.each([true, false])('修改凭据保留重新登录标记 %s', async (data) => {
    const adapter = vi.fn(async (config: InternalAxiosRequestConfig) => ({
      data: { code: 200, data },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }))
    const http = axios.create({ adapter })
    await expect(
      changeUserCredentials(http, { username: 'admin', new_password: 'Pass123' }),
    ).resolves.toBe(data)
    expect(JSON.parse(adapter.mock.calls[0]![0].data)).toEqual({
      username: 'admin',
      new_password: 'Pass123',
    })
  })

  it('空登录设备列表仍为正常结果', async () => {
    const http = axios.create({
      adapter: async (config) => ({
        data: { code: 200, data: null },
        status: 200,
        statusText: 'OK',
        headers: {},
        config,
      }),
    })
    await expect(fetchLoginSessions(http)).resolves.toEqual([])
  })

  it.each([
    ['新密码不能与当前密码相同', '新密码不能与当前密码相同'],
    ['new_password：不能是纯数字或纯字母', '密码不能是纯数字或纯字母'],
  ])('保留用户设置文案：%s', (message, expected) => {
    const failure = parseHttpError(
      new HttpResponseError({ status: 200, data: { code: 500, message } }),
      {
        publicMessages: userSettingsPublicMessages,
        fallbackMessage: '保存用户设置失败',
      },
    )
    expect(failure.message).toBe(expected)
    expect(failure.diagnostics).toEqual({ status: 200 })
  })
})
