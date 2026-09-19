import axios, { AxiosError, CanceledError, type AxiosInstance } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  embyPublicMessages,
  extractEmbyMediaInfo,
  fetchEmbyConfig,
  fetchEmbyLibraries,
  fetchEmbySyncStatus,
  saveEmbyConfig,
  startEmbySync,
  type EmbyConfig,
} from '@/api/emby'
import {
  fetchProxySettings,
  proxySettingsPublicMessages,
  saveProxySettings,
  testProxyConnection,
} from '@/api/proxySettings'
import { HttpResponseError, markAuthInvalidationHandled, parseHttpError } from '@/http/errors'

const emby: EmbyConfig = {
  emby_url: 'http://emby:8096',
  emby_api_key: 'private-key',
  sync_enabled: 1,
  sync_cron: '0 * * * *',
  enable_refresh_library: 1,
  enable_extract_media_info: 1,
  enable_delete_netdisk: 0,
  enable_auth: 1,
  sync_all_libraries: 0,
  selected_libraries: '["movies"]',
  enable_daily_first_full_sync: 0,
  enable_playback_overview: 1,
  enable_playback_progress: 0,
}
const proxy = {
  http_proxy: 'http://user:private-password@proxy:8080',
  preserve_proxy_credentials: false,
}
const operations: [string, (http: AxiosInstance) => Promise<unknown>][] = [
  ['Emby config', fetchEmbyConfig],
  ['save Emby', (http) => saveEmbyConfig(http, emby)],
  ['libraries', fetchEmbyLibraries],
  ['extract', (http) => extractEmbyMediaInfo(http)],
  ['sync start', startEmbySync],
  ['sync status', fetchEmbySyncStatus],
  ['proxy settings', fetchProxySettings],
  ['save proxy', (http) => saveProxySettings(http, proxy)],
  ['test proxy', (http) => testProxyConnection(http, proxy)],
]

describe('Emby 与代理 API', () => {
  it('保留路径、方法、全部字段、JSON 头与客户端超时', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: {
        code: 200,
        data: config.url.endsWith('/emby-config')
          ? { exists: true, config: emby }
          : config.url.endsWith('/emby/libraries')
            ? []
            : { http_proxy: '', credentials_masked: '0' },
      },
    }))
    const http = axios.create({ adapter, timeout: 42_000 })
    for (const [, operation] of operations) await operation(http)
    const configs = adapter.mock.calls.map(([config]) => config)
    expect(configs.map(({ method, url }) => [method, url])).toEqual([
      ['get', '/api/setting/emby-config'],
      ['post', '/api/setting/emby-config'],
      ['get', '/api/emby/libraries'],
      ['post', '/api/setting/emby/parse'],
      ['post', '/api/emby/sync/start'],
      ['get', '/api/emby/sync/status'],
      ['get', '/api/setting/http-proxy'],
      ['post', '/api/setting/http-proxy'],
      ['post', '/api/setting/test-http-proxy'],
    ])
    expect(JSON.parse(configs[1].data)).toEqual(emby)
    expect(configs[3].data).toBeUndefined()
    expect(configs[4].data).toBeUndefined()
    for (const index of [1, 7, 8])
      expect(configs[index].headers.get('Content-Type')).toBe('application/json')
    for (const index of [7, 8]) expect(JSON.parse(configs[index].data)).toEqual(proxy)
    expect(configs.every((config) => config.timeout === 42_000)).toBe(true)
  })

  it.each(operations)('%s 拒绝业务失败并保留安全诊断', async (_name, operation) => {
    const body = {
      code: 500,
      message: '第三方服务暂不可用',
      data: { warning: 'private-key' },
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
    const failure = await operation(http).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(HttpResponseError)
    expect(failure).toMatchObject({ response: { data: body } })
    const parsed = parseHttpError(failure, {
      publicMessages: { ...embyPublicMessages, ...proxySettingsPublicMessages },
    })
    expect(JSON.stringify(parsed.diagnostics)).not.toContain('private')
  })

  it('合法空结果、未配置状态和显式清空代理正常返回', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      data: {
        code: 200,
        data:
          config.url.endsWith('/emby-config') || config.url.endsWith('/sync/status')
            ? { exists: false }
            : null,
      },
      status: 200,
      statusText: 'OK',
      headers: {},
    }))
    const http = axios.create({ adapter })
    await expect(fetchEmbyConfig(http)).resolves.toEqual({ exists: false })
    await expect(fetchEmbySyncStatus(http)).resolves.toEqual({ exists: false })
    await expect(fetchEmbyLibraries(http)).resolves.toEqual([])
    await expect(
      saveProxySettings(http, { http_proxy: '', preserve_proxy_credentials: false }),
    ).resolves.toBeUndefined()
    expect(JSON.parse(adapter.mock.calls.at(-1)?.[0].data)).toEqual({
      http_proxy: '',
      preserve_proxy_credentials: false,
    })
    await expect(extractEmbyMediaInfo(http)).resolves.toBeUndefined()
    await expect(testProxyConnection(http, proxy)).resolves.toBeUndefined()
  })

  it.each([null, {}, { http_proxy: null }])('代理回读缺失地址时拒绝成功响应 %j', async (data) => {
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: { code: 200, data },
      }),
    })
    await expect(fetchProxySettings(http)).rejects.toBeInstanceOf(HttpResponseError)
  })

  it.each([null, {}, { exists: true }, { exists: true, config: null }])(
    'Emby 回读缺失配置时不能按未配置处理 %j',
    async (data) => {
      const http = axios.create({
        adapter: async (config) => ({
          config,
          status: 200,
          statusText: 'OK',
          headers: {},
          data: { code: 200, data },
        }),
      })
      await expect(fetchEmbyConfig(http)).rejects.toBeInstanceOf(HttpResponseError)
    },
  )

  it('取消、已处理 401 和写入超时保留原错误且不重试', async () => {
    const handled = new AxiosError('private-key', 'ERR_BAD_REQUEST')
    markAuthInvalidationHandled(handled)
    for (const error of [
      new CanceledError(),
      handled,
      new AxiosError('private-password', 'ETIMEDOUT'),
    ]) {
      const adapter = vi.fn().mockRejectedValue(error)
      const http = axios.create({ adapter })
      await expect(saveProxySettings(http, proxy)).rejects.toBe(error)
      expect(adapter).toHaveBeenCalledTimes(1)
      if (error.code !== 'ETIMEDOUT') expect(parseHttpError(error).shouldNotify).toBe(false)
    }
  })

  it('来源错误优先于第三方连接说明', () => {
    const message = '已有 Emby 条目同步任务正在运行，请稍后再试'
    const failure = { response: { status: 200, data: { code: 500, message } } }
    expect(parseHttpError(failure, { publicMessages: embyPublicMessages }).message).toBe(message)
    const origin = {
      response: {
        status: 403,
        data: { code: 500, message: '出站代理连接测试失败', error_code: 'REQUEST_ORIGIN_INVALID' },
      },
    }
    expect(
      parseHttpError(origin, { publicMessages: proxySettingsPublicMessages }).message,
    ).toContain('访问地址校验失败')
  })
})
