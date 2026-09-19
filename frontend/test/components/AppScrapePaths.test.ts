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
import AppScrapePathForm from '@/components/AppScrapePathForm.vue'
import AppScrapePathes from '@/components/AppScrapePathes.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { ScrapePath } from '@/api/scrapePaths'

const { route, router } = vi.hoisted(() => ({
  route: { params: { id: '12' } },
  router: { push: vi.fn(), replace: vi.fn(), back: vi.fn() },
}))
vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => router,
}))
vi.mock('@/composables/useRealtimeEvents', () => ({ useRealtimeEvent: vi.fn() }))
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
  get: vi.fn(async (url: string) => {
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
  vi.clearAllMocks()
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
  it.each([
    [failure(200, 'source_path：不能为空'), '来源目录不能为空'],
    [failure(200), '编辑刮削目录失败'],
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
      data: { code: 500, message: 'SQL secret', data: null },
    })
    const wrapper = await mountPage(AppScrapePathForm, http)
    await clickButton(wrapper, '保存修改')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('编辑刮削目录失败')
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

describe('刮削目录列表', () => {
  it.each([
    [0, '启动', 'start'],
    [2, '停止', 'stop'],
  ] as const)('状态 %s 操作的业务失败不误报成功', async (is_running, label, action) => {
    const http = createHTTP({ ...directory, is_running })
    http.post.mockResolvedValue({
      status: 200,
      data: { code: 500, message: 'internal secret', data: null },
    })
    const wrapper = await mountPage(AppScrapePathes, http)
    await clickButton(wrapper, label)
    expect(http.post).toHaveBeenCalledExactlyOnceWith(`/api/scrape/pathes/${action}`, { id: 12 })
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(`${label}刮削任务失败`)
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
    expect(wrapper.findComponent(ElSelect).props('modelValue')).toEqual([8])
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    )
    expect(ElMessage.success).not.toHaveBeenCalled()
  })
})
