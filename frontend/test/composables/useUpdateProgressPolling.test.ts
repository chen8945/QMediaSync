import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { AxiosError, CanceledError } from 'axios'
import { ElMessage } from 'element-plus'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { isUpdateRunningStatus, isUpdateTerminalStatus, useUpdate } from '@/composables/useUpdate'

const { getMock, postMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  postMock: vi.fn(),
}))

vi.mock('@/http/client', () => ({
  useHttpClient: () => ({ get: getMock, post: postMock }),
}))

const terminalStatuses = ['completed', 'failed', 'cancelled'] as const

const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'info').mockImplementation(() => ({ close: vi.fn() }))
})

afterEach(() => {
  vi.clearAllMocks()
  vi.restoreAllMocks()
  vi.clearAllTimers()
  vi.useRealTimers()
})

describe('useUpdate 进度状态分类', () => {
  it.each(terminalStatuses)('把 %s 识别为终态', (status) => {
    expect(isUpdateTerminalStatus(status)).toBe(true)
  })

  it.each(['downloading', 'install'])('把 %s 识别为进行态', (status) => {
    expect(isUpdateRunningStatus(status)).toBe(true)
  })

  it('终态不属于进行态', () => {
    expect(isUpdateRunningStatus('completed')).toBe(false)
    expect(isUpdateRunningStatus('failed')).toBe(false)
    expect(isUpdateRunningStatus('cancelled')).toBe(false)
  })

  it('取消后忽略仍在途的旧进度响应', async () => {
    vi.useFakeTimers()
    const progressRequest = deferred<{
      data: { code: number; data: { status: string; progress: number } }
    }>()
    getMock.mockImplementation((url: string) => {
      if (url.includes('/update/last')) return Promise.resolve({ data: { code: 200, data: [] } })
      return progressRequest.promise
    })
    postMock.mockResolvedValue({ data: { code: 200 } })

    let update!: ReturnType<typeof useUpdate>
    const wrapper = mount(
      defineComponent({
        setup() {
          update = useUpdate()
          return () => null
        },
      }),
    )
    await flushPromises()

    const start = update.updateToVersion('v-next')
    await flushPromises()
    const cancel = update.cancelUpdate()
    await cancel
    progressRequest.resolve({ data: { code: 200, data: { status: 'completed', progress: 100 } } })
    await start
    await flushPromises()

    expect(update.showUpdateCompleteDialog.value).toBe(false)
    expect(update.isUpdating.value).toBe(false)
    wrapper.unmount()
  })
})

const mountUpdate = async () => {
  vi.useFakeTimers()
  Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  vi.spyOn(console, 'error').mockImplementation(() => {})
  getMock.mockImplementation((url: string) =>
    Promise.resolve({
      data: {
        code: 200,
        data: url.includes('/update/last') ? [] : { status: '', progress: 0 },
      },
    }),
  )
  postMock.mockResolvedValue({ data: { code: 200, data: null } })
  let update!: ReturnType<typeof useUpdate>
  const wrapper = mount(
    defineComponent({
      setup() {
        update = useUpdate()
        return () => null
      },
    }),
  )
  await flushPromises()
  return { update, wrapper }
}

describe('useUpdate 请求失败', () => {
  it('启动业务失败不创建轮询', async () => {
    const { update, wrapper } = await mountUpdate()
    getMock.mockClear()
    postMock.mockResolvedValueOnce({ data: { code: 500, message: 'internal secret', data: null } })
    await update.updateToVersion('v-next')
    await vi.advanceTimersByTimeAsync(3000)
    expect(update.isUpdating.value).toBe(false)
    expect(getMock).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('触发版本更新失败')
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
    wrapper.unmount()
  })

  it.each(['business', 'timeout', 'csrf', 'cancel', 'handled'])(
    '取消请求 %s 不停轮询、不报成功或刷新版本列表',
    async (kind) => {
      const { update, wrapper } = await mountUpdate()
      await update.updateToVersion('v-next')
      await flushPromises()
      getMock.mockClear()
      if (kind === 'business')
        postMock.mockResolvedValueOnce({
          data: { code: 500, message: 'internal secret', data: null },
        })
      else if (kind === 'timeout')
        postMock.mockRejectedValueOnce(new AxiosError('secret', 'ECONNABORTED'))
      else if (kind === 'cancel') postMock.mockRejectedValueOnce(new CanceledError())
      else {
        const error = new HttpResponseError({
          status: kind === 'handled' ? 401 : 403,
          data: {
            code: 500,
            error_code: kind === 'handled' ? 'SESSION_INVALID' : 'CSRF_TOKEN_INVALID',
          },
        })
        if (kind === 'handled') markAuthInvalidationHandled(error)
        postMock.mockRejectedValueOnce(error)
      }
      await update.cancelUpdate()
      expect(update.isUpdating.value).toBe(true)
      expect(update.updatingVersion.value).toBe('v-next')
      expect(ElMessage.success).not.toHaveBeenCalled()
      if (kind === 'cancel' || kind === 'handled') expect(ElMessage.error).not.toHaveBeenCalled()
      else expect(ElMessage.error).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(1100)
      expect(getMock).toHaveBeenCalledWith('/api/update/progress')
      expect(getMock.mock.calls.some(([url]) => url.includes('/update/last'))).toBe(false)
      wrapper.unmount()
    },
  )

  it('失败终态不透传下载内部错误，卸载后不执行延迟刷新', async () => {
    const { update, wrapper } = await mountUpdate()
    getMock.mockResolvedValueOnce({
      data: {
        code: 200,
        data: { status: 'failed', error_message: 'secret http://private/?token=secret' },
      },
    })
    await update.updateToVersion('v-next')
    await flushPromises()
    expect(update.isUpdating.value).toBe(false)
    expect(ElMessage.error).toHaveBeenCalledWith({
      message: '更新失败，请稍后重试或手动下载最新版本',
      duration: 5000,
    })
    wrapper.unmount()
    getMock.mockClear()
    await vi.advanceTimersByTimeAsync(2000)
    expect(getMock).not.toHaveBeenCalled()
  })

  it('业务失败后的延迟重置不覆盖新任务', async () => {
    const { update, wrapper } = await mountUpdate()
    getMock.mockResolvedValueOnce({ data: { code: 500, message: '未开始更新', data: null } })
    await update.updateToVersion('old')
    await flushPromises()
    await update.updateToVersion('new')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(2000)
    expect(update.updatingVersion.value).toBe('new')
    expect(update.isUpdating.value).toBe(true)
    wrapper.unmount()
  })
})
