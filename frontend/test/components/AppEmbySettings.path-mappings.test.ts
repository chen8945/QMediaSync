import axios, { AxiosError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppEmbySettings from '@/components/AppEmbySettings.vue'
import type { EmbyPathMapping } from '@/api/emby'
import { httpKey } from '@/http/client'
import { createDeferred } from '../support/deferred'

const originalMapping: EmbyPathMapping = {
  emby_root: '/emby/strm',
  local_root: '/mnt/strm',
  sync_path_id: 9,
}
const mountSettings = async (mappings: unknown = [], syncEnabled = 1) => {
  const storedConfig = { path_mappings: mappings }
  const list = vi.fn(async () => {
    throw new AxiosError('unavailable', 'ERR_NETWORK')
  })
  const save = vi.fn(async () => ({ code: 200, message: '', data: null }))
  const adapter = vi.fn(async (config) => {
    let data: unknown
    if (config.method === 'post') {
      data = await save()
      if ((data as { code: number }).code === 200) {
        const payload = JSON.parse(config.data)
        if (Object.hasOwn(payload, 'path_mappings')) {
          storedConfig.path_mappings = payload.path_mappings
        }
      }
    } else if (config.url.endsWith('/setting/emby-config')) {
      data = {
        code: 200,
        message: '',
        data: {
          exists: true,
          config: {
            emby_url: 'http://emby:8096',
            emby_api_key: 'key',
            sync_enabled: syncEnabled,
            sync_cron: '0 * * * *',
            path_mappings: storedConfig.path_mappings,
          },
        },
      }
    } else if (config.url.endsWith('/sync/path-list')) {
      data = { code: 200, message: '', data: await list() }
    } else {
      data = { code: 200, message: '', data: { is_running: false } }
    }
    return { config, status: 200, statusText: 'OK', headers: {}, data }
  })
  const wrapper = mount(AppEmbySettings, {
    attachTo: document.body,
    global: {
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: { PageHeader: true, RouterLink: true },
    },
  })
  await flushPromises()
  return { wrapper, list, save, adapter, storedConfig }
}
const button = (wrapper: VueWrapper, label: string) => {
  const result = wrapper.findAll('button').find((entry) => entry.text() === label)
  expect(result, `应显示 ${label}`).toBeDefined()
  return result!
}
const click = async (wrapper: VueWrapper, label: string) => {
  await button(wrapper, label).trigger('click')
  await flushPromises()
}
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('Emby 手工 STRM 路径映射入口隐藏', () => {
  it.each([
    { name: '无手工映射', mappings: [] },
    { name: '已有手工映射', mappings: [originalMapping] },
  ])('$name 时不挂载入口，也不加载同步目录列表', async ({ mappings }) => {
    const { wrapper, list } = await mountSettings(mappings)
    expect(wrapper.find('section[aria-label="STRM 路径映射"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('STRM 路径映射')
    expect(list).not.toHaveBeenCalled()
  })

  it.each([0, 1])('同步开关为 %i 时，普通保存不回写页面中的旧映射', async (syncEnabled) => {
    const { wrapper, list, save, adapter, storedConfig } = await mountSettings(
      [originalMapping],
      syncEnabled,
    )
    const latestMappings = [{ emby_root: '/emby/latest', local_root: '/mnt/latest' }]
    storedConfig.path_mappings = latestMappings
    expect(button(wrapper, '保存设置').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('.danger-item .el-switch__input').attributes('disabled') !== undefined).toBe(
      syncEnabled === 0,
    )
    await wrapper.get('input[placeholder="请输入 Emby API Key"]').setValue('updated-key')
    await click(wrapper, '保存设置')
    expect(save).toHaveBeenCalledTimes(1)
    expect(list).not.toHaveBeenCalled()
    const post = adapter.mock.calls.find(([config]) => config.method === 'post')![0]
    expect(JSON.parse(post.data)).toMatchObject({
      emby_api_key: 'updated-key',
      sync_enabled: syncEnabled,
    })
    expect(JSON.parse(post.data)).not.toHaveProperty('path_mappings')
    expect(storedConfig.path_mappings).toEqual(latestMappings)
    expect(wrapper.text()).toContain('保存成功')
  })

  it('无手工映射时正常保存设置', async () => {
    const { wrapper, list, save, adapter, storedConfig } = await mountSettings()
    await click(wrapper, '保存设置')
    expect(save).toHaveBeenCalledTimes(1)
    expect(list).not.toHaveBeenCalled()
    const post = adapter.mock.calls.find(([config]) => config.method === 'post')![0]
    expect(JSON.parse(post.data)).not.toHaveProperty('path_mappings')
    expect(storedConfig.path_mappings).toEqual([])
    expect(wrapper.text()).toContain('保存成功')
  })

  it('保存失败后重试仍保留已有映射', async () => {
    const { wrapper, save, adapter, storedConfig } = await mountSettings([originalMapping])
    save.mockResolvedValueOnce({ code: 500, message: '配置暂时不可写', data: null })
    await click(wrapper, '保存设置')
    expect(wrapper.text()).toContain('配置暂时不可写')
    await click(wrapper, '保存设置')
    expect(save).toHaveBeenCalledTimes(2)
    const posts = adapter.mock.calls.filter(([config]) => config.method === 'post')
    for (const [config] of posts) {
      expect(JSON.parse(config.data)).not.toHaveProperty('path_mappings')
    }
    expect(storedConfig.path_mappings).toEqual([originalMapping])
    expect(wrapper.text()).toContain('保存成功')
  })

  it('保存成功后的回读失败不会清空后续保存中的映射', async () => {
    const { wrapper, save, adapter, storedConfig } = await mountSettings([originalMapping])
    const pending = createDeferred<{ code: number; message: string; data: null }>()
    save.mockReturnValueOnce(pending.promise)
    await click(wrapper, '保存设置')
    adapter.mockRejectedValueOnce(new AxiosError('unavailable', 'ERR_NETWORK'))
    pending.resolve({ code: 200, message: '', data: null })
    await flushPromises()
    expect(wrapper.text()).toContain('Emby 配置已保存，但刷新失败')
    await click(wrapper, '保存设置')
    expect(save).toHaveBeenCalledTimes(2)
    const posts = adapter.mock.calls.filter(([config]) => config.method === 'post')
    for (const [config] of posts) {
      expect(JSON.parse(config.data)).not.toHaveProperty('path_mappings')
    }
    expect(storedConfig.path_mappings).toEqual([originalMapping])
  })

  it('映射响应损坏时仍阻止保存默认空数组', async () => {
    const { wrapper, list, save } = await mountSettings('[]')
    expect(button(wrapper, '保存设置').attributes('disabled')).toBeDefined()
    await click(wrapper, '保存设置')
    expect(save).not.toHaveBeenCalled()
    expect(list).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('加载 Emby 配置失败')
  })
})
