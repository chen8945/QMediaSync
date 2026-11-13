import axios from 'axios'
import { createRouter, createMemoryHistory } from 'vue-router'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppMediaLibrarySettings from '@/views/settings/AppMediaLibrarySettings.vue'
import MediaServerSettings from '@/components/media-library/MediaServerSettings.vue'
import MediaSyncStatus from '@/components/media-library/MediaSyncStatus.vue'
import { httpKey } from '@/http/client'
import { createDeferred } from '../support/deferred'
import type { APIResponse } from '@/api/types'
import { ElMessage } from 'element-plus'
import { copyText } from '@/utils/clipboard'

vi.mock('@/utils/clipboard', () => ({ copyText: vi.fn() }))

const setup = async (hash = '#emby', existing = true) => {
  const configs: Record<string, Record<string, unknown>> = {
    emby: {
      enabled: existing,
      emby_url: existing ? 'http://emby:8096' : '',
      emby_api_key: existing ? 'emby-key' : '',
      enable_media_notification: existing ? 1 : 0,
      sync_enabled: 1,
    },
    jellyfin: {
      enabled: false,
      jellyfin_url: '',
      jellyfin_api_key: '',
      enable_media_notification: 0,
      sync_enabled: 1,
    },
  }
  const saveReply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue({ code: 200, message: '', data: { warnings: [], restart_required: false } })
  const statusReply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue({ code: 200, message: '', data: { is_running: false } })
  const adapter = vi.fn(async (config) => {
    const product = config.url.includes('jellyfin') ? 'jellyfin' : 'emby'
    let data: APIResponse<unknown>
    if (config.method === 'post') {
      data = await saveReply()
      if (data.code === 200 && config.url.endsWith('-config'))
        configs[product] = JSON.parse(config.data)
    } else if (config.url.endsWith('-config'))
      data = { code: 200, message: '', data: { exists: true, config: configs[product] } }
    else if (config.url.endsWith('/fnos-proxy'))
      data = {
        code: 200,
        message: '',
        data: {
          config: { enabled: false, fnos_url: '', path_mappings: [] },
          runtime: { running: false, restart_required: false, last_error: '' },
        },
      }
    else if (config.url.endsWith('/libraries')) data = { code: 200, message: '', data: [] }
    else if (config.url.endsWith('/sync/status')) data = await statusReply()
    else data = { code: 200, message: '', data: { is_running: false } }
    return { config, status: 200, statusText: 'OK', headers: {}, data }
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: '/settings/media-library',
        component: AppMediaLibrarySettings,
        meta: { title: '媒体库', requiresAuth: true },
      },
    ],
  })
  await router.push(`/settings/media-library?keep=1${hash}`)
  const wrapper = mount(AppMediaLibrarySettings, {
    global: {
      plugins: [router],
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: { PageHeader: true, RouterLink: true },
    },
  })
  await flushPromises()
  const panel = (product: string) =>
    wrapper
      .findAllComponents(MediaServerSettings)
      .find((item) => item.props('serverType') === product)!
  const tab = async (label: string) => {
    await wrapper
      .findAll('[role="tab"]')
      .find((item) => item.text() === label)!
      .trigger('click')
    await flushPromises()
  }
  return { wrapper, router, configs, adapter, saveReply, statusReply, panel, tab }
}
const act = async (wrapper: VueWrapper, text: string) => {
  await wrapper
    .findAll('button')
    .find((item) => item.text() === text)!
    .trigger('click')
  await flushPromises()
}
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.restoreAllMocks()
})

describe('媒体库标签与独立表单', () => {
  it('首次访问才挂载，切换保留各端草稿、开关与 query，不重读配置', async () => {
    const { wrapper, router, panel, tab, adapter } = await setup()
    expect(wrapper.findAllComponents(MediaServerSettings)).toHaveLength(1)
    await panel('emby').get('input[aria-label="Emby API Key"]').setValue('emby-draft')
    await tab('Jellyfin')
    expect(router.currentRoute.value.hash).toBe('#jellyfin')
    expect(router.currentRoute.value.query).toEqual({ keep: '1' })
    expect(panel('jellyfin').get('input[aria-label="Jellyfin API Key"]').element).toHaveProperty(
      'value',
      '',
    )
    expect(panel('jellyfin').text()).not.toContain('302')
    expect(panel('jellyfin').text()).not.toContain('Emby')
    await panel('jellyfin').get('input[aria-label="Jellyfin API Key"]').setValue('jelly-draft')
    await tab('Emby')
    expect(panel('emby').get('input[aria-label="Emby API Key"]').element).toHaveProperty(
      'value',
      'emby-draft',
    )
    await tab('Jellyfin')
    expect(panel('jellyfin').get('input[aria-label="Jellyfin API Key"]').element).toHaveProperty(
      'value',
      'jelly-draft',
    )
    expect(
      adapter.mock.calls.filter(
        ([config]) => config.method === 'get' && config.url.endsWith('-config'),
      ),
    ).toHaveLength(2)
    await tab('飞牛影视')
    expect(wrapper.find('[aria-label="飞牛影视运行状态"]').exists()).toBe(true)
    expect(wrapper.findAllComponents(MediaServerSettings)).toHaveLength(2)
  })

  it('隐藏期间保存正常收尾，保留应用警告和待重启，回读失败独立显示，原值可重试', async () => {
    const { panel, tab, saveReply, adapter } = await setup()
    const pending = createDeferred<APIResponse<unknown>>()
    saveReply.mockReturnValueOnce(pending.promise)
    await act(panel('emby'), '保存设置')
    await tab('Jellyfin')
    await panel('jellyfin').get('input[aria-label="Jellyfin API Key"]').setValue('independent')
    // Fail only the committed save's readback, without touching the other panel.
    adapter.mockRejectedValueOnce(new Error('read unavailable'))
    pending.resolve({
      code: 200,
      message: '配置已保存',
      data: { warnings: ['调度未应用，请重新保存'], restart_required: true },
    })
    await flushPromises()
    await tab('Emby')
    expect(panel('emby').get('.emby-status-alert').text()).toContain('调度未应用，请重新保存')
    expect(panel('emby').get('.emby-status-alert').text()).toContain('需重启')
    expect(panel('emby').get('.readback-failure').text()).toContain('已保存，但刷新失败')
    const save = panel('emby')
      .findAll('button')
      .find((item) => item.text() === '保存设置')!
    expect(save.attributes('disabled')).toBeUndefined()
    await act(panel('emby'), '保存设置')
    expect(saveReply).toHaveBeenCalledTimes(2)
    expect(panel('emby').text()).not.toContain('调度未应用')
    expect(panel('emby').find('.readback-failure').exists()).toBe(false)
    expect(panel('emby').get('.emby-status-alert').text()).toContain('保存成功')
    await tab('Jellyfin')
    expect(panel('jellyfin').get('input[aria-label="Jellyfin API Key"]').element).toHaveProperty(
      'value',
      'independent',
    )
  })

  it('迟到的同步状态错误与保存警告、重启要求和回读错误分别展示，恢复只清状态错误', async () => {
    const { panel, tab, saveReply, statusReply, adapter } = await setup()
    const emby = panel('emby')
    const saved = createDeferred<APIResponse<unknown>>()
    const status = createDeferred<void>()
    saveReply.mockReturnValueOnce(saved.promise)
    statusReply.mockImplementationOnce(async () => {
      await status.promise
      throw new axios.AxiosError('private-status-key', 'ERR_NETWORK')
    })
    await act(emby, '保存设置')
    adapter.mockRejectedValueOnce(new axios.AxiosError('private-readback-key', 'ERR_NETWORK'))
    saved.resolve({
      code: 200,
      message: '配置已保存',
      data: { warnings: ['调度未应用，请重新保存'], restart_required: true },
    })
    await flushPromises()
    expect(statusReply).toHaveBeenCalledTimes(2)
    expect(emby.get('.emby-status-alert').text()).toContain('调度未应用，请重新保存')
    const save = emby.findAll('button').find((item) => item.text() === '保存设置')!
    expect(save.attributes('disabled')).toBeUndefined()

    status.resolve()
    await flushPromises()
    const saveFeedback = emby.get('.emby-status-alert').text()
    const readbackFailure = emby.get('.readback-failure').text()
    expect(saveFeedback).toContain('调度未应用，请重新保存')
    expect(saveFeedback).toContain('需重启')
    expect(readbackFailure).toContain('配置已保存，但刷新失败')
    const sync = emby.getComponent(MediaSyncStatus)
    expect(sync.get('[role="alert"]').text()).toContain('查询 Emby 同步状态失败')
    expect(sync.get('[role="alert"]').text()).toContain('无法获取服务器响应')
    expect(emby.text()).not.toContain('private-')
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private-')

    await tab('Jellyfin')
    await tab('Emby')
    expect(sync.find('[role="alert"]').exists()).toBe(false)
    expect(emby.get('.emby-status-alert').text()).toBe(saveFeedback)
    expect(emby.get('.readback-failure').text()).toBe(readbackFailure)
  })

  it('服务端新默认关闭，显式启用与关闭分别提交，禁用保留连接和分项设置', async () => {
    const { panel, tab, configs, adapter } = await setup('#jellyfin', false)
    const jelly = panel('jellyfin')
    expect(jelly.get('input[aria-label="启用 Jellyfin"]').element).toHaveProperty('checked', false)
    expect(jelly.get('input[aria-label="发送媒体通知"]').element).toHaveProperty('checked', false)
    await jelly.get('input[aria-label="Jellyfin 服务器地址"]').setValue('http://jellyfin:8096/base')
    await jelly.get('input[aria-label="Jellyfin API Key"]').setValue('jelly-key')
    await jelly.get('input[aria-label="启用 Jellyfin"]').setValue(true)
    await jelly.get('input[aria-label="发送媒体通知"]').setValue(true)
    await act(jelly, '保存设置')
    expect(configs.jellyfin).toMatchObject({
      enabled: true,
      enable_media_notification: 1,
      jellyfin_url: 'http://jellyfin:8096/base',
      jellyfin_api_key: 'jelly-key',
    })
    expect(configs.jellyfin).not.toHaveProperty('emby_url')
    expect(configs.jellyfin).not.toHaveProperty('path_mappings')
    await jelly.get('input[aria-label="启用 Jellyfin"]').setValue(false)
    await jelly.get('input[aria-label="发送媒体通知"]').setValue(false)
    await act(jelly, '保存设置')
    expect(configs.jellyfin).toMatchObject({
      enabled: false,
      enable_media_notification: 0,
      jellyfin_api_key: 'jelly-key',
      sync_enabled: 1,
    })
    expect(
      jelly
        .findAll('button')
        .find((item) => item.text() === '提取媒体信息')!
        .attributes('disabled'),
    ).toBeDefined()
    await tab('Emby')
    expect(panel('emby').get('input[aria-label="Emby API Key"]').element).toHaveProperty(
      'value',
      '',
    )
    expect(adapter.mock.calls.every(([config]) => !/proxy|capabilit|probe/.test(config.url))).toBe(
      true,
    )
  })

  it('产品错配按服务端原因反馈，未知产品不加客户端门禁，操作使用已保存配置', async () => {
    const { panel, saveReply, adapter } = await setup()
    await panel('emby').get('input[aria-label="Emby 服务器地址"]').setValue('http://unsaved:8096')
    saveReply.mockResolvedValueOnce({
      code: 500,
      message: '连接的是 Jellyfin，请手动调整服务器配置',
      data: null,
    })
    await act(panel('emby'), '提取媒体信息')
    expect(panel('emby').text()).toContain('请手动调整服务器配置')
    expect(adapter.mock.calls.at(-1)?.[0].data).toBeUndefined()
    await act(panel('emby'), '提取媒体信息')
    expect(panel('emby').text()).toContain('触发提取媒体信息成功')
    expect(panel('emby').get('input[aria-label="Emby 服务器地址"]').element).toHaveProperty(
      'value',
      'http://unsaved:8096',
    )
  })

  it('通知链接绑定本端，页面卸载后复制结果不弹出迟到提示', async () => {
    const { panel, wrapper } = await setup('#jellyfin')
    const pending = createDeferred<boolean>()
    vi.mocked(copyText).mockReturnValueOnce(pending.promise)
    const success = vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
    await act(panel('jellyfin'), '复制')
    expect(copyText).toHaveBeenCalledWith(`${window.location.origin}/jellyfin/webhook`)
    wrapper.unmount()
    pending.resolve(true)
    await flushPromises()
    expect(success).not.toHaveBeenCalled()
  })

  it('深链与无效 hash 正确定位，状态显示模式/格式化时间并隐藏内部错误', async () => {
    const { router, wrapper } = await setup('#unknown')
    expect(wrapper.get('[role="tab"][aria-selected="true"]').text()).toBe('Emby')
    await router.replace('/settings/media-library#jellyfin')
    await flushPromises()
    expect(wrapper.get('[role="tab"][aria-selected="true"]').text()).toBe('Jellyfin')
    const sync = mount(MediaSyncStatus, {
      props: {
        productName: 'Jellyfin',
        starting: false,
        disabled: false,
        syncInfo: {
          is_running: true,
          sync_mode: 'refresh_library',
          last_sync_time: 1700000000,
          last_success_sync_mode: 'incremental',
          last_error: 'GET http://secret?api_key=private-key failed',
        },
        isSyncRunning: true,
      },
    })
    expect(sync.text()).toContain('刷新 Jellyfin 媒体库')
    expect(sync.text()).toContain('增量同步 Jellyfin 条目')
    expect(sync.text()).not.toContain('1700000000')
    expect(sync.text()).not.toContain('private-key')
    expect(sync.get('button').attributes('disabled')).toBeDefined()
  })
})
