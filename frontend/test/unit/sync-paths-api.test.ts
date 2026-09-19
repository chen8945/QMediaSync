import axios, { AxiosError, CanceledError } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  deleteSyncPath,
  fetchDirectoryUploadRules,
  fetchSyncPath,
  fetchSyncPaths,
  fetchSyncPathScrapeRelations,
  saveSyncPathAggregate,
  saveSyncPathScrapeRelations,
  scanSyncPathDirectoryUpload,
  startSyncPath,
  stopSyncPath,
  toggleSyncPathCron,
  type SaveSyncPathPayload,
} from '@/api/syncPaths'
import { HttpResponseError } from '@/http/errors'

const payload: SaveSyncPathPayload = {
  sync_path: {
    source_type: '115',
    account_id: 9,
    base_cid: 'cid',
    local_path: '/strm',
    remote_path: '/remote',
    enable_cron: true,
    custom_config: true,
    setting: {
      local_proxy: 0,
      strm_base_url: '',
      cron: '@daily',
      min_video_size: -1,
      video_ext_arr: ['.mkv'],
      meta_ext_arr: [],
      exclude_name_arr: [],
      exclude_name_regex_arr: ['^raw.*$'],
      upload_meta: -1,
      download_meta: -1,
      delete_dir: -1,
      add_path: -1,
      check_meta_mtime: -1,
    },
  },
  directory_upload: { enabled: false, rules: [] },
}
const saved = {
  sync_path: { id: 12 },
  directory_upload: { enabled: false, rules: [] },
  warnings: ['同步目录已保存，但重载定时同步任务失败'],
}
const createHTTP = (data: unknown = null, code = 200) => {
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: { code, message: 'private token=http://secret', data },
  }))
  return { http: axios.create({ adapter }), adapter }
}

describe('同步目录领域 API', () => {
  it('聚合保存保留载荷与警告，仅新增传递原始幂等键', async () => {
    const { http, adapter } = createHTTP(saved)
    expect(await saveSyncPathAggregate(http, 0, payload, 'same-key')).toEqual(saved)
    expect(await saveSyncPathAggregate(http, 12, payload, 'unused-key')).toEqual(saved)
    const [create, update] = adapter.mock.calls.map(([config]) => config)
    expect(create.url).toBe('/api/sync/paths')
    expect(create.method).toBe('post')
    expect(create.headers.get('Idempotency-Key')).toBe('same-key')
    expect(JSON.parse(create.data)).toEqual(payload)
    expect(update.url).toBe('/api/sync/paths/12')
    expect(update.method).toBe('put')
    expect(update.headers.has('Idempotency-Key')).toBe(false)
    expect(JSON.parse(update.data)).toEqual(payload)
  })

  it('写入超时不重试，调用方显式重试仍可复用原键', async () => {
    const { http, adapter } = createHTTP(saved)
    adapter.mockRejectedValueOnce(new AxiosError('private', 'ETIMEDOUT'))
    await expect(saveSyncPathAggregate(http, 0, payload, 'same-key')).rejects.toBeInstanceOf(
      AxiosError,
    )
    expect(adapter).toHaveBeenCalledTimes(1)
    await saveSyncPathAggregate(http, 0, payload, 'same-key')
    expect(adapter.mock.calls.map(([config]) => config.headers.get('Idempotency-Key'))).toEqual([
      'same-key',
      'same-key',
    ])
  })

  it('列表保留来源、分页和超时，null 列表仍是合法空结果', async () => {
    const { http, adapter } = createHTTP({ list: null, total: 0, page: 1, page_size: 9999 })
    expect(
      await fetchSyncPaths(
        http,
        { page: 1, page_size: 9999, source_type: 'local' },
        { timeout: 5000 },
      ),
    ).toEqual({ list: [], total: 0, page: 1, page_size: 9999 })
    expect(adapter.mock.calls[0]![0]).toMatchObject({
      url: '/api/sync/path-list',
      timeout: 5000,
      params: { page: 1, page_size: 9999, source_type: 'local' },
    })
    await fetchSyncPaths(http)
    expect(adapter.mock.calls[1]![0].params).toBeUndefined()
    expect(adapter.mock.calls[1]![0].timeout).toBe(0)
  })

  it.each([
    ['删除', deleteSyncPath, '/sync/path-delete'],
    ['停止', stopSyncPath, '/sync/path/stop'],
    ['定时开关', toggleSyncPathCron, '/sync/path/toggle-cron'],
  ])('%s 保留 JSON 载荷和成功 null', async (_, run, path) => {
    const { http, adapter } = createHTTP()
    await expect(run(http, 12)).resolves.toBeUndefined()
    expect(adapter.mock.calls[0]![0].url).toBe(`/api${path}`)
    expect(adapter.mock.calls[0]![0].headers.get('Content-Type')).toBe('application/json')
    expect(JSON.parse(adapter.mock.calls[0]![0].data)).toEqual({ id: 12 })
  })

  it('运行、扫描、关联保留各自响应和请求形式', async () => {
    const { http, adapter } = createHTTP({ is_running: 2, accepted: 3 })
    expect(await startSyncPath(http, 12)).toMatchObject({ is_running: 2 })
    await startSyncPath(http, 12, true)
    expect(await scanSyncPathDirectoryUpload(http, 12)).toMatchObject({ accepted: 3 })
    await saveSyncPathScrapeRelations(http, 12, [5, 6])
    expect(adapter.mock.calls.map(([config]) => config.url)).toEqual([
      '/api/sync/path/start',
      '/api/sync/path/full-start',
      '/api/directory-upload/sync-paths/12/scan',
      '/api/sync/path/scrape-paths',
    ])
    expect(adapter.mock.calls[2]![0].data).toBeUndefined()
    expect(JSON.parse(adapter.mock.calls[3]![0].data)).toEqual({ id: 12, scrape_path_id: [5, 6] })
    const rules = createHTTP({ list: null })
    expect(await fetchDirectoryUploadRules(rules.http, 12)).toEqual([])
    expect(rules.adapter.mock.calls[0]![0].params).toEqual({ sync_path_id: 12 })
    expect(await fetchSyncPathScrapeRelations(createHTTP(null).http, 12)).toEqual([])
  })

  it.each([
    (http: ReturnType<typeof axios.create>) => saveSyncPathAggregate(http, 0, payload, 'key'),
    (http: ReturnType<typeof axios.create>) => fetchSyncPaths(http),
    (http: ReturnType<typeof axios.create>) => fetchSyncPath(http, 12),
    (http: ReturnType<typeof axios.create>) => fetchDirectoryUploadRules(http),
    (http: ReturnType<typeof axios.create>) => deleteSyncPath(http, 12),
    (http: ReturnType<typeof axios.create>) => startSyncPath(http, 12),
    (http: ReturnType<typeof axios.create>) => startSyncPath(http, 12, true),
    (http: ReturnType<typeof axios.create>) => stopSyncPath(http, 12),
    (http: ReturnType<typeof axios.create>) => toggleSyncPathCron(http, 12),
    (http: ReturnType<typeof axios.create>) => scanSyncPathDirectoryUpload(http, 12),
    (http: ReturnType<typeof axios.create>) => fetchSyncPathScrapeRelations(http, 12),
    (http: ReturnType<typeof axios.create>) => saveSyncPathScrapeRelations(http, 12, []),
  ])('业务失败拒绝并保留原始响应；取消保留原错误引用 %#', async (run) => {
    const { http, adapter } = createHTTP(
      { field_errors: [{ field: 'local_path', message: '不能为空' }] },
      500,
    )
    await expect(run(http)).rejects.toMatchObject({
      response: {
        data: { code: 500, data: { field_errors: [{ field: 'local_path', message: '不能为空' }] } },
      },
    })
    const cancelled = new CanceledError()
    adapter.mockRejectedValueOnce(cancelled)
    await expect(run(http)).rejects.toBe(cancelled)
  })

  it('保存保留警告且不要求未使用字段，读取仍拒绝无效集合', async () => {
    for (const data of [null, { warnings: 'invalid' }, { warnings: [1] }]) {
      await expect(
        saveSyncPathAggregate(createHTTP(data).http, 0, payload, 'key'),
      ).rejects.toBeInstanceOf(HttpResponseError)
    }
    await expect(
      saveSyncPathAggregate(createHTTP({ warnings: ['后续服务重载失败'] }).http, 0, payload, 'key'),
    ).resolves.toEqual({ warnings: ['后续服务重载失败'] })
    for (const data of [null, {}, { list: {} }]) {
      await expect(fetchSyncPaths(createHTTP(data).http)).rejects.toBeInstanceOf(HttpResponseError)
      await expect(fetchDirectoryUploadRules(createHTTP(data).http)).rejects.toBeInstanceOf(
        HttpResponseError,
      )
    }
  })
})
