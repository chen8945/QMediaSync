// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { CanceledError } from 'axios'
import {
  ElDialog,
  ElFormItem,
  ElMessage,
  ElMessageBox,
  ElSelect,
  type MessageBoxData,
} from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, KeepAlive, ref } from 'vue'
import AppScrapePathForm from '@/components/AppScrapePathForm.vue'
import AppScrapePathes from '@/components/AppScrapePathes.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { ScrapePath } from '@/api/scrapePaths'
import { createDeferred } from '../support/deferred'

const { route, router, realtimeListeners } = vi.hoisted(() => ({
  route: { params: { id: '12' } },
  router: { push: vi.fn(), replace: vi.fn(), back: vi.fn() },
  realtimeListeners: new Map<string, { callback: () => void; onReconnect?: () => void }>(),
}))
vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => router,
}))
vi.mock('@/composables/useRealtimeEvents', () => ({
  useRealtimeEvent: vi.fn((event: string, callback: () => void, onReconnect?: () => void) => {
    realtimeListeners.set(event, { callback, onReconnect })
  }),
}))
const wrappers: VueWrapper[] = []
const directory: ScrapePath = {
  id: 12,
  source_type: 'local',
  account_id: 0,
  media_type: 'movie',
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
  enable_cron: false,
  cron_expression: '0 * * * *',
  cron_description: '每小时',
  enable_fanart_tv: false,
  max_threads: 5,
  is_running: 0,
}
const success = (data: unknown = null) => ({ status: 200, data: { code: 200, data } })
const failure = (status: number, message = 'internal secret', error_code?: string) =>
  new HttpResponseError({ status, data: { code: 500, message, error_code } })

const createHTTP = (row: ScrapePath = directory) => ({
  get: vi.fn<
    (
      url: string,
      config?: { params?: Record<string, unknown> },
    ) => Promise<ReturnType<typeof success>>
  >(async (url) => {
    if (url.endsWith('/scrape/pathes')) return success([{ ...row }])
    if (url.endsWith('/scrape/pathes/12')) return success({ ...row })
    if (url.endsWith('/scrape/sync-pathes')) return success([8])
    if (url.endsWith('/sync/path-list'))
      return success({ list: [{ id: 8, source_type: '115', remote_path: '/strm' }] })
    return success([])
  }),
  post: vi.fn().mockResolvedValue(success()),
  delete: vi.fn().mockResolvedValue(success()),
})

const mountPage = async (
  component: typeof AppScrapePathForm | typeof AppScrapePathes,
  http = createHTTP(),
) => {
  const wrapper = mount(component, {
    attachTo: document.body,
    global: {
      provide: { [httpKey]: http },
      stubs: {
        PageHeader: true,
        PageStats: true,
        DirectorySelector: true,
        MetadataExtInput: true,
        RouterLink: true,
      },
    },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

const clickButton = async (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find((button) => button.text() === label)
  expect(button, label).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}

beforeEach(() => {
  route.params.id = '12'
  realtimeListeners.clear()
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe.each([
  ['表单', AppScrapePathForm],
  ['列表', AppScrapePathes],
] as const)('刮削目录%s账号加载', (_label, component) => {
  it('账号来源校验失败显示配置说明并保留目录内容', async () => {
    if (component === AppScrapePathForm) route.params.id = ''
    const http = createHTTP({ ...directory, source_type: '115' })
    const get = http.get.getMockImplementation()!
    http.get.mockImplementation((url) =>
      url.endsWith('/account/list')
        ? Promise.reject(failure(403, 'secret token', 'REQUEST_ORIGIN_INVALID'))
        : get(url),
    )
    const wrapper = await mountPage(component, http)
    expect(wrapper.text()).toContain(component === AppScrapePathForm ? '来源类型' : '/media')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    )
    expect(console.error).toHaveBeenCalledExactlyOnceWith('加载账号列表失败：', {
      status: 403,
      errorCode: 'REQUEST_ORIGIN_INVALID',
    })
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('取消账号查询不显示失败', async () => {
    if (component === AppScrapePathForm) route.params.id = ''
    const http = createHTTP({ ...directory, source_type: '115' })
    const get = http.get.getMockImplementation()!
    http.get.mockImplementation((url) =>
      url.endsWith('/account/list') ? Promise.reject(new CanceledError()) : get(url),
    )
    await mountPage(component, http)
    expect(http.get.mock.calls.some(([url]) => url.endsWith('/account/list'))).toBe(true)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
})

describe('刮削目录表单', () => {
  it('详情读取失败时禁止保存，重试成功后才能编辑原目录', async () => {
    const http = createHTTP()
    http.get.mockRejectedValueOnce(failure(403, '', 'REQUEST_ORIGIN_INVALID'))
    const wrapper = await mountPage(AppScrapePathForm, http)
    const save = wrapper.findAll('button').find((button) => button.text() === '保存修改')!
    expect(save.attributes('disabled')).toBeDefined()
    await save.trigger('click')
    expect(http.post).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('访问地址校验失败')
    expect(ElMessage.error).not.toHaveBeenCalled()

    await clickButton(wrapper, '重新加载目录')
    expect(save.attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).not.toContain('访问地址校验失败')
    await save.trigger('click')
    await flushPromises()
    expect(http.post).toHaveBeenCalledWith(
      '/api/scrape/pathes',
      expect.objectContaining({ id: 12, source_path: '/media' }),
    )
  })

  it('详情读取在途时不能提交默认表单', async () => {
    const pending = createDeferred<ReturnType<typeof success>>()
    const http = createHTTP()
    http.get.mockReturnValueOnce(pending.promise)
    const wrapper = await mountPage(AppScrapePathForm, http)
    const save = wrapper.findAll('button').find((button) => button.text() === '保存修改')!
    expect(save.attributes('disabled')).toBeDefined()
    pending.resolve(success(directory))
    await flushPromises()
    expect(save.attributes('disabled')).toBeUndefined()
    expect(http.post).not.toHaveBeenCalled()
  })

  it.each([
    [failure(200, 'source_path：不能为空'), '来源目录不能为空'],
    [failure(200, '目录已被占用'), '目录已被占用'],
    [
      failure(403, '请求来源无效'),
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    ],
    [
      failure(403, '', 'CSRF_TOKEN_INVALID'),
      '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
    ],
  ])('保存失败保留输入且不跳转：%s', async (error, message) => {
    const http = createHTTP()
    http.post.mockRejectedValue(error)
    const wrapper = await mountPage(AppScrapePathForm, http)
    const item = wrapper
      .findAllComponents(ElFormItem)
      .find((item) => item.props('prop') === 'file_name_template')!
    await item.get('input').setValue('keep {title}')
    await clickButton(wrapper, '保存修改')

    expect(http.post).toHaveBeenCalledTimes(1)
    expect(http.post).toHaveBeenCalledWith(
      '/api/scrape/pathes',
      expect.objectContaining({ id: 12, file_name_template: 'keep {title}', max_threads: 5 }),
    )
    expect(http.post.mock.calls[0]![1]).not.toHaveProperty('source_type')
    expect(item.get<HTMLInputElement>('input').element.value).toBe('keep {title}')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(message)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(router.replace).not.toHaveBeenCalled()
    expect(router.back).not.toHaveBeenCalled()
    expect(console.error).toHaveBeenCalledWith(
      '编辑刮削目录失败',
      expect.objectContaining({ status: error.response.status }),
    )
  })

  it('HTTP 200 业务失败不执行保存后的跳转', async () => {
    const http = createHTTP()
    http.post.mockResolvedValue({
      status: 200,
      data: { code: 500, message: '目录已被占用', data: null },
    })
    const wrapper = await mountPage(AppScrapePathForm, http)
    await clickButton(wrapper, '保存修改')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('目录已被占用')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(router.replace).not.toHaveBeenCalled()
  })

  it('合法空响应保存成功后返回列表', async () => {
    const wrapper = await mountPage(AppScrapePathForm)
    await clickButton(wrapper, '保存修改')
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('编辑刮削目录成功')
    expect(router.replace).toHaveBeenCalledExactlyOnceWith({ name: 'scrape-pathes' })
  })

  it('Cron 验证保留修剪规则，安全校验失败不覆盖已输入表达式或描述', async () => {
    const http = createHTTP({ ...directory, enable_cron: true })
    http.post.mockRejectedValue(failure(403, '', 'CSRF_TOKEN_INVALID'))
    const wrapper = await mountPage(AppScrapePathForm, http)
    const item = wrapper
      .findAllComponents(ElFormItem)
      .find((item) => item.props('prop') === 'cron_expression')!
    await item.get('input').setValue(' 0 3 * * * ')
    await flushPromises()

    expect(http.post).toHaveBeenCalledExactlyOnceWith('/api/cron/validate', {
      cron_expression: '0 3 * * *',
    })
    expect(item.get<HTMLInputElement>('input').element.value).toBe(' 0 3 * * * ')
    expect(wrapper.get('.cron-description').text()).toBe('每小时')
    expect(ElMessage.warning).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
    )
  })

  it.each(['cancel', 'handled'])('保存请求 %s 静默结束且不跳转', async (kind) => {
    const error = kind === 'cancel' ? new CanceledError() : failure(401)
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const http = createHTTP()
    http.post.mockRejectedValue(error)
    const wrapper = await mountPage(AppScrapePathForm, http)
    await clickButton(wrapper, '保存修改')
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
    expect(router.replace).not.toHaveBeenCalled()
  })
})

describe('刮削目录实时刷新', () => {
  const mountKeptPage = async (http: ReturnType<typeof createHTTP>) => {
    const visible = ref(true)
    const wrapper = mount(
      defineComponent({
        setup: () => () =>
          h(KeepAlive, null, { default: () => (visible.value ? h(AppScrapePathes) : null) }),
      }),
      {
        global: {
          provide: { [httpKey]: http },
          stubs: { PageHeader: true, PageStats: true, RouterLink: true },
        },
      },
    )
    wrappers.push(wrapper)
    await flushPromises()
    return { wrapper, visible }
  }
  const emitItems = (count = 1) => {
    const listener = realtimeListeners.get('scraper_item_complete')
    expect(listener).toBeDefined()
    for (let index = 0; index < count; index += 1) listener!.callback()
  }
  const reconnect = () => {
    const callback = realtimeListeners.get('scraper_task_start')?.onReconnect
    expect(callback).toBeDefined()
    callback!()
  }
  const pathReadCount = (http: ReturnType<typeof createHTTP>) =>
    http.get.mock.calls.filter(([url]) => url.endsWith('/scrape/pathes')).length

  it('密集事件只保留一个待刷新请求，重连的全量读取优先于状态刷新', async () => {
    const first = createDeferred<ReturnType<typeof success>>()
    const second = createDeferred<ReturnType<typeof success>>()
    const http = createHTTP()
    const wrapper = await mountPage(AppScrapePathes, http)
    http.get.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    emitItems(20)
    reconnect()
    realtimeListeners.get('scraper_task_complete')!.callback()
    realtimeListeners.get('scraper_task_start')!.callback()
    expect(pathReadCount(http)).toBe(2)
    first.resolve(success([{ ...directory, is_running: 2 }]))
    await flushPromises()
    expect(pathReadCount(http)).toBe(3)
    expect(wrapper.text()).toContain('停止')

    second.resolve(success([{ ...directory, source_path: '/latest', is_running: 0 }]))
    await flushPromises()
    expect(wrapper.text()).toContain('/latest')
    expect(wrapper.text()).not.toContain('/media')
    expect(pathReadCount(http)).toBe(3)
  })

  it('首次快照在途时也合并事件，快照成功后补一次状态读取', async () => {
    const first = createDeferred<ReturnType<typeof success>>()
    const http = createHTTP()
    http.get.mockReturnValueOnce(first.promise)
    const wrapper = await mountPage(AppScrapePathes, http)
    emitItems(20)
    expect(pathReadCount(http)).toBe(1)
    first.resolve(success([{ ...directory, is_running: 2 }]))
    await flushPromises()
    expect(pathReadCount(http)).toBe(2)
    expect(wrapper.text()).toContain('/media')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('连续状态和快照读取失败只提示一次并保留目录，恢复后新故障再次提示', async () => {
    const http = createHTTP()
    const wrapper = await mountPage(AppScrapePathes, http)
    const get = http.get.getMockImplementation()!
    http.get.mockRejectedValue(failure(403, '', 'CSRF_TOKEN_INVALID'))
    emitItems(20)
    await flushPromises()
    reconnect()
    await flushPromises()
    emitItems()
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledTimes(1)
    expect(console.error).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('/media')

    http.get.mockImplementation(get)
    emitItems()
    await flushPromises()
    http.get.mockRejectedValue(failure(403, '', 'REQUEST_ORIGIN_INVALID'))
    emitItems()
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledTimes(2)
    expect(ElMessage.error).toHaveBeenLastCalledWith(
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    )
    expect(wrapper.text()).toContain('/media')
  })

  it.each(['cancelled', 'handled'])('后台读取 %s 静默，不占用下一次故障提示', async (kind) => {
    const http = createHTTP()
    const wrapper = await mountPage(AppScrapePathes, http)
    const error = kind === 'cancelled' ? new CanceledError() : failure(401)
    if (kind === 'handled') markAuthInvalidationHandled(error)
    http.get.mockRejectedValueOnce(error)
    emitItems()
    await flushPromises()
    expect(wrapper.text()).toContain('/media')
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()

    http.get.mockRejectedValueOnce(failure(200, '目录状态读取失败'))
    emitItems()
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('目录状态读取失败')
  })

  it.each(['success', 'failure'])(
    '卸载后旧读取 %s 不更新页面、不提示或补发刷新',
    async (result) => {
      const pending = createDeferred<ReturnType<typeof success>>()
      const http = createHTTP()
      const wrapper = await mountPage(AppScrapePathes, http)
      http.get.mockReturnValueOnce(pending.promise)
      emitItems(20)
      const readsBeforeUnmount = pathReadCount(http)
      wrapper.unmount()
      if (result === 'success') pending.resolve(success([{ ...directory, is_running: 2 }]))
      else pending.resolve({ status: 200, data: { code: 500, data: null } })
      await flushPromises()
      emitItems()
      reconnect()
      await flushPromises()
      expect(pathReadCount(http)).toBe(readsBeforeUnmount)
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(console.error).not.toHaveBeenCalled()
    },
  )

  it.each(['success', 'failure'])(
    '停用期间忽略旧读取 %s 和待刷新，重新激活时读取新快照',
    async (result) => {
      const pending = createDeferred<ReturnType<typeof success>>()
      const http = createHTTP()
      const { wrapper, visible } = await mountKeptPage(http)
      const page = wrapper.findComponent(AppScrapePathes)
      http.get.mockReturnValueOnce(pending.promise)
      emitItems(20)
      const readsBeforeDeactivate = pathReadCount(http)
      visible.value = false
      await flushPromises()
      if (result === 'success') pending.resolve(success([{ ...directory, is_running: 2 }]))
      else pending.resolve({ status: 200, data: { code: 500, data: null } })
      await flushPromises()
      expect(pathReadCount(http)).toBe(readsBeforeDeactivate)
      expect(page.text()).toContain('启动')
      expect(page.text()).not.toContain('停止')
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(console.error).not.toHaveBeenCalled()

      http.get.mockResolvedValueOnce(success([{ ...directory, source_path: '/reactivated' }]))
      visible.value = true
      await flushPromises()
      expect(pathReadCount(http)).toBe(readsBeforeDeactivate + 1)
      expect(wrapper.text()).toContain('/reactivated')
    },
  )

  it('重新激活时等待旧在途查询结束，再读取新快照，期间旧错误保持静默', async () => {
    const stale = createDeferred<ReturnType<typeof success>>()
    const current = createDeferred<ReturnType<typeof success>>()
    const http = createHTTP()
    const { wrapper, visible } = await mountKeptPage(http)
    http.get.mockReturnValueOnce(stale.promise).mockReturnValueOnce(current.promise)
    emitItems(20)
    visible.value = false
    await flushPromises()
    visible.value = true
    await flushPromises()
    emitItems(20)
    expect(pathReadCount(http)).toBe(2)

    stale.resolve({ status: 200, data: { code: 500, data: null } })
    await flushPromises()
    expect(pathReadCount(http)).toBe(3)
    expect(wrapper.text()).toContain('/media')
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()

    current.resolve(success([{ ...directory, source_path: '/current' }]))
    await flushPromises()
    expect(pathReadCount(http)).toBe(3)
    expect(wrapper.text()).toContain('/current')
    expect(wrapper.text()).not.toContain('/media')
  })
})

describe('刮削目录列表', () => {
  it.each([
    [0, '启动', 'start'],
    [2, '停止', 'stop'],
  ] as const)('状态 %s 操作的业务失败不误报成功', async (is_running, label, action) => {
    const http = createHTTP({ ...directory, is_running })
    http.post.mockResolvedValue({
      status: 200,
      data: { code: 500, message: '任务状态冲突', data: null },
    })
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, label)
    expect(http.post).toHaveBeenCalledExactlyOnceWith(`/api/scrape/pathes/${action}`, { id: 12 })
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('任务状态冲突')
    expect(
      wrapper
        .findAll('button')
        .find((button) => button.text() === label)
        ?.classes(),
    ).not.toContain('is-loading')
  })

  it('删除失败保留目录且不刷新列表', async () => {
    // 当前 Element Plus 将返回类型声明成交叉类型，confirm 运行时仍返回动作字符串。
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
    const http = createHTTP()
    http.delete.mockResolvedValue({ status: 200, data: { code: 500, data: null } })
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, '删除')
    expect(wrapper.text()).toContain('/media')
    expect(http.get).toHaveBeenCalledTimes(2)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('删除刮削目录失败')
  })

  it('定时任务切换失败回退开关', async () => {
    const http = createHTTP()
    http.post.mockRejectedValue(failure(403, '', 'CSRF_TOKEN_INVALID'))
    const wrapper = await mountPage(AppScrapePathes, http)
    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
    )
  })

  it('关联保存失败保持弹窗和已选目录', async () => {
    const http = createHTTP({ ...directory, source_type: '115' })
    http.post.mockResolvedValue({ status: 200, data: { code: 500, data: null } })
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, '关联')
    const dialog = wrapper.findComponent(ElDialog)
    await clickButton(dialog, '确定')
    expect(http.post).toHaveBeenCalledExactlyOnceWith('/api/scrape/sync-pathes', {
      scrape_path_id: 12,
      sync_path_ids: [8],
    })
    expect(dialog.props('modelValue')).toBe(true)
    expect(wrapper.findComponent(ElSelect).props('modelValue')).toEqual([8])
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('关联同步目录失败')
  })

  it('关联候选目录查询保留来源参数并按公共策略提示失败', async () => {
    const http = createHTTP({ ...directory, source_type: '115' })
    const get = http.get.getMockImplementation()!
    http.get.mockImplementation((url) =>
      url.endsWith('/sync/path-list')
        ? Promise.reject(failure(403, 'internal secret', 'REQUEST_ORIGIN_INVALID'))
        : get(url),
    )
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, '关联')

    expect(http.get).toHaveBeenCalledWith('/api/sync/path-list', {
      params: { page: 1, page_size: 9999, source_type: '115' },
    })
    expect(wrapper.findComponent(ElDialog).props('modelValue')).toBe(true)
    expect(wrapper.findComponent(ElSelect).props('modelValue')).toEqual([])
    expect(wrapper.findComponent(ElDialog).text()).toContain(
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    )
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it.each(['/sync/path-list', '/scrape/sync-pathes'])(
    '%s 仍在加载时禁止提交未知关联',
    async (endpoint) => {
      const pending = createDeferred<ReturnType<typeof success>>()
      const http = createHTTP({ ...directory, source_type: '115' })
      const get = http.get.getMockImplementation()!
      http.get.mockImplementation((url, config) =>
        url.endsWith(endpoint) ? pending.promise : get(url, config),
      )
      const wrapper = await mountPage(AppScrapePathes, http)
      await clickButton(wrapper, '关联')
      const dialog = wrapper.findComponent(ElDialog)
      const confirm = dialog.findAll('button').find((button) => button.text() === '确定')!
      expect(confirm.attributes('disabled')).toBeDefined()
      expect(dialog.findComponent(ElSelect).props('disabled')).toBe(true)
      await confirm.trigger('click')
      expect(http.post).not.toHaveBeenCalled()
      pending.resolve(endpoint === '/sync/path-list' ? success({ list: [] }) : success([8]))
      await flushPromises()
      expect(confirm.attributes('disabled')).toBeUndefined()
    },
  )

  it.each(['/sync/path-list', '/scrape/sync-pathes'])(
    '%s 加载失败禁止保存，重新加载成功后允许明确清空关联',
    async (endpoint) => {
      let failed = true
      const http = createHTTP({ ...directory, source_type: '115' })
      const get = http.get.getMockImplementation()!
      http.get.mockImplementation((url, config) =>
        failed && url.endsWith(endpoint)
          ? Promise.reject(failure(403, '', 'CSRF_TOKEN_INVALID'))
          : get(url, config),
      )
      const wrapper = await mountPage(AppScrapePathes, http)
      await clickButton(wrapper, '关联')
      const dialog = wrapper.findComponent(ElDialog)
      expect(dialog.text()).toContain('请求安全校验失败')
      const confirm = dialog.findAll('button').find((button) => button.text() === '确定')!
      expect(confirm.attributes('disabled')).toBeDefined()
      await confirm.trigger('click')
      expect(http.post).not.toHaveBeenCalled()
      expect(ElMessage.error).not.toHaveBeenCalled()

      failed = false
      await clickButton(dialog, '重新加载')
      expect(confirm.attributes('disabled')).toBeUndefined()
      expect(dialog.text()).not.toContain('请求安全校验失败')
      expect(dialog.findComponent(ElSelect).props('modelValue')).toEqual([8])
      dialog.findComponent(ElSelect).vm.$emit('update:modelValue', [])
      await flushPromises()
      await clickButton(dialog, '确定')
      expect(http.post).toHaveBeenCalledExactlyOnceWith('/api/scrape/sync-pathes', {
        scrape_path_id: 12,
        sync_path_ids: [],
      })
    },
  )

  it.each(['success', 'failure'])(
    '关闭后打开另一目录，旧关联查询 %s 不能覆盖新选择或提示',
    async (result) => {
      const pending = createDeferred<ReturnType<typeof success>>()
      const http = createHTTP({ ...directory, source_type: '115' })
      const get = http.get.getMockImplementation()!
      http.get.mockImplementation((url, config) => {
        if (url.endsWith('/scrape/pathes'))
          return Promise.resolve(
            success([
              { ...directory, source_type: '115' },
              { ...directory, id: 13, source_type: '115' },
            ]),
          )
        if (url.endsWith('/scrape/sync-pathes'))
          return config?.params?.scrape_path_id === 12
            ? pending.promise
            : Promise.resolve(success([9]))
        return get(url, config)
      })
      const wrapper = await mountPage(AppScrapePathes, http)
      const openButtons = wrapper.findAll('button').filter((button) => button.text() === '关联')
      await openButtons[0]!.trigger('click')
      await flushPromises()
      const dialog = wrapper.findComponent(ElDialog)
      await clickButton(dialog, '取消')
      await openButtons[1]!.trigger('click')
      await flushPromises()
      expect(dialog.findComponent(ElSelect).props('modelValue')).toEqual([9])
      if (result === 'success') pending.resolve(success([8]))
      else
        pending.resolve({
          status: 403,
          data: { code: 500, data: null, error_code: 'CSRF_TOKEN_INVALID' },
        } as ReturnType<typeof success>)
      await flushPromises()
      expect(dialog.findComponent(ElSelect).props('modelValue')).toEqual([9])
      expect(dialog.text()).not.toContain('请求安全校验失败')
      expect(ElMessage.error).not.toHaveBeenCalled()
      await clickButton(dialog, '确定')
      expect(http.post).toHaveBeenCalledExactlyOnceWith('/api/scrape/sync-pathes', {
        scrape_path_id: 13,
        sync_path_ids: [9],
      })
    },
  )

  it('关闭后旧保存成功不能关闭重开的关联窗口', async () => {
    const pending = createDeferred<ReturnType<typeof success>>()
    const http = createHTTP({ ...directory, source_type: '115' })
    http.post.mockReturnValueOnce(pending.promise)
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, '关联')
    const dialog = wrapper.findComponent(ElDialog)
    await clickButton(dialog, '确定')
    await clickButton(dialog, '取消')
    await clickButton(wrapper, '关联')
    pending.resolve(success())
    await flushPromises()
    expect(dialog.props('modelValue')).toBe(true)
    expect(dialog.findComponent(ElSelect).props('modelValue')).toEqual([8])
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('旧候选加载完成不能解除新窗口的加载门禁或覆盖候选', async () => {
    const first = createDeferred<ReturnType<typeof success>>()
    const second = createDeferred<ReturnType<typeof success>>()
    let calls = 0
    const http = createHTTP({ ...directory, source_type: '115' })
    const get = http.get.getMockImplementation()!
    http.get.mockImplementation((url, config) => {
      if (url.endsWith('/sync/path-list')) return ++calls === 1 ? first.promise : second.promise
      return get(url, config)
    })
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, '关联')
    const dialog = wrapper.findComponent(ElDialog)
    await clickButton(dialog, '取消')
    await clickButton(wrapper, '关联')
    first.resolve(success({ list: [{ id: 7, source_type: '115', remote_path: '/old' }] }))
    await flushPromises()
    const confirm = dialog.findAll('button').find((button) => button.text() === '确定')!
    expect(confirm.attributes('disabled')).toBeDefined()
    expect(dialog.findComponent(ElSelect).props('loading')).toBe(true)
    expect(dialog.findComponent(ElSelect).text()).not.toContain('/old')

    second.resolve(success({ list: [{ id: 8, source_type: '115', remote_path: '/current' }] }))
    await flushPromises()
    expect(confirm.attributes('disabled')).toBeUndefined()
    expect(dialog.findComponent(ElSelect).text()).toContain('/current')
    expect(dialog.findComponent(ElSelect).text()).not.toContain('/old')
  })

  it.each(['cancelled', 'handled'])('关联读取 %s 静默且不能把未知绑定保存为空', async (kind) => {
    const error = kind === 'cancelled' ? new CanceledError() : failure(401)
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const http = createHTTP({ ...directory, source_type: '115' })
    const get = http.get.getMockImplementation()!
    http.get.mockImplementation((url, config) =>
      url.endsWith('/scrape/sync-pathes') ? Promise.reject(error) : get(url, config),
    )
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, '关联')
    const dialog = wrapper.findComponent(ElDialog)
    const confirm = dialog.findAll('button').find((button) => button.text() === '确定')!
    expect(confirm.attributes('disabled')).toBeDefined()
    await confirm.trigger('click')
    expect(http.post).not.toHaveBeenCalled()
    expect(dialog.find('.el-alert--error').exists()).toBe(false)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('卸载后迟到的关联读取失败不显示错误', async () => {
    const pending = createDeferred<ReturnType<typeof success>>()
    const http = createHTTP({ ...directory, source_type: '115' })
    const get = http.get.getMockImplementation()!
    http.get.mockImplementation((url, config) =>
      url.endsWith('/scrape/sync-pathes') ? pending.promise : get(url, config),
    )
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, '关联')
    wrapper.unmount()
    pending.resolve({ status: 200, data: { code: 500, data: null } })
    await flushPromises()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
})
