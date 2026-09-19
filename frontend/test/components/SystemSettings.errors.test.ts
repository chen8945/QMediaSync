import axios, { AxiosError, AxiosHeaders, CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Component } from 'vue'
import AppStrmSettings from '@/components/AppStrmSettings.vue'
import AppThreadSettings from '@/components/AppThreadSettings.vue'
import AppLogSettings from '@/components/AppLogSettings.vue'
import type { APIResponse } from '@/api/types'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import { createDeferred } from '../support/deferred'

const strmSettings = {
  video_ext_arr: ['.mkv'],
  min_video_size: 0,
  meta_ext_arr: ['.nfo'],
  cron: '0 2 * * *',
  strm_base_url: 'http://qms.local',
  upload_meta: 0,
  download_meta: 1,
  delete_dir: 0,
  multi_playback_enabled: 1,
  local_proxy: 1,
  exclude_name_arr: [],
  exclude_name_regex_arr: [String.raw`\p{UnknownClass}`],
  add_path: 3,
  check_meta_mtime: 0,
}
const threadSettings = {
  download_threads: 2,
  upload_threads: 3,
  file_detail_threads: 4,
  openlist_qps: 3,
  openlist_retry: 2,
  openlist_retry_delay: 45,
  file_list_page_size: 1000,
  url_validity_check_enabled: 0,
  url_validity_check_timeout_seconds: 8,
}
const logSettings = {
  level: 'debug',
  levels: ['debug', 'info', 'warn', 'error'],
  maxSizeMB: 20,
  maxBackups: 5,
  maxAgeDays: 10,
}
const pages = [
  {
    name: 'STRM',
    component: AppStrmSettings,
    settings: strmSettings,
    field: 'STRM 直连地址',
    changed: 'http://changed.local',
    action: '保存 STRM 配置',
    status: '.strm-status',
  },
  {
    name: '线程',
    component: AppThreadSettings,
    settings: threadSettings,
    field: '同时上传任务数量',
    changed: '4',
    action: '保存设置',
    status: '.save-status',
  },
  {
    name: '日志',
    component: AppLogSettings,
    settings: logSettings,
    field: '单文件最大大小',
    changed: '30',
    action: '保存设置',
    status: '.save-status',
  },
]

const mountSettings = async (
  component: Component,
  settings: object,
  loadError?: unknown,
  loadBody?: APIResponse<unknown>,
) => {
  const reply = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: logSettings,
  })
  const cronReply = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: ['2026-09-21 02:00:00'],
  })
  const configReply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue(loadBody ?? { code: 200, message: '', data: settings })
  if (loadError) configReply.mockRejectedValueOnce(loadError)
  const adapter = vi.fn(async (config) => {
    return {
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data:
        config.method === 'post'
          ? await reply()
          : config.url.endsWith('/cron')
            ? await cronReply()
            : await configReply(),
    }
  })
  const wrapper = mount(component, {
    global: { provide: { [httpKey]: axios.create({ adapter }) }, stubs: { PageHeader: true } },
  })
  await flushPromises()
  const act = async (label: string) => {
    const button = wrapper.findAll('button').find((item) => item.text() === label)
    expect(button, `${label} 按钮应存在`).toBeDefined()
    await button!.trigger('click')
    await flushPromises()
  }
  return { wrapper, reply, cronReply, configReply, adapter, act }
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
    config: { method: 'post', url: '/api/setting/test?token=token-secret#fragment' },
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

describe.each(pages)(
  '$name 设置失败反馈',
  ({ component, settings, field, changed, action, status }) => {
    it('HTTP 200 业务失败保留输入且不显示成功，日志只含安全诊断', async () => {
      const { wrapper, reply, act } = await mountSettings(component, settings)
      const input = fieldInput(wrapper, field)
      await input.setValue(changed)
      reply.mockResolvedValueOnce({
        code: 500,
        message: '配置暂时不可写，请稍后重试',
        data: logSettings,
      })

      await act(action)

      expect(wrapper.get(status).text()).toContain('失败')
      expect(wrapper.get(status).text()).not.toMatch(/保存成功|已保存/)
      expect(wrapper.get(status).text()).toContain('配置暂时不可写，请稍后重试')
      expect((input.element as HTMLInputElement).value).toBe(changed)
      expect(input.attributes('disabled')).toBeUndefined()
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(console.error).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ method: 'POST', status: 200 }),
      )
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
    })

    it.each([
      ['REQUEST_ORIGIN_INVALID', '访问地址校验失败'],
      ['CSRF_TOKEN_INVALID', '请求安全校验失败'],
    ])('保存被 %s 拒绝时只展示可关闭提示条', async (code, message) => {
      const { wrapper, reply, act } = await mountSettings(component, settings)
      reply.mockRejectedValueOnce(rejectedRequest(code))
      await act(action)
      expect(wrapper.get(status).text()).toContain(message)
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('token-secret')
      await wrapper.get(`${status} .el-alert__close-btn`).trigger('click')
      expect(wrapper.find(status).exists()).toBe(false)
    })

    it('读取业务失败不会写入失败响应附带的配置', async () => {
      const { wrapper, adapter } = await mountSettings(component, settings, undefined, {
        code: 500,
        message: '配置暂时不可读，请稍后重试',
        data: settings,
      })
      expect(wrapper.get(status).text()).toContain('失败')
      expect((fieldInput(wrapper, field).element as HTMLInputElement).value).not.toBe(
        String(
          'strm_base_url' in settings
            ? settings.strm_base_url
            : 'upload_threads' in settings
              ? settings.upload_threads
              : settings.maxSizeMB,
        ),
      )
      expect(wrapper.get(status).text()).toContain('配置暂时不可读，请稍后重试')
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(adapter).toHaveBeenCalledTimes(1)
    })

    it('首次读取失败后编辑及关闭提示仍不能保存，重载成功才允许写入', async () => {
      const { wrapper, reply, act } = await mountSettings(
        component,
        settings,
        new AxiosError('unavailable', 'ERR_NETWORK'),
      )
      const save = () => wrapper.findAll('button').find((button) => button.text() === action)!
      await fieldInput(wrapper, field).setValue(changed)
      await wrapper.get(`${status} .el-alert__close-btn`).trigger('click')
      expect(save().attributes('disabled')).toBeDefined()
      wrapper
        .findAllComponents({ name: 'ElButton' })
        .find((button) => button.text() === action)!
        .vm.$emit('click')
      await flushPromises()
      expect(reply).not.toHaveBeenCalled()
      await act('重新加载')
      expect(save().attributes('disabled')).toBeUndefined()
      await act(action)
      expect(reply).toHaveBeenCalledTimes(1)
    })

    it('取消和已处理认证失效在保存及读取时保持静默', async () => {
      const { wrapper, reply, act } = await mountSettings(component, settings)
      const handled = rejectedRequest('SESSION_INVALID', 401)
      markAuthInvalidationHandled(handled)
      for (const failure of [new CanceledError(), handled]) {
        reply.mockRejectedValueOnce(failure)
        await act(action)
        expect(wrapper.find(status).exists()).toBe(false)
        const loaded = await mountSettings(component, settings, failure)
        expect(loaded.wrapper.find(status).exists()).toBe(false)
      }
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(console.error).not.toHaveBeenCalled()
    })

    it('写入超时提示结果未确认，不自动重发', async () => {
      const { wrapper, reply, act } = await mountSettings(component, settings)
      reply.mockRejectedValueOnce(
        new AxiosError('timeout secret', 'ETIMEDOUT', {
          method: 'post',
          headers: new AxiosHeaders(),
        }),
      )
      await act(action)
      expect(wrapper.get(status).text()).toContain('操作结果尚未确认')
      expect(reply).toHaveBeenCalledTimes(1)
    })

    it('先前成功的计时器不会清除新的失败提示', async () => {
      const { wrapper, reply, act } = await mountSettings(component, settings)
      await act(action)
      expect(wrapper.get(status).text()).toMatch(/保存成功|已保存/)
      reply.mockRejectedValueOnce(rejectedRequest('REQUEST_ORIGIN_INVALID'))
      await act(action)
      await vi.advanceTimersByTimeAsync(6000)
      expect(wrapper.get(status).text()).toContain('访问地址校验失败')
    })
  },
)

describe('STRM 校验与 Cron', () => {
  it('后端正则校验显示安全规则位置，保留原文，不公开内部细节', async () => {
    const { wrapper, reply, act } = await mountSettings(AppStrmSettings, strmSettings)
    reply.mockRejectedValueOnce(
      new HttpResponseError({
        status: 400,
        data: {
          code: 500,
          message: 'exclude_name_regex_arr[0]：正则表达式无效：error parsing regexp: secret',
          data: null,
        },
      }),
    )
    await act('保存 STRM 配置')
    expect(wrapper.get('.strm-status').text()).toContain('正则排除名称第 1 条：正则表达式无效')
    expect(wrapper.get('.strm-regex-input .el-tag code').element.textContent).toBe(
      strmSettings.exclude_name_regex_arr[0],
    )
    expect(wrapper.text()).not.toContain('secret')
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
  })

  it('Cron 业务失败清除旧执行时间，在字段显示原因并保留保存反馈', async () => {
    const { wrapper, cronReply, act } = await mountSettings(AppStrmSettings, strmSettings)
    expect(wrapper.text()).toContain('2026-09-21 02:00:00')
    await act('保存 STRM 配置')
    cronReply.mockResolvedValue({
      code: 500,
      message: 'cron：仅支持 5 位 cron 表达式或 robfig 描述符',
      data: ['2030-01-01'],
    })
    await fieldInput(wrapper, '定时同步表达式').setValue('bad cron')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(100)
    expect(wrapper.text()).not.toContain('2026-09-21 02:00:00')
    expect(wrapper.text()).not.toContain('2030-01-01')
    expect(wrapper.get('.el-form-item__error').text()).toContain(
      '仅支持 5 位 Cron 表达式或 robfig 描述符',
    )
    expect(wrapper.get('.strm-status').text()).toContain('STRM 配置已保存')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('快速输入只查询最后一次，编辑和清空立即丢弃旧 Cron 响应', async () => {
    const { wrapper, cronReply } = await mountSettings(AppStrmSettings, strmSettings)
    const cron = fieldInput(wrapper, '定时同步表达式')
    const staleSuccess = createDeferred<APIResponse<unknown>>()
    cronReply.mockReturnValueOnce(staleSuccess.promise)
    await cron.setValue('0 3 * * *')
    await vi.advanceTimersByTimeAsync(300)
    const staleFailure = createDeferred<APIResponse<unknown>>()
    cronReply.mockReturnValueOnce(staleFailure.promise)
    await cron.setValue('0 4 * * *')
    staleSuccess.resolve({ code: 200, message: '', data: ['过期时间'] })
    await flushPromises()
    expect(wrapper.text()).not.toContain('过期时间')
    await vi.advanceTimersByTimeAsync(100)
    await cron.setValue('0 5 * * *')
    await vi.advanceTimersByTimeAsync(299)
    expect(cronReply).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(cronReply).toHaveBeenCalledTimes(3)
    await cron.setValue('')
    staleFailure.resolve({ code: 500, message: '过期查询失败', data: null })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(300)
    expect(cronReply).toHaveBeenCalledTimes(3)
    expect(wrapper.find('.cron-next-times').exists()).toBe(false)
    expect(wrapper.find('.strm-status').exists()).toBe(false)
    expect(console.error).not.toHaveBeenCalled()
  })
})

describe('日志生效配置', () => {
  it('成功保存后按返回值回读配置并维持原有数字规范化', async () => {
    const { wrapper, reply, act } = await mountSettings(AppLogSettings, logSettings)
    const input = fieldInput(wrapper, '单文件最大大小')
    await input.setValue('30')
    reply.mockResolvedValueOnce({
      code: 200,
      message: '',
      data: { level: 'warn', maxSizeMB: 25, maxBackups: 8, maxAgeDays: 20 },
    })
    await act('保存设置')
    expect((input.element as HTMLInputElement).value).toBe('25')
    expect((fieldInput(wrapper, '保留备份数').element as HTMLInputElement).value).toBe('8')
    expect((fieldInput(wrapper, '保留天数').element as HTMLInputElement).value).toBe('20')
    expect(wrapper.get('.el-radio-button.is-active input').attributes('value')).toBe('warn')
    expect(wrapper.get('.save-status').text()).toContain('保存成功')
  })
})
