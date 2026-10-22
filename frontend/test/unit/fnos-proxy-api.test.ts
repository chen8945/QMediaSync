import axios, { AxiosError } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  fetchFnosProxySettings,
  fnosProxyPublicMessage,
  saveFnosProxySettings,
  type FnosProxySettings,
} from '@/api/fnosProxy'
import { HttpResponseError, parseHttpError } from '@/http/errors'

const settings: FnosProxySettings = {
  config: {
    enabled: false,
    fnos_url: '',
    path_mappings: [],
  },
  runtime: {
    running: false,
    listen_port: 0,
    https_enabled: false,
    https_port: 0,
    restart_required: false,
    last_error: '',
  },
}
const client = (data: unknown) => {
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data,
  }))
  return { http: axios.create({ adapter, timeout: 42_000 }), adapter }
}

describe('飞牛影视反代 API', () => {
  it('字段错误使用页面名称，保留具体原因', () => {
    expect(fnosProxyPublicMessage('fnos_url：只支持 http 或 https')).toBe(
      '飞牛影视地址：只支持 http 或 https',
    )
    expect(fnosProxyPublicMessage('path_mappings：第 1 行 QMS 可访问根目录必须是绝对路径')).toBe(
      'STRM 路径映射：第 1 行 QMediaSync 中的 STRM 文件夹必须是绝对路径',
    )
    expect(fnosProxyPublicMessage('服务暂时不可用')).toBe('服务暂时不可用')
  })
  it('使用现有客户端读写，只保存开关、地址和映射', async () => {
    const { http, adapter } = client({ code: 200, data: settings })
    await expect(fetchFnosProxySettings(http)).resolves.toEqual(settings)
    await expect(saveFnosProxySettings(http, settings.config)).resolves.toEqual(settings)
    expect(adapter.mock.calls.map(([config]) => [config.method, config.url])).toEqual([
      ['get', '/api/setting/fnos-proxy'],
      ['post', '/api/setting/fnos-proxy'],
    ])
    const post = adapter.mock.calls[1]![0]
    expect(JSON.parse(post.data)).toEqual(settings.config)
    expect(post.timeout).toBe(42_000)
    expect(post.headers.get('Content-Type')).toBe('application/json')
  })

  it('保留映射与独立运行状态，已保存但应用失败仍返回实际状态', async () => {
    const data = structuredClone(settings)
    data.config = {
      enabled: true,
      fnos_url: 'https://fnos.example.com',
      path_mappings: [{ fnos_root: '/vol1/strm', local_root: '/media/strm' }],
    }
    data.runtime.restart_required = true
    data.runtime.last_error = 'HTTP 端口被占用'
    const { http, adapter } = client({ code: 200, message: '已保存但未能完全应用', data })
    await expect(saveFnosProxySettings(http, data.config)).resolves.toEqual(data)
    expect(JSON.parse(adapter.mock.calls[0]![0].data)).toEqual(data.config)
  })

  it.each([
    null,
    {},
    { ...settings, config: null },
    { ...settings, runtime: null },
    { ...settings, config: { ...settings.config, enabled: 0 } },
    { ...settings, config: { ...settings.config, fnos_url: null } },
    { ...settings, config: { ...settings.config, path_mappings: null } },
    { ...settings, config: { ...settings.config, path_mappings: [null] } },
    { ...settings, config: { ...settings.config, path_mappings: [{ fnos_root: '/a' }] } },
    { ...settings, runtime: { ...settings.runtime, listen_port: 65536 } },
    { ...settings, runtime: { ...settings.runtime, https_port: 1.5 } },
    { ...settings, runtime: { ...settings.runtime, https_enabled: 1 } },
    { ...settings, runtime: { ...settings.runtime, listen_port: '9005' } },
    { ...settings, runtime: { ...settings.runtime, last_error: null } },
  ])('拒绝损坏响应，防止用默认设置覆盖 %j', async (data) => {
    const { http } = client({ code: 200, data })
    await expect(fetchFnosProxySettings(http)).rejects.toBeInstanceOf(HttpResponseError)
    await expect(saveFnosProxySettings(http, settings.config)).rejects.toBeInstanceOf(
      HttpResponseError,
    )
  })

  it('业务失败不能当作保存成功', async () => {
    const { http } = client({ code: 500, message: '设置暂时无法保存', data: null })
    await expect(saveFnosProxySettings(http, settings.config)).rejects.toBeInstanceOf(
      HttpResponseError,
    )
  })

  it('保留认证错误和写入超时，不自动重试', async () => {
    const error = new AxiosError('private-password', 'ETIMEDOUT')
    const adapter = vi.fn().mockRejectedValue(error)
    const http = axios.create({ adapter })
    await expect(saveFnosProxySettings(http, settings.config)).rejects.toBe(error)
    expect(adapter).toHaveBeenCalledTimes(1)
    expect(JSON.stringify(parseHttpError(error).diagnostics)).not.toContain('private')
  })
})
