import axios, { AxiosError, type AxiosInstance } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import { createApiKey, deleteApiKey, fetchApiKeys, updateApiKeyStatus } from '@/api/apiKeys'
import {
  deleteScrapeCategory,
  fetchScrapeCategories,
  fetchScrapeCountries,
  fetchScrapeGenres,
  fetchScrapeLanguages,
  saveScrapeCategory,
} from '@/api/scrapeCategories'
import { fetchAnnouncements, fetchHourlyStats, fetchQueueStats } from '@/api/dashboard'
import { repairDatabase } from '@/api/systemMaintenance'
import { parseHttpError } from '@/http/errors'

const movie = { id: 0, name: '电影', genre_id_array: [], language_array: [] }
const tvshow = { id: 3, name: '剧集', genre_id_array: [18], country_array: ['CN'] }
const operations: [string, string, (http: AxiosInstance) => Promise<unknown>][] = [
  ['get', '/api/api-keys', fetchApiKeys],
  ['post', '/api/api-keys', (http) => createApiKey(http, 'CI')],
  ['put', '/api/api-keys/4/status', (http) => updateApiKeyStatus(http, 4, false)],
  ['delete', '/api/api-keys/4', (http) => deleteApiKey(http, 4)],
  ['get', '/api/scrape/language', fetchScrapeLanguages],
  ['get', '/api/scrape/countries', fetchScrapeCountries],
  ['get', '/api/scrape/movie-genre', (http) => fetchScrapeGenres(http, 'movie')],
  ['get', '/api/scrape/tvshow-genre', (http) => fetchScrapeGenres(http, 'tvshow')],
  ['get', '/api/scrape/movie-categories', (http) => fetchScrapeCategories(http, 'movie')],
  ['get', '/api/scrape/tvshow-categories', (http) => fetchScrapeCategories(http, 'tvshow')],
  ['post', '/api/scrape/movie-categories', (http) => saveScrapeCategory(http, 'movie', movie)],
  ['post', '/api/scrape/tvshow-categories', (http) => saveScrapeCategory(http, 'tvshow', tvshow)],
  ['delete', '/api/scrape/movie-categories/2', (http) => deleteScrapeCategory(http, 'movie', 2)],
  ['delete', '/api/scrape/tvshow-categories/3', (http) => deleteScrapeCategory(http, 'tvshow', 3)],
  ['get', '/api/announce', fetchAnnouncements],
  ['get', '/api/115/stats/hourly', fetchHourlyStats],
  ['get', '/api/115/queue/stats', fetchQueueStats],
  ['post', '/api/database/repair', repairDatabase],
]

describe('设置、分类和首页领域 API', () => {
  it('复用传入客户端的配置并保留 URL、请求方式、空数组、false 和新增/编辑 ID', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: { code: 200, data: null },
    }))
    const http = axios.create({ adapter, timeout: 5432, withCredentials: true })
    for (const [, , request] of operations) await request(http)
    const configs = adapter.mock.calls.map(([config]) => config)
    expect(
      configs.map(({ method, url, timeout, withCredentials }) => [
        method,
        url,
        timeout,
        withCredentials,
      ]),
    ).toEqual(operations.map(([method, url]) => [method, url, 5432, true]))
    expect(configs.every((config) => config.params === undefined)).toBe(true)
    expect(JSON.parse(configs[1].data)).toEqual({ name: 'CI' })
    expect(JSON.parse(configs[2].data)).toEqual({ is_active: false })
    expect(JSON.parse(configs[10].data)).toEqual(movie)
    expect(JSON.parse(configs[11].data)).toEqual(tvshow)
    expect(configs[17].data).toBeUndefined()
  })

  it.each([null, false])('写入成功 data=%s 不误判为失败，统计保留原值', async (data) => {
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: { code: 200, data },
      }),
    })
    for (const request of [
      () => updateApiKeyStatus(http, 4, false),
      () => deleteApiKey(http, 4),
      () => saveScrapeCategory(http, 'movie', movie),
      () => deleteScrapeCategory(http, 'tvshow', 3),
      () => repairDatabase(http),
    ])
      await expect(request()).resolves.toBeUndefined()
    expect(await fetchHourlyStats(http)).toBe(data)
    expect(await fetchQueueStats(http)).toBe(data)
    if (data === null) {
      for (const request of [
        fetchApiKeys,
        fetchScrapeLanguages,
        fetchScrapeCountries,
        fetchAnnouncements,
      ]) {
        expect(await request(http)).toEqual([])
      }
      expect(await fetchScrapeGenres(http, 'movie')).toEqual([])
      expect(await fetchScrapeCategories(http, 'tvshow')).toEqual([])
    }
  })

  it.each([
    [200, 500],
    [200, 0],
    [503, 200],
  ])('所有操作拒绝 HTTP %s 业务码 %s，保留原始失败响应', async (status, code) => {
    const body = { code, message: 'SQL internal secret', data: { field_errors: ['secret'] } }
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status,
        statusText: '',
        headers: {},
        data: body,
      }),
    })
    for (const [, , request] of operations) {
      await expect(request(http)).rejects.toMatchObject({
        name: 'HttpResponseError',
        response: { status, data: body },
      })
    }
  })

  it('公告兼容原始数组和业务包络，原始数组同样拒绝失败 HTTP 状态', async () => {
    const announcements = [{ id: 1, time: '2026-09-20', title: '公告', content: '内容' }]
    for (const data of [announcements, { code: 200, data: announcements }]) {
      const http = axios.create({
        adapter: async (config) => ({ config, status: 200, statusText: 'OK', headers: {}, data }),
      })
      expect(await fetchAnnouncements(http)).toEqual(announcements)
    }
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status: 403,
        statusText: '',
        headers: {},
        data: announcements,
      }),
    })
    await expect(fetchAnnouncements(http)).rejects.toMatchObject({ response: { status: 403 } })
  })

  it('仅创建响应保留明文密钥；请求超时不自动重发', async () => {
    const key = {
      id: 1,
      name: 'CI',
      key: 'qms_one_time',
      key_prefix: 'qms_one_',
      is_active: true,
      created_at: 1,
    }
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: { code: 200, data: key },
      }),
    })
    expect(await createApiKey(http, 'CI')).toEqual(key)
    const adapter = vi.fn(async (config) => {
      throw new AxiosError('secret', 'ETIMEDOUT', config)
    })
    const error = await repairDatabase(axios.create({ adapter })).catch((error: unknown) => error)
    expect(adapter).toHaveBeenCalledTimes(1)
    expect(parseHttpError(error)).toMatchObject({
      kind: 'timeout',
      message: '请求超时，操作结果尚未确认。请先检查操作是否已生效，避免重复提交',
    })
  })
})
