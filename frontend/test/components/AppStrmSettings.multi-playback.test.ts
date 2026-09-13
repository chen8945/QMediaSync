// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElFormItem } from 'element-plus'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AppStrmSettings from '@/components/AppStrmSettings.vue'
import { httpKey } from '@/http/client'

const wrappers: VueWrapper[] = []

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
})

function formItem(wrapper: VueWrapper, field: string) {
  const item = wrapper.findAllComponents(ElFormItem).find((item) => item.props('prop') === field)
  expect(item, field + ' 控件应存在').toBeDefined()
  return item!
}

describe('115 多端播放配置', () => {
  it.each([0, 1])('本地代理初始值为 %i 时，置灰保留选择并保存原值', async (initialProxy) => {
    const config = {
      video_ext_arr: ['.mkv'],
      meta_ext_arr: ['.nfo'],
      exclude_name_arr: [],
      exclude_name_regex_arr: [],
      min_video_size: 0,
      cron: '0 * * * *',
      strm_base_url: 'http://qms.local',
      upload_meta: 0,
      download_meta: 0,
      delete_dir: 0,
      local_proxy: initialProxy,
      multi_playback_enabled: 1,
      add_path: 3,
      check_meta_mtime: 0,
    }
    const http = {
      get: vi.fn(async (url: string) => ({
        data: { code: 200, data: url.endsWith('/setting/cron') ? [] : config },
      })),
      post: vi.fn(async () => ({ data: { code: 200 } })),
    }
    const wrapper = mount(AppStrmSettings, {
      attachTo: document.body,
      global: { provide: { [httpKey]: http }, stubs: { PageHeader: true } },
    })
    wrappers.push(wrapper)
    await flushPromises()

    const fields = wrapper.findAllComponents(ElFormItem).map((item) => item.props('prop'))
    expect(fields.indexOf('multi_playback_enabled') + 1).toBe(fields.indexOf('local_proxy'))
    const multi = formItem(wrapper, 'multi_playback_enabled')
    const proxy = formItem(wrapper, 'local_proxy')
    const enabled = multi.get<HTMLInputElement>('input[value="1"]')
    expect(enabled.element.checked).toBe(true)
    expect(enabled.element.disabled).toBe(initialProxy === 1)

    await proxy.get('input[value="1"]').setValue(true)
    expect(enabled.element.disabled).toBe(true)
    expect(enabled.element.checked).toBe(true)
    expect(multi.text()).toContain('115 不会感知多端，此开关自动失效并保留原设置')
    expect(multi.text()).toContain('使用 8095 端口（Emby 代理）播放时仍按本开关执行')
    const save = wrapper.findAll('button').find((button) => button.text() === '保存 STRM 配置')!
    await save.trigger('click')
    await flushPromises()
    expect(http.post).toHaveBeenCalledWith(
      expect.stringContaining('/setting/strm-config'),
      expect.objectContaining({ local_proxy: 1, multi_playback_enabled: 1 }),
      expect.anything(),
    )

    await proxy.get('input[value="0"]').setValue(true)
    expect(enabled.element.disabled).toBe(false)
    expect(enabled.element.checked).toBe(true)
    await multi.get('input[value="0"]').setValue(true)
    await save.trigger('click')
    await flushPromises()
    expect(http.post).toHaveBeenLastCalledWith(
      expect.stringContaining('/setting/strm-config'),
      expect.objectContaining({ local_proxy: 0, multi_playback_enabled: 0 }),
      expect.anything(),
    )
  })
})
