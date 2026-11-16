import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { CanceledError } from 'axios'
import {
  ElDialog,
  ElMessage,
  ElMessageBox,
  ElSelect,
  ElTabPane,
  type MessageBoxData,
} from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppCategoryStrategy from '@/views/scrape/AppCategoryStrategy.vue'
import type { APIResponse } from '@/api/types'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'

const movie = { id: 2, name: '电影规则', language_array: ['en'], genre_id_array: [18] }
const tvshow = { id: 3, name: '剧集规则', country_array: ['CN'], genre_id_array: [18] }
const success = (data: unknown = null): { status: number; data: APIResponse<unknown> } => ({
  status: 200,
  data: { status: 200, data },
})
const failureBody = {
  status: 500,
  data: {
    status: 500,
    message: '分类服务暂不可用',
    data: null,
    instance: '/request-errors/ComponentTestFailure0001',
    error_code: 'TEST_REQUEST_FAILED',
  },
}
const wrappers: VueWrapper[] = []
const createHTTP = () => ({
  get: vi.fn(async (url: string) => {
    if (url.endsWith('/movie-categories')) return success([{ ...movie }])
    if (url.endsWith('/tvshow-categories')) return success([{ ...tvshow }])
    if (url.endsWith('/language')) return success([{ status: 'en', name: '英语', data: null }])
    if (url.endsWith('/countries')) return success([{ status: 'CN', name: '中国', data: null }])
    return success([{ id: 18, name: '剧情' }])
  }),
  post: vi.fn().mockResolvedValue(success()),
  delete: vi.fn().mockResolvedValue(success()),
})
const mountPage = async (http = createHTTP()) => {
  const wrapper = mount(AppCategoryStrategy, {
    attachTo: document.body,
    global: { provide: { [httpKey]: http }, stubs: { PageHeader: true } },
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
const pane = (wrapper: VueWrapper, type: string) =>
  wrapper.findAllComponents(ElTabPane).find((item) => item.props('name') === type)!
const chooseTvshow = async (wrapper: VueWrapper) => {
  await wrapper
    .findAll('[role="tab"]')
    .find((tab) => tab.text() === '电视剧')!
    .trigger('click')
  await flushPromises()
}

beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('分类策略请求', () => {
  it('删除分类保留服务端反馈与警告，提交后刷新', async () => {
    const http = createHTTP()
    const message = '服务器确认测试分类删除'
    const warning = '测试分类删除仍需人工确认'
    http.delete.mockResolvedValue({
      status: 200,
      data: { status: 200, data: null, message, warnings: [warning] },
    })
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
    const wrapper = await mountPage(http)
    await clickButton(pane(wrapper, 'movie'), '删除')
    expect(ElMessage.warning).toHaveBeenCalledExactlyOnceWith(`${message}；${warning}`)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(http.get.mock.calls.filter(([url]) => url.endsWith('/movie-categories'))).toHaveLength(2)
  })

  it('已保存的分类使用服务端消息与警告，关闭表单并刷新', async () => {
    const http = createHTTP()
    const message = '服务器确认已保存分类测试配置'
    const warning = '测试分类配置应用需要重试'
    http.post.mockResolvedValue({
      status: 200,
      data: { status: 200, data: null, message, warnings: [warning] },
    })
    const wrapper = await mountPage(http)
    await clickButton(pane(wrapper, 'movie'), '编辑')
    const dialog = wrapper.findComponent(ElDialog)
    await clickButton(dialog, '确认')
    expect(dialog.props('modelValue')).toBe(false)
    expect(ElMessage.warning).toHaveBeenCalledWith(`${message}；${warning}`)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(http.get.mock.calls.filter(([url]) => url.endsWith('/movie-categories'))).toHaveLength(2)
  })

  it.each([
    ['/language', '加载语言列表失败'],
    ['/countries', '加载国家列表失败'],
    ['/movie-genre', '加载电影类别失败'],
    ['/tvshow-genre', '加载电视剧类别失败'],
    ['/movie-categories', '加载分类列表失败'],
  ])('字典或列表 %s 的业务失败展示服务端原因', async (path, message) => {
    const http = createHTTP()
    const get = http.get.getMockImplementation()!
    http.get.mockImplementation((url) =>
      url.endsWith(path) ? Promise.resolve(failureBody) : get(url),
    )
    await mountPage(http)
    expect(http.get).toHaveBeenCalledTimes(5)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('分类服务暂不可用')
    expect(console.error).toHaveBeenCalledExactlyOnceWith(
      message,
      expect.objectContaining({ status: 500 }),
    )
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it.each(['movie', 'tvshow'])('%s 新增业务失败保留输入、选择与弹窗', async (type) => {
    const http = createHTTP()
    http.post.mockResolvedValue(failureBody)
    const wrapper = await mountPage(http)
    if (type === 'tvshow') await chooseTvshow(wrapper)
    await clickButton(pane(wrapper, type), '添加分类')
    const dialog = wrapper.findComponent(ElDialog)
    await dialog.get('input').setValue('keep category')
    const selects = dialog.findAllComponents(ElSelect)
    const region = type === 'movie' ? 'en' : 'CN'
    selects[0].vm.$emit('update:modelValue', [region])
    selects[1].vm.$emit('update:modelValue', [18])
    await clickButton(dialog, '确认')
    expect(http.post).toHaveBeenCalledExactlyOnceWith(`/api/scrape/${type}-categories`, {
      id: 0,
      name: 'keep category',
      genre_id_array: [18],
      [type === 'movie' ? 'language_array' : 'country_array']: [region],
    })
    expect(dialog.props('modelValue')).toBe(true)
    expect(dialog.get<HTMLInputElement>('input').element.value).toBe('keep category')
    expect(selects[0].props('modelValue')).toEqual([region])
    expect(selects[1].props('modelValue')).toEqual([18])
    expect(http.get).toHaveBeenCalledTimes(type === 'movie' ? 5 : 6)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('分类服务暂不可用')
  })

  it.each(['movie', 'tvshow'])('%s 编辑安全校验失败保留记录 ID 和原始字段', async (type) => {
    const http = createHTTP()
    http.post.mockRejectedValue(
      new HttpResponseError({
        status: 403,
        data: {
          status: 403,
          message: 'secret',
          data: null,
          instance: '/request-errors/ComponentTestFailure0001',
          error_code: 'CSRF_TOKEN_INVALID',
        },
      }),
    )
    const wrapper = await mountPage(http)
    if (type === 'tvshow') await chooseTvshow(wrapper)
    await clickButton(pane(wrapper, type), '编辑')
    const dialog = wrapper.findComponent(ElDialog)
    await dialog.get('input').setValue('edited name')
    await clickButton(dialog, '确认')
    expect(http.post).toHaveBeenCalledExactlyOnceWith(`/api/scrape/${type}-categories`, {
      ...(type === 'movie' ? movie : tvshow),
      name: 'edited name',
    })
    expect(dialog.props('modelValue')).toBe(true)
    expect(dialog.get<HTMLInputElement>('input').element.value).toBe('edited name')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
    )
  })

  it('字段校验失败不发请求或记录内部校验对象', async () => {
    const http = createHTTP()
    const wrapper = await mountPage(http)
    await clickButton(pane(wrapper, 'movie'), '添加分类')
    const dialog = wrapper.findComponent(ElDialog)
    await clickButton(dialog, '确认')
    expect(http.post).not.toHaveBeenCalled()
    await vi.waitFor(() => expect(dialog.text()).toContain('请输入分类名称'))
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('合法空数据保存成功后刷新失败仍关闭表单并准确提示刷新失败', async () => {
    const http = createHTTP()
    const wrapper = await mountPage(http)
    await clickButton(pane(wrapper, 'movie'), '编辑')
    http.get.mockResolvedValue(failureBody)
    const dialog = wrapper.findComponent(ElDialog)
    await clickButton(dialog, '确认')
    expect(dialog.props('modelValue')).toBe(false)
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('编辑成功')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '操作已成功，但刷新分类列表失败：分类服务暂不可用',
    )
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('电影规则')
  })

  it('刷新失败且无具体原因时不重复拼接加载失败文案', async () => {
    const http = createHTTP()
    const wrapper = await mountPage(http)
    await clickButton(pane(wrapper, 'movie'), '编辑')
    http.get.mockRejectedValue(new Error('secret'))
    await clickButton(wrapper.findComponent(ElDialog), '确认')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('操作已成功，但刷新分类列表失败')
  })

  it.each(['movie', 'tvshow'])('%s 删除失败保留列表且不刷新', async (type) => {
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
    const http = createHTTP()
    http.delete.mockResolvedValue(failureBody)
    const wrapper = await mountPage(http)
    if (type === 'tvshow') await chooseTvshow(wrapper)
    await clickButton(pane(wrapper, type), '删除')
    expect(http.delete).toHaveBeenCalledExactlyOnceWith(
      `/api/scrape/${type}-categories/${type === 'movie' ? 2 : 3}`,
    )
    expect(http.get).toHaveBeenCalledTimes(type === 'movie' ? 5 : 6)
    expect(pane(wrapper, type).text()).toContain(type === 'movie' ? '电影规则' : '剧集规则')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('分类服务暂不可用')
  })

  it.each(['cancel', 'handled'])('保存请求 %s 保持静默和表单', async (kind) => {
    const error =
      kind === 'cancel' ? new CanceledError() : new HttpResponseError({ status: 401, data: null })
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const http = createHTTP()
    http.post.mockRejectedValue(error)
    const wrapper = await mountPage(http)
    await clickButton(pane(wrapper, 'movie'), '编辑')
    const dialog = wrapper.findComponent(ElDialog)
    await clickButton(dialog, '确认')
    expect(dialog.props('modelValue')).toBe(true)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it.each(['cancel', 'close'])('删除确认 %s 静默结束', async (action) => {
    vi.spyOn(ElMessageBox, 'confirm').mockRejectedValue(action)
    const http = createHTTP()
    const wrapper = await mountPage(http)
    await clickButton(pane(wrapper, 'movie'), '删除')
    expect(http.delete).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
})
