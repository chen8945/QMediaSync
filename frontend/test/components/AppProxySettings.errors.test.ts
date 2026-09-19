import axios, { AxiosError, CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppProxySettings from '@/components/AppProxySettings.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { APIResponse } from '@/api/types'

const masked = 'http://xxxxx:xxxxx@proxy:8080'
const privateURL = 'http://private-user:private-password@proxy:8080'
const rejectedRequest = (errorCode: string, status = 403) =>
  new HttpResponseError({
    status,
    data: { code: 500, error_code: errorCode, message: privateURL },
    config: { method: 'post', url: '/api/setting/test-http-proxy?token=private-token' },
  })
const mountSettings = async (loadError?: unknown) => {
  const getReply = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: { http_proxy: masked, credentials_masked: '1' },
  })
  if (loadError) getReply.mockRejectedValueOnce(loadError)
  const reply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue({ code: 200, message: '', data: null })
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: config.method === 'get' ? await getReply() : await reply(),
  }))
  const wrapper = mount(AppProxySettings, {
    global: { provide: { [httpKey]: axios.create({ adapter }) }, stubs: { PageHeader: true } },
  })
  await flushPromises()
  const act = async (label: string) => {
    await wrapper
      .findAll('button')
      .find((item) => item.text() === label)!
      .trigger('click')
    await flushPromises()
  }
  return { wrapper, reply, getReply, adapter, act }
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

describe('代理设置请求反馈', () => {
  it('业务失败不回读、不报成功，输入与诊断安全边界保留', async () => {
    const { wrapper, reply, getReply, act } = await mountSettings()
    await wrapper.get('input').setValue(privateURL)
    reply.mockResolvedValueOnce({ code: 500, message: `数据库错误 ${privateURL}`, data: null })
    await act('保存')
    expect(wrapper.get('.proxy-status').text()).toContain('保存代理设置失败')
    expect(wrapper.get('.proxy-status').text()).not.toMatch(/private|已保存/)
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe(privateURL)
    expect(getReply).toHaveBeenCalledTimes(1)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
  })

  it.each(['transport', 'business', 'empty'])(
    '保存后 %s 回读失败不会被成功说明覆盖或泄露输入凭据',
    async (failure) => {
      const { wrapper, getReply, act } = await mountSettings()
      await wrapper.get('input').setValue(privateURL)
      if (failure === 'transport')
        getReply.mockRejectedValueOnce(new AxiosError(privateURL, 'ERR_NETWORK'))
      else
        getReply.mockResolvedValueOnce({
          code: failure === 'business' ? 500 : 200,
          message: privateURL,
          data: null,
        })
      await act('保存')
      expect(wrapper.get('.proxy-status').text()).toContain('代理设置已保存，但刷新失败')
      expect(wrapper.get('.proxy-status').text()).not.toMatch(/private|已设置代理服务器/)
      expect((wrapper.get('input').element as HTMLInputElement).value).toBe(privateURL)
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
    },
  )

  it('保存成功后使用脱敏回读并恢复凭据保留标记', async () => {
    const { wrapper, adapter, act } = await mountSettings()
    await wrapper.get('input').setValue(`  ${privateURL}  `)
    await act('保存')
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe(masked)
    expect(wrapper.get('.proxy-status').text()).toContain('代理设置已保存')
    expect(wrapper.get('.proxy-status').text()).not.toContain('private')
    expect(
      JSON.parse(adapter.mock.calls.find(([config]) => config.method === 'post')![0].data),
    ).toEqual({ http_proxy: privateURL, preserve_proxy_credentials: false })
    await act('测试')
    expect(JSON.parse(adapter.mock.calls.at(-1)?.[0].data)).toEqual({
      http_proxy: masked,
      preserve_proxy_credentials: true,
    })
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', '访问地址校验失败'],
    ['CSRF_TOKEN_INVALID', '请求安全校验失败'],
  ])('测试请求被 %s 拒绝不误导检查代理连通性', async (code, message) => {
    const { wrapper, reply, act } = await mountSettings()
    reply.mockRejectedValueOnce(rejectedRequest(code))
    await act('测试')
    expect(wrapper.get('.proxy-status').text()).toContain(message)
    expect(wrapper.get('.proxy-status').text()).not.toMatch(
      /无法连接|检查网络连接和代理设置|private/,
    )
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('已核验的连接失败仍展示代理原因', async () => {
    const { wrapper, reply, act } = await mountSettings()
    reply.mockResolvedValueOnce({ code: 500, message: '出站代理连接测试失败', data: null })
    await act('测试')
    expect(wrapper.get('.proxy-status').text()).toContain(
      '出站代理连接测试失败，请检查代理配置和服务状态',
    )
  })

  it.each(['保存', '测试'])('%s 的取消和已处理认证错误不重复提示', async (action) => {
    const { wrapper, reply, act } = await mountSettings()
    const handled = rejectedRequest('SESSION_INVALID', 401)
    markAuthInvalidationHandled(handled)
    for (const error of [new CanceledError(), handled]) {
      reply.mockRejectedValueOnce(error)
      await act(action)
      expect(wrapper.find('.proxy-status').exists()).toBe(false)
    }
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('初始读取失败显示安全原因', async () => {
    const { wrapper } = await mountSettings(rejectedRequest('REQUEST_ORIGIN_INVALID'))
    expect(wrapper.get('.proxy-status').text()).toContain('加载代理设置失败')
    expect(wrapper.get('.proxy-status').text()).toContain('访问地址校验失败')
    expect(wrapper.get('.proxy-status').text()).not.toContain('private')
  })
})
