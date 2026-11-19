// @vitest-environment happy-dom
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/const', () => ({
  SERVER_URL: 'https://api.example.test/api',
}))

import AppLogViewer from '@/components/log/AppLogViewer.vue'

enableAutoUnmount(afterEach)

function mockLogLayout(el: HTMLElement) {
  Object.defineProperties(el, {
    clientHeight: { configurable: true, value: 240 },
    scrollHeight: {
      configurable: true,
      get: () =>
        [...el.children].reduce(
          (total, child) =>
            total +
            (child.classList.contains('log-line')
              ? 24
              : Number.parseFloat((child as HTMLElement).style.height) || 0),
          0,
        ),
    },
  })
}

type Listener = (event: MessageEvent<string>) => void

function logReply(
  entries: unknown[],
  pos: number,
  cursor: number,
  startPos = cursor,
  fileID = 'file-a',
) {
  return new Response(
    JSON.stringify({
      status: 200,
      data: { file_id: fileID, entries, pos, cursor, start_pos: startPos },
    }),
  )
}

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
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    MockEventSource.instances = []
  })

  it('已结束任务仍可清空显示，保留日志文件操作', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        logReply([{ level: 'info', timestamp: 't', message: '已保存日志', cursor: 10 }], 0, 10),
      )
    vi.stubGlobal('fetch', fetch)
    vi.stubGlobal('EventSource', MockEventSource)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: false } })
    await flushPromises()
    expect(wrapper.find('[aria-label="连接实时日志"]').exists()).toBe(false)
    await wrapper.get('button[aria-label="清空当前显示日志"]').trigger('click')
    expect(wrapper.text()).not.toContain('已保存日志')
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(MockEventSource.instances).toHaveLength(0)
  })

  it('默认查看在新标签页打开纯文本，下拉下载仍使用原接口', async () => {
    vi.useFakeTimers()
    const opened: { href: string; download: string | null; target: string; rel: string }[] = []
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      opened.push({
        href: this.href,
        download: this.getAttribute('download'),
        target: this.target,
        rel: this.rel,
      })
    })
    const fetch = vi.fn().mockResolvedValueOnce(logReply([], 0, 0))
    vi.stubGlobal('fetch', fetch)
    const path = 'libs/中文 #日志.log'
    const wrapper = mount(AppLogViewer, { props: { logPath: ' ' + path + ' ', isRealTime: false } })
    await flushPromises()
    await wrapper.get('button[aria-label="查看日志"]').trigger('click')
    wrapper.findComponent({ name: 'ElDropdown' }).vm.$emit('command', 'download')
    expect(opened).toEqual([
      {
        href: 'https://api.example.test/api/logs/view?path=' + encodeURIComponent(path),
        download: null,
        target: '_blank',
        rel: 'noopener',
      },
      {
        href: 'https://api.example.test/api/logs/download?path=' + encodeURIComponent(path),
        download: '中文 #日志.log',
        target: '_blank',
        rel: 'noopener',
      },
    ])
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(100)
    expect(document.querySelector('a[href*="/logs/"]')).toBeNull()
  })

  it('超长未换行尾行保留完整正文，仍可继续读取更早日志', async () => {
    const message = 'x'.repeat(2200000)
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        logReply([{ level: 'info', timestamp: 't', message, cursor: 0 }], 100, 100, 2200100),
      )
      .mockResolvedValueOnce(
        logReply([{ level: 'info', timestamp: 't', message: '更早正文', cursor: 80 }], 0, 100),
      )
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: false } })
    await flushPromises()
    expect(wrapper.get('.log-message').text() === message).toBe(true)
    await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
    await flushPromises()
    expect(fetch.mock.calls[1]?.[0]).toContain('pos=100')
    expect(wrapper.findAll('.log-message').some((row) => row.text() === '更早正文')).toBe(true)
  })

  it('超长完整行去重得到空页后，只推进位置并保留非空阅读页', async () => {
    const long = { level: 'info', timestamp: 't', message: 'x'.repeat(2200000), cursor: 2200100 }
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(logReply([long], 100, 2200100))
      .mockResolvedValueOnce(logReply([long], 100, 2200100))
      .mockResolvedValueOnce(
        logReply([{ level: 'info', timestamp: 't', message: '更早正文', cursor: 100 }], 0, 100),
      )
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: false } })
    await flushPromises()
    await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
    await flushPromises()
    expect(wrapper.findAll('.log-line')).toHaveLength(1)
    expect(wrapper.get('.log-message').text() === long.message).toBe(true)
    await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
    await flushPromises()
    expect(fetch.mock.calls[2]?.[0]).toContain('pos=100')
    expect(wrapper.findAll('.log-message').some((row) => row.text() === '更早正文')).toBe(true)
  })

  it.each(['log_file_rotated', 'log_file_truncated', 'cursor_invalid'])(
    '%s 后取消旧历史请求，使用新文件的位置继续阅读',
    async (reason) => {
      vi.stubGlobal('EventSource', MockEventSource)
      const entry = (cursor: number, message: string) => ({
        level: 'info',
        timestamp: 't',
        message,
        cursor,
      })
      let resolveOld!: (response: Response) => void
      let oldSignal!: AbortSignal
      const fetch = vi
        .fn()
        .mockResolvedValueOnce(
          logReply([entry(10000, '旧文件最新'), entry(9000, '旧文件历史')], 8000, 10000),
        )
        .mockResolvedValueOnce(
          logReply([entry(9000, '旧文件历史'), entry(8000, '旧文件更早')], 7000, 9000),
        )
        .mockImplementationOnce((_url, options: RequestInit) => {
          oldSignal = options.signal!
          return new Promise<Response>((resolve) => {
            resolveOld = resolve
          })
        })
      if (reason === 'cursor_invalid') {
        fetch.mockResolvedValueOnce(
          new Response(
            JSON.stringify({
              status: 409,
              data: null,
              error_code: 'LOG_CURSOR_INVALID',
              message: '日志位置已变化',
              instance: '/request-errors/ComponentTestFailure0001',
            }),
            { status: 409 },
          ),
        )
      }
      fetch
        .mockResolvedValueOnce(logReply([entry(500, '新文件最新')], 400, 500, 500, 'file-b'))
        .mockResolvedValueOnce(
          logReply([entry(500, '新文件最新'), entry(400, '新文件历史')], 0, 500, 500, 'file-b'),
        )
      vi.stubGlobal('fetch', fetch)
      const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: true } })
      await flushPromises()
      await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
      await flushPromises()
      await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
      await flushPromises()
      MockEventSource.instances[0].emit('resync_required', {
        reason: reason === 'cursor_invalid' ? 'subscriber_queue_full' : reason,
      })
      await flushPromises()
      resolveOld(logReply([entry(7000, '迟到的旧文件正文')], 6000, 7000))
      await flushPromises()
      expect(oldSignal.aborted).toBe(true)
      expect(wrapper.text()).toContain('新文件最新')
      expect(wrapper.text()).not.toContain('旧文件')
      await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
      await flushPromises()
      expect(fetch.mock.calls.at(-1)?.[0]).toContain('pos=500')
      expect(fetch.mock.calls.at(-1)?.[0]).toContain('file_id=file-b')
      expect(MockEventSource.instances.at(-1)?.url).toContain('file_id=file-b')
      expect(wrapper.text()).toContain('新文件历史')
      expect(wrapper.text()).not.toContain('旧文件')
    },
  )

  it.each([4, 16])('断线后轮转为 %s 字节的新文件，旧身份补读失效并完整重载', async (end) => {
    vi.stubGlobal('EventSource', MockEventSource)
    const entry = (cursor: number, message: string) => ({
      level: 'info',
      timestamp: 't',
      cursor,
      message,
    })
    const current = [entry(end === 4 ? 4 : 8, '新文件完整首行')]
    if (end > 4) current.unshift(entry(end, '新文件第二行'))
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(logReply([entry(4, '旧文件正文')], 0, 4))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ status: 409, data: null, error_code: 'LOG_CURSOR_INVALID' }),
          { status: 409 },
        ),
      )
      .mockResolvedValueOnce(logReply(current, 0, end, end, 'file-b'))
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: true } })
    await flushPromises()
    const source = MockEventSource.instances[0]
    expect(source.url).toContain('cursor=4')
    expect(source.url).toContain('file_id=file-a')
    source.readyState = MockEventSource.CLOSED
    source.onerror?.(new Event('error'))
    await flushPromises()
    expect(fetch.mock.calls[1]?.[0]).toContain('/api/logs/catch-up?')
    expect(fetch.mock.calls[1]?.[0]).toContain('file_id=file-a')
    expect(fetch.mock.calls[2]?.[0]).toContain('pos=-1')
    expect(MockEventSource.instances[1].url).toContain(`cursor=${end}`)
    expect(MockEventSource.instances[1].url).toContain('file_id=file-b')
    expect(wrapper.text()).toContain('新文件完整首行')
    expect(wrapper.text()).not.toContain('旧文件正文')
  })

  it('缺少文件身份的成功包络不会显示正文或创建恢复流', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          logReply(
            [{ level: 'info', timestamp: 't', message: '未经验证的正文', cursor: 4 }],
            0,
            4,
            4,
            '',
          ),
        ),
    )
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: true } })
    await flushPromises()
    expect(wrapper.text()).not.toContain('未经验证的正文')
    expect(MockEventSource.instances).toHaveLength(0)
    expect(wrapper.text()).toContain('● 已断开')
    expect(wrapper.text()).not.toContain('● 正在连接')
  })

  it('实时批次的文件身份变化时重载快照，不混入跨文件的位置', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const entry = (cursor: number, message: string) => ({
      level: 'info',
      timestamp: 't',
      cursor,
      message,
    })
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(logReply([entry(8, '旧文件正文')], 0, 8))
      .mockResolvedValueOnce(logReply([entry(4, '新文件快照')], 0, 4, 4, 'file-b'))
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: true } })
    await flushPromises()
    MockEventSource.instances[0].emit('log_batch', {
      file_id: 'file-b',
      entries: [entry(16, '未经快照验证的批次')],
      cursor: 16,
    })
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(fetch.mock.calls[1]?.[0]).toContain('pos=-1')
    expect(wrapper.text()).toContain('新文件快照')
    expect(wrapper.text()).not.toContain('旧文件正文')
    expect(wrapper.text()).not.toContain('未经快照验证的批次')
    expect(MockEventSource.instances[1].url).toContain('cursor=4')
    expect(MockEventSource.instances[1].url).toContain('file_id=file-b')
  })

  it('普通重连补读保留历史窗口及阅读位置', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    const entries = Array.from({ length: 1000 }, (_, index) => ({
      level: 'info',
      timestamp: 't',
      message: '正文' + (1000 - index),
      cursor: 1000 - index,
    }))
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(logReply(entries, 0, 1000))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            status: 200,
            data: {
              file_id: 'file-a',
              entries: [{ level: 'info', timestamp: 't', message: '重连新增正文', cursor: 1001 }],
              cursor: 1001,
              has_more: false,
            },
          }),
        ),
      )
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: true } })
    await flushPromises()
    const viewport = wrapper.get<HTMLElement>('.logs')
    mockLogLayout(viewport.element)
    viewport.element.scrollTop = 123
    await viewport.trigger('scroll')
    const visible = wrapper.findAll('.log-message').map((row) => row.text())
    MockEventSource.instances[0].onopen?.()
    MockEventSource.instances[0].onopen?.()
    await flushPromises()
    expect(fetch.mock.calls[1]?.[0]).toContain('/api/logs/catch-up?')
    expect(fetch.mock.calls[1]?.[0]).toContain('cursor=1000')
    expect(fetch.mock.calls[1]?.[0]).toContain('file_id=file-a')
    expect(MockEventSource.instances[1].url).toContain('cursor=1001')
    expect(MockEventSource.instances[1].url).toContain('file_id=file-a')
    expect(wrapper.findAll('.log-message').map((row) => row.text())).toEqual(visible)
    expect(viewport.element.scrollTop).toBe(123)
    await wrapper.get('.log-info button').trigger('click')
    expect(wrapper.text()).toContain('重连新增正文')
  })

  it('空日志滚轮不暂停实时跟随，清空窗口后仍能滚动重读文件', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', MockEventSource)
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            status: 200,
            data: { file_id: 'file-a', entries: [], pos: 0, cursor: 0 },
          }),
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            status: 200,
            data: {
              file_id: 'file-a',
              entries: [{ level: 'info', timestamp: 't', message: '已保存的日志', cursor: 10 }],
              pos: 0,
              cursor: 10,
            },
          }),
        ),
      )
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: true } })
    await flushPromises()
    await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
    expect(fetch).toHaveBeenCalledTimes(1)
    MockEventSource.instances[0].emit('log_batch', {
      file_id: 'file-a',
      entries: [{ level: 'info', timestamp: 't', message: '已保存的日志', cursor: 10 }],
      cursor: 10,
    })
    await vi.advanceTimersByTimeAsync(100)
    expect(wrapper.text()).toContain('已保存的日志')
    expect(wrapper.findAll('button').some((button) => button.text() === '返回最新')).toBe(false)
    await wrapper.get('button[aria-label="清空当前显示日志"]').trigger('click')
    expect(wrapper.text()).not.toContain('已保存的日志')
    await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
    await flushPromises()
    expect(fetch.mock.calls[1][0]).toContain('pos=10')
    expect(wrapper.text()).toContain('已保存的日志')
  })

  it('大日志保持 200 行渲染，历史双向滚动保留锚点并隔离实时输入', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('EventSource', MockEventSource)
    const entry = (cursor: number) => ({
      level: 'info',
      timestamp: 't',
      message: `line-${cursor}`,
      cursor,
    })
    const fetch = vi.fn((url: string) => {
      const query = new URL(url, 'https://example.test').searchParams
      const end = Number(query.get('pos')) === -1 ? 100000 : Number(query.get('pos'))
      const limit = Number(query.get('limit'))
      const entries = Array.from({ length: Math.min(end, limit) }, (_, index) => entry(end - index))
      return Promise.resolve(
        new Response(
          JSON.stringify({
            status: 200,
            data: {
              file_id: 'file-a',
              entries,
              pos: Math.max(0, end - limit),
              cursor: end,
              start_pos: end,
            },
          }),
        ),
      )
    })
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: true } })
    await flushPromises()
    const source = MockEventSource.instances[0]
    source.emit('log_batch', {
      file_id: 'file-a',
      entries: Array.from({ length: 1000 }, (_, i) => entry(100001 + i)),
      cursor: 101000,
    })
    await vi.advanceTimersByTimeAsync(100)
    expect(wrapper.findAll('.log-line')).toHaveLength(200)
    expect(wrapper.text()).toContain('line-101000')
    const viewport = wrapper.get<HTMLElement>('.logs')
    const el = viewport.element
    mockLogLayout(el)
    const edge = async (direction: number) => {
      el.scrollTop = direction > 0 ? el.scrollHeight - el.clientHeight : 0
      await viewport.trigger('scroll')
      await flushPromises()
    }
    await edge(1)
    expect(fetch.mock.calls[1][0]).toContain('pos=99001')
    // 删除视口上方的一页后，仍然停在原来那条日志上，而不是回到页首。
    expect(el.scrollTop).toBe(190 * 24)
    const frozen = wrapper.findAll('.log-line').map((line) => line.text())
    source.emit('log_batch', { file_id: 'file-a', entries: [entry(101001)], cursor: 101001 })
    await vi.advanceTimersByTimeAsync(100)
    expect(wrapper.findAll('.log-line').map((line) => line.text())).toEqual(frozen)
    expect(el.scrollTop).toBe(190 * 24)
    for (let page = 0; page < 30; page++) {
      await edge(1)
      expect(wrapper.findAll('.log-line').length).toBeLessThanOrEqual(200)
      expect(el.scrollHeight).toBeLessThanOrEqual(400 * 24)
    }
    const olderEnd = Number(
      new URL(fetch.mock.calls.at(-1)![0], 'https://example.test').searchParams.get('pos'),
    )
    await edge(-1)
    expect(
      Number(new URL(fetch.mock.calls.at(-1)![0], 'https://example.test').searchParams.get('pos')),
    ).toBeGreaterThan(olderEnd)
    expect(el.scrollTop).toBe(200 * 24)
    await wrapper
      .findAll('button')
      .find((button) => button.text() === '返回最新')!
      .trigger('click')
    expect(wrapper.text()).toContain('line-101001')
    expect(el.scrollTop).toBe(0)
  })

  it('换行高度连续变化后仍保留同一条日志及其阅读位置', async () => {
    vi.useFakeTimers()
    let resize!: () => void
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(private callback: (entries: ResizeObserverEntry[]) => void) {}
        observe(target: Element) {
          if (target.classList.contains('logs')) resize = () => this.callback([])
        }
        disconnect() {}
      },
    )
    let height = 24
    const measure = vi
      .spyOn(HTMLElement.prototype, 'getBoundingClientRect')
      .mockImplementation(function (this: HTMLElement) {
        return new DOMRect(0, 0, 100, this.classList.contains('log-line') ? height : 240)
      })
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            status: 200,
            data: {
              file_id: 'file-a',
              entries: Array.from({ length: 1000 }, (_, i) => ({
                level: 'info',
                timestamp: 't',
                message: `line-${1000 - i}`,
                cursor: 1000 - i,
              })),
              pos: 0,
              cursor: 1000,
            },
          }),
        ),
      ),
    )
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: false } })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(20)
    const viewport = wrapper.get<HTMLElement>('.logs'),
      el = viewport.element
    mockLogLayout(el)
    el.scrollTop = 5 * 24 + 3
    await viewport.trigger('scroll')
    await flushPromises()
    height = 48
    for (let i = 0; i < 10; i++) resize()
    await vi.advanceTimersByTimeAsync(20)
    expect(el.scrollTop).toBe(5 * 48 + 3)
    expect(wrapper.findAll('.log-line')).toHaveLength(200)
    resize()
    const measurements = measure.mock.calls.length
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(20)
    expect(measure.mock.calls).toHaveLength(measurements)
  })

  it('迟到历史不拉走反向滚动的位置，重读静态尾页使用固定 EOF 并保留长行', async () => {
    const partial = '未换行正文'.repeat(10000)
    const entry = (cursor: number) => ({
      level: 'info',
      timestamp: 't',
      message: `line-${cursor}`,
      cursor,
    })
    const initial = [
      { level: 'error', timestamp: 't', message: partial, cursor: 0 },
      ...Array.from({ length: 399 }, (_, i) => entry(400 - i)),
    ]
    const reply = (entries: unknown[], pos: number) =>
      new Response(
        JSON.stringify({
          status: 200,
          data: { file_id: 'file-a', entries, pos, cursor: 400, start_pos: 401 },
        }),
      )
    let resolveHistory!: (reply: Response) => void
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(reply(initial, 1))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveHistory = resolve
          }),
      )
      .mockResolvedValueOnce(reply([entry(2), entry(1)], 0))
      .mockResolvedValueOnce(reply(initial.slice(0, 200), 202))
    vi.stubGlobal('fetch', fetch)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: false } })
    await flushPromises()
    const viewport = wrapper.get<HTMLElement>('.logs'),
      el = viewport.element
    mockLogLayout(el)
    el.scrollTop = el.scrollHeight - el.clientHeight
    await viewport.trigger('scroll')
    el.scrollTop = 100
    await viewport.trigger('scroll')
    resolveHistory(reply([entry(2), entry(1)], 0))
    await flushPromises()
    expect(el.scrollTop).toBe(100)
    expect(wrapper.text()).toContain(partial)
    el.scrollTop = el.scrollHeight - el.clientHeight
    await viewport.trigger('scroll')
    await flushPromises()
    el.scrollTop = 0
    await viewport.trigger('scroll')
    await flushPromises()
    expect(fetch.mock.calls[3][0]).toContain('pos=401')
    await wrapper
      .findAll('button')
      .find((button) => button.text() === '全部')!
      .trigger('click')
    await wrapper
      .findAll('button')
      .find((button) => button.text() === 'Error')!
      .trigger('click')
    expect(wrapper.findAll('.log-message').map((message) => message.text())).toEqual([partial])
  })

  it('历史慢请求合并，失败保留正文并可原位重试，返回最新会取消在途请求', async () => {
    let rejectHistory!: (error: Error) => void
    let pendingSignal!: AbortSignal
    const reply = (message: string, pos: number, cursor: number) =>
      new Response(
        JSON.stringify({
          status: 200,
          data: {
            file_id: 'file-a',
            entries: [{ level: 'info', timestamp: 't', message, cursor }],
            pos,
            cursor,
          },
        }),
      )
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(reply('保留的正文', 20, 30))
      .mockImplementationOnce(
        (_url, options: RequestInit) =>
          new Promise((_resolve, reject) => {
            pendingSignal = options.signal!
            rejectHistory = reject
          }),
      )
      .mockResolvedValueOnce(reply('重试得到的历史', 10, 15))
      .mockImplementationOnce((_url, options: RequestInit) => {
        pendingSignal = options.signal!
        return new Promise(() => {})
      })
    vi.stubGlobal('fetch', fetch)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'app.log', isRealTime: false } })
    await flushPromises()
    const viewport = wrapper.get('.logs')
    for (let i = 0; i < 10; i++) await viewport.trigger('wheel', { deltaY: 100 })
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('保留的正文')
    expect(wrapper.text()).toContain('正在加载历史日志')
    rejectHistory(new TypeError('Failed to fetch'))
    await flushPromises()
    expect(wrapper.text()).toContain('保留的正文')
    await wrapper
      .findAll('button')
      .find((button) => button.text() === '重试')!
      .trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('重试得到的历史')
    expect(fetch.mock.calls[2][0]).toBe(fetch.mock.calls[1][0])
    await viewport.trigger('wheel', { deltaY: 100 })
    await wrapper
      .findAll('button')
      .find((button) => button.text() === '返回最新')!
      .trigger('click')
    expect(pendingSignal.aborted).toBe(true)
    expect(wrapper.text()).toContain('保留的正文')
  })

  it('查看历史不会中断正在补读的重连', async () => {
    vi.stubGlobal('EventSource', MockEventSource)
    let completeCatchUp!: (response: Response) => void
    let catchUpSignal!: AbortSignal
    const reply = (data: object) =>
      new Response(JSON.stringify({ status: 200, data: { file_id: 'file-a', ...data } }))
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(reply({ entries: [], pos: 10, cursor: 10 }))
        .mockImplementationOnce((_url, options: RequestInit) => {
          catchUpSignal = options.signal!
          return new Promise<Response>((resolve) => {
            completeCatchUp = resolve
          })
        })
        .mockResolvedValueOnce(
          reply({
            entries: [{ level: 'info', timestamp: 't', message: '历史页' }],
            pos: 0,
            cursor: 10,
          }),
        ),
    )
    const wrapper = mount(AppLogViewer, {
      props: { logPath: 'app.log', isRealTime: true },
      global: { directives: { loading: () => {} } },
    })
    await flushPromises()
    const source = MockEventSource.instances[0]
    source.onopen?.()
    source.onopen?.()
    await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
    await flushPromises()
    expect(catchUpSignal.aborted).toBe(false)
    completeCatchUp(reply({ entries: [], cursor: 30, has_more: false }))
    await flushPromises()
    expect(MockEventSource.instances[1].url).toContain('cursor=30')
    expect(wrapper.text()).toContain('历史页')
  })

  it('初始快照成功后才订阅，业务错误保留已建立的连接', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve(logReply([], 0, 0))),
    )
    vi.stubGlobal('EventSource', MockEventSource)
    const wrapper = mount(AppLogViewer, {
      props: { logPath: 'app.log', isRealTime: false },
    })
    await flushPromises()
    expect(MockEventSource.instances).toHaveLength(0)
    await wrapper.setProps({ isRealTime: true })
    await flushPromises()
    expect(MockEventSource.instances).toHaveLength(1)
    const source = MockEventSource.instances[0]
    expect(source.url).toBe('/api/logs/stream?path=app.log&cursor=0&file_id=file-a')
    expect(wrapper.text()).toContain('● 正在连接')
    expect(wrapper.text()).not.toContain('实时日志暂时断开，正在重新连接…')
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
        json: vi
          .fn()
          .mockResolvedValue({ status: 200, data: { file_id: 'file-a', entries: [], pos: 0 } }),
        status: 200,
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
      resolve: (response: { ok: boolean; status: number; json: () => Promise<unknown> }) => void
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
        status: 200,
        data: {
          file_id: 'file-a',
          entries: [{ level: 'info', message: 'second snapshot', timestamp: 't2' }],
          pos: 2,
        },
      }),
      status: 200,
    })
    await flushPromises()
    pendingRequests[0].resolve({
      ok: true,
      json: vi.fn().mockResolvedValue({
        status: 200,
        data: {
          file_id: 'file-a',
          entries: [{ level: 'info', message: 'stale first snapshot', timestamp: 't1' }],
          pos: 1,
        },
      }),
      status: 200,
    })
    await flushPromises()

    expect(pendingRequests[0].signal?.aborted).toBe(true)
    expect(wrapper.text()).toContain('second snapshot')
    expect(wrapper.text()).not.toContain('stale first snapshot')
  })

  it.each([
    [
      403,
      {
        status: 500,
        error_code: 'REQUEST_ORIGIN_INVALID',
        message: 'private secret',
        data: null,
        instance: '/request-errors/ComponentTestFailure0001',
      },
      '访问地址校验失败',
    ],
    [
      403,
      {
        status: 500,
        error_code: 'CSRF_TOKEN_INVALID',
        data: null,
        message: '请求处理失败',
        instance: '/request-errors/ComponentTestFailure0001',
      },
      '请求安全校验失败',
    ],
    [
      401,
      {
        status: 401,
        data: null,
        message: '请求处理失败',
        error_code: 'TEST_REQUEST_FAILED',
        instance: '/request-errors/ComponentTestFailure0001',
      },
      '登录已失效',
    ],
    [502, '<html>private secret</html>', '服务器处理请求失败'],
  ])('HTTP %s 失败使用公共提示且不创建实时流', async (status, body, message) => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(typeof body === 'string' ? body : JSON.stringify({ ...body, status }), {
          status,
        }),
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
    expect(wrapper.text()).toContain('● 已断开')
    expect(wrapper.text()).not.toContain('● 正在连接')
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
        status: 200,
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
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ status: 200, data: { file_id: 'file-a', entries: [], pos: 20 } }),
        ),
      )
      .mockImplementationOnce((_url, options: RequestInit) => {
        oldSignal = options.signal ?? undefined
        return new Promise((_resolve, reject) => {
          rejectOld = reject
        })
      })
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            status: 200,
            data: {
              file_id: 'file-a',
              entries: [{ level: 'info', message: 'new snapshot', timestamp: 't2' }],
              pos: 10,
            },
          }),
        ),
      )
    vi.stubGlobal('fetch', fetch)
    vi.stubGlobal('EventSource', MockEventSource)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'a.log', isRealTime: true } })
    await flushPromises()
    await wrapper.get('.logs').trigger('wheel', { deltaY: 100 })
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
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(
          JSON.stringify({ status: 200, data: { file_id: 'file-a', entries: [], pos: 0 } }),
        ),
      )
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
  it('补读后再次 CLOSED 停止自动重建，显式连接和成功建连可恢复', async () => {
    const fetch = vi.fn((url: string) =>
      Promise.resolve(
        url.includes('/catch-up?')
          ? new Response(
              JSON.stringify({
                status: 200,
                data: { file_id: 'file-a', entries: [], cursor: 4, has_more: false },
              }),
            )
          : logReply([], 0, 4),
      ),
    )
    vi.stubGlobal('fetch', fetch)
    vi.stubGlobal('EventSource', MockEventSource)
    const wrapper = mount(AppLogViewer, { props: { logPath: 'a.log', isRealTime: true } })
    await flushPromises()
    const failConnection = async () => {
      const source = MockEventSource.instances.at(-1)!
      const close = vi.spyOn(source, 'close')
      source.readyState = MockEventSource.CLOSED
      source.onerror?.(new Event('error'))
      await flushPromises()
      expect(close).toHaveBeenCalled()
    }
    await failConnection()
    await failConnection()
    expect(MockEventSource.instances).toHaveLength(2)
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('已断开')
    expect(wrapper.text()).not.toContain('正在重新连接')

    await wrapper.get('button[aria-label="连接实时日志"]').trigger('click')
    await flushPromises()
    expect(MockEventSource.instances).toHaveLength(3)
    await failConnection()
    expect(MockEventSource.instances).toHaveLength(4)
    MockEventSource.instances.at(-1)!.onopen?.()
    await failConnection()
    expect(MockEventSource.instances).toHaveLength(5)
    await failConnection()
    expect(MockEventSource.instances).toHaveLength(5)
    expect(fetch).toHaveBeenCalledTimes(4)
  })
})
