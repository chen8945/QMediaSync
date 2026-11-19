import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchLogCatchUp, fetchLogSnapshot, logDownloadURL } from '@/api/logs'
import { fetchSyncTask } from '@/api/syncTasks'
import { parseHttpError } from '@/http/errors'

const jsonResponse = (body: unknown, status = 200, headers?: HeadersInit) =>
  new Response(JSON.stringify(body), { status, headers })

afterEach(() => vi.unstubAllGlobals())

describe('原生 HTTP 快照 API', () => {
  it.each([undefined, 'file:test&1'])(
    '日志快照保留文件身份 %s、Cookie 和取消信号',
    async (fileID) => {
      const snapshot = { entries: [], pos: 12, start_pos: -1, file_id: 'file:test&1' }
      const fetch = vi.fn().mockResolvedValue(jsonResponse({ status: 200, data: snapshot }))
      vi.stubGlobal('fetch', fetch)
      const controller = new AbortController()

      await expect(
        fetchLogSnapshot('logs/a b&c.log', -1, 1000, controller.signal, fileID),
      ).resolves.toEqual(snapshot)
      expect(fetch).toHaveBeenCalledExactlyOnceWith(
        '/api/logs/old?path=logs%2Fa%20b%26c.log&pos=-1&direction=forward&limit=1000' +
          (fileID ? '&file_id=file%3Atest%261' : ''),
        { credentials: 'include', signal: controller.signal },
      )
    },
  )

  it('日志补读将文件身份与 cursor 一起传回服务端', async () => {
    const page = { entries: [], cursor: 42, has_more: false, file_id: 'file:test&1' }
    const fetch = vi.fn().mockResolvedValue(jsonResponse({ status: 200, data: page }))
    vi.stubGlobal('fetch', fetch)
    const controller = new AbortController()

    await expect(fetchLogCatchUp('a b.log', 42, controller.signal, page.file_id)).resolves.toEqual(
      page,
    )
    expect(fetch).toHaveBeenCalledExactlyOnceWith(
      '/api/logs/catch-up?path=a%20b.log&cursor=42&limit=500&file_id=file%3Atest%261',
      { credentials: 'include', signal: controller.signal },
    )
  })

  it.each([
    [
      403,
      {
        status: 403,
        data: null,
        message: '测试拒绝',
        error_code: 'REQUEST_ORIGIN_INVALID',
        instance: '/request-errors/abcdefghijklmnopqrstuvwx',
      },
      'origin',
    ],
    [
      403,
      {
        status: 403,
        data: null,
        message: '测试拒绝',
        error_code: 'CSRF_TOKEN_INVALID',
        instance: '/request-errors/abcdefghijklmnopqrstuvwx',
      },
      'csrf',
    ],
    [401, { status: 401 }, 'unauthorized'],
    [404, { error: 'secret path' }, 'not-found'],
    [500, { error: 'secret database connection' }, 'server'],
  ])('保留 HTTP %s 和错误体供公共分类', async (status, body, kind) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(body, status)))
    const error = await fetchLogSnapshot('private.log', -1, 1000).catch((error: unknown) => error)
    const parsed = parseHttpError(error)
    expect(parsed.kind).toBe(kind)
    expect(parsed.diagnostics).toMatchObject({
      method: 'GET',
      path: '/api/logs/old',
      status,
      ...(kind === 'origin' ? { errorCode: 'REQUEST_ORIGIN_INVALID' } : {}),
    })
    expect(parsed.response?.data).toEqual(body)
    expect(parsed.message).not.toContain('secret')
  })

  it('HTML 错误页按 HTTP 状态分类，不把解析错误或正文交给用户', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('<html>secret</html>', { status: 502 })),
    )
    const error = await fetchLogSnapshot('a.log', -1, 1000).catch((error: unknown) => error)
    const parsed = parseHttpError(error)
    expect(parsed.kind).toBe('server')
    expect(parsed.message).not.toContain('secret')
  })

  it('只将 fetch 传输 TypeError 归为无响应', async () => {
    const fetch = vi.fn().mockRejectedValueOnce(new TypeError('Failed to fetch'))
    fetch.mockResolvedValueOnce({
      ok: true,
      json: () => Promise.reject(new TypeError('parser bug')),
    })
    vi.stubGlobal('fetch', fetch)
    const network = await fetchLogSnapshot('a.log', -1, 1000).catch((error: unknown) => error)
    const parser = await fetchLogSnapshot('a.log', -1, 1000).catch((error: unknown) => error)
    expect(parseHttpError(network).kind).toBe('network')
    expect(parseHttpError(parser).kind).toBe('unknown')
  })

  it('主动取消保持静默', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new DOMException('aborted', 'AbortError')))
    const error = await fetchLogSnapshot('a.log', -1, 1000).catch((error: unknown) => error)
    expect(parseHttpError(error).shouldNotify).toBe(false)
  })

  it.each([
    null,
    { status: 500, message: 'secret' },
    { entries: 'invalid', pos: 1 },
    { status: 200, data: { entries: [], pos: 0 } },
    { status: 200, data: { entries: [], pos: 0, file_id: '' } },
    { status: 200, data: { entries: [], pos: 0, file_id: 123 } },
    { status: 200, data: { entries: [], pos: 0, file_id: 'another-file' } },
  ])('无效日志快照不得误判为成功', async (body) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(body)))
    await expect(fetchLogSnapshot('a.log', -1, 1000, undefined, 'file:test&1')).rejects.toThrow()
  })

  it('同步任务读取校验业务成功和记录身份，不把错误对象写入任务', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ status: 200, data: { id: 8, status: 1 } }))
      .mockResolvedValueOnce(
        jsonResponse({
          error_code: 'INVALID_REQUEST',
          instance: '/request-errors/abcdefghijklmnopqrstuvwx',
          status: 500,
          message: 'secret',
          data: null,
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ status: 200, data: { id: 9, status: 1 } }))
    vi.stubGlobal('fetch', fetch)
    await expect(fetchSyncTask(8)).resolves.toEqual({ id: 8, status: 1 })
    await expect(fetchSyncTask(8)).rejects.toThrow()
    await expect(fetchSyncTask(8)).rejects.toThrow()
    expect(fetch).toHaveBeenCalledWith('/api/sync/task?sync_id=8', { credentials: 'include' })
  })

  it('读取正文时超时或中止仍按传输错误分类', async () => {
    for (const [ok, name, kind] of [
      [true, 'TimeoutError', 'timeout'],
      [false, 'TimeoutError', 'timeout'],
      [true, 'AbortError', 'cancelled'],
      [false, 'AbortError', 'cancelled'],
    ] as const) {
      vi.stubGlobal(
        'fetch',
        vi.fn().mockResolvedValue({
          ok,
          status: ok ? 200 : 502,
          json: () => Promise.reject(new DOMException('secret', name)),
        }),
      )
      const error = await fetchLogSnapshot('a.log', -1, 1000).catch((error: unknown) => error)
      expect(parseHttpError(error).kind).toBe(kind)
    }
  })

  it('日志下载保留浏览器 URL 并编码路径', () => {
    expect(logDownloadURL('a b&c.log')).toBe('/api/logs/download?path=a%20b%26c.log')
  })
})
