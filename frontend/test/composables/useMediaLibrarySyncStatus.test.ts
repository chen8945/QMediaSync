import { defineComponent, h, ref } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { AxiosInstance } from 'axios'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  fetchMediaLibrarySyncStatus,
  type MediaLibrarySyncInfo,
  type MediaServerType,
} from '@/api/mediaLibrary'
import { useMediaLibrarySyncStatus } from '@/composables/useMediaLibrarySyncStatus'
import { createDeferred } from '../support/deferred'

vi.mock('@/api/mediaLibrary', () => ({ fetchMediaLibrarySyncStatus: vi.fn() }))
const query = vi.mocked(fetchMediaLibrarySyncStatus)
const harness = (serverType: MediaServerType = 'emby') => {
  const active = ref(true)
  let state!: ReturnType<typeof useMediaLibrarySyncStatus>
  const wrapper = mount(
    defineComponent({
      setup() {
        state = useMediaLibrarySyncStatus({} as AxiosInstance, serverType, active)
        return () => h('div')
      },
    }),
  )
  return { state, active, wrapper }
}
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers()
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  query.mockReset().mockResolvedValue({ is_running: false })
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('媒体库状态生命周期', () => {
  it('固定产品请求，合并在途启动刷新并按运行状态轮询', async () => {
    const initial = createDeferred<MediaLibrarySyncInfo>()
    query.mockReturnValueOnce(initial.promise).mockResolvedValue({ is_running: true })
    const { state } = harness('jellyfin')
    await state.refresh(true)
    expect(query).toHaveBeenCalledTimes(1)
    expect(query.mock.calls[0]?.[1]).toBe('jellyfin')
    initial.resolve({ is_running: false })
    await flushPromises()
    expect(query).toHaveBeenCalledTimes(2)
    expect(state.running.value).toBe(true)
    await vi.advanceTimersByTimeAsync(3000)
    expect(query).toHaveBeenCalledTimes(3)
    query.mockResolvedValue({ is_running: false })
    await vi.advanceTimersByTimeAsync(9000)
    expect(query).toHaveBeenCalledTimes(4)
  })

  it('隐藏标签使迟到响应失效，激活立即补读且请求不重叠', async () => {
    const old = createDeferred<MediaLibrarySyncInfo>()
    query.mockReturnValueOnce(old.promise)
    const { state, active } = harness()
    active.value = false
    await flushPromises()
    await vi.advanceTimersByTimeAsync(9000)
    expect(query).toHaveBeenCalledTimes(1)
    active.value = true
    await flushPromises()
    expect(query).toHaveBeenCalledTimes(1)
    old.resolve({ is_running: true, total_items: 999 })
    await flushPromises()
    expect(query).toHaveBeenCalledTimes(2)
    expect(state.status.value).toEqual({ is_running: false })
  })

  it('启动前的在途响应不能清除启动状态，启动后查询失败仍继续轮询', async () => {
    const old = createDeferred<MediaLibrarySyncInfo>()
    query.mockReturnValueOnce(old.promise).mockRejectedValueOnce(new Error('offline'))
    const { state } = harness()
    await state.refresh(true)
    old.resolve({ is_running: false })
    await flushPromises()
    expect(query).toHaveBeenCalledTimes(2)
    expect(state.running.value).toBe(true)
    expect(state.status.value).toBeNull()
    await vi.advanceTimersByTimeAsync(3000)
    expect(query).toHaveBeenCalledTimes(3)
    expect(state.running.value).toBe(false)
    expect(state.error.value).toBeNull()
  })

  it('页面后台暂停轮询，恢复后立即刷新，慢请求期间不创建并发请求', async () => {
    const hidden = vi.spyOn(document, 'hidden', 'get')
    query.mockResolvedValue({ is_running: true })
    harness()
    await flushPromises()
    hidden.mockReturnValue(true)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(9000)
    expect(query).toHaveBeenCalledTimes(1)
    const pending = createDeferred<MediaLibrarySyncInfo>()
    query.mockReturnValueOnce(pending.promise)
    hidden.mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(9000)
    expect(query).toHaveBeenCalledTimes(2)
    pending.resolve({ is_running: false })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(9000)
    expect(query).toHaveBeenCalledTimes(2)
  })

  it('连续失败仅发布一次错误，成功后恢复，卸载丢弃响应和定时器', async () => {
    query.mockResolvedValue({ is_running: true })
    const { state, wrapper } = harness()
    await flushPromises()
    const first = new Error('offline')
    query.mockRejectedValue(first)
    await vi.advanceTimersByTimeAsync(3000)
    expect(state.error.value).toBe(first)
    query.mockRejectedValue(new Error('still offline'))
    await vi.advanceTimersByTimeAsync(6000)
    expect(state.error.value).toBe(first)
    query.mockResolvedValue({ is_running: true })
    await vi.advanceTimersByTimeAsync(3000)
    expect(state.error.value).toBeNull()
    const pending = createDeferred<MediaLibrarySyncInfo>()
    query.mockReturnValueOnce(pending.promise)
    await vi.advanceTimersByTimeAsync(3000)
    const count = query.mock.calls.length
    wrapper.unmount()
    pending.resolve({ is_running: true, total_items: 999 })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(9000)
    expect(query).toHaveBeenCalledTimes(count)
    expect(state.status.value?.total_items).toBeUndefined()
  })
})
