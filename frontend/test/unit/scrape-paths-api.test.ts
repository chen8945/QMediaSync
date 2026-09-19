import axios, { AxiosError } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  deleteScrapePath,
  fetchScrapePath,
  fetchScrapePaths,
  fetchScrapeSyncPathIDs,
  saveScrapePath,
  saveScrapeSyncPathIDs,
  startScrapePath,
  stopScrapePath,
  toggleScrapePathCron,
  validateScrapePathCron,
  type SaveScrapePathPayload,
} from '@/api/scrapePaths'
import { HttpResponseError } from '@/http/errors'

const payload: SaveScrapePathPayload = {
  id: 12,
  source_path: '/media',
  source_path_id: 'media-id',
  dest_path: '/organized',
  dest_path_id: 'organized-id',
  scrape_type: 'scrape_and_rename',
  rename_type: 'move',
  enable_category: false,
  folder_name_template: '{title} ({year})',
  file_name_template: '{title}',
  delete_keyword: [],
  min_video_file_size: 0,
  video_ext_list: ['.mkv'],
  enable_ai: 'off',
  ai_prompt: '',
  exclude_no_image_actor: false,
  force_delete_source_path: false,
  enable_cron: true,
  cron_expression: '0 * * * *',
  cron_description: '每小时',
  enable_fanart_tv: false,
  max_threads: 5,
}

describe('刮削目录 API', () => {
  it('同步目录关联查询保留来源筛选参数', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      data: { code: 200, data: [] },
      status: 200,
      statusText: 'OK',
      headers: {},
    }))
    const http = axios.create({ adapter })

    await expect(fetchScrapePaths(http, { source_type: 'local' })).resolves.toEqual([])
    expect(adapter.mock.calls[0][0].params).toEqual({ source_type: 'local' })
  })

  it('保留现有路径、方法、完整载荷、空关联与 Cron 表达式', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      data: { code: 200, data: null },
      status: 200,
      statusText: 'OK',
      headers: {},
    }))
    const http = axios.create({ adapter })
    await fetchScrapePaths(http)
    await fetchScrapePath(http, 12)
    await saveScrapePath(http, payload)
    await saveScrapePath(http, {
      ...payload,
      id: 0,
      source_type: 'local',
      account_id: 0,
      media_type: 'movie',
    })
    await deleteScrapePath(http, 12)
    await startScrapePath(http, 12)
    await stopScrapePath(http, 12)
    await toggleScrapePathCron(http, 12)
    await fetchScrapeSyncPathIDs(http, 12)
    await saveScrapeSyncPathIDs(http, 12, [])
    await validateScrapePathCron(http, '0 * * * *')
    const configs = adapter.mock.calls.map(([config]) => config)
    expect(configs.map(({ method, url }) => [method, url])).toEqual([
      ['get', '/api/scrape/pathes'],
      ['get', '/api/scrape/pathes/12'],
      ['post', '/api/scrape/pathes'],
      ['post', '/api/scrape/pathes'],
      ['delete', '/api/scrape/pathes/12'],
      ['post', '/api/scrape/pathes/start'],
      ['post', '/api/scrape/pathes/stop'],
      ['post', '/api/scrape/pathes/toggle-cron'],
      ['get', '/api/scrape/sync-pathes'],
      ['post', '/api/scrape/sync-pathes'],
      ['post', '/api/cron/validate'],
    ])
    expect(JSON.parse(configs[2].data)).toEqual(payload)
    expect(JSON.parse(configs[3].data)).toEqual({
      ...payload,
      id: 0,
      source_type: 'local',
      account_id: 0,
      media_type: 'movie',
    })
    for (const config of configs.slice(5, 8)) expect(JSON.parse(config.data)).toEqual({ id: 12 })
    expect(configs[8].params).toEqual({ scrape_path_id: 12 })
    expect(JSON.parse(configs[9].data)).toEqual({ scrape_path_id: 12, sync_path_ids: [] })
    expect(JSON.parse(configs[10].data)).toEqual({ cron_expression: '0 * * * *' })
  })

  it('拒绝 HTTP 200 的业务失败', async () => {
    const http = axios.create({
      adapter: async (config) => ({
        config,
        data: { code: 500, message: 'internal secret', data: null },
        status: 200,
        statusText: 'OK',
        headers: {},
      }),
    })
    await expect(saveScrapePath(http, payload)).rejects.toBeInstanceOf(HttpResponseError)
  })

  it('保存超时保留原错误且不自动重试', async () => {
    const error = new AxiosError('timeout', 'ETIMEDOUT')
    const adapter = vi.fn().mockRejectedValue(error)
    const http = axios.create({ adapter })
    await expect(saveScrapePath(http, payload)).rejects.toBe(error)
    expect(adapter).toHaveBeenCalledTimes(1)
  })
})
