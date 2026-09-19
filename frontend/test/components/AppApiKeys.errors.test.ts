import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { AxiosError, CanceledError, type InternalAxiosRequestConfig } from 'axios'
import { ElDialog, ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppApiKeys from '@/components/AppApiKeys.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'

const row = { id: 4, name: 'CI', key_prefix: 'qms_ci', is_active: true, created_at: 1 }
const success = (data: unknown = null) => ({ status: 200, data: { code: 200, data } })
const failure = (status = 200, error_code?: string) =>
  new HttpResponseError({
    status,
    data: { code: 500, message: 'API Key 操作失败，请稍后重试', error_code },
  })
const wrappers: VueWrapper[] = []
const createHTTP = () => ({
  get: vi.fn().mockResolvedValue(success([{ ...row }])),
  post: vi.fn().mockResolvedValue(success({ ...row, key: 'qms_created_once' })),
  put: vi.fn().mockResolvedValue(success()),
  delete: vi.fn().mockResolvedValue(success()),
})
const mountPage = async (http = createHTTP()) => {
  const wrapper = mount(AppApiKeys, {
    attachTo: document.body,
    global: {
      provide: { [httpKey]: http },
      stubs: { PageHeader: { template: '<div><slot name="actions" /></div>' } },
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
const openCreate = async (wrapper: VueWrapper) => {
  await clickButton(wrapper, '生成 API Key')
  const dialog = wrapper.findAllComponents(ElDialog)[0]
  await dialog.get('input').setValue('  kept name  ')
  return dialog
}

beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('API Key 请求失败行为', () => {
  it.each([
    [failure(), 'API Key 操作失败，请稍后重试'],
    [
      failure(403, 'REQUEST_ORIGIN_INVALID'),
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    ],
    [
      failure(403, 'CSRF_TOKEN_INVALID'),
      '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
    ],
    [
      new AxiosError('timeout qms_secret_key', 'ETIMEDOUT', {
        method: 'post',
        url: '/api/api-keys',
      } as InternalAxiosRequestConfig),
      '请求超时，操作结果尚未确认。请先检查操作是否已生效，避免重复提交',
    ],
  ])('创建失败保留弹窗和原始输入：%s', async (error, message) => {
    const http = createHTTP()
    http.post.mockRejectedValue(error)
    const wrapper = await mountPage(http)
    const dialog = await openCreate(wrapper)
    await clickButton(dialog, '生成')
    expect(http.post).toHaveBeenCalledExactlyOnceWith('/api/api-keys', { name: 'kept name' })
    expect(dialog.props('modelValue')).toBe(true)
    expect(dialog.get<HTMLInputElement>('input').element.value).toBe('  kept name  ')
    expect(wrapper.findAllComponents(ElDialog)[1].props('modelValue')).toBe(false)
    expect(http.get).toHaveBeenCalledTimes(1)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(message)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('qms_secret_key')
  })

  it('HTTP 200 业务失败不展示新密钥或刷新列表', async () => {
    const http = createHTTP()
    http.post.mockResolvedValue({
      status: 200,
      data: { code: 500, message: 'API Key 操作失败，请稍后重试', data: null },
    })
    const wrapper = await mountPage(http)
    const dialog = await openCreate(wrapper)
    await clickButton(dialog, '生成')
    expect(dialog.props('modelValue')).toBe(true)
    expect(http.get).toHaveBeenCalledTimes(1)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('API Key 操作失败，请稍后重试')
  })

  it.each([true, false])('状态 %s 的开关更新失败恢复原值', async (is_active) => {
    const http = createHTTP()
    http.get.mockResolvedValue(success([{ ...row, is_active }]))
    http.put.mockResolvedValue({
      status: 200,
      data: { code: 500, message: 'API Key 操作失败，请稍后重试', data: null },
    })
    const wrapper = await mountPage(http)
    const toggle = wrapper.get('[role="switch"]')
    await toggle.trigger('click')
    await flushPromises()
    expect(http.put).toHaveBeenCalledExactlyOnceWith('/api/api-keys/4/status', {
      is_active: !is_active,
    })
    expect(toggle.attributes('aria-checked')).toBe(String(is_active))
    expect(http.get).toHaveBeenCalledTimes(1)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('API Key 操作失败，请稍后重试')
  })

  it('创建成功后刷新失败且无具体原因时不重复拼接加载失败文案', async () => {
    const http = createHTTP()
    const wrapper = await mountPage(http)
    const dialog = await openCreate(wrapper)
    http.get.mockRejectedValue(new Error('secret'))
    await clickButton(dialog, '生成')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('操作已成功，但刷新 API Key 列表失败')
  })

  it('创建成功后刷新失败仍展示一次明文，并准确说明刷新失败', async () => {
    const http = createHTTP()
    const wrapper = await mountPage(http)
    const dialog = await openCreate(wrapper)
    http.get.mockRejectedValue(failure())
    await clickButton(dialog, '生成')
    expect(wrapper.get('.el-table').text()).toContain('qms_ci')
    const created = wrapper.findAllComponents(ElDialog)[1]
    expect(dialog.props('modelValue')).toBe(false)
    expect(created.props('modelValue')).toBe(true)
    expect(created.get<HTMLInputElement>('input').element.value).toBe('qms_created_once')
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('API Key 创建成功')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '操作已成功，但刷新 API Key 列表失败：API Key 操作失败，请稍后重试',
    )
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toMatch(
      /qms_created_once|qms_secret_key/,
    )
    await clickButton(created, '我已妥善保存')
    expect(created.props('modelValue')).toBe(false)
  })

  it('删除失败保留原列表，不进行成功刷新', async () => {
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
    const http = createHTTP()
    http.delete.mockRejectedValue(failure())
    const wrapper = await mountPage(http)
    await clickButton(wrapper, '删除')
    expect(http.delete).toHaveBeenCalledExactlyOnceWith('/api/api-keys/4')
    expect(http.get).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('qms_ci')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('API Key 操作失败，请稍后重试')
  })

  it.each(['cancel', 'handled'])('创建请求 %s 静默保留输入', async (kind) => {
    const error = kind === 'cancel' ? new CanceledError() : failure(401)
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const http = createHTTP()
    http.post.mockRejectedValue(error)
    const wrapper = await mountPage(http)
    const dialog = await openCreate(wrapper)
    await clickButton(dialog, '生成')
    expect(dialog.props('modelValue')).toBe(true)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it.each(['cancel', 'close'])('取消删除 %s 不发请求', async (action) => {
    vi.spyOn(ElMessageBox, 'confirm').mockRejectedValue(action)
    const http = createHTTP()
    const wrapper = await mountPage(http)
    await clickButton(wrapper, '删除')
    expect(http.delete).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
})
