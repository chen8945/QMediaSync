// @vitest-environment happy-dom
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/const', () => ({
  SERVER_URL: 'https://api.example.test/api',
}))

import AppLogViewer from '@/components/AppLogViewer.vue'

type Listener = (event: MessageEvent<string>) => void

class MockEventSource {
  static instances: MockEventSource[] = []
  static readonly CLOSED = 2
  readyState = 0
  onopen: (() => void) | null = null
  onerror: ((event: Event) => void) | null = null
  private readonly listeners = new Map<string, Set<Listener>>()

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

  close() {}
}

describe('AppLogViewer', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    MockEventSource.instances = []
  })

  it('creates an EventSource only after the initial log snapshot succeeds', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: vi.fn().mockResolvedValue({ entries: [], pos: 0 }),
      }),
    )
    vi.stubGlobal('EventSource', MockEventSource)

    const wrapper = mount(AppLogViewer, {
      props: {
        logPath: 'app.log',
        isRealTime: false,
      },
      global: {
        directives: { loading: {} },
        stubs: {
          ElCard: { template: '<div><slot name="header" /><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElText: { template: '<span><slot /></span>' },
        },
      },
    })

    await flushPromises()
    expect(MockEventSource.instances).toHaveLength(0)

    await wrapper.setProps({ isRealTime: true })
    await flushPromises()

    expect(MockEventSource.instances).toHaveLength(1)
    expect(MockEventSource.instances[0].url).toBe('/api/logs/stream?path=app.log')
    expect(wrapper.text()).toContain('● 正在连接')
    expect(wrapper.text()).not.toContain('实时日志暂时断开，正在重新连接…')
  })

  it('keeps a successful stream connected when the server sends a business error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: vi.fn().mockResolvedValue({ entries: [], pos: 0 }),
      }),
    )
    vi.stubGlobal('EventSource', MockEventSource)

    const wrapper = mount(AppLogViewer, {
      props: {
        logPath: 'app.log',
        isRealTime: false,
      },
      global: {
        directives: { loading: {} },
        stubs: {
          ElCard: { template: '<div><slot name="header" /><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElText: { template: '<span><slot /></span>' },
        },
      },
    })
    await flushPromises()
    await wrapper.setProps({ isRealTime: true })
    await flushPromises()
    expect(MockEventSource.instances).toHaveLength(1)
    const source = MockEventSource.instances[0]

    expect(source.onopen).toBeTypeOf('function')
    source.onopen?.()
    await flushPromises()
    expect(wrapper.text()).toContain('● 已连接')
    source.emit('error', { reason: 'tailer failed' })
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('● 已连接')
    expect(wrapper.text()).not.toContain('实时日志暂时断开，正在重新连接…')
  })

  it('shows only unsupported feedback when EventSource is unavailable', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: vi.fn().mockResolvedValue({ entries: [], pos: 0 }),
      }),
    )
    vi.stubGlobal('EventSource', undefined)

    const wrapper = mount(AppLogViewer, {
      props: {
        logPath: 'app.log',
        isRealTime: true,
      },
      global: {
        directives: { loading: {} },
        stubs: {
          ElCard: { template: '<div><slot name="header" /><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElText: { template: '<span><slot /></span>' },
        },
      },
    })

    await flushPromises()

    expect(MockEventSource.instances).toHaveLength(0)
    expect(wrapper.text()).toContain('当前浏览器不支持实时日志，请手动刷新查看最新内容')
    expect(wrapper.text()).not.toContain('● 正在连接')
    expect(wrapper.text()).not.toContain('● 已断开')
  })

  it('stops the initial connection state when the log snapshot fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        json: vi.fn().mockResolvedValue({ error: '日志快照不可用' }),
      }),
    )
    vi.stubGlobal('EventSource', MockEventSource)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)

    const wrapper = mount(AppLogViewer, {
      props: {
        logPath: 'app.log',
        isRealTime: true,
      },
      global: {
        directives: { loading: {} },
        stubs: {
          ElCard: { template: '<div><slot name="header" /><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElText: { template: '<span><slot /></span>' },
        },
      },
    })

    await flushPromises()

    expect(MockEventSource.instances).toHaveLength(0)
    expect(wrapper.text()).toContain('加载初始日志失败')
    expect(wrapper.text()).not.toContain('日志快照不可用')
    expect(wrapper.text()).toContain('● 已断开')
    expect(wrapper.text()).not.toContain('● 正在连接')
  })

  it('stops reporting an initial connection when realtime mode is disabled before the snapshot loads', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => new Promise(() => {})),
    )
    vi.stubGlobal('EventSource', MockEventSource)

    const wrapper = mount(AppLogViewer, {
      props: {
        logPath: 'app.log',
        isRealTime: true,
      },
      global: {
        directives: { loading: {} },
        stubs: {
          ElCard: { template: '<div><slot name="header" /><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElText: { template: '<span><slot /></span>' },
        },
      },
    })

    await flushPromises()
    expect(wrapper.text()).toContain('● 正在连接')

    await wrapper.setProps({ isRealTime: false })
    await flushPromises()

    expect(wrapper.text()).toContain('● 已断开')
    expect(wrapper.text()).not.toContain('● 正在连接')
  })

  it('aborts and ignores a stale snapshot after the log path changes', async () => {
    type PendingRequest = {
      url: string
      signal: AbortSignal | undefined
      resolve: (response: { ok: boolean; json: () => Promise<unknown> }) => void
    }
    const pendingRequests: PendingRequest[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (url: string, options?: RequestInit) =>
          new Promise((resolve) => {
            pendingRequests.push({
              url,
              signal: options?.signal ?? undefined,
              resolve,
            })
          }),
      ),
    )
    vi.stubGlobal('EventSource', MockEventSource)

    const wrapper = mount(AppLogViewer, {
      props: {
        logPath: 'first.log',
        isRealTime: true,
      },
      global: {
        directives: { loading: {} },
        stubs: {
          ElCard: { template: '<div><slot name="header" /><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElText: { template: '<span><slot /></span>' },
        },
      },
    })

    await vi.waitFor(() => expect(pendingRequests).toHaveLength(1))
    await wrapper.setProps({ logPath: 'second.log' })
    await vi.waitFor(() => expect(pendingRequests).toHaveLength(2))

    pendingRequests[1].resolve({
      ok: true,
      json: vi.fn().mockResolvedValue({
        entries: [{ level: 'info', message: 'second snapshot', timestamp: 't2' }],
        pos: 2,
      }),
    })
    await flushPromises()
    pendingRequests[0].resolve({
      ok: true,
      json: vi.fn().mockResolvedValue({
        entries: [{ level: 'info', message: 'stale first snapshot', timestamp: 't1' }],
        pos: 1,
      }),
    })
    await flushPromises()

    expect(pendingRequests[0].signal?.aborted).toBe(true)
    expect(wrapper.text()).toContain('second snapshot')
    expect(wrapper.text()).not.toContain('stale first snapshot')
  })

  it.each([
    [
      403,
      { code: 500, error_code: 'REQUEST_ORIGIN_INVALID', message: 'private secret' },
      '访问地址校验失败',
    ],
    [403, { code: 500, error_code: 'CSRF_TOKEN_INVALID' }, '请求安全校验失败'],
    [401, { code: 401 }, '登录已失效'],
    [502, '<html>private secret</html>', '服务器处理请求失败'],
  ])('HTTP %s 失败使用公共提示且不创建实时流', async (status, body, message) => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          new Response(typeof body === 'string' ? body : JSON.stringify(body), { status }),
        ),
    )
    vi.stubGlobal('EventSource', MockEventSource)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const wrapper = mount(AppLogViewer, {
      props: { logPath: 'private.log', isRealTime: true },
      global: {
        directives: { loading: {} },
        stubs: { ElCard: { template: '<div><slot name="header" /><slot /></div>' } },
      },
    })
    await flushPromises()
    expect(wrapper.text()).toContain(message)
    expect(wrapper.text()).not.toContain('private secret')
    expect(MockEventSource.instances).toHaveLength(0)
    expect(console.error).toHaveBeenCalledExactlyOnceWith(
      '加载初始日志失败：',
      expect.objectContaining({
        method: 'GET',
        path: '/api/logs/old',
        status,
      }),
    )
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private.log')
    wrapper.unmount()
  })

  it('取消请求保持静默，普通解析异常不显示为网络故障', async () => {
    const fetch = vi
      .fn()
      .mockRejectedValueOnce(new DOMException('cancel', 'AbortError'))
      .mockResolvedValueOnce({
        ok: true,
        json: () => Promise.reject(new TypeError('internal parser')),
      })
    vi.stubGlobal('fetch', fetch)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'a.log', isRealTime: false } })
    await flushPromises()
    expect(console.error).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('失败')
    await wrapper.setProps({ logPath: 'b.log', isRealTime: true })
    await flushPromises()
    expect(wrapper.text()).toContain('加载初始日志失败')
    expect(wrapper.text()).not.toContain('网络')
    expect(wrapper.text()).not.toContain('internal parser')
    wrapper.unmount()
  })

  it('旧日志翻页失败不会污染已经切换的新路径', async () => {
    let rejectOld!: (error: Error) => void
    let oldSignal: AbortSignal | undefined
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ entries: [], pos: 20 })))
      .mockImplementationOnce((_url, options: RequestInit) => {
        oldSignal = options.signal ?? undefined
        return new Promise((_resolve, reject) => {
          rejectOld = reject
        })
      })
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            entries: [{ level: 'info', message: 'new snapshot', timestamp: 't2' }],
            pos: 10,
          }),
        ),
      )
    vi.stubGlobal('fetch', fetch)
    vi.stubGlobal('EventSource', MockEventSource)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'a.log', isRealTime: true } })
    await flushPromises()
    await wrapper.get('.logs').trigger('scroll')
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(2)
    await wrapper.setProps({ logPath: 'b.log' })
    await flushPromises()
    rejectOld(new TypeError('old network failure'))
    await flushPromises()
    expect(oldSignal?.aborted).toBe(true)
    expect(wrapper.text()).toContain('new snapshot')
    expect(wrapper.text()).not.toContain('失败')
    expect(console.error).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('原生流断开仍由浏览器重连，不附加 HTTP 探测', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ entries: [], pos: 0 })))
    vi.stubGlobal('fetch', fetch)
    vi.stubGlobal('EventSource', MockEventSource)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'a.log', isRealTime: true } })
    await flushPromises()
    const source = MockEventSource.instances[0]
    source.onerror?.(new Event('error'))
    await flushPromises()
    expect(wrapper.text()).toContain('实时日志暂时断开，正在重新连接')
    expect(fetch).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
  it('浏览器放弃重连（CLOSED）时回到已断开，不再提示正在重连', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ entries: [], pos: 0 }))),
    )
    vi.stubGlobal('EventSource', MockEventSource)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'a.log', isRealTime: true } })
    await flushPromises()
    const source = MockEventSource.instances[0]
    const close = vi.spyOn(source, 'close')
    source.readyState = MockEventSource.CLOSED
    source.onerror?.(new Event('error'))
    await flushPromises()
    expect(close).toHaveBeenCalled()
    expect(wrapper.text()).toContain('已断开')
    expect(wrapper.text()).not.toContain('正在重新连接')
    wrapper.unmount()
  })
})
