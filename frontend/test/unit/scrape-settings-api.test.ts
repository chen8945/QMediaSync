import axios, { AxiosError } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  fetchAiSettings,
  fetchTmdbSettings,
  saveAiSettings,
  saveTmdbSettings,
  testAiConnection,
  testTmdbConnection,
  type AiSettings,
  type TmdbSettings,
} from '@/api/scrapeSettings'
import { parseHttpError } from '@/http/errors'

const ai: AiSettings = {
  ai_base_url: 'https://ai.example.com',
  ai_api_key: 'ai-secret',
  ai_model_name: 'example-model',
  ai_timeout: 120,
}
const tmdb: TmdbSettings = {
  tmdb_url: 'https://tmdb.example.com',
  tmdb_image_url: 'https://images.example.com',
  tmdb_enable_proxy: true,
  tmdb_api_key: 'tmdb-secret',
  tmdb_access_token: 'token-secret',
  fanart_api_key: 'fanart-secret',
  tmdb_language: 'zh-CN',
  tmdb_image_language: 'en-US',
  local_max_threads: 5,
}
const aiTest = {
  ai_base_url: ai.ai_base_url,
  ai_api_key: ai.ai_api_key,
  ai_model_name: ai.ai_model_name,
}
const tmdbTest = {
  tmdb_url: tmdb.tmdb_url,
  tmdb_image_url: tmdb.tmdb_image_url,
  tmdb_enable_proxy: tmdb.tmdb_enable_proxy,
  tmdb_api_key: tmdb.tmdb_api_key,
  tmdb_access_token: tmdb.tmdb_access_token,
  tmdb_language: tmdb.tmdb_language,
  tmdb_image_language: tmdb.tmdb_image_language,
}

describe('刮削设置 API', () => {
  it('保留客户端、接口地址、载荷与专用超时，允许成功空值和 TMDB 未连通结果', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: {
        code: 200,
        message: '',
        data:
          config.method === 'get'
            ? config.url.endsWith('/ai-settings')
              ? ai
              : tmdb
            : config.url.endsWith('/tmdb-test')
              ? false
              : null,
      },
    }))
    const http = axios.create({ adapter, timeout: 9876 })

    expect(await fetchAiSettings(http)).toEqual(ai)
    expect(await fetchTmdbSettings(http)).toEqual(tmdb)
    await expect(saveAiSettings(http, ai)).resolves.toBeUndefined()
    await expect(saveTmdbSettings(http, tmdb)).resolves.toBeUndefined()
    await expect(testAiConnection(http, aiTest)).resolves.toBeUndefined()
    await expect(testTmdbConnection(http, tmdbTest)).resolves.toBe(false)

    const configs = adapter.mock.calls.map(([config]) => config)
    expect(configs.map(({ method, url, timeout }) => [method, url, timeout])).toEqual([
      ['get', '/api/scrape/ai-settings', 9876],
      ['get', '/api/scrape/tmdb', 9876],
      ['post', '/api/scrape/ai-settings', 9876],
      ['post', '/api/scrape/tmdb', 9876],
      ['post', '/api/scrape/ai-test', 120000],
      ['post', '/api/scrape/tmdb-test', 20000],
    ])
    expect(configs.slice(2).map((config) => JSON.parse(config.data))).toEqual([
      ai,
      tmdb,
      aiTest,
      tmdbTest,
    ])
  })

  it('拒绝 HTTP 200 业务失败，保留原响应信息', async () => {
    const body = { code: 500, message: 'internal secret', data: null }
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status: 200,
        statusText: 'OK',
        headers: {},
        data: body,
      }),
    })

    await expect(saveTmdbSettings(http, tmdb)).rejects.toMatchObject({
      name: 'HttpResponseError',
      response: { status: 200, data: body },
    })
  })

  it('超时不重试且保留请求信息，让保存提示操作结果未确认', async () => {
    const adapter = vi.fn(async (config) => {
      throw new AxiosError('timeout secret', 'ETIMEDOUT', config)
    })
    const http = axios.create({ adapter })

    const request = saveAiSettings(http, ai)
    await expect(request).rejects.toBeInstanceOf(AxiosError)
    const failure: unknown = await request.catch((error: unknown) => error)
    expect(parseHttpError(failure)).toMatchObject({
      kind: 'timeout',
      message: '请求超时，操作结果尚未确认。请先检查操作是否已生效，避免重复提交',
      diagnostics: { method: 'POST', path: '/api/scrape/ai-settings' },
    })
    expect(adapter).toHaveBeenCalledTimes(1)
  })
})
