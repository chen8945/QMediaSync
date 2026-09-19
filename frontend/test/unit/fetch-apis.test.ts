import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchLogSnapshot, logDownloadURL } from '@/api/logs'
import { fetchSyncTask } from '@/api/syncTasks'
import { parseHttpError } from '@/http/errors'

const jsonResponse = (body: unknown, status = 200, headers?: HeadersInit) =>
  new Response(JSON.stringify(body), { status, headers })

afterEach(() => vi.unstubAllGlobals())

describe('原生 HTTP 快照 API', () => {
  it('保留日志参数、Cookie、取消信号和原始快照格式', async () => {
    const snapshot = { entries: [], pos: 12, start_pos: -1 }
    const fetch = vi.fn().mockResolvedValue(jsonResponse(snapshot))
    vi.stubGlobal('fetch', fetch)
    const controller = new AbortController()

    await expect(fetchLogSnapshot('logs/a b&c.log', -1, 1000, controller.signal)).resolves.toEqual(
      snapshot,
    )
    expect(fetch).toHaveBeenCalledExactlyOnceWith(
      '/api/logs/old?path=logs%2Fa%20b%26c.log&pos=-1&direction=forward&limit=1000',
      { credentials: 'include', signal: controller.signal },
    )
  })

  it.each([
    [403, { code: 500, error_code: 'REQUEST_ORIGIN_INVALID' }, 'origin'],
    [403, { code: 500, message: 'CSRF 校验失败' }, 'csrf'],
    [401, { code: 401 }, 'unauthorized'],
    [404, { error: 'secret path' }, 'not-found'],
    [500, { error: 'secret database connection' }, 'server'],
  ])('保留 HTTP %s 和错误体供公共分类', async (status, body, kind) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(body, status)))
    const error = await fetchLogSnapshot('private.log', -1, 1000).catch((error: unknown) => error)
    const parsed = parseHttpError(error)
    expect(parsed.kind).toBe(kind)
    expect(parsed.diagnostics).toEqual({
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

  it.each([null, { code: 500, message: 'secret' }, { entries: 'invalid', pos: 1 }])(
    '无效日志快照不得误判为成功',
    async (body) => {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(body)))
      await expect(fetchLogSnapshot('a.log', -1, 1000)).rejects.toThrow()
    },
  )

  it('同步任务读取校验业务成功和记录身份，不把错误对象写入任务', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ code: 200, data: { id: 8, status: 1 } }))
      .mockResolvedValueOnce(jsonResponse({ code: 500, message: 'secret', data: null }))
      .mockResolvedValueOnce(jsonResponse({ code: 200, data: { id: 9, status: 1 } }))
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
