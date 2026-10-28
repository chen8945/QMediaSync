// @vitest-environment happy-dom
import axios, { AxiosError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AppStrmSettings from '@/components/AppStrmSettings.vue'
import type { StrmSettings } from '@/api/systemSettings'
import type { APIResponse } from '@/api/types'
import { httpKey } from '@/http/client'
import { createDeferred } from '../support/deferred'

const settings: StrmSettings = {
  video_ext_arr: ['.mkv'],
  min_video_size: 0,
  meta_ext_arr: ['.nfo'],
  cron: '0 * * * *',
  strm_base_url: 'http://qms.local',
  upload_meta: 0,
  download_meta: 0,
  delete_dir: 0,
  strm_preload_enabled: 0,
  multi_playback_enabled: 0,
  local_proxy: 0,
  exclude_name_arr: [],
  exclude_name_regex_arr: [],
  add_path: 3,
  check_meta_mtime: 0,
}
const originalWidth = window.innerWidth
const preloadItem = (wrapper: VueWrapper) =>
  wrapper
    .findAll('.el-form-item')
    .find((item) => item.find('.el-form-item__label').text() === '启用 STRM 预加载')!
const saveButton = (wrapper: VueWrapper) =>
  wrapper.findAll('button').find((button) => button.text() === '保存 STRM 配置')!

function mountSettings(
  initial: Partial<StrmSettings> = settings,
  failLoad = false,
  initialRead?: Promise<APIResponse<unknown>>,
) {
  const read = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: initial,
  })
  if (initialRead) read.mockReturnValueOnce(initialRead)
  if (failLoad) read.mockRejectedValueOnce(new AxiosError('unavailable', 'ERR_NETWORK'))
  const write = vi.fn<() => Promise<APIResponse<null>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: null,
  })
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data:
      config.method === 'post'
        ? await write()
        : config.url.endsWith('/cron')
          ? { code: 200, data: [] }
          : await read(),
  }))
  const wrapper = mount(AppStrmSettings, {
    attachTo: document.body,
    global: {
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: { PageHeader: true },
    },
  })
  const save = async () => {
    await saveButton(wrapper).trigger('click')
    await flushPromises()
  }
  const payloads = () =>
    adapter.mock.calls
      .filter(([config]) => config.method === 'post')
      .map(([config]) => JSON.parse(config.data) as StrmSettings)
  return { wrapper, read, write, save, payloads }
}

enableAutoUnmount(afterEach)
afterEach(() => {
  window.innerWidth = originalWidth
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('STRM 预加载设置', () => {
  it('首次读取前默认关闭并禁止提交，读取成功后显示已保存的选择', async () => {
    const pending = createDeferred<APIResponse<unknown>>()
    const initial: StrmSettings = { ...settings, strm_preload_enabled: 1 }
    const { wrapper, write } = mountSettings(initial, false, pending.promise)
    await flushPromises()
    expect(preloadItem(wrapper).get<HTMLInputElement>('input[value="0"]').element.checked).toBe(
      true,
    )
    expect(preloadItem(wrapper).get<HTMLInputElement>('input[value="1"]').element.disabled).toBe(
      true,
    )
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()
    expect(write).not.toHaveBeenCalled()
    pending.resolve({ code: 200, message: '', data: initial })
    await flushPromises()
    expect(preloadItem(wrapper).get<HTMLInputElement>('input[value="1"]').element.checked).toBe(
      true,
    )
  })

  it('旧响应缺少字段时按关闭显示并提交，不额外请求其他配置', async () => {
    const legacy: Partial<StrmSettings> = { ...settings }
    delete legacy.strm_preload_enabled
    const { wrapper, read, save, payloads } = mountSettings(legacy)
    await flushPromises()
    expect(preloadItem(wrapper).get<HTMLInputElement>('input[value="0"]').element.checked).toBe(
      true,
    )
    await save()
    expect(payloads()).toEqual([{ ...settings, strm_preload_enabled: 0 }])
    expect(read).toHaveBeenCalledTimes(1)
  })

  it.each([0, 1] as const)('已保存值为 %i 时可切换并沿用原设置接口保存', async (initial) => {
    const { wrapper, save, payloads } = mountSettings({
      ...settings,
      strm_preload_enabled: initial,
    })
    await flushPromises()
    const target = initial === 0 ? 1 : 0
    await preloadItem(wrapper).get(`input[value="${target}"]`).setValue(true)
    await save()
    expect(payloads()).toEqual([{ ...settings, strm_preload_enabled: target }])
    expect(wrapper.get('.strm-status').text()).toContain('STRM 配置已保存')
    expect(
      preloadItem(wrapper).get<HTMLInputElement>(`input[value="${target}"]`).element.checked,
    ).toBe(true)
  })

  it('保存失败本地化字段错误、保留选择且不自动重发', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const { wrapper, write, save, payloads } = mountSettings()
    await flushPromises()
    write.mockResolvedValueOnce({
      code: 500,
      message: 'strm_preload_enabled：不是允许的取值',
      data: null,
    })
    await preloadItem(wrapper).get('input[value="1"]').setValue(true)
    await save()
    expect(wrapper.get('.strm-status').text()).toContain('保存 STRM 配置失败')
    expect(wrapper.get('.strm-status').text()).toContain('STRM 预加载设置无效')
    expect(wrapper.get('.strm-status').text()).not.toContain('strm_preload_enabled')
    expect(preloadItem(wrapper).get<HTMLInputElement>('input[value="1"]').element.checked).toBe(
      true,
    )
    expect(write).toHaveBeenCalledTimes(1)
    expect(payloads()[0]?.strm_preload_enabled).toBe(1)
  })

  it('初次加载失败不能保存，重试成功后可继续操作', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const { wrapper, write, save } = mountSettings(settings, true)
    await flushPromises()
    await save()
    expect(write).not.toHaveBeenCalled()
    expect(wrapper.get('.strm-status').text()).toContain('加载 STRM 配置失败')
    await wrapper
      .findAll('button')
      .find((button) => button.text() === '重新加载')!
      .trigger('click')
    await flushPromises()
    expect(saveButton(wrapper).attributes('disabled')).toBeUndefined()
    await save()
    expect(write).toHaveBeenCalledTimes(1)
  })

  it('窄屏提供可聚焦的选择和通俗说明，不显示内部参数', async () => {
    window.innerWidth = 390
    const { wrapper } = mountSettings()
    await flushPromises()
    const item = preloadItem(wrapper)
    expect(wrapper.get('form').classes()).toContain('el-form--label-top')
    const group = item.get('[role="radiogroup"]')
    expect(group.attributes('aria-labelledby')).toBe(item.get('label').attributes('id'))
    const selected = item.get<HTMLInputElement>('input[value="0"]')
    expect(selected.element.tabIndex).toBe(0)
    selected.element.focus()
    expect(document.activeElement).toBe(selected.element)
    expect(item.text()).toContain('减少点击播放和接着看下一集时的等待')
    expect(item.text()).toContain('默认关闭，会增加少量网盘请求')
    expect(item.text()).toContain('部分播放器无法提前准备，仍可正常点击播放')
    expect(item.text()).not.toMatch(/generation|配额|SQL|UA|锁/)
    await item.get('input[value="1"]').setValue(true)
    expect(item.get<HTMLInputElement>('input[value="1"]').element.checked).toBe(true)
  })
})
