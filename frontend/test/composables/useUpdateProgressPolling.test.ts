import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
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
enableAutoUnmount(afterEach)

const deferred = <T>() => {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'info').mockImplementation(() => ({ close: vi.fn() }))
})

afterEach(() => {
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

const mountUpdate = async (initialProgressError?: unknown) => {
  vi.useFakeTimers()
  Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  vi.spyOn(console, 'error').mockImplementation(() => {})
  getMock.mockImplementation((url: string) =>
    initialProgressError && !url.includes('/update/last')
      ? Promise.reject(initialProgressError)
      : Promise.resolve({
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
  const switchVisibility = () => {
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange'))
  }
  const originFailure = () =>
    new HttpResponseError({
      status: 403,
      data: { code: 500, error_code: 'REQUEST_ORIGIN_INVALID', data: null },
    })

  it('后台更新状态失败只记诊断，版本列表读取失败仍向用户提示', async () => {
    const { update } = await mountUpdate(originFailure())
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).toHaveBeenCalledExactlyOnceWith(
      '检查更新状态失败',
      expect.objectContaining({ status: 403 }),
    )
    getMock.mockRejectedValueOnce(originFailure())
    await update.loadUpdateList(true)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      expect.stringContaining('访问地址校验失败'),
    )
  })

  it.each(['v-next', 'v-old'])(
    '进程重启后用当前版本 %s 核验目标，不能仅凭旧 install 状态报成功',
    async (version) => {
      const { update } = await mountUpdate()
      getMock.mockResolvedValueOnce({
        status: 200,
        data: { code: 200, data: { status: 'install', progress: 100 } },
      })
      await update.updateToVersion('v-next')
      await flushPromises()
      getMock.mockImplementation(async (url: string) => {
        if (url.includes('/update/last')) return { status: 200, data: { code: 200, data: [] } }
        return url.endsWith('/version')
          ? { status: 200, data: { version, date: '', isWindows: false, isRelease: true } }
          : { status: 200, data: { code: 500, message: '未开始更新', data: null } }
      })
      await vi.advanceTimersByTimeAsync(1000)
      expect(getMock).toHaveBeenCalledWith('/api/version')
      expect(update.showUpdateCompleteDialog.value).toBe(version === 'v-next')
      getMock.mockClear()
      await vi.advanceTimersByTimeAsync(2100)
      expect(update.showUpdateCompleteDialog.value).toBe(version === 'v-next')
      expect(getMock.mock.calls.some(([url]) => url === '/api/update/progress')).toBe(false)
      if (version === 'v-next') {
        expect(ElMessage.error).not.toHaveBeenCalled()
        expect(update.updateProgress.value.status).toBe('completed')
        expect(update.countdown.value).toBe(28)
      } else {
        expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
          '更新未生效，请查看服务日志或手动下载安装',
        )
        expect(update.isUpdating.value).toBe(false)
        expect(ElMessage.success).not.toHaveBeenCalled()
      }
    },
  )

  it.each(['success', 'failure'])('启动请求在途切换标签页后仍处理 %s 结果', async (outcome) => {
    const { update, wrapper } = await mountUpdate()
    const pending = deferred<{ data: { code: number; data: null } }>()
    postMock.mockReturnValueOnce(pending.promise)
    getMock.mockClear()
    getMock.mockResolvedValue({
      data: { code: 200, data: { status: 'downloading', progress: 37 } },
    })

    const start = update.updateToVersion('v-next')
    switchVisibility()
    await flushPromises()
    expect(getMock).not.toHaveBeenCalled()
    if (outcome === 'success') pending.resolve({ data: { code: 200, data: null } })
    else pending.reject(originFailure())
    await start
    await flushPromises()

    expect(postMock).toHaveBeenCalledTimes(1)
    if (outcome === 'success') {
      expect(update.isUpdating.value).toBe(true)
      expect(update.updateProgress.value.progress).toBe(37)
      expect(getMock).toHaveBeenCalledExactlyOnceWith('/api/update/progress')
    } else {
      expect(update.isUpdating.value).toBe(false)
      expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
        expect.stringContaining('访问地址校验失败'),
      )
      expect(getMock).not.toHaveBeenCalled()
    }
    wrapper.unmount()
  })

  it.each(['success', 'failure'])('取消请求在途切换标签页后仍处理 %s 结果', async (outcome) => {
    const { update, wrapper } = await mountUpdate()
    getMock.mockResolvedValue({
      data: { code: 200, data: { status: 'downloading', progress: 37 } },
    })
    await update.updateToVersion('v-next')
    await flushPromises()
    const pending = deferred<{ data: { code: number; data: null } }>()
    postMock.mockReturnValueOnce(pending.promise)
    const cancel = update.cancelUpdate()
    switchVisibility()
    await flushPromises()
    if (outcome === 'success') pending.resolve({ data: { code: 200, data: null } })
    else pending.reject(originFailure())
    await cancel
    if (outcome === 'success') {
      expect(update.isUpdating.value).toBe(false)
      expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('已取消更新')
    } else {
      expect(update.isUpdating.value).toBe(true)
      expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
        expect.stringContaining('访问地址校验失败'),
      )
    }
    wrapper.unmount()
  })

  it.each(['success', 'failure'])('新任务启动后丢弃旧启动请求的 %s 结果', async (outcome) => {
    const { update, wrapper } = await mountUpdate()
    const pending = deferred<{ data: { code: number; data: null } }>()
    postMock.mockReturnValueOnce(pending.promise)
    const oldStart = update.updateToVersion('old')
    await update.updateToVersion('new')
    await flushPromises()
    getMock.mockClear()
    if (outcome === 'success') pending.resolve({ data: { code: 200, data: null } })
    else pending.reject(originFailure())
    await oldStart
    expect(update.updatingVersion.value).toBe('new')
    expect(update.isUpdating.value).toBe(true)
    expect(getMock).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each([
    ['start', 'success'],
    ['start', 'failure'],
    ['cancel', 'success'],
    ['cancel', 'failure'],
  ])('卸载后丢弃在途 %s 请求的 %s 结果', async (action, outcome) => {
    const { update, wrapper } = await mountUpdate()
    if (action === 'cancel') await update.updateToVersion('v-next')
    const pending = deferred<{ data: { code: number; data: null } }>()
    postMock.mockReturnValueOnce(pending.promise)
    const request = action === 'start' ? update.updateToVersion('v-next') : update.cancelUpdate()
    wrapper.unmount()
    getMock.mockClear()
    if (outcome === 'success') pending.resolve({ data: { code: 200, data: null } })
    else pending.reject(originFailure())
    await request
    await vi.advanceTimersByTimeAsync(3000)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(getMock).not.toHaveBeenCalled()
  })

  it('启动尚未返回时取消失败，仍接收原启动请求的成功结果', async () => {
    const { update, wrapper } = await mountUpdate()
    const pending = deferred<{ data: { code: number; data: null } }>()
    postMock.mockReturnValueOnce(pending.promise)
    const start = update.updateToVersion('v-next')
    postMock.mockRejectedValueOnce(originFailure())
    await update.cancelUpdate()
    getMock.mockClear()
    getMock.mockResolvedValue({
      data: { code: 200, data: { status: 'downloading', progress: 37 } },
    })
    pending.resolve({ data: { code: 200, data: null } })
    await start
    await flushPromises()
    expect(update.isUpdating.value).toBe(true)
    expect(update.updateProgress.value.progress).toBe(37)
    expect(getMock).toHaveBeenCalledExactlyOnceWith('/api/update/progress')
    wrapper.unmount()
  })

  it('进度已确认完成时，晚到的取消结果不覆盖完成通知', async () => {
    const { update, wrapper } = await mountUpdate()
    await update.updateToVersion('v-next')
    await flushPromises()
    const pending = deferred<{ data: { code: number; data: null } }>()
    postMock.mockReturnValueOnce(pending.promise)
    const cancel = update.cancelUpdate()
    getMock.mockResolvedValue({ data: { code: 200, data: { status: 'completed', progress: 100 } } })
    await vi.advanceTimersByTimeAsync(1000)
    pending.resolve({ data: { code: 200, data: null } })
    await cancel
    expect(update.showUpdateCompleteDialog.value).toBe(true)
    expect(ElMessage.success).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('启动业务失败不创建轮询', async () => {
    const { update, wrapper } = await mountUpdate()
    getMock.mockClear()
    postMock.mockResolvedValueOnce({
      data: { code: 500, message: '更新操作失败，请稍后重试', data: null },
    })
    await update.updateToVersion('v-next')
    await vi.advanceTimersByTimeAsync(3000)
    expect(update.isUpdating.value).toBe(false)
    expect(getMock).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('更新操作失败，请稍后重试')
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
          data: { code: 500, message: '更新操作失败，请稍后重试', data: null },
        })
      else if (kind === 'timeout')
        postMock.mockRejectedValueOnce(new AxiosError('secret', 'ETIMEDOUT'))
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
