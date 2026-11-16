import axios, { AxiosError, CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import MediaServerSettings from '@/components/media-library/MediaServerSettings.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { APIResponse } from '@/api/types'
import { createDeferred } from '../support/deferred'

const settings = {
  enabled: true,
  emby_url: 'http://emby:8096',
  emby_api_key: 'private-key',
  sync_enabled: 1,
  sync_cron: '0 * * * *',
  selected_libraries: '["movies"]',
}
const rejectedRequest = (errorCode: string, status = 403) =>
  new HttpResponseError({
    status,
    data: {
      status: status,
      error_code: errorCode ?? 'TEST_REQUEST_FAILED',
      message: 'internal private-key',
      data: null,
      instance: '/request-errors/ComponentTestFailure0001',
    },
    config: { method: 'post', url: '/api/setting/emby/parse?api_key=private-key' },
  })
const mountSettings = async (
  configError?: unknown,
  syncError = '',
  config: (typeof settings & { sync_all_libraries?: number }) | null = settings,
  libraryError?: unknown,
) => {
  const getReply = vi.fn(async (url: string): Promise<APIResponse<unknown>> => {
    if (url.endsWith('/setting/emby-config')) {
      if (configError) throw configError
      return { status: 200, message: '', data: { exists: config !== null, config } }
    }
    if (url.endsWith('/emby/libraries')) {
      if (libraryError) throw libraryError
      return { status: 200, message: '', data: [] }
    }
    if (url.endsWith('/setting/cron')) return { status: 200, message: '', data: [] }
    return {
      status: 200,
      message: '',
      data: { is_running: false, sync_enabled: 1, total_items: 0, last_error: syncError },
    }
  })
  const reply = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    status: 200,
    message: '',
    data: { restart_required: false },
    warnings: [],
  })
  const adapter = vi.fn(async (config) => {
    const responseBody = config.method === 'get' ? await getReply(config.url) : await reply()
    return {
      config,
      status: (responseBody as { status: number }).status,
      statusText: 'OK',
      headers: {},
      data: responseBody,
    }
  })
  const wrapper = mount(MediaServerSettings, {
    props: { serverType: 'emby', active: true },
    global: {
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: { PageHeader: true, RouterLink: true },
    },
  })
  await flushPromises()
  const act = async (label: string) => {
    const button = wrapper.findAll('button').find((item) => item.text() === label)
    expect(button, `${label} 按钮应存在`).toBeDefined()
    await button!.trigger('click')
    await flushPromises()
  }
  return { wrapper, reply, getReply, adapter, act }
}
const input = (wrapper: VueWrapper, label: string) =>
  wrapper
    .findAll('.el-form-item')
    .find((item) => item.find('.el-form-item__label').text() === label)!
    .get('input')

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('Emby 设置请求反馈', () => {
  it('尚未配置时同步默认关闭，首次保存不擅自开启', async () => {
    const { wrapper, adapter, act } = await mountSettings(undefined, '', null)
    expect(input(wrapper, '启用同步').element).toHaveProperty('checked', false)
    await act('保存设置')
    const post = adapter.mock.calls.find(([config]) => config.method === 'post')![0]
    expect(JSON.parse(post.data)).toMatchObject({ enabled: false, sync_enabled: 0 })
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存成功')
  })

  it('首次读取失败不能保存默认配置，重试读取成功后才允许保存', async () => {
    const { wrapper, reply, getReply, act } = await mountSettings(
      rejectedRequest('REQUEST_ORIGIN_INVALID'),
    )
    const save = () => wrapper.findAll('button').find((button) => button.text() === '保存设置')!
    expect(save().attributes('disabled')).toBeDefined()
    await act('保存设置')
    expect(reply).not.toHaveBeenCalled()
    getReply.mockResolvedValueOnce({
      status: 200,
      message: '',
      data: { exists: true, config: settings },
    })
    await act('重试加载')
    expect((input(wrapper, 'Emby 服务器地址').element as HTMLInputElement).value).toBe(
      settings.emby_url,
    )
    expect(save().attributes('disabled')).toBeUndefined()
    await act('保存设置')
    expect(reply).toHaveBeenCalledTimes(1)
  })

  it('业务失败保留输入和选择，只显示一次具体原因', async () => {
    const { wrapper, reply, act, adapter } = await mountSettings()
    await input(wrapper, 'Emby 服务器地址').setValue('http://changed:8096')
    await input(wrapper, 'Emby API Key').setValue('private-new-key')
    reply.mockResolvedValueOnce({
      status: 500,
      message: 'Emby 配置暂时不可写',
      data: null,
      error_code: 'TEST_REQUEST_FAILED',
      instance: '/request-errors/ComponentTestFailure0001',
    })
    await act('保存设置')
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存 Emby 配置失败')
    expect(wrapper.text()).not.toContain('保存成功')
    expect(wrapper.get('.emby-status-alert').text()).toContain('Emby 配置暂时不可写')
    expect((input(wrapper, 'Emby 服务器地址').element as HTMLInputElement).value).toBe(
      'http://changed:8096',
    )
    expect((input(wrapper, 'Emby API Key').element as HTMLInputElement).value).toBe(
      'private-new-key',
    )
    const payload = JSON.parse(
      adapter.mock.calls.find(([config]) => config.method === 'post')![0].data,
    )
    expect(payload.selected_libraries).toBe('["movies"]')
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存 Emby 配置失败')
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', '访问地址校验失败'],
    ['CSRF_TOKEN_INVALID', '请求安全校验失败'],
  ])('提取请求被 %s 拒绝时不会误报 Emby 连通性', async (code, message) => {
    const { wrapper, reply, act } = await mountSettings()
    reply.mockRejectedValueOnce(rejectedRequest(code))
    await act('提取媒体信息')
    expect(wrapper.get('.emby-status-alert').text()).toContain('请求被 QMediaSync 拒绝')
    expect(wrapper.get('.emby-status-alert').text()).toContain(message)
    expect(wrapper.get('.emby-status-alert').text()).not.toMatch(
      /无法连接|网络错误|提取媒体信息成功/,
    )
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('加载配置失败使用安全错误条，保存后的回读失败保留编辑值', async () => {
    const { wrapper, getReply, reply, act } = await mountSettings()
    await input(wrapper, 'Emby 服务器地址').setValue('http://changed:8096')
    getReply.mockRejectedValueOnce(new AxiosError('private-key', 'ERR_NETWORK'))
    await act('保存设置')
    expect(wrapper.get('.readback-failure').text()).toContain('Emby 配置已保存，但刷新失败')
    expect(wrapper.get('.readback-failure').text()).toContain('无法获取服务器响应')
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存成功')
    expect((input(wrapper, 'Emby 服务器地址').element as HTMLInputElement).value).toBe(
      'http://changed:8096',
    )
    expect(ElMessage.error).not.toHaveBeenCalled()
    await act('保存设置')
    expect(reply).toHaveBeenCalledTimes(2)
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存成功')
  })

  it('同步全部媒体库时，初始加载和保存均不请求媒体库', async () => {
    const { wrapper, getReply, act } = await mountSettings()
    await act('保存设置')
    expect(getReply.mock.calls.some(([url]) => url.endsWith('/emby/libraries'))).toBe(false)
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存成功')
  })

  it('首次读取配置成功但媒体库失败时允许保存，提示不能声称已保存', async () => {
    const { wrapper } = await mountSettings(
      undefined,
      '',
      { ...settings, sync_all_libraries: 0 },
      new AxiosError('unavailable', 'ERR_NETWORK'),
    )
    expect(wrapper.get('.emby-status-alert').text()).toContain('无法获取 Emby 媒体库')
    expect(wrapper.get('.emby-status-alert').text()).not.toContain('已保存')
    expect(
      wrapper
        .findAll('button')
        .find((button) => button.text() === '保存设置')!
        .attributes('disabled'),
    ).toBeUndefined()
  })

  it('保存后媒体库刷新失败保留已保存上下文，不覆盖成保存成功', async () => {
    const config = { ...settings, sync_all_libraries: 0 }
    const { wrapper, getReply, act } = await mountSettings(undefined, '', config)
    getReply.mockImplementation(async (url: string) => {
      if (url.endsWith('/emby/libraries')) throw new AxiosError('private-key', 'ERR_NETWORK')
      return { status: 200, message: '', data: { exists: true, config } }
    })
    await act('保存设置')
    const alert = wrapper.get('.readback-failure').text()
    expect(alert).toContain('Emby 配置已保存，但无法获取媒体库')
    expect(alert).not.toContain('保存成功')
    expect(ElMessage.error).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.get('.readback-failure').text()).toContain('无法获取媒体库')
  })

  it('保存前的选库请求失败不能覆盖已提交配置的应用警告', async () => {
    const { wrapper, getReply, reply, act } = await mountSettings()
    const libraries = createDeferred<void>()
    getReply.mockImplementationOnce(async () => {
      await libraries.promise
      throw new AxiosError('private-library-key', 'ERR_NETWORK')
    })
    await wrapper
      .findAll('.el-radio')
      .find((radio) => radio.text() === '指定媒体库')!
      .get('input')
      .setValue(true)
    await flushPromises()
    const readback = createDeferred<APIResponse<unknown>>()
    getReply.mockReturnValueOnce(readback.promise)
    reply.mockResolvedValueOnce({
      status: 200,
      message: '配置已保存',
      data: { restart_required: false },
      warnings: ['代理未应用，请重新保存'],
    })
    await act('保存设置')
    libraries.resolve()
    await flushPromises()
    const feedbackDuringReadback = wrapper.get('.emby-status-alert').text()
    readback.resolve({
      status: 200,
      message: '',
      data: { exists: true, config: { ...settings, sync_all_libraries: 0 } },
    })
    await flushPromises()
    expect(feedbackDuringReadback).toContain('代理未应用，请重新保存')
    expect(feedbackDuringReadback).toContain('配置已保存，尚未完全生效')
    expect(wrapper.get('.emby-status-alert').text()).toContain('代理未应用，请重新保存')
    expect(wrapper.find('.readback-failure').exists()).toBe(false)
  })

  it.each(['REQUEST_ORIGIN_INVALID', 'CSRF_TOKEN_INVALID'])(
    '保存后回读被 %s 拒绝仍说明配置已保存',
    async (code) => {
      const { wrapper, getReply, act } = await mountSettings()
      getReply.mockRejectedValueOnce(rejectedRequest(code))
      await act('保存设置')
      expect(wrapper.get('.readback-failure').text()).toContain('Emby 配置已保存，但刷新失败')
      expect(wrapper.get('.readback-failure').text()).toContain('请求被 QMediaSync 拒绝')
      expect(wrapper.get('.readback-failure').text()).toContain('校验失败')
    },
  )

  it('首次加载失败可见且不透传内部异常', async () => {
    const { wrapper } = await mountSettings(rejectedRequest('REQUEST_ORIGIN_INVALID'))
    expect(wrapper.get('.emby-status-alert').text()).toContain('请求被 QMediaSync 拒绝')
    expect(wrapper.get('.emby-status-alert').text()).toContain('访问地址校验失败')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it.each(['保存设置', '提取媒体信息', '启动全量同步'])(
    '%s 取消或已处理认证错误不提示',
    async (action) => {
      const { wrapper, reply, act } = await mountSettings()
      const handled = rejectedRequest('SESSION_INVALID', 401)
      markAuthInvalidationHandled(handled)
      for (const error of [new CanceledError(), handled]) {
        reply.mockRejectedValueOnce(error)
        await act(action)
        expect(wrapper.find('.emby-status-alert').exists()).toBe(false)
      }
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(console.error).not.toHaveBeenCalled()
    },
  )

  it('启动业务失败不发起状态刷新或轮询，并展示固定原因', async () => {
    const { wrapper, reply, getReply, act } = await mountSettings()
    const readsBeforeStart = getReply.mock.calls.length
    reply.mockResolvedValueOnce({
      status: 500,
      message: '已有 Emby 条目同步任务正在运行，请稍后再试',
      data: null,
      error_code: 'TEST_REQUEST_FAILED',
      instance: '/request-errors/ComponentTestFailure0001',
    })
    await act('启动全量同步')
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.get('.emby-status-alert').text()).toContain('已有 Emby 条目同步任务正在运行')
    expect(getReply).toHaveBeenCalledTimes(readsBeforeStart)
    expect(wrapper.text()).not.toContain('同步已启动')
  })

  it('保存请求在途时禁用启动同步，避免保存结果覆盖同步反馈', async () => {
    const { wrapper, reply, act } = await mountSettings()
    const sync = () => wrapper.findAll('button').find((button) => button.text() === '启动全量同步')!
    const pending = createDeferred<APIResponse<unknown>>()
    reply.mockReturnValueOnce(pending.promise)
    await act('保存设置')
    expect(sync().attributes('disabled')).toBeDefined()
    pending.resolve({ status: 200, message: '', data: { restart_required: false }, warnings: [] })
    await flushPromises()
    expect(sync().attributes('disabled')).toBeUndefined()
  })

  it('同步启动请求在途时禁用保存与提取，避免其结果覆盖同步反馈', async () => {
    const { wrapper, reply, act } = await mountSettings()
    const button = (label: string) =>
      wrapper.findAll('button').find((item) => item.text() === label)!
    const pending = createDeferred<APIResponse<unknown>>()
    reply.mockReturnValueOnce(pending.promise)
    await act('启动全量同步')
    expect(button('保存设置').attributes('disabled')).toBeDefined()
    expect(button('提取媒体信息').attributes('disabled')).toBeDefined()
    pending.resolve({ status: 200, message: '', data: { restart_required: false }, warnings: [] })
    await flushPromises()
    expect(button('保存设置').attributes('disabled')).toBeUndefined()
    expect(button('提取媒体信息').attributes('disabled')).toBeUndefined()
  })

  it('Cron 保留 trim 参数，来源拒绝不误报表达式格式', async () => {
    const { wrapper, adapter, getReply } = await mountSettings()
    getReply.mockRejectedValueOnce(rejectedRequest('REQUEST_ORIGIN_INVALID'))
    const cron = input(wrapper, '同步时间')
    await cron.setValue('  0 * * * *  ')
    await cron.trigger('blur')
    await flushPromises()
    expect(adapter.mock.calls.at(-1)?.[0].params).toEqual({ cron: '0 * * * *' })
    await vi.advanceTimersByTimeAsync(100)
    expect(wrapper.get('.el-form-item__error').text()).toContain('访问地址校验失败')
    expect(wrapper.find('.emby-status-alert').exists()).toBe(false)
    expect(wrapper.get('.el-form-item__error').text()).not.toContain('请检查表达式格式')
  })

  it('Cron 失焦读取失败保留操作反馈，编辑和清空立即丢弃旧响应', async () => {
    const { wrapper, reply, getReply, act } = await mountSettings()
    reply.mockResolvedValueOnce({
      status: 500,
      message: 'Emby 配置暂时不可写',
      data: null,
      error_code: 'TEST_REQUEST_FAILED',
      instance: '/request-errors/ComponentTestFailure0001',
    })
    await act('保存设置')
    const cron = input(wrapper, '同步时间')
    const old = createDeferred<APIResponse<unknown>>()
    getReply.mockReturnValueOnce(old.promise)
    await cron.setValue('0 2 * * *')
    await cron.trigger('blur')
    await flushPromises()
    await cron.setValue('0 3 * * *')
    getReply.mockRejectedValueOnce(rejectedRequest('REQUEST_ORIGIN_INVALID'))
    await cron.trigger('blur')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(100)
    expect(wrapper.get('.el-form-item__error').text()).toContain('访问地址校验失败')
    old.resolve({ status: 200, message: '', data: ['2030-01-01'] })
    await flushPromises()
    expect(wrapper.get('.el-form-item__error').text()).toContain('访问地址校验失败')
    expect(wrapper.text()).not.toContain('2030-01-01')
    const pending = createDeferred<APIResponse<unknown>>()
    getReply.mockReturnValueOnce(pending.promise)
    await cron.trigger('blur')
    await flushPromises()
    await cron.setValue('')
    pending.resolve({
      status: 500,
      message: '过期查询失败',
      data: null,
      error_code: 'TEST_REQUEST_FAILED',
      instance: '/request-errors/ComponentTestFailure0001',
    })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(100)
    expect(wrapper.find('.el-form-item__error').exists()).toBe(false)
    expect(wrapper.get('.emby-status-alert').text()).toContain('Emby 配置暂时不可写')
  })

  it('同步错误卡片不渲染后台异常中的 URL 和凭据', async () => {
    const { wrapper } = await mountSettings(undefined, 'GET http://emby?api_key=private-key failed')
    expect(wrapper.get('.error-card').text()).toContain('上次同步失败，请查看服务日志')
    expect(wrapper.get('.error-card').text()).not.toContain('private-key')
  })
})
