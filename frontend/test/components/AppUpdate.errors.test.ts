import axios, { CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppUpdate from '@/views/system/AppUpdate.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { APIResponse } from '@/api/types'

const setup = async (platformError?: unknown, fnos = false, progressStatus = 'downloading') => {
  const reply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue({ status: 200, message: '', data: null })
  let running = false
  const adapter = vi.fn(async (config) => {
    let data: unknown
    if (config.method === 'post') {
      data = await reply()
      if (config.url.endsWith('/to-version') && (data as APIResponse<unknown>).status === 200)
        running = true
    } else if (config.url.endsWith('/version')) {
      data = {
        status: 200,
        data: { version: 'v1.0.0', date: '', isWindows: false, isRelease: true },
      }
    } else if (config.url.endsWith('/is-fn-os')) {
      if (platformError) throw platformError
      data = { status: 200, data: fnos }
    } else if (config.url.includes('/update/last')) {
      data = {
        status: 200,
        data: [
          { version: 'v1.2.3', date: '', note: 'Release', url: 'https://example.test/update.zip' },
        ],
      }
    } else {
      data = running
        ? {
            status: 200,
            data: { status: progressStatus, progress: 10, total_size: 100, downloaded: 10 },
          }
        : { status: 200, data: { status: 'idle', progress: 0, total_size: 0, downloaded: 0 } }
    }
    return {
      config,
      status: (data as { status: number }).status,
      statusText: 'OK',
      headers: {},
      data,
    }
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
  vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('更新页面请求反馈', () => {
  it.each([false, true])('取消已受理使用服务端反馈，警告=%s 不报告完全成功', async (warn) => {
    const { wrapper, reply, click } = await setup()
    await click('在线更新')
    const message = '服务器确认取消测试更新任务'
    const warning = '测试更新取消需要人工确认'
    reply.mockResolvedValueOnce({
      status: 200,
      data: null,
      message,
      warnings: warn ? [warning] : [],
    })
    await click('取消')
    expect(wrapper.find('.update-progress').exists()).toBe(false)
    if (warn) {
      expect(ElMessage.warning).toHaveBeenCalledExactlyOnceWith(`${message}；${warning}`)
      expect(ElMessage.success).not.toHaveBeenCalled()
    } else expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith(message)
  })

  it('初始化无更新任务保持静默，启动业务失败不开始进度轮询', async () => {
    const { wrapper, reply, adapter, click } = await setup()
    expect(ElMessage.error).not.toHaveBeenCalled()
    reply.mockResolvedValueOnce({
      status: 500,
      message: '更新操作失败，请稍后重试',
      data: null,
      error_code: 'TEST_REQUEST_FAILED',
      instance: '/request-errors/ComponentTestFailure0001',
    })
    adapter.mockClear()
    await click('在线更新')
    await vi.advanceTimersByTimeAsync(2500)
    expect(adapter.mock.calls).toHaveLength(1)
    expect(wrapper.find('.update-progress').exists()).toBe(false)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('更新操作失败，请稍后重试')
    expect(wrapper.text()).toContain('v1.2.3')
  })

  it('取消业务失败保留运行进度与取消按钮，手动下载保持原链接', async () => {
    const { wrapper, reply, click } = await setup()
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await click('手动下载')
    expect(open).toHaveBeenCalledWith('https://example.test/update.zip', '_blank')
    await click('在线更新')
    reply.mockResolvedValueOnce({
      status: 500,
      message: '更新操作失败，请稍后重试',
      data: null,
      error_code: 'TEST_REQUEST_FAILED',
      instance: '/request-errors/ComponentTestFailure0001',
    })
    await click('取消')
    expect(wrapper.get('.update-progress').text()).toContain('下载中')
    expect(wrapper.get('.update-progress').text()).toContain('取消')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('更新操作失败，请稍后重试')
  })

  it('进入安装阶段后不再提供取消', async () => {
    const { wrapper, click } = await setup(undefined, false, 'install')
    await click('在线更新')
    expect(wrapper.get('.update-progress').text()).toContain('安装中')
    expect(wrapper.get('.update-progress').text()).not.toContain('取消')
  })

  it.each([
    ['在线更新', '当前运行方式不支持在线更新，请手动下载安装'],
    ['在线更新', '安装目录不可写，无法在线更新，请手动下载安装'],
    ['在线更新', '该版本未发布校验文件，无法在线更新，请手动下载安装'],
    ['取消', '正在安装更新，无法取消'],
  ])('%s被拒绝时展示后端说明：%s', async (action, message) => {
    const { reply, click } = await setup()
    if (action === '取消') await click('在线更新')
    reply.mockResolvedValueOnce({
      status: 500,
      message,
      data: null,
      error_code: 'TEST_REQUEST_FAILED',
      instance: '/request-errors/ComponentTestFailure0001',
    })
    await click(action)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(message)
  })

  it.each(['business', 'cancel', 'handled'])(
    '飞牛环境检测 %s 保持静默且保留平台回退',
    async (kind) => {
      const error =
        kind === 'cancel'
          ? new CanceledError()
          : new HttpResponseError({
              status: kind === 'handled' ? 401 : 500,
              data: {
                status: kind === 'handled' ? 401 : 500,
                message: '更新操作失败，请稍后重试',
                data: null,
                error_code: 'TEST_REQUEST_FAILED',
                instance: '/request-errors/ComponentTestFailure0001',
              },
            })
      if (kind === 'handled') markAuthInvalidationHandled(error)
      const { wrapper } = await setup(error)
      expect(wrapper.text()).toContain('可用版本')
      expect(ElMessage.error).not.toHaveBeenCalled()
      if (kind === 'business') expect(console.error).toHaveBeenCalledOnce()
      else expect(console.error).not.toHaveBeenCalled()
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
    },
  )

  it('原生 fnOS 显示应用中心和手动 FPK 更新说明，保持在线安装关闭', async () => {
    const { wrapper, adapter } = await setup(undefined, true)
    expect(wrapper.text()).toContain('请在飞牛应用中心更新 QMediaSync')
    expect(wrapper.text()).toContain('手动安装的版本，请按发布说明下载对应的 FPK 更新')
    expect(wrapper.text()).not.toContain('可用版本')
    expect(wrapper.findAll('button').some((button) => button.text() === '在线更新')).toBe(false)
    expect(adapter.mock.calls.some(([config]) => config.url.endsWith('/to-version'))).toBe(false)
  })
})
