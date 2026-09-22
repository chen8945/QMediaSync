import axios, { AxiosError, CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppEmbySettings from '@/components/AppEmbySettings.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { APIResponse } from '@/api/types'
import { createDeferred } from '../support/deferred'

const settings = {
  emby_url: 'http://emby:8096',
  emby_api_key: 'private-key',
  sync_enabled: 1,
  sync_cron: '0 * * * *',
  selected_libraries: '["movies"]',
}
const rejectedRequest = (errorCode: string, status = 403) =>
  new HttpResponseError({
    status,
    data: { code: 500, error_code: errorCode, message: 'internal private-key' },
    config: { method: 'post', url: '/api/setting/emby/parse?api_key=private-key' },
  })
const mountSettings = async (configError?: unknown, syncError = '') => {
  const getReply = vi.fn(async (url: string): Promise<APIResponse<unknown>> => {
    if (url.endsWith('/setting/emby-config')) {
      if (configError) throw configError
      return { code: 200, message: '', data: { exists: true, config: settings } }
    }
    if (url.endsWith('/emby/libraries')) return { code: 200, message: '', data: [] }
    if (url.endsWith('/setting/cron')) return { code: 200, message: '', data: [] }
    return {
      code: 200,
      message: '',
      data: { is_running: false, sync_enabled: 1, total_items: 0, last_error: syncError },
    }
  })
  const reply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue({ code: 200, message: '', data: null })
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: config.method === 'get' ? await getReply(config.url) : await reply(),
  }))
  const wrapper = mount(AppEmbySettings, {
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
  it('首次读取失败不能保存默认配置，重试读取成功后才允许保存', async () => {
    const { wrapper, reply, getReply, act } = await mountSettings(
      rejectedRequest('REQUEST_ORIGIN_INVALID'),
    )
    const save = () => wrapper.findAll('button').find((button) => button.text() === '保存设置')!
    expect(save().attributes('disabled')).toBeDefined()
    await act('保存设置')
    expect(reply).not.toHaveBeenCalled()
    getReply.mockResolvedValueOnce({
      code: 200,
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

  it('业务失败保留输入和选择，只有一个安全错误提示', async () => {
    const { wrapper, reply, act, adapter } = await mountSettings()
    await input(wrapper, 'Emby 服务器地址').setValue('http://changed:8096')
    await input(wrapper, 'Emby API Key').setValue('private-new-key')
    reply.mockResolvedValueOnce({ code: 500, message: 'internal private-key', data: null })
    await act('保存设置')
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存 Emby 配置失败')
    expect(wrapper.text()).not.toContain('保存成功')
    expect(wrapper.get('.emby-status-alert').text()).not.toContain('private')
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
    expect(wrapper.get('.emby-status-alert').text()).toContain('Emby 配置已保存，但刷新失败')
    expect(wrapper.get('.emby-status-alert').text()).toContain('无法获取服务器响应')
    expect(wrapper.get('.emby-status-alert').text()).not.toContain('保存成功')
    expect((input(wrapper, 'Emby 服务器地址').element as HTMLInputElement).value).toBe(
      'http://changed:8096',
    )
    expect(ElMessage.error).not.toHaveBeenCalled()
    await act('保存设置')
    expect(reply).toHaveBeenCalledTimes(2)
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存成功')
  })

  it('首次加载失败可见且不透传内部异常', async () => {
    const { wrapper } = await mountSettings(rejectedRequest('REQUEST_ORIGIN_INVALID'))
    expect(wrapper.get('.emby-status-alert').text()).toContain('加载 Emby 配置失败')
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
      code: 500,
      message: '已有 Emby 条目同步任务正在运行，请稍后再试',
      data: null,
    })
    await act('启动全量同步')
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.get('.emby-status-alert').text()).toContain('已有 Emby 条目同步任务正在运行')
    expect(getReply).toHaveBeenCalledTimes(readsBeforeStart)
    expect(wrapper.text()).not.toContain('同步已启动')
  })

  it('较早保存成功的定时器不会清掉后来的错误', async () => {
    const { wrapper, reply, act } = await mountSettings()
    await act('保存设置')
    expect(wrapper.get('.emby-status-alert').text()).toContain('保存成功')
    reply.mockRejectedValueOnce(rejectedRequest('REQUEST_ORIGIN_INVALID'))
    await act('提取媒体信息')
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.get('.emby-status-alert').text()).toContain('访问地址校验失败')
  })

  it('保存请求在途时禁用启动同步，避免保存结果覆盖同步反馈', async () => {
    const { wrapper, reply, act } = await mountSettings()
    const sync = () => wrapper.findAll('button').find((button) => button.text() === '启动全量同步')!
    const pending = createDeferred<APIResponse<unknown>>()
    reply.mockReturnValueOnce(pending.promise)
    await act('保存设置')
    expect(sync().attributes('disabled')).toBeDefined()
    pending.resolve({ code: 200, message: '', data: null })
    await flushPromises()
    expect(sync().attributes('disabled')).toBeUndefined()
  })

  it('Cron 保留 trim 参数，来源拒绝不误报表达式格式', async () => {
    const { wrapper, adapter, getReply } = await mountSettings()
    getReply.mockRejectedValueOnce(rejectedRequest('REQUEST_ORIGIN_INVALID'))
    const cron = input(wrapper, '同步时间')
    await cron.setValue('  0 * * * *  ')
    await cron.trigger('blur')
    await flushPromises()
    expect(adapter.mock.calls.at(-1)?.[0].params).toEqual({ cron: '0 * * * *' })
    expect(wrapper.get('.emby-status-alert').text()).toContain('访问地址校验失败')
    expect(wrapper.get('.emby-status-alert').text()).not.toContain('请检查表达式格式')
  })

  it('同步错误卡片不渲染后台异常中的 URL 和凭据', async () => {
    const { wrapper } = await mountSettings(undefined, 'GET http://emby?api_key=private-key failed')
    expect(wrapper.get('.error-card').text()).toContain('上次同步失败，请查看服务日志')
    expect(wrapper.get('.error-card').text()).not.toContain('private-key')
  })

  it('卸载后在途状态成功响应不能重新创建轮询', async () => {
    const { wrapper, getReply, act } = await mountSettings()
    const pending = createDeferred<APIResponse<unknown>>()
    getReply.mockReturnValueOnce(pending.promise)
    await act('启动全量同步')
    const reads = getReply.mock.calls.length
    wrapper.unmount()
    pending.resolve({ code: 200, message: '', data: { is_running: true } })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(9000)
    expect(getReply).toHaveBeenCalledTimes(reads)
    expect(console.error).not.toHaveBeenCalled()
  })

  it('运行中隐藏页面暂停状态轮询，恢复后立即刷新且请求不重叠', async () => {
    const { getReply, act } = await mountSettings()
    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    getReply.mockResolvedValue({ code: 200, message: '', data: { is_running: true } })
    await act('启动全量同步')
    const reads = getReply.mock.calls.length
    hidden.mockReturnValue(true)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(9000)
    expect(getReply).toHaveBeenCalledTimes(reads)
    const pending = createDeferred<APIResponse<unknown>>()
    getReply.mockReturnValueOnce(pending.promise)
    hidden.mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    expect(getReply).toHaveBeenCalledTimes(reads + 1)
    pending.resolve({ code: 200, message: '', data: { is_running: false } })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    expect(getReply).toHaveBeenCalledTimes(reads + 1)
  })
})
