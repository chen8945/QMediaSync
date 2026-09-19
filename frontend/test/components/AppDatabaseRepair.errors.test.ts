import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { AxiosError, CanceledError } from 'axios'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppDatabaseRepair from '@/components/AppDatabaseRepair.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'

const wrappers: VueWrapper[] = []
const failure = (status: number, error_code?: string) =>
  new HttpResponseError({ status, data: { code: 500, message: 'SQL secret', error_code } })
const runRepair = async (post: ReturnType<typeof vi.fn>) => {
  const wrapper = mount(AppDatabaseRepair, {
    global: { provide: { [httpKey]: { post } }, stubs: { PageHeader: true } },
  })
  wrappers.push(wrapper)
  await wrapper.get('button').trigger('click')
  await flushPromises()
  expect(post).toHaveBeenCalledExactlyOnceWith('/api/database/repair')
  expect(wrapper.get('button').text()).toBe('修复数据库')
  return wrapper
}

beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('数据库修复请求', () => {
  it('业务失败不误报成功并展示服务端原因', async () => {
    await runRepair(
      vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 500, message: '修复数据库失败：磁盘空间不足', data: null },
      }),
    )
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('修复数据库失败：磁盘空间不足')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).toHaveBeenCalledExactlyOnceWith('数据库修复失败：', { status: 200 })
  })
  it.each([
    [
      failure(403, 'CSRF_TOKEN_INVALID'),
      '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
    ],
    [
      failure(403, 'REQUEST_ORIGIN_INVALID'),
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    ],
    [
      new AxiosError('SQL secret', 'ETIMEDOUT', {
        method: 'post',
        url: '/x',
        headers: {},
      } as never),
      '请求超时，操作结果尚未确认。请先检查操作是否已生效，避免重复提交',
    ],
  ])('请求失败使用公共分类且不重发：%s', async (error, message) => {
    await runRepair(vi.fn().mockRejectedValue(error))
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(message)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
  })
  it.each(['cancel', 'handled'])('%s 静默结束', async (kind) => {
    const error = kind === 'cancel' ? new CanceledError() : failure(401)
    if (kind === 'handled') markAuthInvalidationHandled(error)
    await runRepair(vi.fn().mockRejectedValue(error))
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
  it('合法空数据成功显示修复成功', async () => {
    await runRepair(vi.fn().mockResolvedValue({ status: 200, data: { code: 200, data: null } }))
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('数据库修复成功')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})
