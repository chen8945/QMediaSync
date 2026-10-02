// @vitest-environment happy-dom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, shallowRef, type App } from 'vue'
import { flushPromises } from '@vue/test-utils'

vi.mock('@/const', () => ({
  SERVER_URL: 'https://api.example.test',
}))

import { useSyncTaskStream } from '@/composables/useSyncTaskStream'

const mountedApps: App[] = []

const withSetup = <T>(composable: () => T): T => {
  let result!: T
  const app = createApp({
    setup() {
      result = composable()
      return () => null
    },
  })
  app.mount(document.createElement('div'))
  mountedApps.push(app)
  return result
}

type Listener = (event: MessageEvent<string>) => void

class MockEventSource {
  static instances: MockEventSource[] = []
  static readonly CLOSED = 2
  readyState = 0
  onopen: (() => void) | null = null
  onerror: ((event: Event) => void) | null = null
  private readonly listeners = new Map<string, Set<Listener>>()
  closed = false

  constructor(public readonly url: string) {
    MockEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: Listener) {
    const listeners = this.listeners.get(type) ?? new Set<Listener>()
    listeners.add(listener)
    this.listeners.set(type, listeners)
  }

  removeEventListener(type: string, listener: Listener) {
    this.listeners.get(type)?.delete(listener)
  }

  emit(type: string, data: unknown) {
    const event = { data: JSON.stringify(data) } as MessageEvent<string>
    if (type === 'error') this.onerror?.(event)
    this.listeners.get(type)?.forEach((listener) => listener(event))
  }

  close() {
    this.closed = true
  }
}

const snapshot = {
  task: {
    id: 8,
    sync_path_id: 2,
    created_at: 1,
    updated_at: 1,
    finish_at: 0,
    status: 1,
    sub_status: 1,
    total: 2,
    new_strm: 1,
    new_meta: 0,
    new_upload: 0,
    net_file_start_at: 0,
    net_file_finish_at: 0,
    local_file_start_at: 0,
    local_file_finish_at: 0,
    local_path: '/media',
    remote_path: '/cloud',
    fail_reason: '',
  },
  logs: [{ level: 'info', message: 'start', timestamp: 't', cursor: 12 }],
  log_cursor: 12,
  log_path: 'libs/sync_8.log',
}

describe('useSyncTaskStream', () => {
  const scanResult = {
    succeeded_files: 2,
    failed_files: 1,
    skipped_files: 3,
    failures: [{ kind: 'directory', path: '/cloud/locked', reason: '读取失败' }],
    cleanup_status: 'partial',
    cleanup_reason: '保留无法完整读取的目录',
  }

  it.each([4, 5, 6])('新终态 %s 的结果在事件和刷新快照中一致', async (status) => {
    vi.stubGlobal('EventSource', MockEventSource)
    const stream = withSetup(() => useSyncTaskStream(8))
    const source = MockEventSource.instances[0]
    source.emit('snapshot', { type: 'snapshot', data: snapshot })
    source.emit('complete', {
      type: 'complete',
      data: { sync_id: 8, status, finish_at: 100, scan_result: scanResult },
    })
    expect(stream.task.value?.scan_result).toEqual(scanResult)
    expect(stream.task.value?.status).toBe(status)
    expect(stream.terminal.value).toBe(true)
    expect(source.closed).toBe(true)

    const refreshed = withSetup(() => useSyncTaskStream(8))
    const refreshedSource = MockEventSource.instances[1]
    refreshedSource.emit('snapshot', {
      type: 'snapshot',
      data: {
        ...snapshot,
        task: { ...snapshot.task, status, finish_at: 100, scan_result: scanResult },
      },
    })
    expect(refreshed.task.value).toEqual(stream.task.value)
    expect(refreshedSource.closed).toBe(true)
  })

  it.each([4, 5, 6])('HTTP 降级收到终态 %s 后停止轮询并保留结果', async (status) => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', undefined)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({
        code: 200,
        data: { ...snapshot.task, status, finish_at: 100, scan_result: scanResult },
      }),
    })
    vi.stubGlobal('fetch', fetchMock)
    const stream = withSetup(() => useSyncTaskStream(8))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(15000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(stream.terminal.value).toBe(true)
    expect(stream.task.value?.scan_result).toEqual(scanResult)
  })

  it.each(['pending', 'running'])(
    '生成终态快照的后台 %s 保持连接，最终日志后由 complete 关闭',
    (ledgerStatus) => {
      vi.stubGlobal('EventSource', MockEventSource)
      const stream = withSetup(() => useSyncTaskStream(8))
      const source = MockEventSource.instances[0]
      source.emit('snapshot', {
        type: 'snapshot',
        data: {
          ...snapshot,
          task: { ...snapshot.task, status: 2, finish_at: 100, ledger_status: ledgerStatus },
        },
      })
      expect(source.closed).toBe(false)
      expect(stream.terminal.value).toBe(false)
      source.emit('task_patch', {
        type: 'task_patch',
        data: {
          sync_id: 8,
          status: 1,
          total: 0,
          new_strm: 0,
          finish_at: 0,
          ledger_status: 'failed',
          ledger_finished_at: 108,
          ledger_error: '写入失败',
        },
      })
      expect(stream.task.value).toMatchObject({
        status: 2,
        total: 2,
        new_strm: 1,
        finish_at: 100,
        ledger_status: 'failed',
        ledger_finished_at: 108,
        ledger_error: '写入失败',
      })
      expect(source.closed).toBe(false)
      source.emit('log_append', {
        type: 'log_append',
        data: { entry: { level: 'error', message: '后台退出', timestamp: 't3' } },
      })
      source.emit('complete', { type: 'complete', data: { sync_id: 8, ledger_status: 'failed' } })
      expect(stream.logs.value[0].message).toBe('后台退出')
      expect(stream.terminal.value).toBe(true)
      expect(source.closed).toBe(true)
    },
  )

  it.each(['completed', 'failed', 'interrupted', 'not_required', null])(
    '后台 %s 的重连快照结束订阅并保留生成计数',
    (ledgerStatus) => {
      vi.stubGlobal('EventSource', MockEventSource)
      const stream = withSetup(() => useSyncTaskStream(8))
      const source = MockEventSource.instances[0]
      source.emit('snapshot', { type: 'snapshot', data: snapshot })
      source.onerror?.(new Event('error'))
      source.emit('snapshot', {
        type: 'snapshot',
        data: {
          ...snapshot,
          task: {
            ...snapshot.task,
            status: 2,
            finish_at: 100,
            ledger_status: ledgerStatus,
            ledger_finished_at: null,
          },
        },
      })
      expect(stream.task.value?.new_strm).toBe(1)
      expect(stream.task.value?.ledger_status).toBe(ledgerStatus)
      expect(stream.terminal.value).toBe(true)
      expect(source.closed).toBe(true)
    },
  )

  it('HTTP 降级在生成完成后持续读取后台，失败保留快照，后台结束停止', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', undefined)
    const generated = { ...snapshot.task, status: 2, finish_at: 100 }
    const reply = (data: unknown) => new Response(JSON.stringify({ code: 200, data }))
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(reply({ ...generated, ledger_status: 'pending' }))
      .mockResolvedValueOnce(new Response('{}', { status: 503 }))
      .mockResolvedValueOnce(reply({ ...generated, ledger_status: 'running' }))
      .mockResolvedValueOnce(
        reply({
          ...generated,
          ledger_status: 'failed',
          ledger_finished_at: 108,
          ledger_error: '写入失败',
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const stream = withSetup(() => useSyncTaskStream(8))
    await flushPromises()
    expect(stream.terminal.value).toBe(false)
    await vi.advanceTimersByTimeAsync(5000)
    expect(stream.task.value?.ledger_status).toBe('pending')
    expect(stream.errorMessage.value).not.toBe('')
    await vi.advanceTimersByTimeAsync(5000)
    expect(stream.task.value?.ledger_status).toBe('running')
    expect(stream.errorMessage.value).toBe('')
    await vi.advanceTimersByTimeAsync(15000)
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(stream.task.value).toMatchObject({
      ...generated,
      ledger_status: 'failed',
      ledger_finished_at: 108,
      ledger_error: '写入失败',
    })
    expect(stream.terminal.value).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })

  afterEach(() => {
    mountedApps.splice(0).forEach((app) => app.unmount())
    vi.unstubAllGlobals()
    vi.useRealTimers()
    MockEventSource.instances = []
  })

  it('applies snapshot, patches and log events from EventSource', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const syncId = shallowRef(8)
    const stream = withSetup(() => useSyncTaskStream(syncId))
    await nextTick()
    const source = MockEventSource.instances[0]

    expect(source.url).toBe('/api/sync/tasks/8/stream')
    expect(stream.connectionState.value).toBe('connecting')
    source.onopen?.()
    expect(stream.connectionState.value).toBe('connected')
    source.emit('snapshot', { type: 'snapshot', version: 1, sync_id: 8, data: snapshot })
    source.emit('task_patch', {
      type: 'task_patch',
      version: 1,
      sync_id: 8,
      data: { sync_id: 8, status: 1, sub_status: 2, total: 2, net_file_start_at: 101 },
    })
    source.emit('log_append', {
      type: 'log_append',
      version: 1,
      sync_id: 8,
      data: { entry: { level: 'info', message: 'next', timestamp: 't2', cursor: 30 }, cursor: 30 },
    })

    await nextTick()
    expect(stream.task.value?.sub_status).toBe(2)
    expect(stream.task.value?.net_file_start_at).toBe(101)
    expect(stream.logs.value.map((line) => line.message)).toEqual(['next', 'start'])
    expect(stream.connected.value).toBe(true)
  })

  it('enters reconnecting only after a native EventSource error', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const stream = withSetup(() => useSyncTaskStream(8))
    await nextTick()
    const source = MockEventSource.instances[0]

    expect(stream.connectionState.value).toBe('connecting')
    source.onerror?.(new Event('error'))

    expect(stream.connectionState.value).toBe('reconnecting')
    expect(stream.connected.value).toBe(false)
  })

  it('浏览器放弃重连（CLOSED）后关闭流并改用 HTTP 降级轮询', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ code: 200, data: { ...snapshot.task } }),
    })
    vi.stubGlobal('fetch', fetchMock)
    const stream = withSetup(() => useSyncTaskStream(8))
    await nextTick()
    const source = MockEventSource.instances[0]

    source.readyState = MockEventSource.CLOSED
    source.onerror?.(new Event('error'))
    await flushPromises()

    expect(source.closed).toBe(true)
    expect(stream.connectionState.value).toBe('idle')
    expect(fetchMock).toHaveBeenCalledWith('/api/sync/task?sync_id=8', {
      credentials: 'include',
    })
    expect(stream.task.value?.id).toBe(8)
  })

  it.each([401, 403, 404])('CLOSED 后首次 HTTP %s 不再创建降级轮询', async (status) => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', MockEventSource)
    const fetchMock = vi.fn().mockResolvedValue(new Response('{}', { status }))
    vi.stubGlobal('fetch', fetchMock)
    const stream = withSetup(() => useSyncTaskStream(8))
    const source = MockEventSource.instances[0]
    source.emit('snapshot', { type: 'snapshot', version: 1, sync_id: 8, data: snapshot })
    source.readyState = MockEventSource.CLOSED
    source.onerror?.(new Event('error'))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(15000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(stream.task.value?.id).toBe(8)
    expect(stream.unsupported.value).toBe(false)
  })

  it('closes terminal sources and ignores callbacks from a replaced source', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const syncId = shallowRef(8)
    const stream = withSetup(() => useSyncTaskStream(syncId))
    await nextTick()
    const source = MockEventSource.instances[0]

    syncId.value = 9
    await nextTick()
    const replacement = MockEventSource.instances[1]
    source.emit('snapshot', { type: 'snapshot', version: 1, sync_id: 8, data: snapshot })
    replacement.emit('snapshot', {
      type: 'snapshot',
      version: 1,
      sync_id: 9,
      data: { ...snapshot, task: { ...snapshot.task, id: 9 } },
    })
    replacement.emit('complete', {
      type: 'complete',
      version: 1,
      sync_id: 9,
      data: { sync_id: 9, status: 2 },
    })

    await nextTick()
    expect(source.closed).toBe(true)
    expect(stream.task.value?.id).toBe(9)
    expect(replacement.closed).toBe(true)
    expect(stream.terminal.value).toBe(true)
  })

  it('falls back to running-task HTTP polling when EventSource is unsupported', async () => {
    vi.stubGlobal('EventSource', undefined)
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ code: 200, data: { ...snapshot.task } }),
    })
    vi.stubGlobal('fetch', fetchMock)

    const stream = withSetup(() => useSyncTaskStream(8))
    await nextTick()
    await flushPromises()

    expect(MockEventSource.instances).toHaveLength(0)
    expect(stream.unsupported.value).toBe(true)
    expect(stream.task.value?.id).toBe(8)
    expect(fetchMock).toHaveBeenCalledWith('/api/sync/task?sync_id=8', {
      credentials: 'include',
    })
  })

  it('keeps the stream connected for a server-sent business error', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const stream = withSetup(() => useSyncTaskStream(8))
    await nextTick()
    const source = MockEventSource.instances[0]

    source.onopen?.()
    source.emit('error', {
      type: 'error',
      version: 1,
      sync_id: 8,
      data: { reason: 'tailer failed' },
    })
    await nextTick()

    expect(stream.connected.value).toBe(true)
    expect(stream.errorMessage.value).toBe('同步任务实时流返回错误')
  })

  it('降级查询业务失败不把错误包络写入任务', async () => {
    vi.stubGlobal('EventSource', undefined)
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            code: 500,
            message: '同步任务读取失败，请稍后重试',
            data: null,
          }),
        ),
      ),
    )
    const stream = withSetup(() => useSyncTaskStream(8))
    await flushPromises()
    expect(stream.task.value).toBeNull()
    expect(stream.loading.value).toBe(false)
    expect(stream.errorMessage.value).toBe('同步任务读取失败，请稍后重试')
    expect(MockEventSource.instances).toHaveLength(0)
  })

  it('降级轮询失败保留已有快照，后续成功清除错误', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', undefined)
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: 200, data: snapshot.task })))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ code: 500 }), {
          status: 503,
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ code: 200, data: { ...snapshot.task, status: 2 } })),
      )
    vi.stubGlobal('fetch', fetch)
    const stream = withSetup(() => useSyncTaskStream(8))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    expect(stream.task.value?.id).toBe(8)
    expect(stream.task.value?.status).toBe(1)
    expect(stream.errorMessage.value).toContain('服务器处理请求失败')
    await vi.advanceTimersByTimeAsync(5000)
    expect(stream.terminal.value).toBe(true)
    expect(stream.errorMessage.value).toBe('')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('停止降级读取后，飞行请求不能回写或重新建立定时器', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', undefined)
    let resolve!: (value: Response) => void
    vi.stubGlobal(
      'fetch',
      vi.fn(
        () =>
          new Promise((done) => {
            resolve = done
          }),
      ),
    )
    const stream = withSetup(() => useSyncTaskStream(8))
    stream.disconnect()
    resolve(new Response(JSON.stringify({ code: 200, data: snapshot.task })))
    await flushPromises()
    expect(stream.task.value).toBeNull()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('降级请求未结束时不重叠发起下一轮读取', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', undefined)
    let resolve!: (value: Response) => void
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: 200, data: snapshot.task })))
      .mockImplementation(
        () =>
          new Promise((done) => {
            resolve = done
          }),
      )
    vi.stubGlobal('fetch', fetch)
    const stream = withSetup(() => useSyncTaskStream(8))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(15000)
    expect(fetch).toHaveBeenCalledTimes(2)
    resolve(new Response(JSON.stringify({ code: 200, data: { ...snapshot.task, status: 2 } })))
    await flushPromises()
    expect(stream.terminal.value).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })
})
