import axios, { type AxiosInstance } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import * as download from '@/api/downloadQueue'
import * as upload from '@/api/uploadQueue'
import { parseHttpError } from '@/http/errors'

const queues = [
  {
    name: 'download',
    fetch: download.fetchDownloadQueue,
    status: download.fetchDownloadQueueStatus,
    operations: [
      ['clear-pending', download.clearPendingDownloadQueue],
      ['clear-success-failed', download.clearCompletedDownloadQueue],
      ['retry-failed', download.retryFailedDownloadQueue],
      ['stop', download.pauseDownloadQueue],
      ['start', download.resumeDownloadQueue],
    ] as const,
  },
  {
    name: 'upload',
    fetch: upload.fetchUploadQueue,
    status: upload.fetchUploadQueueStatus,
    operations: [
      ['clear-pending', upload.clearPendingUploadQueue],
      ['clear-success-failed', upload.clearCompletedUploadQueue],
      ['retry-failed', upload.retryFailedUploadQueue],
      ['stop', upload.pauseUploadQueue],
      ['start', upload.resumeUploadQueue],
    ] as const,
  },
]
const query = { page: 2, page_size: 50, status: -1 }
const createHTTP = (
  code = 200,
  data: unknown = null,
  status = 200,
  message = '',
  error_code?: string,
) => {
  const adapter = vi.fn(async (config) => ({
    config,
    status,
    statusText: '',
    headers: {},
    data: { code, data, message, error_code },
  }))
  return { http: axios.create({ adapter }), adapter }
}

describe.each(queues)('$name queue API', (queue) => {
  it('preserves pagination, global counts and task identity fields', async () => {
    const snapshot = {
      total: 14,
      uploading: 2,
      downloading: 3,
      queue_status: {
        running: false,
        pending: 6,
        processing: 4,
        completed: 2,
        failed: 1,
        cancelled: 1,
        total: 14,
      },
      list: [
        {
          id: '123',
          source: 'strm_sync',
          source_type: 'baidu',
          remote_full_path: '/video/name.mkv',
          remote_file_id: '456',
          remote_pick_code: '',
          remote_sha1: '',
          remote_md5: 'md5',
          upload_phase: 'finalizing',
          status: 6,
        },
      ],
    }
    const { http, adapter } = createHTTP(200, snapshot)
    expect(await queue.fetch(http, query)).toEqual(snapshot)
    expect(adapter.mock.calls[0]![0]).toMatchObject({
      method: 'get',
      url: `/api/${queue.name}/queue`,
      params: query,
    })
    expect(await queue.fetch(createHTTP(200, { total: 0, list: null }).http, query)).toEqual({
      total: 0,
      list: null,
    })
  })

  it('preserves false status snapshots and validates null mutation success', async () => {
    const status = createHTTP(200, false)
    expect(await queue.status(status.http)).toBe(false)
    expect(status.adapter.mock.calls[0]![0].url).toBe(`/api/${queue.name}/queue/status`)
    const writes = createHTTP()
    for (const [, operation] of queue.operations)
      await expect(operation(writes.http)).resolves.toBeUndefined()
    expect(
      writes.adapter.mock.calls.map(([config]) => [config.method, config.url, config.data]),
    ).toEqual(
      queue.operations.map(([path]) => ['post', `/api/${queue.name}/queue/${path}`, undefined]),
    )
  })

  it('rejects every business failure before exposing data or resolving a mutation', async () => {
    const operations: Array<(http: AxiosInstance) => Promise<unknown>> = [
      (http) => queue.fetch(http, query),
      queue.status,
      ...queue.operations.map(([, operation]) => operation),
    ]
    for (const code of [0, 400, 500]) {
      for (const operation of operations) {
        await expect(operation(createHTTP(code, { token: 'secret' }).http)).rejects.toMatchObject({
          response: { status: 200, data: { code, data: { token: 'secret' } } },
        })
      }
    }
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', 'origin', '域名、协议和端口'],
    ['CSRF_TOKEN_INVALID', 'csrf', '刷新页面'],
  ])('preserves HTTP and machine code for %s classification', async (code, kind, message) => {
    const { http } = createHTTP(403, null, 403, 'secret https://private/?token=secret', code)
    try {
      await queue.operations[0][1](http)
      expect.fail('must reject')
    } catch (error) {
      expect(parseHttpError(error)).toMatchObject({
        kind,
        message: expect.stringContaining(message),
        diagnostics: {
          method: 'POST',
          path: `/api/${queue.name}/queue/clear-pending`,
          status: 403,
          errorCode: code,
        },
      })
    }
  })
})
