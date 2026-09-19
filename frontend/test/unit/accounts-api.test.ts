import axios, { AxiosError, CanceledError, type AxiosInstance } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  accountPublicMessages,
  authorizationPublicMessages,
  cancelAccountAuthorization,
  confirmBaiduOAuth,
  confirmV115OAuth,
  createAccount,
  deleteAccount,
  fetchAccountStatus,
  fetchBaiduOAuthURL,
  fetchV115AppIds,
  fetchV115OAuthStatus,
  fetchV115OAuthURL,
  fetchV115QRCodeStatus,
  listAccounts,
  openV115QRCode,
  prepareAccountAuthorization,
  saveOpenListAccount,
  updateAccount,
  V115_QR_STATUS_TIMEOUT_MS,
  type CreateAccountPayload,
  type PrepareAccountAuthorizationPayload,
} from '@/api/accounts'
import { SERVER_URL } from '@/const'
import { HttpResponseError, markAuthInvalidationHandled, parseHttpError } from '@/http/errors'

const createPayload: CreateAccountPayload = {
  source_type: '115',
  name: '我的网盘',
  app_id: '100197849',
  app_id_name: 'QMediaSync',
  custom_app_name: '',
  auth_source_type: 'built_in_appid',
  auth_provider: 'official_pkce',
}
const preparePayload: PrepareAccountAuthorizationPayload = {
  ...createPayload,
  account_id: 12,
  confirmed: true,
  auth_source_type: 'built_in_appid',
  auth_provider: 'official_pkce',
}
const oauthParams = {
  account_id: 12,
  redirect_url: 'https://qms.example/callback?tab=accounts',
  authorization_id: 'replacement-session',
}
const callbackPayload = {
  account_id: 12,
  data: 'encrypted-callback',
  payload: { state: 'oauth-state', authorization_id: 'replacement-session' },
  authorization_id: 'replacement-session',
}

const operations: [string, (http: AxiosInstance) => Promise<unknown>][] = [
  ['list', (http) => listAccounts(http)],
  ['create', (http) => createAccount(http, createPayload)],
  [
    'OpenList',
    (http) =>
      saveOpenListAccount(http, {
        base_url: 'http://openlist:5244',
        auth_type: 'token',
        token: 'private-token',
      }),
  ],
  ['update', (http) => updateAccount(http, { id: 12, name: '新备注' })],
  ['delete', (http) => deleteAccount(http, 12)],
  ['115 status', (http) => fetchAccountStatus(http, '115', 12)],
  ['Baidu status', (http) => fetchAccountStatus(http, 'baidupan', 12)],
  ['prepare', (http) => prepareAccountAuthorization(http, preparePayload)],
  ['cancel', (http) => cancelAccountAuthorization(http, 12, 'replacement-session')],
  ['115 OAuth URL', (http) => fetchV115OAuthURL(http, oauthParams)],
  [
    '115 OAuth status',
    (http) =>
      fetchV115OAuthStatus(http, {
        account_id: 12,
        state: 'oauth-state',
        authorization_id: 'replacement-session',
      }),
  ],
  ['115 OAuth confirm', (http) => confirmV115OAuth(http, callbackPayload)],
  ['Baidu OAuth URL', (http) => fetchBaiduOAuthURL(http, oauthParams)],
  ['Baidu OAuth confirm', (http) => confirmBaiduOAuth(http, callbackPayload)],
  ['QR open', (http) => openV115QRCode(http, 12, 'replacement-session')],
  [
    'QR status',
    (http) =>
      fetchV115QRCodeStatus(http, {
        account_id: 12,
        uid: 'qr-uid',
        authorization_id: 'replacement-session',
      }),
  ],
  ['APP ID search', (http) => fetchV115AppIds(http, { keyword: '应用', offset: 50, limit: 50 })],
]

describe('账号与授权 API', () => {
  it('保留接口、载荷、查询参数与专用超时', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      data: {
        code: 200,
        data: config.url.endsWith('/account/list')
          ? []
          : {
              authorization_id: 'replacement-session',
              expires_in: 600,
              done: false,
            },
      },
      status: 200,
      statusText: 'OK',
      headers: {},
    }))
    const http = axios.create({ adapter, timeout: 30_000 })
    for (const [, operation] of operations) await operation(http)
    const configs = adapter.mock.calls.map(([config]) => config)
    expect(configs.map(({ method, url }) => [method, url.replace(SERVER_URL, '')])).toEqual([
      ['get', '/account/list'],
      ['post', '/account/add'],
      ['post', '/account/openlist'],
      ['post', '/account/update'],
      ['post', '/account/delete'],
      ['get', '/115/status'],
      ['get', '/baidupan/status'],
      ['post', '/account/authorization/prepare'],
      ['post', '/account/authorization/cancel'],
      ['get', '/115/oauth-url'],
      ['get', '/115/oauth-status'],
      ['post', '/115/oauth-confirm'],
      ['get', '/baidupan/oauth-url'],
      ['post', '/baidupan/oauth-confirm'],
      ['post', '/auth/115-qrcode-open'],
      ['post', '/auth/115-qrcode-status'],
      ['get', '/115/appids'],
    ])
    expect(JSON.parse(configs[1].data)).toEqual(createPayload)
    expect(JSON.parse(configs[2].data)).toEqual({
      base_url: 'http://openlist:5244',
      auth_type: 'token',
      token: 'private-token',
    })
    expect(JSON.parse(configs[3].data)).toEqual({ id: 12, name: '新备注' })
    expect(JSON.parse(configs[4].data)).toEqual({ id: 12 })
    expect(configs[5].params).toEqual({ account_id: 12 })
    expect(configs[6].params).toEqual({ account_id: 12 })
    expect(JSON.parse(configs[7].data)).toEqual(preparePayload)
    expect(JSON.parse(configs[8].data)).toEqual({
      account_id: 12,
      authorization_id: 'replacement-session',
    })
    expect(configs[9].params).toEqual(oauthParams)
    expect(configs[10].params).toEqual({
      account_id: 12,
      state: 'oauth-state',
      authorization_id: 'replacement-session',
    })
    expect(JSON.parse(configs[11].data)).toEqual(callbackPayload)
    expect(configs[12].params).toEqual(oauthParams)
    expect(JSON.parse(configs[13].data)).toEqual(callbackPayload)
    expect(JSON.parse(configs[14].data)).toEqual({
      account_id: 12,
      authorization_id: 'replacement-session',
    })
    expect(JSON.parse(configs[15].data)).toEqual({
      account_id: 12,
      uid: 'qr-uid',
      authorization_id: 'replacement-session',
    })
    expect(configs[15].timeout).toBe(70_000)
    expect(V115_QR_STATUS_TIMEOUT_MS).toBe(70_000)
    expect(configs[14].timeout).toBe(30_000)
    expect(configs[16].params).toEqual({ keyword: '应用', offset: 50, limit: 50 })
  })

  // 业务失败拒绝由共享 unwrapResponse 保证，这里只保留一个代表性写入操作验证凭据不外泄。
  it('拒绝 HTTP 200 业务失败并保留原响应', async () => {
    const body = {
      code: 500,
      message: '账号授权已过期',
      data: { error_code: 'ACCOUNT_CONFLICT' },
    }
    const http = axios.create({
      adapter: async (config) => ({
        config,
        data: body,
        status: 200,
        statusText: 'OK',
        headers: {},
      }),
    })
    const error = await createAccount(http, createPayload).catch((failure: unknown) => failure)
    expect(error).toBeInstanceOf(HttpResponseError)
    expect(error).toMatchObject({ response: { status: 200, data: body } })
    expect(parseHttpError(error, { publicMessages: authorizationPublicMessages }).message).toBe(
      body.message,
    )
  })

  it('空账号列表、空写入结果与 done=false 都按各自契约保留', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      data: { code: 200, data: config.url.endsWith('/oauth-status') ? { done: false } : null },
      status: 200,
      statusText: 'OK',
      headers: {},
    }))
    const http = axios.create({ adapter })
    await expect(listAccounts(http, '115')).resolves.toEqual([])
    expect(adapter.mock.calls[0]?.[0].params).toEqual({ source_type: '115' })
    await expect(
      saveOpenListAccount(http, {
        id: 12,
        base_url: 'http://openlist:5244',
        auth_type: 'password',
        username: '',
        password: '',
      }),
    ).resolves.toBeUndefined()
    await expect(deleteAccount(http, 12)).resolves.toBeUndefined()
    await expect(confirmV115OAuth(http, callbackPayload)).resolves.toBeUndefined()
    await expect(confirmBaiduOAuth(http, callbackPayload)).resolves.toBeUndefined()
    await expect(
      cancelAccountAuthorization(http, 12, 'replacement-session'),
    ).resolves.toBeUndefined()
    await expect(
      fetchV115OAuthStatus(http, { account_id: 12, state: 'oauth-state' }),
    ).resolves.toEqual({ done: false })
  })

  it.each([null, {}, { authorization_id: '' }, { authorization_id: '  ' }])(
    '拒绝缺失会话 ID 的授权准备响应 %j',
    async (data) => {
      const http = axios.create({
        adapter: async (config) => ({
          config,
          data: { code: 200, data },
          status: 200,
          statusText: 'OK',
          headers: {},
        }),
      })
      await expect(prepareAccountAuthorization(http, preparePayload)).rejects.toBeInstanceOf(
        HttpResponseError,
      )
    },
  )

  it('账号新增无响应时保留传输异常，超时不自动重试', async () => {
    for (const code of ['ERR_NETWORK', 'ETIMEDOUT']) {
      const adapter = vi.fn(async (config) => {
        throw new AxiosError('transport token=private-token', code, config)
      })
      const http = axios.create({ adapter })
      const error = await createAccount(http, createPayload).catch((failure: unknown) => failure)
      expect(error).toBeInstanceOf(AxiosError)
      const parsed = parseHttpError(error)
      expect(parsed.kind).toBe(code === 'ERR_NETWORK' ? 'network' : 'timeout')
      expect(parsed.message).not.toContain('private-token')
      if (code === 'ETIMEDOUT') expect(parsed.message).toContain('操作结果尚未确认')
      expect(adapter).toHaveBeenCalledTimes(1)
    }
  })

  it('取消与已处理认证异常不被包装，保留静默标记', async () => {
    const handled = new AxiosError('login expired', 'ERR_BAD_REQUEST')
    markAuthInvalidationHandled(handled)
    for (const failure of [new CanceledError(), handled]) {
      const http = axios.create({ adapter: vi.fn().mockRejectedValue(failure) })
      await expect(openV115QRCode(http, 12)).rejects.toBe(failure)
      expect(parseHttpError(failure).shouldNotify).toBe(false)
    }
  })

  it('保留授权文案改写，OAuth 诊断不包含回调和凭据', async () => {
    const allowed = '保存 115 授权失败：当前账号已存在，不允许添加重复账号'
    const parse = (message: string) =>
      parseHttpError(
        { response: { status: 200, data: { code: 500, message } } },
        { publicMessages: authorizationPublicMessages, fallbackMessage: '授权失败' },
      )
    expect(parse(allowed).message).toBe('当前账号已存在，不允许添加重复账号')
    expect(accountPublicMessages['创建开放平台账号失败：账号备注已存在，请换一个']).toBe(
      '账号备注已存在，请换一个',
    )
    const http = axios.create({
      adapter: async (config) => ({
        config: {
          ...config,
          url: `${config.url}?authorization_id=private-session#private-fragment`,
        },
        data: {
          code: 500,
          error_code: 'SESSION_INVALID',
          message: 'private-token',
          data: callbackPayload,
        },
        status: 401,
        statusText: 'Unauthorized',
        headers: {},
      }),
    })
    const error = await confirmV115OAuth(http, callbackPayload).catch((failure: unknown) => failure)
    const parsed = parseHttpError(error, { publicMessages: authorizationPublicMessages })
    expect(parsed.diagnostics).toEqual({
      method: 'POST',
      path: `${SERVER_URL}/115/oauth-confirm`,
      status: 401,
      errorCode: 'SESSION_INVALID',
    })
    expect(JSON.stringify(parsed.diagnostics)).not.toMatch(/private|encrypted|callback|payload/)
  })
})
