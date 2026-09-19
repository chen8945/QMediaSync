import axios, { CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Component } from 'vue'
import AppAiSettings from '@/components/AppAiSettings.vue'
import AppTmdbSettings from '@/components/AppTmdbSettings.vue'
import type { APIResponse } from '@/api/types'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'

const settings = {
  ai_base_url: 'https://ai.example.com',
  ai_api_key: 'ai-secret',
  ai_model_name: 'model',
  ai_timeout: 60,
  tmdb_url: 'https://tmdb.example.com',
  tmdb_image_url: 'https://images.example.com',
  tmdb_enable_proxy: false,
  tmdb_api_key: 'tmdb-secret',
  tmdb_access_token: 'token-secret',
  fanart_api_key: 'fanart-secret',
  tmdb_language: 'zh-CN',
  tmdb_image_language: 'en-US',
  local_max_threads: 8,
}

const mountSettings = async (component: Component, initial = settings, loadError?: unknown) => {
  const reply = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: null,
  })
  const adapter = vi.fn(async (config) => {
    if (config.method === 'get' && loadError) throw loadError
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: config.method === 'get' ? { code: 200, message: '', data: initial } : await reply(),
    }
  })
  const http = axios.create({ adapter })
  const wrapper = mount(component, {
    global: { provide: { [httpKey]: http }, stubs: { PageHeader: true } },
  })
  await flushPromises()
  const act = async (label: string) => {
    const button = wrapper.findAll('button').find((item) => item.text() === label)
    expect(button, `${label} 按钮应存在`).toBeDefined()
    await button!.trigger('click')
    await flushPromises()
  }
  return { wrapper, reply, adapter, act }
}

const fieldInput = (wrapper: VueWrapper, label: string) => {
  const field = wrapper
    .findAll('.el-form-item')
    .find((item) => item.find('.el-form-item__label').text() === label)
  expect(field, `${label} 字段应存在`).toBeDefined()
  return field!.get('input')
}

const rejectedRequest = (errorCode: string, status = 403) =>
  new HttpResponseError({
    status,
    data: { code: 500, message: 'internal token-secret', error_code: errorCode, data: null },
    config: { method: 'post', url: '/api/scrape/test?api_key=token-secret' },
  })

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe.each([
  { name: 'AI', component: AppAiSettings, urlLabel: 'API 接口地址', fallback: '保存 AI 设置失败' },
  {
    name: 'TMDB',
    component: AppTmdbSettings,
    urlLabel: 'TMDB 接口地址',
    fallback: '保存刮削设置失败',
  },
])('$name 设置失败反馈', ({ component, urlLabel, fallback }) => {
  it('HTTP 200 业务失败保留输入，恢复操作按钮，且不显示保存成功', async () => {
    const { wrapper, reply, act } = await mountSettings(component)
    const input = fieldInput(wrapper, urlLabel)
    await input.setValue('https://changed.example.com')
    reply.mockResolvedValueOnce({ code: 500, message: 'database password=secret', data: null })

    await act('保存设置')

    expect(wrapper.get('.save-status').text()).toContain(fallback)
    expect(wrapper.text()).not.toContain('保存成功')
    expect(wrapper.text()).not.toContain('database password')
    expect((input.element as HTMLInputElement).value).toBe('https://changed.example.com')
    expect(input.attributes('disabled')).toBeUndefined()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ method: 'POST', status: 200 }),
    )
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
  })

  it('测试的业务失败即使携带 data:true 也不显示连接成功', async () => {
    const { wrapper, reply, act } = await mountSettings(component)
    reply.mockResolvedValueOnce({ code: 500, message: 'internal token-secret', data: true })
    await act('测试连通性')
    expect(wrapper.get('.test-status').text()).toContain('测试失败')
    expect(wrapper.get('.test-status').text()).not.toContain('连接成功')
    expect(wrapper.get('.test-status').text()).not.toContain('测试成功')
    expect(wrapper.get('.test-status').text()).not.toContain('token-secret')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', '访问地址校验失败'],
    ['CSRF_TOKEN_INVALID', '请求安全校验失败'],
  ])('测试请求被 %s 拒绝时展示请求原因，错误条持续保留', async (code, message) => {
    const { wrapper, reply, act } = await mountSettings(component)
    reply.mockRejectedValueOnce(rejectedRequest(code))
    await act('测试连通性')

    expect(wrapper.get('.test-status').text()).toContain('测试失败')
    expect(wrapper.get('.test-status').text()).toContain(message)
    expect(wrapper.get('.test-status').text()).not.toContain('连接失败')
    expect(wrapper.get('.test-status').text()).not.toContain('网络连接和设置')
    expect(ElMessage.error).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.get('.test-status').text()).toContain(message)
  })

  it('读取设置失败只显示已有错误条，保留空表单默认值', async () => {
    const { wrapper } = await mountSettings(
      component,
      settings,
      rejectedRequest('REQUEST_ORIGIN_INVALID'),
    )
    expect(wrapper.get('.save-status').text()).toContain('获取设置失败')
    expect(wrapper.get('.save-status').text()).toContain('访问地址校验失败')
    expect((fieldInput(wrapper, urlLabel).element as HTMLInputElement).value).toBe('')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it.each(['保存设置', '测试连通性'])('取消或已处理的认证失败不在%s时重复提示', async (action) => {
    const { wrapper, reply, act } = await mountSettings(component)
    const handled = rejectedRequest('SESSION_INVALID', 401)
    markAuthInvalidationHandled(handled)
    for (const failure of [new CanceledError(), handled]) {
      reply.mockRejectedValueOnce(failure)
      await act(action)
      expect(wrapper.find('.save-status').exists()).toBe(false)
      expect(wrapper.find('.test-status').exists()).toBe(false)
    }
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('之前保存成功的计时器不会清除新的保存失败提示', async () => {
    const { wrapper, reply, act } = await mountSettings(component)
    await act('保存设置')
    expect(wrapper.get('.save-status').text()).toContain('保存成功')
    reply.mockRejectedValueOnce(rejectedRequest('REQUEST_ORIGIN_INVALID'))
    await act('保存设置')
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.get('.save-status').text()).toContain('访问地址校验失败')
  })
})

describe('AI 识别设置', () => {
  it('保留模型名称与 API Key 的本地关联校验，不发送无效保存请求', async () => {
    const { wrapper, reply, act } = await mountSettings(AppAiSettings)
    await fieldInput(wrapper, 'API Key').setValue('')
    await act('保存设置')
    expect(reply).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('如果填写了模型名称，必须填写 API Key')
    expect((fieldInput(wrapper, '模型名称').element as HTMLInputElement).value).toBe('model')
    expect(wrapper.find('.save-status').exists()).toBe(false)
  })

  it('业务失败只公开核验过的字段原因，内部 AI 测试错误使用安全回退文案', async () => {
    const { wrapper, reply, act } = await mountSettings(AppAiSettings)
    reply.mockResolvedValueOnce({
      code: 500,
      message: 'ai_base_url：只支持 http 或 https',
      data: null,
    })
    await act('保存设置')
    expect(wrapper.get('.save-status').text()).toContain('AI 接口地址只支持 HTTP 或 HTTPS')

    reply.mockResolvedValueOnce({ code: 500, message: 'upstream Authorization=secret', data: null })
    await act('测试连通性')
    expect(wrapper.get('.test-status').text()).toContain('AI 服务连通性测试失败，请检查设置')
    expect(wrapper.get('.test-status').text()).not.toContain('secret')
    expect(wrapper.get('.test-status').text()).not.toContain('测试成功')
    expect(ElMessage.error).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.find('.test-status').exists()).toBe(true)
  })

  it('保存沿用超时值，连接测试不额外发送超时字段', async () => {
    const { adapter, act } = await mountSettings(AppAiSettings)
    await act('保存设置')
    await act('测试连通性')
    const requests = adapter.mock.calls
      .map(([config]) => config)
      .filter((config) => config.method === 'post')
    expect(JSON.parse(requests[0].data)).toEqual({
      ai_base_url: settings.ai_base_url,
      ai_api_key: settings.ai_api_key,
      ai_model_name: settings.ai_model_name,
      ai_timeout: 60,
    })
    expect(JSON.parse(requests[1].data)).toEqual({
      ai_base_url: settings.ai_base_url,
      ai_api_key: settings.ai_api_key,
      ai_model_name: settings.ai_model_name,
    })
    expect(requests[1].timeout).toBe(120000)
  })
})

describe('TMDB 刮削设置', () => {
  it('请求成功但 data:false 表示连接失败，之前测试成功的计时器不清除错误条', async () => {
    const { wrapper, reply, act } = await mountSettings(AppTmdbSettings)
    reply.mockResolvedValueOnce({ code: 200, message: '', data: true })
    await act('测试连通性')
    expect(wrapper.get('.test-status').text()).toContain('连接成功')

    reply.mockResolvedValueOnce({ code: 200, message: '', data: false })
    await act('测试连通性')
    expect(wrapper.get('.test-status').text()).toContain('连接失败')
    expect(wrapper.get('.test-status').text()).toContain('TMDB 连通性测试失败，请检查设置')
    expect(ElMessage.error).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.get('.test-status').text()).toContain('连接失败')
  })

  it('保留隐藏凭据、语言和 fanart 配置，无 TMDB Key 时仍保存默认线程数', async () => {
    const { wrapper, adapter, act } = await mountSettings(AppTmdbSettings)
    await fieldInput(wrapper, 'TMDB 密钥').setValue('')
    await act('保存设置')
    await act('测试连通性')
    const requests = adapter.mock.calls
      .map(([config]) => config)
      .filter((config) => config.method === 'post')
    const connection = {
      tmdb_url: settings.tmdb_url,
      tmdb_image_url: settings.tmdb_image_url,
      tmdb_enable_proxy: false,
      tmdb_api_key: '',
      tmdb_access_token: 'token-secret',
      tmdb_language: 'zh-CN',
      tmdb_image_language: 'en-US',
    }
    expect(JSON.parse(requests[0].data)).toEqual({
      ...connection,
      fanart_api_key: 'fanart-secret',
      local_max_threads: 5,
    })
    expect(JSON.parse(requests[1].data)).toEqual(connection)
    expect(requests[1].timeout).toBe(20000)
  })
})
