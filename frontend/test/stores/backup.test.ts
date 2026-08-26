import { flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useBackupStore } from '@/stores/backup'
import { createDeferred } from '../support/deferred'

vi.mock('element-plus', () => ({
  ElMessage: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}))

describe('backup store 进度轮询', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setActivePinia(createPinia())
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  })

  afterEach(() => {
    vi.useRealTimers()
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
})
