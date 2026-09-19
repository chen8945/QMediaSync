import axios, { CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppUpdate from '@/components/AppUpdate.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { APIResponse } from '@/api/types'

const setup = async (platformError?: unknown, fnos = false) => {
  const reply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue({ code: 200, message: '', data: null })
  let running = false
  const adapter = vi.fn(async (config) => {
    let data: unknown
    if (config.method === 'post') {
      data = await reply()
      if (config.url.endsWith('/to-version') && (data as APIResponse<unknown>).code === 200)
        running = true
    } else if (config.url.endsWith('/version')) {
      data = { version: 'v1.0.0', date: '', isWindows: false, isRelease: true }
    } else if (config.url.endsWith('/is-fn-os')) {
      if (platformError) throw platformError
      data = { code: 200, data: fnos }
    } else if (config.url.includes('/update/last')) {
      data = {
        code: 200,
        data: [
          { version: 'v1.2.3', date: '', note: 'Release', url: 'https://example.test/update.zip' },
        ],
      }
    } else {
      data = running
        ? {
            code: 200,
            data: { status: 'downloading', progress: 10, total_size: 100, downloaded: 10 },
          }
        : { code: 500, message: '未开始更新', data: null }
    }
    return { config, status: 200, statusText: 'OK', headers: {}, data }
  })
  const wrapper = mount(AppUpdate, {
    global: { provide: { [httpKey]: axios.create({ adapter }) }, stubs: { PageHeader: true } },
  })
  await flushPromises()
  const click = async (label: string) => {
    const button = wrapper.findAll('button').find((button) => button.text() === label)
    expect(button, label).toBeDefined()
    await button!.trigger('click')
    await flushPromises()
  }
  return { wrapper, reply, adapter, click }
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('更新页面请求反馈', () => {
  it('初始化无更新任务保持静默，启动业务失败不开始进度轮询', async () => {
    const { wrapper, reply, adapter, click } = await setup()
    expect(ElMessage.error).not.toHaveBeenCalled()
    reply.mockResolvedValueOnce({ code: 500, message: 'private secret', data: null })
    adapter.mockClear()
    await click('在线更新')
    await vi.advanceTimersByTimeAsync(2500)
    expect(adapter.mock.calls).toHaveLength(1)
    expect(wrapper.find('.update-progress').exists()).toBe(false)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('触发版本更新失败')
    expect(wrapper.text()).toContain('v1.2.3')
  })

  it('取消业务失败保留运行进度与取消按钮，手动下载保持原链接', async () => {
    const { wrapper, reply, click } = await setup()
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await click('手动下载')
    expect(open).toHaveBeenCalledWith('https://example.test/update.zip', '_blank')
    await click('在线更新')
    reply.mockResolvedValueOnce({ code: 500, message: 'private secret', data: null })
    await click('取消')
    expect(wrapper.get('.update-progress').text()).toContain('下载中')
    expect(wrapper.get('.update-progress').text()).toContain('取消')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('取消更新失败，请稍后重试')
  })

  it.each(['business', 'cancel', 'handled'])(
    '飞牛环境检测 %s 使用安全错误且保留平台回退',
    async (kind) => {
      const error =
        kind === 'cancel'
          ? new CanceledError()
          : new HttpResponseError({
              status: kind === 'handled' ? 401 : 200,
              data: { code: 500, message: 'private secret' },
            })
      if (kind === 'handled') markAuthInvalidationHandled(error)
      const { wrapper } = await setup(error)
      expect(wrapper.text()).toContain('可用版本')
      if (kind === 'business')
        expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('检查飞牛环境失败')
      else expect(ElMessage.error).not.toHaveBeenCalled()
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
    },
  )

  it('飞牛环境成功检测后显示应用商店提示', async () => {
    const { wrapper } = await setup(undefined, true)
    expect(wrapper.text()).toContain('版本更新请通过飞牛应用商店进行')
    expect(wrapper.text()).not.toContain('可用版本')
  })
})
