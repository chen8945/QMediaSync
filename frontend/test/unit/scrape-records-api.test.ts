import axios, { AxiosError } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  clearFailedScrapeRecords,
  deleteScrapeRecords,
  fetchScrapeRecords,
  finishScrapeRecord,
  getScrapeRecordsExportUrl,
  reidentifyScrapeRecord,
  renameFailedScrapeRecords,
  scrapeRecordErrorOptions,
  searchScrapeTmdb,
  truncateScrapeRecords,
} from '@/api/scrapeRecords'
import { HttpResponseError, parseHttpError } from '@/http/errors'

describe('刮削记录 API', () => {
  it('保留查询参数、搜索方式、重新识别载荷和专用超时', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      data: { code: 200, message: '已处理', data: null },
      status: 200,
      statusText: 'OK',
      headers: {},
    }))
    const http = axios.create({ adapter, timeout: 12000 })
    await fetchScrapeRecords(http, {
      page: 3,
      pageSize: 100,
      status: 'scrape_failed',
      type: 'tvshow',
      name: '剧名 & 空格',
    })
    await searchScrapeTmdb(http, { type: 'movie', name: '电影名', year: '2024' })
    await searchScrapeTmdb(http, { type: 'tvshow', tmdb_id: '550' })
    await reidentifyScrapeRecord(http, { id: 7, tmdb_id: 550, season: 0, episode: -1 })
    await deleteScrapeRecords(http, [7, 2])
    await renameFailedScrapeRecords(http, [7, 2])
    await clearFailedScrapeRecords(http)
    await truncateScrapeRecords(http)
    await finishScrapeRecord(http, 7)

    const configs = adapter.mock.calls.map(([config]) => config)
    expect(configs.map(({ method, url, timeout }) => [method, url, timeout])).toEqual([
      ['get', '/api/scrape/records', 12000],
      ['get', '/api/scrape/tmdb-search', 30000],
      ['get', '/api/scrape/tmdb-search', 30000],
      ['post', '/api/scrape/re-scrape', 60000],
      ['delete', '/api/scrape/records?ids=7,2', 12000],
      ['post', '/api/scrape/rename-failed?ids=7,2', 12000],
      ['post', '/api/scrape/clear-failed', 12000],
      ['post', '/api/scrape/truncate-all', 12000],
      ['post', '/api/scrape/finish', 12000],
    ])
    expect(configs[0].params).toEqual({
      page: 3,
      pageSize: 100,
      status: 'scrape_failed',
      type: 'tvshow',
      name: '剧名 & 空格',
    })
    expect(configs[1].params).toEqual({ type: 'movie', name: '电影名', year: '2024' })
    expect(configs[2].params).toEqual({ type: 'tvshow', tmdb_id: '550' })
    expect(JSON.parse(configs[3].data)).toEqual({ id: 7, tmdb_id: 550, season: 0, episode: -1 })
    expect(JSON.parse(configs[8].data)).toEqual({ id: 7 })
    expect(getScrapeRecordsExportUrl([7, 2])).toBe('/api/scrape/records/export?ids=7,2')
  })

  it('HTTP 200 业务失败时拒绝，并保留错误详情', async () => {
    const body = {
      code: 500,
      message: 'internal database password=secret',
      error_code: 'RECORD_CONFLICT',
      data: { field_errors: { id: 'invalid' } },
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
    const failure = await deleteScrapeRecords(http, [2]).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(HttpResponseError)
    const parsed = parseHttpError(failure, {
      ...scrapeRecordErrorOptions,
      fallbackMessage: '操作失败，请稍后重试',
    })
    expect(parsed.message).toBe(body.message)
    expect(parsed.details).toEqual(body.data)
    expect(parsed.response?.data).toEqual(body)
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', 'origin', '访问地址校验失败'],
    ['CSRF_TOKEN_INVALID', 'csrf', '请求安全校验失败'],
  ])('HTTP 403 的 %s 通过公共错误层解释', async (errorCode, kind, message) => {
    const http = axios.create({
      adapter: async (config) => {
        throw new AxiosError(
          'Request failed with status code 403',
          'ERR_BAD_REQUEST',
          config,
          {},
          {
            config,
            data: { code: 500, message: 'raw private text', error_code: errorCode, data: null },
            status: 403,
            statusText: 'Forbidden',
            headers: {},
          },
        )
      },
    })
    const failure = await deleteScrapeRecords(http, [2]).catch((error: unknown) => error)
    const parsed = parseHttpError(failure, scrapeRecordErrorOptions)
    expect(parsed.kind).toBe(kind)
    expect(parsed.message).toContain(message)
    expect(parsed.diagnostics).toEqual({
      method: 'DELETE',
      path: '/api/scrape/records',
      status: 403,
      errorCode,
    })
  })

  it('返回重新识别的成功消息', async () => {
    for (const data of [null, { name: '电影', year: 2024, tmdb_id: 550 }]) {
      const body = { code: 200, message: '下次扫描会按新的名称和年份重新刮削', data }
      const http = axios.create({
        adapter: async (config) => ({
          config,
          data: body,
          status: 200,
          statusText: 'OK',
          headers: {},
        }),
      })
      await expect(
        reidentifyScrapeRecord(http, { id: 7, tmdb_id: 550, season: -1, episode: -1 }),
      ).resolves.toBe(body.message)
    }
  })

  it('写入超时保留原始错误，不自动重试', async () => {
    const failure = new AxiosError('timeout', 'ETIMEDOUT')
    const adapter = vi.fn().mockRejectedValue(failure)
    const http = axios.create({ adapter })
    await expect(deleteScrapeRecords(http, [2])).rejects.toBe(failure)
    expect(adapter).toHaveBeenCalledTimes(1)
  })
})
