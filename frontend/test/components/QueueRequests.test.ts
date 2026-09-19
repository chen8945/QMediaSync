import axios, { AxiosError, CanceledError, type InternalAxiosRequestConfig } from 'axios'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { defineComponent, h, KeepAlive, ref, type PropType } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppDownloadQueue from '@/components/AppDownloadQueue.vue'
import AppUploadQueue from '@/components/AppUploadQueue.vue'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import { httpKey } from '@/http/client'
import { markAuthInvalidationHandled } from '@/http/errors'

const mocks = vi.hoisted(() => ({
  events: new Map<
    string,
    { event: (data: Record<string, unknown>) => void; reconnect?: () => void }
  >(),
}))
vi.mock('@/composables/useRealtimeEvents', () => ({
  useRealtimeEvent: (
    type: string,
    event: (data: Record<string, unknown>) => void,
    reconnect?: () => void,
  ) => {
    mocks.events.set(type, { event, reconnect })
  },
}))

const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const rows = [
  {
    id: '1',
    file_name: 'pending.mkv',
    status: 0,
    source: 'strm_sync',
    source_type: 'baidu',
    remote_file_id: 'file-1',
    remote_full_path: '/movies/pending.mkv',
  },
  {
    id: '2',
    file_name: 'done.mkv',
    status: 2,
    source: 'strm_sync',
    source_type: 'baidu',
    remote_file_id: 'file-2',
    remote_full_path: '/movies/done.mkv',
  },
]
const queueStatus = {
  running: true,
  pending: 1,
  processing: 0,
  completed: 1,
  failed: 0,
  cancelled: 0,
  total: 2,
}
const listData = () => ({
  list: rows.map((row) => ({ ...row })),
  total: 2,
  queue_status: { ...queueStatus },
})
const ok = (data: unknown = null) => ({ code: 200, data })
interface Reply {
  code: number
  data?: unknown
  message?: string
  error_code?: string
}
type Handler = (config: InternalAxiosRequestConfig) => Reply | Promise<Reply>
const tableStub = defineComponent({
  props: { data: { type: Array as PropType<typeof rows>, default: () => [] } },
  setup: (props, { expose }) => {
    expose({ doLayout: () => {} })
    return () =>
      h(
        'div',
        { class: 'queue-rows' },
        props.data.map((row) =>
          h('div', `${row.file_name}:${row.status}:${row.remote_full_path}:${row.remote_file_id}`),
        ),
      )
  },
})

enableAutoUnmount(afterEach)
beforeEach(() => {
  sessionStorage.clear()
  vi.useFakeTimers()
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  mocks.events.clear()
})
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.clearAllMocks()
})

const failure =
  (kind: string): Handler =>
  (config) => {
    if (kind === 'network' || kind === 'timeout') {
      throw new AxiosError(
        'secret token from transport',
        kind === 'network' ? 'ERR_NETWORK' : 'ETIMEDOUT',
        config,
      )
    }
    if (kind === 'cancel') throw new CanceledError('secret', config)
    if (kind === '401') {
      const error = new AxiosError('secret', 'ERR_BAD_REQUEST', config, undefined, {
        status: 401,
        statusText: '',
        headers: {},
        config,
        data: { code: 401, message: 'secret' },
      })
      markAuthInvalidationHandled(error)
      throw error
    }
    return {
      code: 400,
      message:
        kind === 'business' ? '队列正忙' : 'secret token and https://private.test/?token=secret',
      error_code:
        kind === 'origin'
          ? 'REQUEST_ORIGIN_INVALID'
          : kind === 'csrf'
            ? 'CSRF_TOKEN_INVALID'
            : undefined,
    }
  }

describe.each([
  { component: AppDownloadQueue, queue: 'download', label: '下载' },
  { component: AppUploadQueue, queue: 'upload', label: '上传' },
])('$label 队列真实 API 交互', ({ component, queue, label }) => {
  const setup = (cached = false) => {
    const handlers: { list: Handler; status: Handler; write: Handler } = {
      list: () => ok(listData()),
      status: () => ok(queueStatus),
      write: () => ok(),
    }
    const adapter = vi.fn(async (config: InternalAxiosRequestConfig) => {
      const handler =
        config.method === 'post'
          ? handlers.write
          : config.url?.endsWith('/status')
            ? handlers.status
            : handlers.list
      return { config, status: 200, statusText: '', headers: {}, data: await handler(config) }
    })
    const visible = ref(true)
    const host = defineComponent({
      setup: () => () => h(KeepAlive, () => (visible.value ? h(component) : h('div'))),
    })
    const wrapper = shallowMount(cached ? host : component, {
      global: {
        plugins: [createPinia()],
        provide: { [httpKey]: axios.create({ adapter }) },
        stubs: {
          PageHeader: { template: '<header><slot name="actions" /></header>' },
          ElButton: false,
          ElTable: tableStub,
          KeepAlive: false,
          ...(cached ? { AppDownloadQueue: false, AppUploadQueue: false } : {}),
        },
      },
    })
    const button = (name: string) => wrapper.findAll('button').find((item) => item.text() === name)!
    const calls = (method: string) =>
      adapter.mock.calls.filter(([config]) => config.method === method)
    return { wrapper, handlers, adapter, button, calls, visible }
  }

  it.each([
    ['origin', '域名、协议和端口'],
    ['csrf', '刷新页面'],
    ['business', '队列正忙'],
    ['network', '无法获取服务器响应'],
    ['timeout', '操作结果尚未确认'],
  ])(
    '%s mutation failure keeps rows and does not refresh or report success',
    async (kind, message) => {
      const state = setup()
      await flushPromises()
      const before = state.wrapper.get('.queue-rows').text()
      state.handlers.write = failure(kind)
      await state.button('清空等待').trigger('click')
      await flushPromises()
      expect(state.wrapper.get('.queue-rows').text()).toBe(before)
      expect(state.calls('post')).toHaveLength(1)
      expect(state.calls('get')).toHaveLength(2)
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(expect.stringContaining(message))
      expect(state.button('清空等待').attributes('disabled')).toBeUndefined()
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
      expect(JSON.stringify(vi.mocked(ElMessage.error).mock.calls)).not.toContain('secret')
    },
  )

  it('exposes verified business reasons and keeps retry failure distinct from success', async () => {
    const state = setup()
    await flushPromises()
    state.handlers.write = () => ({ code: 400, message: `重试失败的${label}任务失败` })
    await state.button('重试失败').trigger('click')
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      `重试失败任务时出错：重试失败的${label}任务失败`,
    )
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(state.calls('get')).toHaveLength(2)
  })

  it.each(['cancel', '401'])('silences %s on mutation and both snapshot stages', async (kind) => {
    for (const stage of ['write', 'list', 'status'] as const) {
      const state = setup()
      await flushPromises()
      state.handlers[stage] = failure(kind)
      await state.button('清空等待').trigger('click')
      await flushPromises()
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(state.button('清空等待').attributes('disabled')).toBeUndefined()
      if (stage === 'write') expect(state.calls('get')).toHaveLength(2)
      state.wrapper.unmount()
    }
    expect(console.error).not.toHaveBeenCalled()
  })

  it.each(['list', 'status'])(
    'reports a failed %s refresh once after a successful clear',
    async (stage) => {
      const state = setup()
      await flushPromises()
      state.handlers[stage as 'list' | 'status'] = failure('network')
      await state.button('清空等待').trigger('click')
      await flushPromises()
      expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('队列已清空')
      expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
        expect.stringMatching(/操作已成功，但刷新队列快照失败.*无法获取服务器响应/),
      )
      expect(state.calls('post')).toHaveLength(1)
      expect(state.button('清空等待').attributes('disabled')).toBeUndefined()
      if (stage === 'list') {
        expect(state.wrapper.get('.queue-rows').text()).not.toContain('pending.mkv')
        expect(state.wrapper.get('.queue-rows').text()).toContain('done.mkv')
        expect(state.wrapper.getComponent(ResponsivePagination).props('total')).toBe(1)
      }
    },
  )

  it('treats an HTTP 200 business failure during refresh as a snapshot failure', async () => {
    const state = setup()
    await flushPromises()
    state.handlers.list = failure('business')
    await state.button('全部暂停').trigger('click')
    await flushPromises()
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('已暂停所有任务')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '操作已成功，但刷新队列快照失败，请手动刷新。',
    )
    expect(state.wrapper.get('.queue-rows').text()).toContain('pending.mkv')
  })

  it('preserves filters and rows on ordinary refresh failure with classified feedback', async () => {
    const state = setup()
    await flushPromises()
    const before = state.wrapper.get('.queue-rows').text()
    state.handlers.list = failure('csrf')
    state.wrapper.getComponent({ name: 'ElSelect' }).vm.$emit('change', 3)
    await flushPromises()
    expect(state.wrapper.get('.queue-rows').text()).toBe(before)
    expect(state.wrapper.getComponent({ name: 'ElSelect' }).props('modelValue')).toBe(3)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(expect.stringContaining('刷新页面'))
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('coalesces in-flight changes and applies only the latest filtered snapshot', async () => {
    const state = setup()
    await flushPromises()
    const old = deferred<Reply>()
    state.handlers.list = () => old.promise
    await state.button('刷新').trigger('click')
    state.wrapper.getComponent({ name: 'ElSelect' }).vm.$emit('change', 3)
    state.wrapper.getComponent(ResponsivePagination).vm.$emit('current-change', 2)
    await flushPromises()
    expect(state.calls('get')).toHaveLength(3)
    state.handlers.list = () =>
      ok({ ...listData(), list: [{ ...rows[1], file_name: 'latest.mkv' }], total: 1 })
    old.resolve({ code: 400, message: 'secret obsolete failure' })
    await flushPromises()
    expect(state.calls('get')).toHaveLength(4)
    expect(state.adapter.mock.calls.at(-1)![0].params).toEqual({
      page: 2,
      page_size: 20,
      status: 3,
    })
    expect(state.wrapper.get('.queue-rows').text()).toContain('latest.mkv')
    expect(state.wrapper.get('.queue-rows').text()).not.toContain('pending.mkv')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('shares an existing refresh with mutation reload and reports the final error once', async () => {
    const state = setup()
    await flushPromises()
    const old = deferred<Reply>()
    state.handlers.list = () => old.promise
    await state.button('刷新').trigger('click')
    await state.button('清空等待').trigger('click')
    await flushPromises()
    expect(state.button('清空等待').attributes('disabled')).toBeDefined()
    state.handlers.list = failure('network')
    old.resolve(ok(listData()))
    await flushPromises()
    expect(state.calls('get')).toHaveLength(4)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      expect.stringContaining('操作已成功，但刷新队列快照失败'),
    )
    expect(ElMessage.success).toHaveBeenCalledOnce()
    expect(state.button('清空等待').attributes('disabled')).toBeUndefined()
  })

  it('ignores deferred mutation success after cached page deactivation', async () => {
    const state = setup(true)
    await flushPromises()
    expect(state.wrapper.get('.queue-rows').text()).toContain('pending.mkv')
    const response = deferred<Reply>()
    state.handlers.write = () => response.promise
    await state.button('清空等待').trigger('click')
    await flushPromises()
    state.visible.value = false
    await flushPromises()
    response.resolve(ok())
    await flushPromises()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(state.calls('get')).toHaveLength(2)
    state.visible.value = true
    await flushPromises()
    expect(state.calls('get')).toHaveLength(4)
    expect(state.button('清空等待').attributes('disabled')).toBeUndefined()
  })

  it('reports the current page refresh after a previous mutation context was deactivated', async () => {
    const state = setup(true)
    await flushPromises()
    const old = deferred<Reply>()
    state.handlers.list = () => old.promise
    await state.button('清空等待').trigger('click')
    await flushPromises()
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('队列已清空')
    state.visible.value = false
    await flushPromises()
    state.visible.value = true
    await flushPromises()
    state.handlers.list = failure('network')
    old.resolve(ok(listData()))
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '无法获取服务器响应，请检查网络连接、服务状态或访问配置',
    )
    expect(state.calls('get')).toHaveLength(5)
    expect(state.button('清空等待').attributes('disabled')).toBeUndefined()
  })

  it.each([
    ['全部暂停', 'stop', '已暂停所有任务'],
    ['全部恢复', 'start', '已恢复所有任务'],
    ['清空等待', 'clear-pending', '队列已清空'],
    ['清空完成/失败', 'clear-success-failed', '队列已清空'],
    [
      '重试失败',
      'retry-failed',
      queue === 'upload' ? '已开始重试所有失败任务' : '失败任务已重新加入队列',
    ],
  ])(
    '%s keeps its endpoint, success message and authoritative refresh',
    async (name, endpoint, message) => {
      const state = setup()
      if (name === '全部恢复') {
        state.handlers.list = () =>
          ok({ ...listData(), queue_status: { ...queueStatus, running: false } })
        state.handlers.status = () => ok(false)
      }
      await flushPromises()
      if (name === '全部恢复') {
        await state.button('刷新').trigger('click')
        await flushPromises()
      }
      const before = state.calls('get').length
      await state.button(name).trigger('click')
      await flushPromises()
      expect(state.calls('post')).toHaveLength(1)
      expect(state.calls('post')[0]![0].url).toBe(`/api/${queue}/queue/${endpoint}`)
      expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith(message)
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(state.calls('get')).toHaveLength(before + (endpoint === 'clear-pending' ? 2 : 1))
    },
  )

  it('retains the visible-page fallback and realtime snapshot triggers', async () => {
    const state = setup()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(state.calls('get')).toHaveLength(3)
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(10000)
    expect(state.calls('get')).toHaveLength(3)
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(state.calls('get')).toHaveLength(4)
    mocks.events.get(`${queue}_queue_changed`)!.event({ reason: 'created' })
    await flushPromises()
    expect(state.calls('get')).toHaveLength(5)
    mocks.events.get(`${queue}_queue_status_changed`)!.reconnect!()
    await flushPromises()
    expect(state.calls('get')).toHaveLength(6)
    state.wrapper.unmount()
    await vi.advanceTimersByTimeAsync(10000)
    expect(state.calls('get')).toHaveLength(6)
  })
})
