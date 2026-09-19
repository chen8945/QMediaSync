import { flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CanceledError } from 'axios'
import { ElMessage } from 'element-plus'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import { useBackupStore } from '@/stores/backup'
import { createDeferred } from '../support/deferred'

describe('backup store 进度轮询', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessage, 'info').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(console, 'error').mockImplementation(() => {})
    setActivePinia(createPinia())
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('初次延迟期间切回前台后重新启动轮询', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: { is_running: true, count: 1, total: 2, elapsed: 1, desc: '备份中' },
        },
      }),
    }
    const store = useBackupStore()

    store.startProgressPolling('backup', undefined, http as never)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(3000)
    expect(http.get).not.toHaveBeenCalled()

    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(http.get).toHaveBeenCalledTimes(1)
    store.stopProgressPolling()
  })

  it.each([
    { type: 'backup' as const, desc: '旧备份' },
    { type: 'restore' as const, desc: '旧恢复' },
  ])('$desc 请求在新一轮任务启动后不写回状态', async ({ type }) => {
    const oldRequest = createDeferred<{
      data: {
        code: number
        data: {
          is_running: boolean
          count: number
          total: number
          elapsed: number
          desc: string
        }
      }
    }>()
    const oldHttp = { get: vi.fn(() => oldRequest.promise) }
    const newHttp = { get: vi.fn() }
    const store = useBackupStore()

    store.startProgressPolling(type, undefined, oldHttp as never)
    await vi.advanceTimersByTimeAsync(3000)
    expect(oldHttp.get).toHaveBeenCalledTimes(1)

    store.startProgressPolling(
      type === 'backup' ? 'restore' : 'backup',
      undefined,
      newHttp as never,
    )
    oldRequest.resolve({
      data: {
        code: 200,
        data: { is_running: false, count: 1, total: 1, elapsed: 1, desc: '旧任务完成' },
      },
    })
    await flushPromises()

    expect(store.progress).toBeNull()
    expect(store.showProgressDialog).toBe(true)
    store.stopProgressPolling()
  })

  it('页面隐藏后忽略仍在途的进度响应', async () => {
    const request = createDeferred<{
      data: {
        code: number
        data: {
          is_running: boolean
          count: number
          total: number
          elapsed: number
          desc: string
        }
      }
    }>()
    const http = { get: vi.fn(() => request.promise) }
    const store = useBackupStore()

    store.startProgressPolling('backup', undefined, http as never)
    await vi.advanceTimersByTimeAsync(3000)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    request.resolve({
      data: {
        code: 200,
        data: { is_running: false, count: 1, total: 1, elapsed: 1, desc: '备份完成' },
      },
    })
    await flushPromises()

    expect(store.progress).toBeNull()
    expect(store.showProgressDialog).toBe(true)
    store.stopProgressPolling()
  })
  it('业务失败计入连续失败次数且不写回成功进度', async () => {
    const http = {
      get: vi
        .fn()
        .mockResolvedValue({ status: 200, data: { code: 500, message: 'secret', data: null } }),
    }
    const store = useBackupStore()
    store.startProgressPolling('backup', undefined, http as never)
    await vi.advanceTimersByTimeAsync(7000)
    expect(http.get).toHaveBeenCalledTimes(3)
    expect(store.errorRetryCount).toBe(3)
    expect(store.progress).toBeNull()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('查询备份或恢复进度失败，页面即将刷新…')
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
    store.stopProgressPolling()
  })

  it.each(['cancel', 'handled'])('%s 不计入失败次数，不弹重复错误', async (kind) => {
    const error =
      kind === 'cancel'
        ? new CanceledError()
        : new HttpResponseError({ status: 401, data: { code: 401 } })
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const http = { get: vi.fn().mockRejectedValue(error) }
    const store = useBackupStore()
    store.startProgressPolling('restore', undefined, http as never)
    await vi.advanceTimersByTimeAsync(7000)
    expect(store.errorRetryCount).toBe(0)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
    store.stopProgressPolling()
  })

  it('慢请求不重叠，旧失败在停止后不增加重试次数', async () => {
    const request = createDeferred<{ data: { code: number; message: string; data: null } }>()
    const http = { get: vi.fn(() => request.promise) }
    const store = useBackupStore()
    store.startProgressPolling('backup', undefined, http as never)
    await vi.advanceTimersByTimeAsync(7000)
    expect(http.get).toHaveBeenCalledTimes(1)
    store.stopProgressPolling()
    request.resolve({ data: { code: 500, message: 'secret', data: null } })
    await flushPromises()
    expect(store.errorRetryCount).toBe(0)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})
