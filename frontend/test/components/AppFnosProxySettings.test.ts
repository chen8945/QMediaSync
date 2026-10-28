import axios, { AxiosError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import AppFnosProxySettings from '@/components/AppFnosProxySettings.vue'
import type { FnosProxySettings } from '@/api/fnosProxy'
import type { APIResponse } from '@/api/types'
import { httpKey } from '@/http/client'
import { createDeferred } from '../support/deferred'

const settings = (): FnosProxySettings => ({
  config: {
    enabled: false,
    fnos_url: '',
    path_mappings: [],
  },
  runtime: {
    running: false,
    listen_port: 0,
    https_enabled: false,
    https_port: 0,
    restart_required: false,
    last_error: '',
  },
})
const button = (wrapper: VueWrapper, label: string) =>
  wrapper.findAll('button').find((entry) => entry.text() === label)!

const mountSettings = async (initial = settings(), failLoad = false) => {
  const read = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: initial,
  })
  if (failLoad) read.mockRejectedValueOnce(new AxiosError('unavailable', 'ERR_NETWORK'))
  const write = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: initial,
  })
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: config.method === 'post' ? await write() : await read(),
  }))
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: '/',
        component: { template: '<div />' },
        meta: { title: '飞牛影视', requiresAuth: false, page: { variant: 'settings' } },
      },
      { path: '/settings/strm', component: { template: '<div />' } },
    ],
  })
  await router.push('/')
  await router.isReady()
  const wrapper = mount(AppFnosProxySettings, {
    attachTo: document.body,
    global: { provide: { [httpKey]: axios.create({ adapter }) }, plugins: [router] },
  })
  await flushPromises()
  const save = async () => {
    await button(wrapper, '保存设置').trigger('click')
    await flushPromises()
  }
  return { wrapper, read, write, adapter, save, router }
}

enableAutoUnmount(afterEach)
afterEach(() => {
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('飞牛影视反代设置', () => {
  it('预加载仅引导到 STRM 设置，不增加独立开关、请求或保存字段', async () => {
    const { wrapper, router, adapter, write } = await mountSettings()
    expect(wrapper.text()).toContain('想减少播放等待？可前往 STRM 设置开启预加载')
    expect(wrapper.findAll('[role="switch"]')).toHaveLength(1)
    const link = wrapper.get('a[href="/settings/strm"]')
    expect(link.text()).toBe('STRM 设置')
    expect(link.attributes('tabindex')).not.toBe('-1')
    await link.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/settings/strm')
    expect(adapter).toHaveBeenCalledTimes(1)
    expect(write).not.toHaveBeenCalled()
  })

  it('默认关闭且首次读取失败禁止编辑和保存，重试后可保存', async () => {
    const { wrapper, write, save } = await mountSettings(settings(), true)
    expect(button(wrapper, '保存设置').attributes('disabled')).toBeDefined()
    expect(wrapper.get('input[aria-label="飞牛影视地址"]').attributes('disabled')).toBeDefined()
    await save()
    expect(write).not.toHaveBeenCalled()
    await button(wrapper, '重试加载').trigger('click')
    await flushPromises()
    expect(button(wrapper, '保存设置').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('input[aria-label="HTTP 端口"]').exists()).toBe(false)
    expect(wrapper.find('input[aria-label="HTTPS 端口"]').exists()).toBe(false)
    expect(wrapper.findAll('.el-switch')).toHaveLength(1)
    expect(wrapper.get('[role="switch"]').attributes('aria-label')).toBe('启用反代')
    await save()
    expect(write).toHaveBeenCalledTimes(1)
  })

  it('保存后更新页头状态，只提交三个配置字段，保存按钮不附带说明面板', async () => {
    const current = settings()
    Object.assign(current.config, { id: 1, created_at: 123, updated_at: 123 })
    const saved = settings()
    saved.config = {
      ...saved.config,
      enabled: true,
      fnos_url: 'http://fnos:8005',
    }
    saved.runtime = { ...saved.runtime, running: true, listen_port: 9005 }
    const { wrapper, write, adapter, read, save } = await mountSettings(current)
    const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    write.mockResolvedValueOnce({ code: 200, message: '', data: saved })
    await wrapper.findAll('.el-switch')[0]!.trigger('click')
    await wrapper.get('input[aria-label="飞牛影视地址"]').setValue('http://fnos:8005')
    await save()
    expect(write).toHaveBeenCalledTimes(1)
    expect(
      JSON.parse(adapter.mock.calls.find(([config]) => config.method === 'post')![0].data),
    ).toEqual(saved.config)
    expect(read).toHaveBeenCalledTimes(1)
    expect(scroll).not.toHaveBeenCalled()
    const feedback = wrapper.get('.settings-feedback')
    expect(feedback.get('.el-alert__title').text()).toBe('保存成功')
    expect(feedback.get('.el-alert__description').text()).toBe('设置已保存并应用')
    expect(feedback.text()).toContain('设置已保存并应用')
    expect(wrapper.get('.runtime-status').text()).toBe('运行中')
    expect(wrapper.find('.restart-notice').exists()).toBe(false)
    const actions = wrapper.get('.fnos-save-actions')
    expect(actions.element.nextElementSibling).toBe(feedback.element)
    expect(wrapper.get('.qms-page-header__stats .runtime-status').text()).toBe('运行中')
    expect(
      wrapper.get('.fnos-status').element.compareDocumentPosition(wrapper.get('form').element) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(wrapper.text()).not.toContain('连接地址示例')
    expect(wrapper.text()).not.toContain('已保存配置')
    expect(button(wrapper, '保存设置').classes()).toEqual(
      expect.arrayContaining(['el-button--success', 'el-button--large']),
    )
    expect(wrapper.find('.responsive-action-bar').exists()).toBe(false)
    expect(actions.text()).toBe('保存设置')
    expect(wrapper.text()).not.toContain('修改设置可能中断当前飞牛播放连接')
    expect(wrapper.text()).toContain('在飞牛影视网页和支持 Emby 连接方式的播放器中播放 STRM 媒体')
    expect(wrapper.text()).not.toContain('重启')
    await feedback.get('.el-alert__close-btn').trigger('click')
    expect(wrapper.find('.settings-feedback').exists()).toBe(false)
  })

  it('已启用时地址无效不能提交，保留填写内容供修正', async () => {
    const current = settings()
    current.config = {
      ...current.config,
      enabled: true,
      fnos_url: 'http://fnos:8005',
    }
    const { wrapper, write, save } = await mountSettings(current)
    await wrapper.get('input[aria-label="飞牛影视地址"]').setValue('fnos:8005')
    await save()
    expect(write).not.toHaveBeenCalled()
    await expect.poll(() => wrapper.text()).toContain('请输入飞牛影视完整服务地址')
    expect(
      (wrapper.get('input[aria-label="飞牛影视地址"]').element as HTMLInputElement).value,
    ).toBe('fnos:8005')
    await wrapper.get('input[aria-label="飞牛影视地址"]').setValue('http://fnos:8005')
    await save()
    expect(write).toHaveBeenCalledTimes(1)
  })

  it('已保存但 HTTPS 应用失败时，按钮旁显示警告并更新顶部状态，再次保存可恢复', async () => {
    const current = settings()
    current.config = {
      ...current.config,
      enabled: true,
      fnos_url: 'http://fnos:8005',
    }
    current.runtime = {
      ...current.runtime,
      running: true,
      listen_port: 9005,
      restart_required: true,
      last_error: 'HTTPS 证书无效',
    }
    const { wrapper, write, read, save } = await mountSettings()
    write.mockResolvedValueOnce({ code: 200, message: '已保存但未能完全应用', data: current })
    await wrapper.find('.el-switch').trigger('click')
    await wrapper.get('input[aria-label="飞牛影视地址"]').setValue('http://fnos:8005')
    await save()
    const feedback = wrapper.get('.settings-feedback')
    expect(
      wrapper
        .findAllComponents({ name: 'ElAlert' })
        .find((alert) => alert.classes().includes('settings-feedback'))!
        .props('type'),
    ).toBe('warning')
    expect(feedback.get('.el-alert__title').text()).toBe('设置未完全应用')
    expect(feedback.get('.el-alert__description').text()).toBe(
      '设置已保存，请检查运行状态后重新保存',
    )
    expect(wrapper.get('.fnos-save-actions').element.nextElementSibling).toBe(feedback.element)
    expect(wrapper.get('.runtime-status').text()).toBe('运行异常')
    expect(wrapper.get('.runtime-error').text()).toContain('HTTPS 证书无效')
    expect(
      (wrapper.get('input[aria-label="飞牛影视地址"]').element as HTMLInputElement).value,
    ).toBe('http://fnos:8005')
    expect(wrapper.text()).not.toContain('重启')
    const recovered = structuredClone(current)
    recovered.runtime = {
      ...recovered.runtime,
      https_enabled: true,
      https_port: 9006,
      restart_required: false,
      last_error: '',
    }
    write.mockResolvedValueOnce({ code: 200, message: '', data: recovered })
    await save()
    expect(wrapper.get('.runtime-status').text()).toBe('运行中')
    expect(wrapper.find('.runtime-error').exists()).toBe(false)
    expect(wrapper.get('.settings-feedback').text()).toContain('设置已保存并应用')
    expect(read).toHaveBeenCalledTimes(1)
    expect(write).toHaveBeenCalledTimes(2)
  })

  it('顶部状态只显示实际运行结果，启用失败时说明原因', async () => {
    const current = settings()
    current.runtime = {
      ...current.runtime,
      running: true,
      listen_port: 9005,
      https_enabled: true,
      https_port: 9006,
    }
    const running = await mountSettings(current)
    expect(running.wrapper.get('.runtime-status').text()).toBe('运行中')
    expect(running.wrapper.text()).not.toContain('连接地址示例')
    const failed = settings()
    failed.runtime.last_error = 'HTTP 端口 9005 已被占用'
    const { wrapper } = await mountSettings(failed)
    expect(wrapper.get('.runtime-status').text()).toBe('未能启用')
    expect(wrapper.text()).toContain('HTTP 端口 9005 已被占用')
    expect(
      wrapper.get('.fnos-status').element.compareDocumentPosition(wrapper.get('form').element) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('保存业务失败保留输入和映射，不显示成功、不回读', async () => {
    const current = settings()
    current.config.path_mappings = [{ fnos_root: '/vol1/strm', local_root: '/media/strm' }]
    const { wrapper, write, read, save } = await mountSettings(current)
    write.mockResolvedValueOnce({ code: 500, message: '设置暂时无法保存', data: null })
    await wrapper.get('input[aria-label="飞牛影视地址"]').setValue('http://new-fnos:8005')
    await save()
    expect(wrapper.text()).toContain('设置暂时无法保存')
    expect(wrapper.text()).not.toContain('设置已保存')
    const feedback = wrapper.get('.settings-feedback')
    expect(feedback.get('.el-alert__title').text()).toBe('保存失败')
    expect(feedback.get('.el-alert__description').text()).toBe('设置暂时无法保存')
    expect(wrapper.get('.fnos-save-actions').element.nextElementSibling).toBe(feedback.element)
    expect(
      (wrapper.get('input[aria-label="飞牛影视地址"]').element as HTMLInputElement).value,
    ).toBe('http://new-fnos:8005')
    expect(
      (
        wrapper.get('input[aria-label="映射 1 QMediaSync 中的 STRM 文件夹"]')
          .element as HTMLInputElement
      ).value,
    ).toBe('/media/strm')
    expect(read).toHaveBeenCalledTimes(1)
  })

  it('尚未保存的开关不冒充实际启用状态', async () => {
    const { wrapper, write } = await mountSettings()
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.get('.runtime-status').text()).toBe('未启用')
    expect(write).not.toHaveBeenCalled()
  })

  it.each(['保存成功', '设置未完全应用', '保存失败'])(
    '按钮位于视口底部时，将%s提示滚入视口',
    async (title) => {
      const { wrapper, write, save } = await mountSettings()
      const result = settings()
      if (title === '设置未完全应用') result.runtime.last_error = 'HTTPS 证书无效'
      write.mockResolvedValueOnce(
        title === '保存失败'
          ? { code: 500, message: '设置暂时无法保存', data: null }
          : { code: 200, message: '', data: result },
      )
      vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (
        this: HTMLElement,
      ) {
        return DOMRect.fromRect({
          y: this.classList.contains('settings-feedback') ? window.innerHeight + 20 : 0,
          width: 400,
          height: 80,
        })
      })
      const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
      await save()
      expect(wrapper.get('.settings-feedback .el-alert__title').text()).toBe(title)
      expect(scroll).toHaveBeenCalledWith({ block: 'nearest' })
      expect(scroll.mock.contexts).toContain(wrapper.get('.settings-feedback').element)
    },
  )

  it('提交期间锁定表单和映射，重复保存不重复请求', async () => {
    const { wrapper, write, save } = await mountSettings()
    const pending = createDeferred<APIResponse<unknown>>()
    write.mockReturnValueOnce(pending.promise)
    await save()
    expect(write).toHaveBeenCalledTimes(1)
    expect(button(wrapper, '添加路径映射').attributes('disabled')).toBeDefined()
    expect(wrapper.get('input[aria-label="飞牛影视地址"]').attributes('disabled')).toBeDefined()
    await save()
    expect(write).toHaveBeenCalledTimes(1)
    pending.resolve({ code: 200, message: '', data: settings() })
    await flushPromises()
    expect(button(wrapper, '添加路径映射').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('input[aria-label="飞牛影视地址"]').attributes('disabled')).toBeUndefined()
  })
})
