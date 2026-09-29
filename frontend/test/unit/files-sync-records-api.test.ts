import axios, { type AxiosInstance } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import * as files from '@/api/files'
import * as records from '@/api/syncRecords'
import { parseHttpError } from '@/http/errors'

const directory = { id: 'root', name: '媒体', path: '/媒体' }
const directoryQuery = {
  parent_id: 'root',
  parent_path: '/媒体',
  source_type: 'openlist',
  account_id: 3,
}
const filesQuery = { account_id: 3, path: 'root', page: 2, page_size: 50, refresh: 1 }
const createPayload = { ...directoryQuery, name: '电影' }
const deleteQuery = { parent_id: 'root', file_id: 'file', account_id: 3 }
const manualPayload = { path_id: 'file', target_path: '/strm', account_id: 3 }
const recordsQuery = { page: 2, page_size: 20 }
const createHTTP = (data: unknown = null, code = 200, status = 200, message = '') => {
  const adapter = vi.fn(async (config) => ({
    config,
    status,
    statusText: '',
    headers: {},
    data: { code, message, data },
  }))
  return { http: axios.create({ adapter }), adapter }
}
const operations: Array<(http: AxiosInstance) => Promise<unknown>> = [
  (http) => files.fetchFiles(http, filesQuery),
  (http) => files.fetchDirectories(http, directoryQuery),
  (http) => files.createDirectory(http, createPayload),
  (http) => files.deleteFile(http, deleteQuery),
  (http) => files.generateManualStrm(http, manualPayload),
  (http) => records.fetchSyncRecords(http, recordsQuery),
  (http) => records.deleteSyncRecords(http, [1]),
  (http) => records.deleteSyncRecords(http, [1, 2], { batch: true }),
]

describe('file and sync record APIs', () => {
  it('保留排序能力、置顶关闭、目录刷新和取消参数', async () => {
    const { http, adapter } = createHTTP({
      fields: ['name'],
      folders_first: false,
      default: { sort_by: 'name', sort_order: 'asc' },
    })
    await files.fetchBrowseSortOptions(http, '115', 'directories')
    expect(adapter.mock.calls[0]![0]).toMatchObject({
      url: '/api/path/sort-options',
      params: { source_type: '115', scope: 'directories' },
    })
    const signal = new AbortController().signal
    await files.fetchFiles(
      http,
      { ...filesQuery, sort_by: 'time', sort_order: 'desc', folders_first: false },
      signal,
    )
    expect(adapter.mock.calls[1]![0]).toMatchObject({
      params: { sort_by: 'time', sort_order: 'desc', folders_first: false },
      signal,
    })
    await files.fetchDirectories(
      http,
      { ...directoryQuery, sort_by: 'name', sort_order: 'asc', refresh: 1 },
      signal,
    )
    expect(adapter.mock.calls[2]![0]).toMatchObject({
      params: { sort_by: 'name', sort_order: 'asc', refresh: 1 },
      signal,
    })
  })

  it('preserves transfer status/task IDs and accepts legacy null responses', async () => {
    const payload = {
      account_id: 3,
      parent_id: '/source',
      target_parent_id: '/',
      file_ids: ['/source/A'],
    }
    for (const operation of [files.moveFiles, files.copyFiles]) {
      for (const result of [
        null,
        { status: 'completed', task_ids: [] },
        { status: 'submitted', task_ids: ['task-2', 'task-1'] },
      ]) {
        expect(await operation(createHTTP(result).http, payload)).toEqual(result)
      }
    }
  })

  it('preserves file pagination, directory source/root and read timeouts', async () => {
    const snapshot = { list: [], total: 305, total_exact: false, has_more: true, page: 2 }
    const { http, adapter } = createHTTP(snapshot)
    expect(await files.fetchFiles(http, filesQuery)).toBe(snapshot)
    expect(adapter.mock.calls[0]![0]).toMatchObject({
      method: 'get',
      url: '/api/path/files',
      params: filesQuery,
      timeout: 60000,
    })
    expect(adapter.mock.calls[0]![0].params).not.toHaveProperty('sort_by')
    const dirs = createHTTP([directory])
    expect(await files.fetchDirectories(dirs.http, directoryQuery)).toEqual([directory])
    expect(dirs.adapter.mock.calls[0]![0]).toMatchObject({
      method: 'get',
      url: '/api/path/list',
      params: directoryQuery,
      timeout: 60000,
    })
    expect(await files.fetchDirectories(createHTTP().http, directoryQuery)).toBeNull()
    const page = { records: null, total: 0 }
    const sync = createHTTP(page)
    expect(await records.fetchSyncRecords(sync.http, recordsQuery)).toBe(page)
    expect(sync.adapter.mock.calls[0]![0]).toMatchObject({
      url: '/api/sync/records',
      params: recordsQuery,
      timeout: 0,
    })
  })

  it('keeps file DELETE in query and preserves write bodies and per-operation timeouts', async () => {
    const { http, adapter } = createHTTP(directory)
    expect(await files.createDirectory(http, createPayload)).toBe(directory)
    await files.deleteFile(http, deleteQuery)
    await files.generateManualStrm(http, manualPayload)
    await records.deleteSyncRecords(http, [1])
    await records.deleteSyncRecords(http, [1, 2], { batch: true })
    const configs = adapter.mock.calls.map(([config]) => config)
    expect(configs[0]).toMatchObject({ method: 'post', url: '/api/path/create', timeout: 0 })
    expect(JSON.parse(configs[0].data)).toEqual(createPayload)
    expect(configs[1]).toMatchObject({
      method: 'delete',
      url: '/api/path',
      params: deleteQuery,
      timeout: 0,
    })
    expect(configs[1].data).toBeUndefined()
    expect(configs[2]).toMatchObject({ method: 'post', url: '/api/sync/manual', timeout: 0 })
    expect(JSON.parse(configs[2].data)).toEqual(manualPayload)
    for (const [index, ids, timeout] of [
      [3, [1], 0],
      [4, [1, 2], 60000],
    ] as const) {
      expect(configs[index]).toMatchObject({
        method: 'post',
        url: '/api/sync/delete-records',
        timeout,
      })
      expect(configs[index].headers.get('Content-Type')).toBe('application/json')
      expect(JSON.parse(configs[index].data)).toEqual({ ids })
    }
    await expect(files.deleteFile(createHTTP().http, deleteQuery)).resolves.toBeUndefined()
    await expect(
      files.generateManualStrm(createHTTP().http, manualPayload),
    ).resolves.toBeUndefined()
    await expect(records.deleteSyncRecords(createHTTP().http, [1])).resolves.toBeNull()
  })

  it.each([0, 400, 500])(
    'rejects business code %s for every read/write before applying data',
    async (code) => {
      for (const operation of operations) {
        await expect(operation(createHTTP({ private: 'secret' }, code).http)).rejects.toMatchObject(
          {
            response: { status: 200, data: { code, data: { private: 'secret' } } },
          },
        )
      }
    },
  )

  it('rejects HTTP failures even with a successful business envelope', async () => {
    for (const operation of operations) {
      await expect(operation(createHTTP(null, 200, 403).http)).rejects.toMatchObject({
        response: { status: 403 },
      })
    }
  })

  it('preserves partial-delete details', async () => {
    const details = { deleted_ids: [1], failures: [{ id: 2, reason: 'private SQL' }] }
    const error = await records
      .deleteSyncRecords(createHTTP(details, 400, 200, '部分同步记录删除失败').http, [1, 2])
      .catch((error: unknown) => error)
    const parsed = parseHttpError(error, { publicMessages: records.syncRecordPublicMessages })
    expect(parsed.message).toBe('部分同步记录删除失败')
    expect(parsed.details).toEqual(details)
  })
})
