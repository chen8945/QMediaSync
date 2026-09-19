import axios, { AxiosError } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  fetchCronTimes,
  fetchLogSettings,
  fetchStrmSettings,
  fetchThreadSettings,
  saveLogSettings,
  saveStrmSettings,
  saveThreadSettings,
  strmRegexValidationIssue,
  type LogSettings,
  type StrmSettings,
  type ThreadSettings,
} from '@/api/systemSettings'
import { HttpResponseError, parseHttpError } from '@/http/errors'

const strm: StrmSettings = {
  video_ext_arr: [],
  min_video_size: 0,
  meta_ext_arr: [],
  cron: ' 0 2 * * * ',
  strm_base_url: 'http://qms.local',
  upload_meta: 0,
  download_meta: 1,
  delete_dir: 0,
  multi_playback_enabled: 1,
  local_proxy: 1,
  exclude_name_arr: [],
  exclude_name_regex_arr: [String.raw`  (?i)Sample{1,3},Trailer;\D+  `],
  add_path: 3,
  check_meta_mtime: 0,
}
const threads: ThreadSettings = {
  download_threads: 2,
  upload_threads: 10,
  file_detail_threads: 4,
  openlist_qps: 3,
  openlist_retry: 2,
  openlist_retry_delay: 45,
  file_list_page_size: 1000,
  url_validity_check_enabled: 0,
  url_validity_check_timeout_seconds: 8,
  upload_rapid_wait_enabled: 1,
  upload_rapid_wait_timeout_seconds: 0,
  upload_rapid_wait_interval_seconds: 30,
  upload_rapid_wait_min_size: 100 * 1024 * 1024,
  upload_rapid_wait_force_size: 500 * 1024 * 1024,
  upload_rapid_wait_skip_upload: 1,
}
const logs: LogSettings = { level: 'debug', maxSizeMB: 100, maxBackups: 5, maxAgeDays: 30 }
const logResponse = { ...logs, levels: ['debug', 'info', 'warn', 'error'] }

describe('系统设置 API', () => {
  it('保留地址、客户端超时、完整载荷与正则原文，日志保存返回生效配置', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: {
        code: 200,
        message: '',
        data: config.url.endsWith('/log')
          ? logResponse
          : config.method === 'post' || config.url.endsWith('/cron')
            ? null
            : config.url.endsWith('/threads')
              ? threads
              : strm,
      },
    }))
    const http = axios.create({ adapter, timeout: 8765 })

    expect(await fetchStrmSettings(http)).toEqual(strm)
    expect(await fetchThreadSettings(http)).toEqual(threads)
    expect(await fetchLogSettings(http)).toEqual(logResponse)
    await expect(saveStrmSettings(http, strm)).resolves.toBeUndefined()
    await expect(saveThreadSettings(http, threads)).resolves.toBeUndefined()
    expect(await saveLogSettings(http, logs)).toEqual(logResponse)
    expect(await fetchCronTimes(http, strm.cron)).toEqual([])

    const configs = adapter.mock.calls.map(([config]) => config)
    expect(configs.map(({ method, url, timeout }) => [method, url, timeout])).toEqual([
      ['get', '/api/setting/strm-config', 8765],
      ['get', '/api/setting/threads', 8765],
      ['get', '/api/setting/log', 8765],
      ['post', '/api/setting/strm-config', 8765],
      ['post', '/api/setting/threads', 8765],
      ['post', '/api/setting/log', 8765],
      ['get', '/api/setting/cron', 8765],
    ])
    expect(configs.slice(3, 6).map((config) => JSON.parse(config.data))).toEqual([
      strm,
      threads,
      logs,
    ])
    expect(configs[3].headers.get('Content-Type')).toBe('application/json')
    expect(configs[6].params).toEqual({ cron: strm.cron })
  })

  it('Cron 字符串及数字列表按接口原样返回', async () => {
    const times = ['2026-09-21 02:00:00', 1790000000]
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: { code: 200, message: '', data: times },
      }),
    })
    expect(await fetchCronTimes(http, strm.cron)).toEqual(times)
  })

  it('所有操作均拒绝 HTTP 200 业务失败，保留响应及字段详情', async () => {
    const body = {
      code: 500,
      message: 'internal secret',
      data: { field_errors: [{ field: 'cron', message: 'internal secret' }] },
    }
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: body,
      }),
    })
    for (const request of [
      () => fetchStrmSettings(http),
      () => fetchThreadSettings(http),
      () => fetchLogSettings(http),
      () => fetchCronTimes(http, strm.cron),
      () => saveStrmSettings(http, strm),
      () => saveThreadSettings(http, threads),
      () => saveLogSettings(http, logs),
    ]) {
      await expect(request()).rejects.toMatchObject({
        name: 'HttpResponseError',
        response: { status: 200, data: body },
      })
    }
  })

  it('保存超时不重发写入请求，保留结果未确认的提示', async () => {
    const adapter = vi.fn(async (config) => {
      throw new AxiosError('timeout secret', 'ETIMEDOUT', config)
    })
    const failure = await saveStrmSettings(axios.create({ adapter }), strm).catch(
      (error: unknown) => error,
    )
    expect(adapter).toHaveBeenCalledTimes(1)
    expect(parseHttpError(failure)).toMatchObject({
      kind: 'timeout',
      message: '请求超时，操作结果尚未确认。请先检查操作是否已生效，避免重复提交',
      diagnostics: { method: 'POST', path: '/api/setting/strm-config' },
    })
  })

  it('正则错误只提取字段位置与原因，丢弃内部详情且不覆盖来源或服务器失败', () => {
    const parse = (message: string, status = 400, errorCode?: string) =>
      parseHttpError(
        new HttpResponseError({
          status,
          data: { code: 500, message, error_code: errorCode, data: null },
        }),
      )
    const message = 'exclude_name_regex_arr[2]：正则表达式无效：error parsing regexp: secret'
    expect(strmRegexValidationIssue(parse(message))).toEqual({ position: 3, reason: 'invalid' })
    expect(
      strmRegexValidationIssue(parse('exclude_name_regex_arr[0]：正则表达式不能为空')),
    ).toEqual({
      position: 1,
      reason: 'empty',
    })
    for (const error of [
      parse(message, 403, 'REQUEST_ORIGIN_INVALID'),
      parse(message, 500),
      parse('exclude_name_regex_arr[-1]：正则表达式无效'),
      parse('exclude_name_regex_arr[9999999999999999]：正则表达式无效'),
      parse('exclude_name_regex_arr[0]：secret'),
      parse('prefix ' + message),
    ]) {
      expect(strmRegexValidationIssue(error)).toBeUndefined()
    }
  })
})
